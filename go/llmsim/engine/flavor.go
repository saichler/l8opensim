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
	"strconv"
	"strings"
)

// Placeholder kinds recognized inside {{...}}. Anything else is literal text,
// so code that legitimately contains "{{" is left alone. A literal "{{" that
// must not be parsed is written as "{{{{".
var placeholderKinds = map[string]bool{
	"name": true, "first": true, "last": true, "company": true, "city": true,
	"int": true, "money": true, "date": true, "uuid": true, "p": true, "csv": true,
}

type segment struct {
	lit string
	ph  *placeholder
}

type placeholder struct {
	raw  string   // full text between the braces; the hash input
	kind string   // first field
	args []string // remaining ':'-separated fields
}

// Template is a pre-parsed text with placeholders. Rendering is a single pass
// over the segments.
type Template struct {
	segs   []segment
	static bool
	srcLen int
}

// ParseTemplate splits s into literal and placeholder segments.
func ParseTemplate(s string) *Template {
	t := &Template{srcLen: len(s)}
	lit := strings.Builder{}
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "{{{{") {
			lit.WriteString("{{")
			i += 4
			continue
		}
		if strings.HasPrefix(s[i:], "{{") {
			end := strings.Index(s[i+2:], "}}")
			if end > 0 {
				body := s[i+2 : i+2+end]
				if ph := parsePlaceholder(body); ph != nil {
					if lit.Len() > 0 {
						t.segs = append(t.segs, segment{lit: lit.String()})
						lit.Reset()
					}
					t.segs = append(t.segs, segment{ph: ph})
					i += 4 + end
					continue
				}
			}
		}
		lit.WriteByte(s[i])
		i++
	}
	if lit.Len() > 0 {
		t.segs = append(t.segs, segment{lit: lit.String()})
	}
	t.static = true
	for _, sg := range t.segs {
		if sg.ph != nil {
			t.static = false
		}
	}
	return t
}

func parsePlaceholder(body string) *placeholder {
	if body == "" || strings.ContainsAny(body, " \n\t{}\"") {
		return nil
	}
	parts := strings.Split(body, ":")
	if !placeholderKinds[parts[0]] {
		return nil
	}
	return &placeholder{raw: body, kind: parts[0], args: parts[1:]}
}

// Static reports whether the template has no placeholders.
func (t *Template) Static() bool { return t.static }

// renderCtx carries the per-conversation seed and the cassette (for params
// and CSV specs). Values are a pure function of (seed, placeholder), so the
// same placeholder renders identically in every step and turn of a
// conversation, and differently across conversations.
type renderCtx struct {
	seed      uint64
	c         *Cassette
	overrides map[string]string // param → value named in the user's message
}

// newRenderCtx prepares rendering for a conversation. A parameter whose pool
// value appears in the user's message takes that value, so the reply talks
// about what the user asked for (e.g. the site they named).
func newRenderCtx(seed uint64, c *Cassette, userText string) *renderCtx {
	rc := &renderCtx{seed: seed, c: c}
	if len(c.Params) == 0 || userText == "" {
		return rc
	}
	lower := strings.ToLower(userText)
	for name, pool := range c.Params {
		best := ""
		for _, v := range pool {
			if len(v) > len(best) && strings.Contains(lower, strings.ToLower(v)) {
				best = v
			}
		}
		if best != "" {
			if rc.overrides == nil {
				rc.overrides = map[string]string{}
			}
			rc.overrides[name] = best
		}
	}
	return rc
}

// Render writes the flavored text into b.
func (t *Template) Render(rc *renderCtx, b *strings.Builder) {
	for _, sg := range t.segs {
		if sg.ph == nil {
			b.WriteString(sg.lit)
			continue
		}
		b.WriteString(rc.value(sg.ph))
	}
}

// RenderString renders to a new string.
func (t *Template) RenderString(rc *renderCtx) string {
	if t.static && len(t.segs) <= 1 {
		if len(t.segs) == 0 {
			return ""
		}
		return t.segs[0].lit
	}
	b := strings.Builder{}
	b.Grow(t.srcLen + 64)
	t.Render(rc, &b)
	return b.String()
}

func (rc *renderCtx) value(ph *placeholder) string {
	h := mix(rc.seed, hashString(ph.raw))
	switch ph.kind {
	case "name":
		return pick(firstNames, h) + " " + pick(lastNames, mix(h, 1))
	case "first":
		return pick(firstNames, h)
	case "last":
		return pick(lastNames, h)
	case "company":
		return pick(companies, h)
	case "city":
		return pick(cities, h)
	case "int":
		lo, hi := argInt(ph.args, 0, 1), argInt(ph.args, 1, 100)
		if hi < lo {
			lo, hi = hi, lo
		}
		return strconv.FormatInt(lo+int64(h%uint64(hi-lo+1)), 10)
	case "money":
		lo, hi := argFloat(ph.args, 0, 1), argFloat(ph.args, 1, 1000)
		v := lo + (hi-lo)*unitFloat(h)
		return strconv.FormatFloat(v, 'f', 2, 64)
	case "date":
		return dateBack(int(h % 365))
	case "uuid":
		return uuidFrom(h, mix(h, 7))
	case "p":
		return rc.param(ph)
	case "csv":
		return rc.csvRows(ph)
	}
	return ""
}

// param renders {{p:<name>[:title|upper|lower|snake|camel]}}: one value from
// the cassette's param pool, the same for every occurrence of the param.
func (rc *renderCtx) param(ph *placeholder) string {
	if len(ph.args) == 0 || rc.c == nil {
		return ""
	}
	v, ok := rc.overrides[ph.args[0]]
	if !ok {
		pool := rc.c.Params[ph.args[0]]
		if len(pool) == 0 {
			return ""
		}
		v = pick(pool, mix(rc.seed, hashString("p:"+ph.args[0])))
	}
	if len(ph.args) > 1 {
		v = transformCase(v, ph.args[1])
	}
	return v
}

func (rc *renderCtx) csvRows(ph *placeholder) string {
	if len(ph.args) == 0 || rc.c == nil {
		return ""
	}
	idx, err := strconv.Atoi(ph.args[0])
	if err != nil || idx < 0 || idx >= len(rc.c.CSV) {
		return ""
	}
	return generateCSVRows(&rc.c.CSV[idx], mix(rc.seed, hashString(ph.raw)))
}

func transformCase(v, mode string) string {
	switch mode {
	case "upper":
		return strings.ToUpper(v)
	case "lower":
		return strings.ToLower(v)
	case "title":
		words := strings.Fields(v)
		for i, w := range words {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
		return strings.Join(words, " ")
	case "snake":
		return strings.ToLower(strings.Join(strings.Fields(v), "_"))
	case "camel":
		words := strings.Fields(strings.ToLower(v))
		for i := 1; i < len(words); i++ {
			words[i] = strings.ToUpper(words[i][:1]) + words[i][1:]
		}
		return strings.Join(words, "")
	}
	return v
}

func argInt(args []string, i int, def int64) int64 {
	if i < len(args) {
		if v, err := strconv.ParseInt(args[i], 10, 64); err == nil {
			return v
		}
	}
	return def
}

func argFloat(args []string, i int, def float64) float64 {
	if i < len(args) {
		if v, err := strconv.ParseFloat(args[i], 64); err == nil {
			return v
		}
	}
	return def
}
