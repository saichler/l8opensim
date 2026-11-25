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
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Upstream is an OpenAI-compatible LLM (Halo) plus its web-tools service.
type Upstream struct {
	key  string
	http *http.Client
}

// NewUpstream creates a client that trusts the CA file (if given) in
// addition to the system CAs, for this client only.
func NewUpstream(key, caFile string) (*Upstream, error) {
	if key == "" {
		return nil, errors.New("the upstream API key is empty (set L8_LLM_KEY)")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("no certificate in " + caFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &Upstream{key: key, http: &http.Client{Timeout: 15 * time.Minute, Transport: transport}}, nil
}

// ChatCall is one tool call returned by the upstream model.
type ChatCall struct {
	ID        string
	Name      string
	Arguments string
}

// ChatResult is the upstream model's answer.
type ChatResult struct {
	Content          string
	Reasoning        string
	Calls            []ChatCall
	Finish           string
	PromptTokens     int
	CompletionTokens int
}

// Chat sends a non-streaming chat completion.
func (u *Upstream) Chat(ctx context.Context, baseURL string, body map[string]interface{}) (*ChatResult, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	out, err := u.post(ctx, strings.TrimRight(baseURL, "/")+"/chat/completions", data)
	if err != nil {
		return nil, err
	}
	resp := struct {
		Choices []struct {
			Message struct {
				Content          *string `json:"content"`
				ReasoningContent string  `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}{}
	if err = json.Unmarshal(out, &resp); err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("upstream returned no choices")
	}
	ch := resp.Choices[0]
	res := &ChatResult{Reasoning: ch.Message.ReasoningContent, Finish: ch.FinishReason,
		PromptTokens: resp.Usage.PromptTokens, CompletionTokens: resp.Usage.CompletionTokens}
	if ch.Message.Content != nil {
		res.Content = *ch.Message.Content
	}
	for _, tc := range ch.Message.ToolCalls {
		res.Calls = append(res.Calls, ChatCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return res, nil
}

// WebTool calls the upstream web tools (POST {base}/v1/search or /v1/fetch).
func (u *Upstream) WebTool(ctx context.Context, baseURL, path string, args map[string]interface{}) (string, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	out, err := u.post(ctx, strings.TrimRight(baseURL, "/")+path, data)
	return string(out), err
}

// Healthy reports whether {base}/health answers 200 (base without /v1).
func (u *Upstream) Healthy(ctx context.Context, baseURL string) bool {
	root := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := u.http.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// post retries connection errors, 429 and 5xx with backoff.
func (u *Upstream) post(ctx context.Context, url string, data []byte) ([]byte, error) {
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt*attempt) * 5 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+u.key)
		resp, err := u.http.Do(req)
		if err != nil {
			last = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return body, nil
		}
		last = fmt.Errorf("upstream %s: %s: %s", url, resp.Status, truncateErr(body))
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return nil, last
		}
	}
	return nil, last
}

func truncateErr(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "..."
	}
	return string(b)
}
