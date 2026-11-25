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

package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

type antRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    json.RawMessage `json:"system"`
	Messages  []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Name        string          `json:"name"`
		InputSchema json.RawMessage `json:"input_schema"`
	} `json:"tools"`
	ToolChoice *struct {
		Type string `json:"type"`
	} `json:"tool_choice"`
	Metadata *struct {
		UserID string `json:"user_id"`
	} `json:"metadata"`
	Stream bool `json:"stream"`
}

// ParseAnthropic parses a /v1/messages body into the neutral request. A user
// message that only carries tool_result blocks is a tool round, not a new
// user turn.
func ParseAnthropic(body []byte) (*Request, error) {
	in := antRequest{}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, errors.New("invalid JSON body: " + err.Error())
	}
	if in.Stream {
		return nil, errors.New("streaming is not supported by the simulator; send stream=false")
	}
	if len(in.Messages) == 0 {
		return nil, errors.New("messages must not be empty")
	}
	req := &Request{API: APIAnthropic, Model: in.Model, MaxTokens: in.MaxTokens, BodyLen: len(body),
		Tools: map[string]*ToolDef{}}
	if in.Metadata != nil {
		req.Session = in.Metadata.UserID
	}
	req.System, _ = textOf(in.System)
	last := -1
	for i, m := range in.Messages {
		if m.Role != "user" {
			continue
		}
		t, toolResult := textOf(m.Content)
		if toolResult && t == "" {
			continue
		}
		if req.UserCount == 0 {
			req.FirstUser = t
		}
		req.UserCount++
		req.LastUser = t
		last = i
	}
	if last >= 0 {
		for _, m := range in.Messages[last+1:] {
			if m.Role == "assistant" && hasBlockType(m.Content, "tool_use") {
				req.ToolStep++
			}
		}
	}
	for _, t := range in.Tools {
		if t.Name == "" {
			continue
		}
		req.Tools[t.Name] = &ToolDef{Name: t.Name, Enums: parseEnums(t.InputSchema)}
		req.ToolOrder = append(req.ToolOrder, t.Name)
	}
	if in.ToolChoice != nil && in.ToolChoice.Type == "none" {
		req.NoTools = true
	}
	return req, nil
}

type antBlock struct {
	Type  string                 `json:"type"`
	Text  *string                `json:"text,omitempty"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
}

type antResponse struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	Role         string     `json:"role"`
	Model        string     `json:"model"`
	Content      []antBlock `json:"content"`
	StopReason   string     `json:"stop_reason"`
	StopSequence *string    `json:"stop_sequence"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// EncodeAnthropic writes the reply as an Anthropic message.
func EncodeAnthropic(buf *bytes.Buffer, model string, seed uint64, rep *Reply) error {
	out := antResponse{ID: "msg_" + strconv.FormatUint(seed, 36), Type: "message", Role: "assistant",
		Model: model, Content: []antBlock{}}
	if rep.Text != "" || len(rep.Calls) == 0 {
		text := rep.Text
		out.Content = append(out.Content, antBlock{Type: "text", Text: &text})
	}
	for _, c := range rep.Calls {
		if c.Raw != "" {
			// A malformed-arguments fault: Anthropic input is an object, so
			// the broken JSON is carried as a string inside it.
			out.Content = append(out.Content, antBlock{Type: "tool_use", ID: c.ID, Name: c.Name,
				Input: map[string]interface{}{"_raw": c.Raw}})
			continue
		}
		input := c.Args
		if input == nil {
			input = map[string]interface{}{}
		}
		out.Content = append(out.Content, antBlock{Type: "tool_use", ID: c.ID, Name: c.Name, Input: input})
	}
	switch rep.Finish {
	case FinishToolCalls:
		out.StopReason = "tool_use"
	case FinishLength:
		out.StopReason = "max_tokens"
	default:
		out.StopReason = "end_turn"
	}
	out.Usage.InputTokens = rep.PromptTokens
	out.Usage.OutputTokens = rep.CompletionTokens
	return json.NewEncoder(buf).Encode(&out)
}

// EncodeAnthropicError writes an Anthropic-style error body.
func EncodeAnthropicError(buf *bytes.Buffer, status int, message string) {
	typ := "api_error"
	switch status {
	case 400:
		typ = "invalid_request_error"
	case 401:
		typ = "authentication_error"
	case 404:
		typ = "not_found_error"
	case 429:
		typ = "rate_limit_error"
	case 503:
		typ = "overloaded_error"
	}
	_ = json.NewEncoder(buf).Encode(map[string]interface{}{
		"type": "error", "error": map[string]interface{}{"type": typ, "message": message}})
}
