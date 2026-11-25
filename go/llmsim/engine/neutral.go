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
	"encoding/json"
	"strings"
)

// Helpers shared by the OpenAI and Anthropic parsers.

// textOf extracts the text of a message content that is either a JSON string
// or an array of blocks with "type":"text". It also reports whether the
// content holds tool results (Anthropic tool_result blocks).
func textOf(raw json.RawMessage) (text string, toolResult bool) {
	if len(raw) == 0 {
		return "", false
	}
	if raw[0] == '"' {
		_ = json.Unmarshal(raw, &text)
		return text, false
	}
	if raw[0] != '[' {
		return "", false
	}
	blocks := []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{}
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	b := strings.Builder{}
	for _, blk := range blocks {
		switch blk.Type {
		case "text", "input_text":
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(blk.Text)
		case "tool_result":
			toolResult = true
		}
	}
	return b.String(), toolResult
}

// hasBlockType reports whether a content array holds a block of the type.
func hasBlockType(raw json.RawMessage, typ string) bool {
	if len(raw) == 0 || raw[0] != '[' {
		return false
	}
	blocks := []struct {
		Type string `json:"type"`
	}{}
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == typ {
			return true
		}
	}
	return false
}

// parseEnums reads the enums of a JSON schema's top-level properties.
func parseEnums(schema json.RawMessage) map[string][]string {
	if len(schema) == 0 {
		return nil
	}
	s := struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}{}
	if json.Unmarshal(schema, &s) != nil {
		return nil
	}
	var out map[string][]string
	for name, p := range s.Properties {
		if len(p.Enum) > 0 {
			if out == nil {
				out = map[string][]string{}
			}
			out[name] = p.Enum
		}
	}
	return out
}

// normalize lowercases, collapses whitespace, replaces masking tokens such as
// [NAME_AB12CD] with [x] and removes #sim: markers, so equal questions map to
// equal keys.
func normalize(s string) string {
	b := strings.Builder{}
	b.Grow(len(s))
	space := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '#' && strings.HasPrefix(s[i:], "#sim:") {
			for i < len(s) && !isSpace(s[i]) {
				i++
			}
			space = b.Len() > 0
			continue
		}
		if c == '[' {
			if n := maskTokenLen(s[i:]); n > 0 {
				if space {
					b.WriteByte(' ')
					space = false
				}
				b.WriteString("[x]")
				i += n - 1
				continue
			}
		}
		if isSpace(c) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// maskTokenLen returns the length of a [WORD_ALNUM] token at the start of s.
func maskTokenLen(s string) int {
	i := 1
	for i < len(s) && s[i] >= 'A' && s[i] <= 'Z' {
		i++
	}
	if i == 1 || i >= len(s) || s[i] != '_' {
		return 0
	}
	i++
	start := i
	for i < len(s) && ((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	if i == start || i >= len(s) || s[i] != ']' {
		return 0
	}
	return i + 1
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\t' || c == '\r'
}

// parseMarker returns the value of a #sim:<value> marker, if any.
func parseMarker(s string) string {
	i := strings.Index(s, "#sim:")
	if i < 0 {
		return ""
	}
	j := i + 5
	for j < len(s) && !isSpace(s[j]) {
		j++
	}
	return s[i+5 : j]
}

// stripMarker removes #sim: markers from a text.
func stripMarker(s string) string {
	for {
		i := strings.Index(s, "#sim:")
		if i < 0 {
			return s
		}
		j := i + 5
		for j < len(s) && !isSpace(s[j]) {
			j++
		}
		for j < len(s) && s[j] == ' ' {
			j++
		}
		s = s[:i] + s[j:]
	}
}
