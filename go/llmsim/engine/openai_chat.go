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
	"time"
)

type oaiRequest struct {
	Model               string          `json:"model"`
	MaxTokens           int             `json:"max_tokens"`
	MaxCompletionTokens int             `json:"max_completion_tokens"`
	Messages            []oaiMessage    `json:"messages"`
	Tools               []oaiTool       `json:"tools"`
	ToolChoice          json.RawMessage `json:"tool_choice"`
	User                string          `json:"user"`
	Stream              bool            `json:"stream"`
}

type oaiMessage struct {
	Role      string            `json:"role"`
	Content   json.RawMessage   `json:"content"`
	ToolCalls []json.RawMessage `json:"tool_calls"`
}

type oaiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name       string          `json:"name"`
		Parameters json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// ParseOpenAI parses a /v1/chat/completions body into the neutral request.
func ParseOpenAI(body []byte) (*Request, error) {
	in := oaiRequest{}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, errors.New("invalid JSON body: " + err.Error())
	}
	if in.Stream {
		return nil, errors.New("streaming is not supported by the simulator; send stream=false")
	}
	if len(in.Messages) == 0 {
		return nil, errors.New("messages must not be empty")
	}
	req := &Request{API: APIOpenAI, Model: in.Model, MaxTokens: in.MaxTokens, BodyLen: len(body),
		Session: in.User, Tools: map[string]*ToolDef{}}
	if req.MaxTokens == 0 {
		req.MaxTokens = in.MaxCompletionTokens
	}
	last := -1
	for i, m := range in.Messages {
		switch m.Role {
		case "system", "developer":
			t, _ := textOf(m.Content)
			if req.System != "" {
				req.System += "\n\n"
			}
			req.System += t
		case "user":
			t, _ := textOf(m.Content)
			if req.UserCount == 0 {
				req.FirstUser = t
			}
			req.UserCount++
			req.LastUser = t
			last = i
		}
	}
	if last >= 0 {
		for _, m := range in.Messages[last+1:] {
			if m.Role == "assistant" && len(m.ToolCalls) > 0 {
				req.ToolStep++
			}
		}
	}
	for _, t := range in.Tools {
		if t.Function.Name == "" {
			continue
		}
		req.Tools[t.Function.Name] = &ToolDef{Name: t.Function.Name, Enums: parseEnums(t.Function.Parameters)}
		req.ToolOrder = append(req.ToolOrder, t.Function.Name)
	}
	if len(in.ToolChoice) > 0 && bytes.Equal(bytes.TrimSpace(in.ToolChoice), []byte(`"none"`)) {
		req.NoTools = true
	}
	return req, nil
}

type oaiOutCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiOutMessage struct {
	Role             string       `json:"role"`
	Content          *string      `json:"content"`
	ReasoningContent string       `json:"reasoning_content,omitempty"`
	ToolCalls        []oaiOutCall `json:"tool_calls,omitempty"`
}

type oaiChoice struct {
	Index        int           `json:"index"`
	Message      oaiOutMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type oaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type oaiResponse struct {
	ID      string      `json:"id"`
	Object  string      `json:"object"`
	Created int64       `json:"created"`
	Model   string      `json:"model"`
	Choices []oaiChoice `json:"choices"`
	Usage   oaiUsage    `json:"usage"`
}

// EncodeOpenAI writes the reply as a chat.completion object.
func EncodeOpenAI(buf *bytes.Buffer, model string, seed uint64, rep *Reply) error {
	msg := oaiOutMessage{Role: "assistant", ReasoningContent: rep.Reasoning}
	if rep.Text != "" || len(rep.Calls) == 0 {
		text := rep.Text
		msg.Content = &text
	}
	for _, c := range rep.Calls {
		oc := oaiOutCall{ID: c.ID, Type: "function"}
		oc.Function.Name = c.Name
		oc.Function.Arguments = callArgs(&c)
		msg.ToolCalls = append(msg.ToolCalls, oc)
	}
	out := oaiResponse{ID: "chatcmpl-" + strconv.FormatUint(seed, 36), Object: "chat.completion",
		Created: time.Now().Unix(), Model: model,
		Choices: []oaiChoice{{Index: 0, Message: msg, FinishReason: rep.Finish}},
		Usage: oaiUsage{PromptTokens: rep.PromptTokens, CompletionTokens: rep.CompletionTokens,
			TotalTokens: rep.PromptTokens + rep.CompletionTokens}}
	return json.NewEncoder(buf).Encode(&out)
}

func callArgs(c *OutCall) string {
	if c.Raw != "" {
		return c.Raw
	}
	data, err := json.Marshal(c.Args)
	if err != nil || c.Args == nil {
		return "{}"
	}
	return string(data)
}

// EncodeOpenAIError writes an OpenAI-style error body.
func EncodeOpenAIError(buf *bytes.Buffer, status int, message string) {
	typ, code := errorKind(status, message)
	_ = json.NewEncoder(buf).Encode(map[string]interface{}{
		"error": map[string]interface{}{"message": message, "type": typ, "code": code}})
}

func errorKind(status int, message string) (string, string) {
	switch {
	case status == 401:
		return "authentication_error", "invalid_api_key"
	case status == 404:
		return "invalid_request_error", "model_not_found"
	case status == 429:
		return "rate_limit_error", "rate_limit_exceeded"
	case status == 400 && message == contextExceededMessage:
		return "invalid_request_error", "context_length_exceeded"
	case status == 400:
		return "invalid_request_error", "invalid_request"
	case status == 503:
		return "server_error", "overloaded"
	}
	return "server_error", "internal_error"
}

const contextExceededMessage = "the request exceeds the available context size, try increasing it (context length exceeded)"
