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

// Package recorder records cassettes against a real OpenAI-compatible LLM
// (Halo): it runs the scenario catalog, answers the model's tool calls from
// fixtures, validates and templates the conversations, and offers a
// transparent recording proxy.
package recorder

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/saichler/l8opensim/go/llmsim/engine"
)

// Fixtures is the synthetic data the recorder's data tools read.
type Fixtures struct {
	models  map[string]*fixtureModel
	order   []string
	records map[string][]map[string]interface{} // model → flat records (path → value)
}

type fixtureModel struct {
	Name       string         `json:"name"`
	Service    string         `json:"service"`
	Area       string         `json:"area"`
	PrimaryKey string         `json:"primary_key"`
	Count      int            `json:"count"`
	Fields     []fixtureField `json:"fields"`
}

type fixtureField struct {
	Path   string   `json:"path"`
	Type   string   `json:"type"`
	Gen    string   `json:"gen"`
	Values []string `json:"values"`
}

// LoadFixtures reads the model schemas and generates their records.
func LoadFixtures(path string) (*Fixtures, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc := struct {
		Models []*fixtureModel `json:"models"`
	}{}
	if err = json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("fixtures %s: %w", path, err)
	}
	f := &Fixtures{models: map[string]*fixtureModel{}, records: map[string][]map[string]interface{}{}}
	for _, m := range doc.Models {
		f.models[m.Name] = m
		f.order = append(f.order, m.Name)
		f.records[m.Name] = generateRecords(m)
	}
	return f, nil
}

func generateRecords(m *fixtureModel) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, m.Count)
	for i := 0; i < m.Count; i++ {
		rec := map[string]interface{}{}
		for _, fld := range m.Fields {
			h := engine.Mix(engine.HashString(m.Name+"/"+fld.Path), uint64(i))
			rec[fld.Path] = fieldValue(&fld, i, h)
		}
		out = append(out, rec)
	}
	return out
}

func fieldValue(fld *fixtureField, i int, h uint64) interface{} {
	gen, arg := fld.Gen, ""
	if k := strings.Index(gen, ":"); k >= 0 {
		gen, arg = gen[:k], gen[k+1:]
	}
	switch gen {
	case "id":
		return fmt.Sprintf("%s%03d", arg, i+1)
	case "host":
		return fmt.Sprintf("%s%02d", arg, i+1)
	case "ip":
		return fmt.Sprintf("%s%d", arg, i+1)
	case "serial":
		return fmt.Sprintf("%s%08X", arg, uint32(h))
	case "uptime":
		return fmt.Sprintf("%dd %dh", h%200, (h>>8)%24)
	case "age":
		return fmt.Sprintf("%dd", 1+h%300)
	case "date":
		return time.Now().UTC().Add(-time.Duration(h%72) * time.Hour).Format(time.RFC3339)
	case "ts":
		return time.Now().Add(-time.Duration(h%(7*24*60)) * time.Minute).UnixMilli()
	case "pod":
		return fmt.Sprintf("%s-%x-%s", engine.Pick([]string{"api", "web", "worker", "billing", "search", "auth"}, h),
			uint16(h>>16), strconv.FormatUint(h%46656, 36))
	case "seqvalue":
		if len(fld.Values) > 0 {
			return fld.Values[i%len(fld.Values)]
		}
	case "int":
		parts := strings.Split(arg, ":")
		lo, _ := strconv.Atoi(parts[0])
		hi := lo
		if len(parts) > 1 {
			hi, _ = strconv.Atoi(parts[1])
		}
		if hi <= lo {
			return lo
		}
		return lo + int(h%uint64(hi-lo+1))
	case "choice":
		v, _ := strconv.Atoi(arg)
		return v
	case "bool":
		return h%5 == 0
	}
	if len(fld.Values) > 0 {
		return engine.Pick(fld.Values, h)
	}
	return ""
}

// Answer runs a data tool against the fixtures and returns its JSON result.
func (f *Fixtures) Answer(name string, args map[string]interface{}) string {
	str := func(k string) string {
		s, _ := args[k].(string)
		return s
	}
	switch name {
	case "list_modules":
		list := []map[string]string{}
		for _, n := range f.order {
			m := f.models[n]
			list = append(list, map[string]string{"model": m.Name, "service": m.Service, "area": m.Area,
				"primaryKey": m.PrimaryKey})
		}
		return toJSON(map[string]interface{}{"models": list})
	case "describe_model":
		m := f.models[str("model")]
		if m == nil {
			return toJSON(map[string]string{"error": "unknown model '" + str("model") + "'; use list_modules"})
		}
		fields := []map[string]string{}
		for _, fld := range m.Fields {
			fields = append(fields, map[string]string{"path": fld.Path, "type": fld.Type})
		}
		return toJSON(map[string]interface{}{"model": m.Name, "primaryKey": m.PrimaryKey, "fields": fields})
	case "l8query":
		return f.Query(str("query"), false)
	case "export_csv":
		return f.export(str("query"), args["columns"], str("file_name"))
	case "create_file":
		return toJSON(map[string]interface{}{"attached": true, "file_name": str("file_name"),
			"bytes": len(str("content"))})
	case "create_record", "update_record":
		m := f.models[str("model")]
		if m == nil {
			return toJSON(map[string]string{"error": "unknown model '" + str("model") + "'"})
		}
		return toJSON(map[string]interface{}{"ok": true, "model": m.Name, "action": strings.TrimSuffix(name, "_record")})
	case "delete_record":
		res := f.run(str("query"), true)
		if res.err != "" {
			return toJSON(map[string]string{"error": res.err})
		}
		return toJSON(map[string]interface{}{"ok": true, "deleted": res.total})
	}
	return toJSON(map[string]string{"error": "unknown tool " + name})
}

func (f *Fixtures) export(query string, columns interface{}, fileName string) string {
	res := f.run(query, true)
	if res.err != "" {
		return toJSON(map[string]string{"error": res.err})
	}
	cols := []string{}
	if list, ok := columns.([]interface{}); ok {
		for _, c := range list {
			if s, ok := c.(string); ok {
				cols = append(cols, s)
			}
		}
	}
	if len(cols) == 0 && res.model != nil {
		for _, fld := range res.model.Fields {
			cols = append(cols, fld.Path)
		}
	}
	if fileName == "" && res.model != nil {
		fileName = strings.ToLower(res.model.Name) + ".csv"
	}
	return toJSON(map[string]interface{}{"attached": true, "file_name": fileName, "rows": res.total,
		"columns": cols})
}

func toJSON(v interface{}) string {
	data, _ := json.Marshal(v)
	return string(data)
}
