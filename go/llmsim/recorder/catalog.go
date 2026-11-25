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
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/saichler/l8opensim/go/llmsim/engine"
)

// Recording modes of a catalog.
const (
	ModeTanto      = "tanto"      // Tanto's system prompt and data tools
	ModeTantoWeb   = "tanto_web"  // plus the web tools ('@web' mode)
	ModeCompaction = "compaction" // Tanto's compaction prompt, no tools
)

// Catalog is one flavor's catalog file.
type Catalog struct {
	Flavor       string           `json:"flavor"`
	Mode         string           `json:"mode"`
	Validators   []string         `json:"validators"`
	ExpectTools  []string         `json:"expect_tools"`
	Samples      int              `json:"samples_per_variant"`
	Variants     int              `json:"variants_per_message"`
	MuseFraction float64          `json:"muse_fraction"`
	Messages     []CatalogMessage `json:"messages"`
}

// CatalogMessage is a scripted conversation: one or more user turns with
// {{param}} slots.
type CatalogMessage struct {
	ID     string              `json:"id"`
	Params map[string][]string `json:"params"`
	Turns  []string            `json:"turns"`
}

// Job is one conversation to record.
type Job struct {
	ID          string
	File        string
	Flavor      string
	Mode        string
	Turns       []string
	Chosen      map[string]string
	Pools       map[string][]string
	Muse        bool
	Validators  []string
	ExpectTools []string
}

// LoadCatalogs reads every *.json catalog in dir.
func LoadCatalogs(dir string) ([]*Catalog, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	out := []*Catalog{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		c := &Catalog{}
		if err = json.Unmarshal(data, c); err != nil {
			return nil, fmt.Errorf("catalog %s: %w", f, err)
		}
		if c.Flavor == "" || len(c.Messages) == 0 {
			return nil, fmt.Errorf("catalog %s: needs a flavor and messages", f)
		}
		if c.Mode == "" {
			c.Mode = ModeTanto
		}
		if c.Samples <= 0 {
			c.Samples = 3
		}
		if c.Variants <= 0 {
			c.Variants = 1
		}
		out = append(out, c)
	}
	return out, nil
}

// Expand turns the catalogs into recording jobs: variants_per_message
// distinct parameter combinations × samples_per_variant samples.
func Expand(cats []*Catalog) []Job {
	jobs := []Job{}
	for _, c := range cats {
		for _, m := range c.Messages {
			names := make([]string, 0, len(m.Params))
			for n := range m.Params {
				names = append(names, n)
			}
			sort.Strings(names)
			for v := 0; v < c.Variants; v++ {
				chosen := map[string]string{}
				idx := v
				for _, n := range names {
					vals := m.Params[n]
					chosen[n] = vals[idx%len(vals)]
					idx /= len(vals)
				}
				turns := make([]string, len(m.Turns))
				for i, t := range m.Turns {
					turns[i] = fill(t, chosen)
				}
				for s := 0; s < c.Samples; s++ {
					id := fmt.Sprintf("%s/%s/v%d/s%d", c.Flavor, m.ID, v, s)
					muse := c.MuseFraction > 0 &&
						float64(engine.HashString(id)%1000) < c.MuseFraction*1000
					jobs = append(jobs, Job{ID: id, Flavor: c.Flavor, Mode: c.Mode, Turns: turns,
						File:   filepath.Join(c.Flavor, fmt.Sprintf("%s-v%d-s%d.json", safeName(m.ID), v, s)),
						Chosen: chosen, Pools: m.Params, Muse: muse,
						Validators: c.Validators, ExpectTools: c.ExpectTools})
				}
			}
		}
	}
	return jobs
}

func fill(t string, chosen map[string]string) string {
	for k, v := range chosen {
		t = strings.ReplaceAll(t, "{{"+k+"}}", v)
	}
	return t
}

func safeName(s string) string {
	b := strings.Builder{}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// TantoSetup is Tanto's prompt and tools, copied from l8ai (tanto_tools.json).
type TantoSetup struct {
	SystemPrompt     string            `json:"system_prompt"`
	CompactionPrompt string            `json:"compaction_prompt"`
	Models           []string          `json:"models"`
	Tools            []json.RawMessage `json:"tools"`
	WebTools         []json.RawMessage `json:"web_tools"`
}

// LoadTanto reads tanto_tools.json.
func LoadTanto(path string) (*TantoSetup, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t := &TantoSetup{}
	if err = json.Unmarshal(data, t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}
