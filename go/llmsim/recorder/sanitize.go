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
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/saichler/l8opensim/go/llmsim/engine"
)

var (
	placeholderRe = regexp.MustCompile(`\{\{[a-z]+(:[^{}\s"]*)?\}\}`)
	uuidRe        = regexp.MustCompile(`\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	dateRe        = regexp.MustCompile(`\b20\d\d-[01]\d-[0-3]\d\b`)
	nameRe        = regexp.MustCompile(`\b([A-Z][a-z]+) ([A-Z][a-z]+)\b`)
	secretRe      = regexp.MustCompile(`sk-[A-Za-z0-9-]{16,}`)
)

// sanCtx carries the per-cassette state of templating.
type sanCtx struct {
	c       *engine.Cassette
	chosen  map[string]string
	ids     map[string]string // original value → placeholder, for uuid/date/name/city
	counter map[string]int
	first   map[string]bool
	cityRe  *regexp.Regexp
}

// Sanitize turns a recorded cassette into a template: secrets are removed,
// the catalog parameters, CSV rows, names, cities, dates, UUIDs and numbers
// in prose become placeholders, and each step's original size is kept for
// token rescaling.
func Sanitize(c *engine.Cassette, chosen map[string]string, pools map[string][]string) {
	sc := &sanCtx{c: c, chosen: chosen, ids: map[string]string{}, counter: map[string]int{}, first: map[string]bool{}}
	for _, n := range engine.FirstNames() {
		sc.first[n] = true
	}
	skip := map[string]bool{}
	for _, v := range chosen {
		skip[v] = true
	}
	cityList := []string{}
	for _, city := range engine.Cities() {
		if !skip[city] {
			cityList = append(cityList, regexp.QuoteMeta(city))
		}
	}
	sort.Slice(cityList, func(i, j int) bool { return len(cityList[i]) > len(cityList[j]) })
	sc.cityRe = regexp.MustCompile(`\b(` + strings.Join(cityList, "|") + `)\b`)
	if len(chosen) > 0 {
		c.Params = map[string][]string{}
		for k := range chosen {
			c.Params[k] = pools[k]
		}
	}
	for ti := range c.Turns {
		t := &c.Turns[ti]
		t.UserMessage = scrub(t.UserMessage)
		for si := range t.Steps {
			s := &t.Steps[si]
			s.Chars = len(s.Text) + len(s.Reasoning)
			for _, call := range s.ToolCalls {
				data, _ := json.Marshal(call.Arguments)
				s.Chars += len(call.Name) + len(data)
			}
			s.Text = sc.text(scrub(s.Text))
			s.Reasoning = sc.text(scrub(s.Reasoning))
			for ci := range s.ToolCalls {
				sc.args(&s.ToolCalls[ci])
			}
			for ri := range s.ToolResults {
				s.ToolResults[ri] = scrub(s.ToolResults[ri])
			}
		}
	}
}

func scrub(s string) string {
	return secretRe.ReplaceAllString(s, "sk-REDACTED")
}

// args templates the string leaves of a tool call: parameters everywhere,
// and the full text treatment for a written file's content.
func (sc *sanCtx) args(call *engine.RecordedCall) {
	csvFile := false
	if fn, ok := call.Arguments["file_name"].(string); ok && strings.HasSuffix(strings.ToLower(fn), ".csv") {
		csvFile = true
	}
	for k, v := range call.Arguments {
		s, ok := v.(string)
		if !ok {
			continue
		}
		s = strings.ReplaceAll(scrub(s), "{{", "{{{{")
		switch {
		case k == "content" && csvFile:
			if lines := strings.Split(strings.TrimRight(s, "\n"), "\n"); csvLike(lines) {
				s = sc.csvBlock(lines)
			}
		case k == "content":
			s = sc.textEscaped(s)
		default:
			s = sc.params(s)
		}
		call.Arguments[k] = s
	}
}

// text templates a free text (an answer or reasoning).
func (sc *sanCtx) text(s string) string {
	if s == "" {
		return s
	}
	return sc.textEscaped(strings.ReplaceAll(s, "{{", "{{{{"))
}

// textEscaped processes a text whose literal "{{" are already escaped: CSV
// blocks are regenerated, code keeps only parameter changes, prose gets the
// full treatment.
func (sc *sanCtx) textEscaped(s string) string {
	lines := strings.Split(s, "\n")
	if csvLike(lines) {
		return sc.csvBlock(lines)
	}
	out := make([]string, 0, len(lines))
	prose := []string{}
	flush := func() {
		if len(prose) > 0 {
			out = append(out, sc.prose(strings.Join(prose, "\n")))
			prose = prose[:0]
		}
	}
	for i := 0; i < len(lines); i++ {
		trim := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trim, "```") {
			prose = append(prose, lines[i])
			continue
		}
		flush()
		lang := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trim, "```")))
		end := i + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "```") {
			end++
		}
		body := lines[i+1 : minInt(end, len(lines))]
		out = append(out, lines[i])
		if (lang == "csv" || lang == "" || lang == "text") && csvLike(body) {
			out = append(out, sc.csvBlock(body))
		} else if len(body) > 0 {
			out = append(out, sc.params(strings.Join(body, "\n")))
		}
		if end < len(lines) {
			out = append(out, lines[end])
		}
		i = end
	}
	flush()
	return strings.Join(out, "\n")
}

// csvBlock keeps the header line and replaces the data rows with a
// {{csv:<index>}} placeholder generated from the observed columns.
func (sc *sanCtx) csvBlock(lines []string) string {
	rows := nonEmpty(lines)
	spec := buildCSVSpec(rows)
	if spec == nil {
		return strings.Join(lines, "\n")
	}
	sc.c.CSV = append(sc.c.CSV, *spec)
	return rows[0] + "\n" + fmt.Sprintf("{{csv:%d}}", len(sc.c.CSV)-1)
}

// params replaces the catalog parameter values (and their case variants).
func (sc *sanCtx) params(s string) string {
	names := make([]string, 0, len(sc.chosen))
	for n := range sc.chosen {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return len(sc.chosen[names[i]]) > len(sc.chosen[names[j]]) })
	for _, n := range names {
		v := sc.chosen[n]
		if len(v) < 3 {
			continue
		}
		variants := []struct{ val, mode string }{{v, ""}, {titleCase(v), "title"}, {strings.ToUpper(v), "upper"},
			{strings.ToLower(v), "lower"}}
		if strings.Contains(v, " ") {
			variants = append(variants, struct{ val, mode string }{strings.ToLower(strings.Join(strings.Fields(v), "_")), "snake"})
		}
		done := map[string]bool{}
		for _, vr := range variants {
			if done[vr.val] {
				continue
			}
			done[vr.val] = true
			ph := "{{p:" + n + "}}"
			if vr.mode != "" && vr.val != v {
				ph = "{{p:" + n + ":" + vr.mode + "}}"
			}
			s = outsidePlaceholders(s, func(part string) string { return strings.ReplaceAll(part, vr.val, ph) })
		}
	}
	return s
}

// prose applies every replacement to non-code text.
func (sc *sanCtx) prose(s string) string {
	s = sc.params(s)
	return outsidePlaceholders(s, func(part string) string {
		part = uuidRe.ReplaceAllStringFunc(part, func(m string) string { return sc.id("uuid", m) })
		part = dateRe.ReplaceAllStringFunc(part, func(m string) string { return sc.id("date", m) })
		part = nameRe.ReplaceAllStringFunc(part, func(m string) string {
			if sc.first[strings.SplitN(m, " ", 2)[0]] {
				return sc.id("name", m)
			}
			return m
		})
		part = sc.cityRe.ReplaceAllStringFunc(part, func(m string) string { return sc.id("city", m) })
		return numbers(part)
	})
}

// id returns a stable placeholder per distinct original value.
func (sc *sanCtx) id(kind, value string) string {
	key := kind + "\x00" + value
	if ph, ok := sc.ids[key]; ok {
		return ph
	}
	sc.counter[kind]++
	ph := fmt.Sprintf("{{%s:%d}}", kind, sc.counter[kind])
	sc.ids[key] = ph
	return ph
}

// outsidePlaceholders applies fn only to the text between placeholders.
func outsidePlaceholders(s string, fn func(string) string) string {
	locs := placeholderRe.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return fn(s)
	}
	b := strings.Builder{}
	prev := 0
	for _, l := range locs {
		b.WriteString(fn(s[prev:l[0]]))
		b.WriteString(s[l[0]:l[1]])
		prev = l[1]
	}
	b.WriteString(fn(s[prev:]))
	return b.String()
}

// numbers replaces standalone integers 10..9,999,999 (not years, versions,
// IPs, ids or parts of words) with a ±20% range.
func numbers(s string) string {
	b := strings.Builder{}
	for i := 0; i < len(s); {
		if !isDigit(s[i]) {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		num := s[i:j]
		if standalone(s, i, j) {
			var v int64
			fmt.Sscan(num, &v)
			if v >= 10 && v <= 9999999 && (v < 1900 || v > 2100) {
				lo, hi := v*8/10, (v*12+9)/10
				b.WriteString(fmt.Sprintf("{{int:%d:%d}}", lo, hi))
				i = j
				continue
			}
		}
		b.WriteString(num)
		i = j
	}
	return b.String()
}

func standalone(s string, i, j int) bool {
	if i > 0 {
		p := s[i-1]
		if isAlnum(p) || strings.IndexByte("._-/:#$v", p) >= 0 {
			return false
		}
		if p == ',' && i > 1 && isDigit(s[i-2]) {
			return false
		}
	}
	if j < len(s) {
		n := s[j]
		if isAlnum(n) || strings.IndexByte("_/:%-x", n) >= 0 {
			return false
		}
		if (n == '.' || n == ',') && j+1 < len(s) && isDigit(s[j+1]) {
			return false
		}
	}
	return true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isAlnum(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func titleCase(v string) string {
	words := strings.Fields(v)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
