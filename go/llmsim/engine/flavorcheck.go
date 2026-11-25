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
	"encoding/csv"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// RenderStep renders one step of a cassette for a seed, as replay would
// (without tool fitting).
func RenderStep(c *Cassette, turn, step int, seed uint64) (text, reasoning string, calls []OutCall) {
	rc := &renderCtx{seed: mix(seed, hashString(c.ID)), c: c}
	st := c.turns[turn].steps[step]
	for i, call := range st.calls {
		args, _ := call.args.render(rc).(map[string]interface{})
		calls = append(calls, OutCall{ID: callID(APIOpenAI, seed, step, i), Name: call.name, Args: args})
	}
	return st.text.RenderString(rc), st.reasoning.RenderString(rc), calls
}

// CheckReport is the result of FlavorCheck.
type CheckReport struct {
	Cassettes int
	Steps     int
	Static    int      // steps without any placeholder (identical for every conversation)
	Failures  []string // hard failures
	Warnings  []string
	ByFlavor  map[string]int
}

// FlavorCheck renders every step of every cassette with several seeds and
// checks that the renders differ, CSV still parses, tool arguments are valid
// JSON, code-block languages are unchanged and the length stays within 10%
// of the original.
func FlavorCheck(lib *Library, seeds int) *CheckReport {
	if seeds < 2 {
		seeds = 2
	}
	rep := &CheckReport{ByFlavor: map[string]int{}}
	ids := make([]string, 0, len(lib.byID))
	for id := range lib.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c := lib.byID[id]
		rep.Cassettes++
		rep.ByFlavor[c.Flavor]++
		for ti, t := range c.turns {
			for si, st := range t.steps {
				rep.Steps++
				checkStep(rep, c, ti, si, st, seeds)
			}
		}
	}
	return rep
}

func checkStep(rep *CheckReport, c *Cassette, ti, si int, st *compiledStep, seeds int) {
	where := fmt.Sprintf("%s turn %d step %d", c.ID, ti, si)
	renders := map[string]bool{}
	static := st.text.Static() && st.reasoning.Static()
	for _, call := range st.calls {
		if !argStatic(call.args) {
			static = false
		}
	}
	if static {
		rep.Static++
	}
	origLang := fenceLangs(st.src.Text)
	for s := 0; s < seeds; s++ {
		text, reasoning, calls := RenderStep(c, ti, si, uint64(s+1)*0x9e3779b97f4a7c15)
		size := len(text) + len(reasoning)
		sig := text + "\x00" + reasoning
		for _, call := range calls {
			data, err := json.Marshal(call.Args)
			if err != nil {
				rep.Failures = append(rep.Failures, where+": tool arguments are not valid JSON")
			}
			size += len(call.Name) + len(data)
			sig += string(data)
		}
		renders[sig] = true
		if fenceLangs(text) != origLang {
			rep.Failures = append(rep.Failures, where+": code block languages changed")
		}
		if err := csvBlocksValid(c, st, uint64(s+1)); err != nil {
			rep.Failures = append(rep.Failures, where+": CSV does not parse: "+err.Error())
		}
		if st.origChars > 200 {
			ratio := float64(size) / float64(st.origChars)
			if ratio < 0.9 || ratio > 1.1 {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: length %.0f%% of the original", where, ratio*100))
			}
		}
	}
	if !static && len(renders) < 2 {
		rep.Failures = append(rep.Failures, where+": placeholders render identically for every seed")
	}
}

func argStatic(n *argNode) bool {
	switch {
	case n.tmpl != nil:
		return n.tmpl.Static()
	case n.obj != nil:
		for _, v := range n.obj {
			if !argStatic(v) {
				return false
			}
		}
	case n.list != nil:
		for _, v := range n.list {
			if !argStatic(v) {
				return false
			}
		}
	}
	return true
}

func fenceLangs(text string) string {
	langs := []string{}
	in := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			if !in {
				langs = append(langs, strings.TrimSpace(strings.TrimPrefix(t, "```")))
			}
			in = !in
		}
	}
	return strings.Join(langs, ",")
}

// csvBlocksValid regenerates every CSV block the step uses (in its text and
// in tool arguments) and parses it with its header.
func csvBlocksValid(c *Cassette, st *compiledStep, seed uint64) error {
	idx := map[int]bool{}
	collect := func(t *Template) {
		for _, sg := range t.segs {
			if sg.ph != nil && sg.ph.kind == "csv" && len(sg.ph.args) > 0 {
				var n int
				if _, err := fmt.Sscan(sg.ph.args[0], &n); err == nil {
					idx[n] = true
				}
			}
		}
	}
	collect(st.text)
	for _, call := range st.calls {
		walkArgs(call.args, collect)
	}
	for n := range idx {
		if n < 0 || n >= len(c.CSV) {
			return fmt.Errorf("unknown csv block %d", n)
		}
		spec := &c.CSV[n]
		hb := strings.Builder{}
		w := csv.NewWriter(&hb)
		_ = w.Write(spec.Header)
		w.Flush()
		body := hb.String() + generateCSVRows(spec, seed)
		r := csv.NewReader(strings.NewReader(body))
		r.FieldsPerRecord = len(spec.Header)
		if _, err := r.ReadAll(); err != nil {
			return err
		}
	}
	return nil
}

func walkArgs(n *argNode, fn func(*Template)) {
	switch {
	case n.tmpl != nil:
		fn(n.tmpl)
	case n.obj != nil:
		for _, v := range n.obj {
			walkArgs(v, fn)
		}
	case n.list != nil:
		for _, v := range n.list {
			walkArgs(v, fn)
		}
	}
}
