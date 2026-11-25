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
	"go/parser"
	"go/token"
	"os/exec"
	"strings"

	"github.com/saichler/l8opensim/go/llmsim/engine"
)

// Validate checks a raw (not yet templated) recording. Flawed recordings are
// kept and tagged, never discarded: real LLMs produce them too.
func Validate(c *engine.Cassette, validators, expectTools []string) (string, []string) {
	notes := []string{}
	used := map[string]bool{}
	for _, t := range c.Turns {
		for _, s := range t.Steps {
			for _, call := range s.ToolCalls {
				used[call.Name] = true
				if _, bad := call.Arguments["_raw"]; bad {
					notes = append(notes, "malformed tool arguments for "+call.Name)
				}
			}
		}
		last := t.Steps[len(t.Steps)-1]
		if strings.TrimSpace(last.Text) == "" {
			notes = append(notes, "empty final answer")
		}
		if last.FinishReason == "length" {
			notes = append(notes, "answer cut by max_tokens")
		}
	}
	for _, v := range validators {
		switch v {
		case "csv":
			notes = append(notes, checkCSV(c, used)...)
		case "code":
			notes = append(notes, checkCode(c)...)
		case "tool_sequence":
			for _, t := range expectTools {
				if !used[t] {
					notes = append(notes, "expected tool "+t+" was not called")
				}
			}
		}
	}
	if len(notes) > 0 {
		return "flawed", notes
	}
	return "ok", nil
}

// checkCSV looks for a CSV in the answers (fenced, whole-text, or written
// with create_file) and checks that it parses with consistent columns.
func checkCSV(c *engine.Cassette, used map[string]bool) []string {
	found := 0
	notes := []string{}
	check := func(lines []string) {
		rows := nonEmpty(lines)
		if len(rows) < 2 {
			return
		}
		found++
		if _, err := parseCSV(rows); err != nil {
			notes = append(notes, "CSV does not parse: "+err.Error())
		}
	}
	for _, t := range c.Turns {
		for _, s := range t.Steps {
			for _, call := range s.ToolCalls {
				if fn, _ := call.Arguments["file_name"].(string); strings.HasSuffix(strings.ToLower(fn), ".csv") {
					if content, ok := call.Arguments["content"].(string); ok {
						check(strings.Split(content, "\n"))
					}
				}
			}
			for _, block := range fencedBlocks(s.Text, "csv", "") {
				if strings.Contains(block, ",") {
					check(strings.Split(block, "\n"))
				}
			}
			if lines := strings.Split(strings.TrimSpace(s.Text), "\n"); !strings.Contains(s.Text, "```") &&
				len(lines) >= 3 && strings.Count(lines[0], ",") >= 1 {
				check(lines)
			}
		}
	}
	if found == 0 && !used["export_csv"] {
		notes = append(notes, "no CSV in the answer")
	}
	return notes
}

// checkCode parses Go blocks that are whole files and Python blocks (when
// python3 is available).
func checkCode(c *engine.Cassette) []string {
	notes := []string{}
	_, pyErr := exec.LookPath("python3")
	for _, t := range c.Turns {
		for _, s := range t.Steps {
			for _, block := range fencedBlocks(s.Text, "go", "golang") {
				if !isGoFile(block) {
					continue
				}
				if _, err := parser.ParseFile(token.NewFileSet(), "x.go", block, 0); err != nil {
					notes = append(notes, "Go code does not parse: "+firstLine(err.Error()))
				}
			}
			if pyErr != nil {
				continue
			}
			for _, block := range fencedBlocks(s.Text, "python", "py") {
				cmd := exec.Command("python3", "-I", "-c", "import ast,sys,textwrap; ast.parse(textwrap.dedent(sys.stdin.read()))")
				cmd.Stdin = strings.NewReader(block)
				if out, err := cmd.CombinedOutput(); err != nil {
					notes = append(notes, "Python code does not parse: "+firstLine(string(out)))
				}
			}
		}
	}
	return notes
}

// fencedBlocks returns the bodies of ``` blocks whose language is one of langs.
func fencedBlocks(text string, langs ...string) []string {
	out := []string{}
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		trim := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trim, "```") {
			continue
		}
		lang := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trim, "```")))
		end := i + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "```") {
			end++
		}
		for _, l := range langs {
			if lang == l {
				out = append(out, strings.Join(lines[i+1:minInt(end, len(lines))], "\n"))
				break
			}
		}
		i = end
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// isGoFile reports whether a Go block is a whole file: its first line that
// is not blank or a comment is a package clause.
func isGoFile(block string) bool {
	for _, line := range strings.Split(block, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		return strings.HasPrefix(t, "package ")
	}
	return false
}
