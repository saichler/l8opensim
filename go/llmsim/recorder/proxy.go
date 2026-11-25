/*
 * © 2025 Sharon Aicler (saichler@gmail.com)
 *
 * Layer 8 Ecosystem is licensed under the Apache License, Version 2.0.
 * You may obtain a copy of the License at:
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package recorder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/saichler/l8opensim/go/llmsim/engine"
)

// Proxy is a transparent recording proxy: OpenAI-compatible requests are
// forwarded to the upstream with the upstream key, and every exchange is
// written into a cassette per conversation.
type Proxy struct {
	upstream string
	up       *Upstream
	outDir   string
	flavor   string
	mtx      sync.Mutex
	convs    map[string]*engine.Cassette
}

// NewProxy creates a recording proxy.
func NewProxy(upstream string, up *Upstream, outDir, flavor string) *Proxy {
	if flavor == "" {
		flavor = "proxy"
	}
	return &Proxy{upstream: strings.TrimRight(upstream, "/"), up: up, outDir: outDir, flavor: flavor,
		convs: map[string]*engine.Cassette{}}
}

// ServeHTTP forwards the request and records chat completions.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	req, err := http.NewRequestWithContext(r.Context(), r.Method, p.upstream+path, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.up.key)
	resp, err := p.up.http.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(out)
	if resp.StatusCode == http.StatusOK && strings.HasSuffix(path, "/chat/completions") {
		if err := p.record(body, out); err != nil {
			log.Printf("proxy: not recorded: %v", err)
		}
	}
}

// record adds one request/response exchange to its conversation's cassette.
// The conversation is identified by its system prompt and first user
// message; the turn by the number of user messages; the step by the number
// of tool rounds since the last user message.
func (p *Proxy) record(reqBody, respBody []byte) error {
	req, err := engine.ParseOpenAI(reqBody)
	if err != nil {
		return err
	}
	res := struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content          *string `json:"content"`
				ReasoningContent string  `json:"reasoning_content"`
				ToolCalls        []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage engine.Usage `json:"usage"`
	}{}
	if err = json.Unmarshal(respBody, &res); err != nil || len(res.Choices) == 0 {
		return fmt.Errorf("unreadable response")
	}
	msg := res.Choices[0].Message
	step := engine.Step{Reasoning: msg.ReasoningContent, FinishReason: res.Choices[0].FinishReason, Usage: res.Usage}
	if msg.Content != nil {
		step.Text = *msg.Content
	}
	for _, tc := range msg.ToolCalls {
		args := map[string]interface{}{}
		if json.Unmarshal([]byte(tc.Function.Arguments), &args) != nil {
			args = map[string]interface{}{"_raw": tc.Function.Arguments}
		}
		step.ToolCalls = append(step.ToolCalls, engine.RecordedCall{Name: tc.Function.Name, Arguments: args})
	}
	key := fmt.Sprintf("%016x", engine.Mix(engine.HashString(req.System), engine.HashString(engine.Normalize(req.FirstUser))))
	toolResults := lastToolResults(reqBody)

	p.mtx.Lock()
	defer p.mtx.Unlock()
	c := p.convs[key]
	if c == nil {
		c = &engine.Cassette{ID: p.flavor + "/proxy-" + key, Flavor: p.flavor, Model: res.Model, Quality: "ok",
			RecordedAt: time.Now().UTC().Format(time.RFC3339)}
		p.convs[key] = c
	}
	turn := req.UserCount - 1
	for len(c.Turns) <= turn {
		c.Turns = append(c.Turns, engine.Turn{})
	}
	t := &c.Turns[turn]
	t.UserMessage = req.LastUser
	for len(t.Steps) <= req.ToolStep {
		t.Steps = append(t.Steps, engine.Step{})
	}
	t.Steps[req.ToolStep] = step
	// The tool results of earlier steps arrive with this request.
	k := 0
	for si := 0; si < req.ToolStep && si < len(t.Steps); si++ {
		n := len(t.Steps[si].ToolCalls)
		if k+n <= len(toolResults) {
			t.Steps[si].ToolResults = toolResults[k : k+n]
		}
		k += n
	}
	return p.save(c)
}

func (p *Proxy) save(c *engine.Cassette) error {
	cp := *c
	cp.Turns = make([]engine.Turn, len(c.Turns))
	for i, t := range c.Turns {
		cp.Turns[i] = engine.Turn{UserMessage: t.UserMessage, Steps: append([]engine.Step{}, t.Steps...)}
		for j := range cp.Turns[i].Steps {
			s := &cp.Turns[i].Steps[j]
			s.ToolCalls = cloneCalls(s.ToolCalls)
		}
	}
	Sanitize(&cp, nil, nil)
	data, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(p.outDir, p.flavor, strings.TrimPrefix(c.ID, p.flavor+"/")+".json")
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func cloneCalls(in []engine.RecordedCall) []engine.RecordedCall {
	out := make([]engine.RecordedCall, len(in))
	for i, c := range in {
		args := make(map[string]interface{}, len(c.Arguments))
		for k, v := range c.Arguments {
			args[k] = v
		}
		out[i] = engine.RecordedCall{Name: c.Name, Arguments: args}
	}
	return out
}

// lastToolResults returns the tool message contents after the last user
// message, in order.
func lastToolResults(body []byte) []string {
	in := struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}{}
	if json.Unmarshal(body, &in) != nil {
		return nil
	}
	last := -1
	for i, m := range in.Messages {
		if m.Role == "user" {
			last = i
		}
	}
	out := []string{}
	for _, m := range in.Messages[last+1:] {
		if m.Role == "tool" {
			s := ""
			if json.Unmarshal(m.Content, &s) != nil {
				s = string(m.Content)
			}
			out = append(out, s)
		}
	}
	return out
}
