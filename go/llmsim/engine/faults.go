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
	"math/rand/v2"
	"strings"
)

// Fault names. Rates are fractions of requests (0..1) in the profile.
const (
	FaultHTTP429         = "http_429"
	FaultHTTP503         = "http_503"
	FaultHTTP500         = "http_500"
	FaultHTTP400         = "http_400"
	FaultDropConnection  = "drop_connection"
	FaultSlowResponse    = "slow_response"
	FaultContextExceeded = "context_exceeded"
	FaultTruncated       = "truncated_length"
	FaultMalformedTool   = "malformed_tool_json"
	FaultBadModel        = "bad_model_in_tool"
	FaultEmptyAnswer     = "empty_answer"
)

// faultOrder fixes the draw order so a single random number selects at most
// one fault per request.
var faultOrder = []string{FaultHTTP429, FaultHTTP503, FaultHTTP500, FaultHTTP400, FaultDropConnection,
	FaultSlowResponse, FaultContextExceeded, FaultTruncated, FaultMalformedTool, FaultBadModel, FaultEmptyAnswer}

func knownFault(name string) bool {
	for _, f := range faultOrder {
		if f == name {
			return true
		}
	}
	return false
}

// drawFault picks the fault for this request, or "".
func drawFault(p *runtimeProfile) string {
	if len(p.Faults) == 0 {
		return ""
	}
	r := rand.Float64()
	for _, name := range faultOrder {
		rate := p.Faults[name]
		if rate <= 0 {
			continue
		}
		if r < rate {
			return name
		}
		r -= rate
	}
	return ""
}

// isReplyFault reports whether the fault changes the reply rather than
// replacing it with an error.
func isReplyFault(f string) bool {
	return f == FaultTruncated || f == FaultMalformedTool || f == FaultBadModel || f == FaultEmptyAnswer
}

// applyReplyFault degrades a built reply. Faults that do not apply to the
// reply (a malformed-arguments fault on a text answer) leave it unchanged
// and return false.
func applyReplyFault(f string, rep *Reply) bool {
	switch f {
	case FaultTruncated:
		rep.Calls = nil
		cut := len(rep.Text) / 3
		for cut > 0 && cut < len(rep.Text) && (rep.Text[cut]&0xC0) == 0x80 {
			cut--
		}
		rep.Text = rep.Text[:cut]
		rep.Finish = FinishLength
		rep.CompletionTokens = rep.CompletionTokens/3 + 1
		return true
	case FaultMalformedTool:
		if len(rep.Calls) == 0 {
			return false
		}
		raw := strings.TrimSuffix(callArgs(&rep.Calls[0]), "}")
		rep.Calls[0].Raw = raw + `,"`
		return true
	case FaultBadModel:
		for i := range rep.Calls {
			if _, ok := rep.Calls[i].Args["model"]; ok {
				rep.Calls[i].Args["model"] = "NoSuchModel"
				return true
			}
			if q, ok := rep.Calls[i].Args["query"].(string); ok {
				if fixed, changed := replaceFrom(q, "NoSuchModel"); changed {
					rep.Calls[i].Args["query"] = fixed
					return true
				}
			}
		}
		return false
	case FaultEmptyAnswer:
		rep.Calls = nil
		rep.Text = ""
		rep.Finish = FinishStop
		return true
	}
	return false
}

func replaceFrom(q, model string) (string, bool) {
	i := strings.Index(strings.ToLower(q), " from ")
	if i < 0 {
		return q, false
	}
	start := i + len(" from ")
	end := start
	for end < len(q) && !isSpace(q[end]) {
		end++
	}
	return q[:start] + model + q[end:], true
}
