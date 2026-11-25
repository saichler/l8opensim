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

import "strings"

// remapHints maps a keyword of a recorded tool name to keywords to look for
// in the request's tool names when the recorded tool is not defined.
var remapHints = []struct {
	from string
	to   []string
}{
	{"search", []string{"search"}},
	{"fetch", []string{"fetch", "url", "read", "browse"}},
	{"query", []string{"query", "select", "sql"}},
	{"export", []string{"export", "csv"}},
	{"file", []string{"file", "attach"}},
	{"describe", []string{"describe", "schema", "fields"}},
	{"list", []string{"list", "modules", "models"}},
	{"create", []string{"create", "add", "insert"}},
	{"update", []string{"update", "edit", "patch"}},
	{"delete", []string{"delete", "remove"}},
}

// queryKeywords end a where clause.
var queryKeywords = []string{" sort-by ", " group-by ", " limit "}

// fitCalls makes recorded tool calls valid for this request: unknown tools
// are remapped or dropped, enum parameters (the model) are kept inside the
// request's enum, and queries are rewritten to use an allowed model. It
// returns the fitted calls and the number of substitutions made.
func fitCalls(calls []OutCall, req *Request, seed uint64) ([]OutCall, int) {
	out := calls[:0]
	subs := 0
	for i := range calls {
		c := calls[i]
		def := req.Tools[c.Name]
		if def == nil {
			name := remapTool(c.Name, req)
			if name == "" {
				subs++
				continue
			}
			c.Name = name
			def = req.Tools[name]
			subs++
		}
		for param, enum := range def.Enums {
			v, ok := c.Args[param].(string)
			if !ok || inList(enum, v) {
				continue
			}
			c.Args[param] = nearest(enum, v, seed)
			subs++
		}
		if models := req.ModelEnum(); len(models) > 0 {
			for _, key := range []string{"query"} {
				if q, ok := c.Args[key].(string); ok {
					if fixed, changed := fitQuery(q, models, seed); changed {
						c.Args[key] = fixed
						subs++
					}
				}
			}
		}
		out = append(out, c)
	}
	return out, subs
}

func remapTool(name string, req *Request) string {
	lower := strings.ToLower(name)
	for _, h := range remapHints {
		if !strings.Contains(lower, h.from) {
			continue
		}
		for _, cand := range req.ToolOrder {
			cl := strings.ToLower(cand)
			for _, kw := range h.to {
				if strings.Contains(cl, kw) {
					return cand
				}
			}
		}
	}
	return ""
}

func inList(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// nearest returns the enum value closest to v: same name ignoring case, then
// containment, then the longest common prefix (3+), else a seeded choice.
func nearest(enum []string, v string, seed uint64) string {
	lv := strings.ToLower(v)
	for _, e := range enum {
		if strings.ToLower(e) == lv {
			return e
		}
	}
	for _, e := range enum {
		le := strings.ToLower(e)
		if lv != "" && (strings.Contains(le, lv) || strings.Contains(lv, le)) {
			return e
		}
	}
	best, bestLen := "", 2
	for _, e := range enum {
		le := strings.ToLower(e)
		n := 0
		for n < len(le) && n < len(lv) && le[n] == lv[n] {
			n++
		}
		if n > bestLen {
			best, bestLen = e, n
		}
	}
	if best != "" {
		return best
	}
	return enum[mix(seed, hashString(v))%uint64(len(enum))]
}

// fitQuery rewrites "from <Model>" to an allowed model. When the model
// changes, the where clause (written for the old model's fields) is dropped.
func fitQuery(q string, models []string, seed uint64) (string, bool) {
	lower := strings.ToLower(q)
	i := strings.Index(lower, " from ")
	if i < 0 {
		return q, false
	}
	start := i + len(" from ")
	for start < len(q) && q[start] == ' ' {
		start++
	}
	end := start
	for end < len(q) && !isSpace(q[end]) {
		end++
	}
	model := q[start:end]
	if model == "" || inList(models, model) {
		return q, false
	}
	repl := nearest(models, model, seed)
	rest := q[end:]
	lrest := strings.ToLower(rest)
	if w := strings.Index(lrest, " where "); w >= 0 {
		cut := len(rest)
		for _, kw := range queryKeywords {
			if k := strings.Index(lrest[w+1:], kw); k >= 0 && w+1+k < cut {
				cut = w + 1 + k
			}
		}
		rest = rest[:w] + rest[cut:]
	}
	return q[:start] + repl + rest, true
}
