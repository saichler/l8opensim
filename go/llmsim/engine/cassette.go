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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type compiledTurn struct {
	userNorm string
	steps    []*compiledStep
}

type compiledStep struct {
	reasoning *Template
	text      *Template
	calls     []compiledCall
	src       *Step
	origChars int
}

type compiledCall struct {
	name string
	args *argNode
}

// argNode is a pre-parsed JSON value whose string leaves are templates.
type argNode struct {
	tmpl *Template
	obj  map[string]*argNode
	list []*argNode
	lit  interface{}
}

// cassetteRef points at one turn of one cassette.
type cassetteRef struct {
	c    *Cassette
	turn int
}

// Library is the loaded, indexed set of cassettes.
type Library struct {
	byID     map[string]*Cassette
	byFlavor map[string]*flavorSet
	byUser   map[string][]cassetteRef
	search   map[string]string // normalized web_search query → recorded result JSON
	fetch    map[string]string // URL → recorded fetch result JSON
	flavors  []string
}

type flavorSet struct {
	ok     []*Cassette
	flawed []*Cassette
}

// LoadLibrary reads every *.json cassette under dir (recursively).
func LoadLibrary(dir string) (*Library, error) {
	lib := newLibrary()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		c := &Cassette{}
		if err = json.Unmarshal(data, c); err != nil {
			return fmt.Errorf("cassette %s: %w", path, err)
		}
		if err = lib.Add(c); err != nil {
			return fmt.Errorf("cassette %s: %w", path, err)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	lib.finish()
	return lib, nil
}

func newLibrary() *Library {
	return &Library{byID: map[string]*Cassette{}, byFlavor: map[string]*flavorSet{},
		byUser: map[string][]cassetteRef{}, search: map[string]string{}, fetch: map[string]string{}}
}

// NewLibrary builds a library from in-memory cassettes.
func NewLibrary(cassettes []*Cassette) (*Library, error) {
	lib := newLibrary()
	for _, c := range cassettes {
		if err := lib.Add(c); err != nil {
			return nil, err
		}
	}
	lib.finish()
	return lib, nil
}

// Add compiles and indexes one cassette.
func (lib *Library) Add(c *Cassette) error {
	if c.ID == "" || c.Flavor == "" || len(c.Turns) == 0 {
		return errors.New("cassette needs id, flavor and at least one turn")
	}
	if _, dup := lib.byID[c.ID]; dup {
		return errors.New("duplicate cassette id " + c.ID)
	}
	if err := c.compile(); err != nil {
		return err
	}
	lib.byID[c.ID] = c
	set := lib.byFlavor[c.Flavor]
	if set == nil {
		set = &flavorSet{}
		lib.byFlavor[c.Flavor] = set
	}
	if c.Quality == "flawed" {
		set.flawed = append(set.flawed, c)
	} else {
		set.ok = append(set.ok, c)
	}
	for i, t := range c.turns {
		if t.userNorm != "" {
			lib.byUser[t.userNorm] = append(lib.byUser[t.userNorm], cassetteRef{c: c, turn: i})
		}
	}
	lib.indexWeb(c)
	return nil
}

func (lib *Library) finish() {
	lib.flavors = lib.flavors[:0]
	for f, set := range lib.byFlavor {
		lib.flavors = append(lib.flavors, f)
		sort.Slice(set.ok, func(i, j int) bool { return set.ok[i].ID < set.ok[j].ID })
		sort.Slice(set.flawed, func(i, j int) bool { return set.flawed[i].ID < set.flawed[j].ID })
	}
	sort.Strings(lib.flavors)
}

// indexWeb remembers recorded web tool results so /v1/search and /v1/fetch
// can return them.
func (lib *Library) indexWeb(c *Cassette) {
	for _, t := range c.Turns {
		for _, s := range t.Steps {
			for i, call := range s.ToolCalls {
				if i >= len(s.ToolResults) {
					break
				}
				switch call.Name {
				case "web_search":
					if q, ok := call.Arguments["query"].(string); ok {
						lib.search[normalize(q)] = s.ToolResults[i]
					}
				case "fetch_url":
					if u, ok := call.Arguments["url"].(string); ok {
						lib.fetch[u] = s.ToolResults[i]
					}
				}
			}
		}
	}
}

// Size returns the number of cassettes.
func (lib *Library) Size() int { return len(lib.byID) }

// Flavors returns the flavors that have cassettes.
func (lib *Library) Flavors() []string { return lib.flavors }

// Cassette returns a cassette by id.
func (lib *Library) Cassette(id string) *Cassette { return lib.byID[id] }

func (c *Cassette) compile() error {
	c.turns = make([]*compiledTurn, 0, len(c.Turns))
	for ti := range c.Turns {
		t := &c.Turns[ti]
		if len(t.Steps) == 0 {
			return fmt.Errorf("turn %d has no steps", ti)
		}
		ct := &compiledTurn{userNorm: normalize(t.UserMessage)}
		for si := range t.Steps {
			s := &t.Steps[si]
			cs := &compiledStep{reasoning: ParseTemplate(s.Reasoning), text: ParseTemplate(s.Text), src: s}
			for _, call := range s.ToolCalls {
				cs.calls = append(cs.calls, compiledCall{name: call.Name, args: compileArg(call.Arguments)})
			}
			cs.origChars = s.Chars
			if cs.origChars <= 0 {
				cs.origChars = len(s.Text) + len(s.Reasoning)
			}
			ct.steps = append(ct.steps, cs)
		}
		c.turns = append(c.turns, ct)
	}
	return nil
}

func compileArg(v interface{}) *argNode {
	switch x := v.(type) {
	case string:
		return &argNode{tmpl: ParseTemplate(x)}
	case map[string]interface{}:
		n := &argNode{obj: map[string]*argNode{}}
		for k, val := range x {
			n.obj[k] = compileArg(val)
		}
		return n
	case []interface{}:
		n := &argNode{list: []*argNode{}}
		for _, val := range x {
			n.list = append(n.list, compileArg(val))
		}
		return n
	}
	return &argNode{lit: v}
}

func (n *argNode) render(rc *renderCtx) interface{} {
	switch {
	case n.tmpl != nil:
		return n.tmpl.RenderString(rc)
	case n.obj != nil:
		out := make(map[string]interface{}, len(n.obj))
		for k, v := range n.obj {
			out[k] = v.render(rc)
		}
		return out
	case n.list != nil:
		out := make([]interface{}, len(n.list))
		for i, v := range n.list {
			out[i] = v.render(rc)
		}
		return out
	}
	return n.lit
}

// finalTextStep returns the last step of a turn that has text, or nil.
func (t *compiledTurn) finalTextStep() *compiledStep {
	for i := len(t.steps) - 1; i >= 0; i-- {
		if t.steps[i].src.Text != "" {
			return t.steps[i]
		}
	}
	return nil
}
