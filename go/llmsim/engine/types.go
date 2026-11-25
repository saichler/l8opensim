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

// Package engine is the LLM router simulator: it parses OpenAI and Anthropic
// requests, matches them to recorded conversations (cassettes), flavors the
// recorded replies per conversation and answers instantly.
package engine

// APIs served by the simulator.
const (
	APIOpenAI    = "openai"
	APIAnthropic = "anthropic"
	APIWebTools  = "webtools"
)

// Finish reasons, in OpenAI terms (the Anthropic writer maps them).
const (
	FinishStop      = "stop"
	FinishToolCalls = "tool_calls"
	FinishLength    = "length"
)

// Cassette is one recorded conversation: one or more user turns, each with
// the LLM steps (tool-call rounds and the final answer) that followed it.
type Cassette struct {
	ID         string              `json:"id"`
	Flavor     string              `json:"flavor"`
	Model      string              `json:"model"`
	Quality    string              `json:"quality"`
	RecordedAt string              `json:"recorded_at,omitempty"`
	Params     map[string][]string `json:"params,omitempty"`
	CSV        []CSVSpec           `json:"csv,omitempty"`
	Turns      []Turn              `json:"turns"`

	turns []*compiledTurn
}

// Turn is one user message and the LLM steps that answered it.
type Turn struct {
	UserMessage string `json:"user_message"`
	Steps       []Step `json:"steps"`
}

// Step is one LLM response: tool calls, or the text answer.
type Step struct {
	Reasoning    string         `json:"reasoning,omitempty"`
	Text         string         `json:"text,omitempty"`
	ToolCalls    []RecordedCall `json:"tool_calls,omitempty"`
	ToolResults  []string       `json:"tool_results,omitempty"`
	FinishReason string         `json:"finish_reason,omitempty"`
	Usage        Usage          `json:"usage"`
	Chars        int            `json:"chars,omitempty"` // length of the original (un-templated) output
}

// RecordedCall is a tool call as recorded; string leaves of Arguments may
// contain placeholders.
type RecordedCall struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// Usage is the recorded token usage of one step.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// CSVSpec describes a CSV block whose data rows are regenerated per
// conversation by the {{csv:<index>}} placeholder.
type CSVSpec struct {
	Header  []string    `json:"header"`
	Columns []CSVColumn `json:"columns"`
	Rows    int         `json:"rows"`
}

// CSVColumn describes how to generate one CSV column.
// Kinds: seq, int, float, date, name, email, city, company, choice.
type CSVColumn struct {
	Kind     string   `json:"kind"`
	Min      float64  `json:"min,omitempty"`
	Max      float64  `json:"max,omitempty"`
	Decimals int      `json:"decimals,omitempty"`
	Values   []string `json:"values,omitempty"`
}

// ToolDef is the part of a request's tool definition the simulator needs.
type ToolDef struct {
	Name  string
	Enums map[string][]string // parameter name → allowed values
}

// Request is the provider-neutral view of a chat request.
type Request struct {
	API       string
	Model     string
	MaxTokens int
	System    string
	FirstUser string
	LastUser  string
	UserCount int
	ToolStep  int
	Tools     map[string]*ToolDef
	ToolOrder []string
	NoTools   bool
	BodyLen   int
	Session   string
	Key       string // API key presented

	models []string
}

// Reply is the provider-neutral answer.
type Reply struct {
	Text             string
	Reasoning        string
	Calls            []OutCall
	Finish           string
	PromptTokens     int
	CompletionTokens int

	Flavor     string
	CassetteID string
	Turn       int
	Step       int
	Fault      string
}

// OutCall is a rendered tool call.
type OutCall struct {
	ID   string
	Name string
	Args map[string]interface{}
	Raw  string // when set, sent verbatim instead of Args (malformed JSON fault)
}

// ModelEnum returns the union of the enums of every "model" tool parameter:
// the models the caller may use.
func (r *Request) ModelEnum() []string {
	if r.models != nil {
		return r.models
	}
	seen := map[string]bool{}
	out := []string{}
	for _, name := range r.ToolOrder {
		for _, v := range r.Tools[name].Enums["model"] {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	r.models = out
	return out
}
