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
	"encoding/csv"
	"regexp"
	"strconv"
	"strings"

	"github.com/saichler/l8opensim/go/llmsim/engine"
)

var isoDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// csvLike reports whether the lines are a CSV table: at least a header and
// two data rows, every row with the same number (2+) of fields.
func csvLike(lines []string) bool {
	rows := nonEmpty(lines)
	if len(rows) < 3 {
		return false
	}
	recs, err := parseCSV(rows)
	if err != nil || len(recs[0]) < 2 {
		return false
	}
	for _, line := range rows {
		if strings.HasPrefix(strings.TrimSpace(line), "|") || strings.HasPrefix(strings.TrimSpace(line), "```") {
			return false
		}
	}
	return true
}

func parseCSV(rows []string) ([][]string, error) {
	r := csv.NewReader(strings.NewReader(strings.Join(rows, "\n")))
	r.FieldsPerRecord = 0
	return r.ReadAll()
}

func nonEmpty(lines []string) []string {
	out := []string{}
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return out
}

// buildCSVSpec infers how to regenerate each column from the observed rows.
func buildCSVSpec(rows []string) *engine.CSVSpec {
	recs, err := parseCSV(rows)
	if err != nil || len(recs) < 2 {
		return nil
	}
	header, data := recs[0], recs[1:]
	spec := &engine.CSVSpec{Header: header, Rows: len(data)}
	for c := range header {
		col := make([]string, len(data))
		for r := range data {
			col[r] = data[r][c]
		}
		spec.Columns = append(spec.Columns, inferColumn(strings.ToLower(header[c]), col))
	}
	return spec
}

func inferColumn(header string, vals []string) engine.CSVColumn {
	if ints, ok := allInts(vals); ok {
		seq := true
		for i := 1; i < len(ints); i++ {
			if ints[i] != ints[i-1]+1 {
				seq = false
				break
			}
		}
		lo, hi := minMax(ints)
		if seq {
			return engine.CSVColumn{Kind: "seq", Min: float64(ints[0])}
		}
		return engine.CSVColumn{Kind: "int", Min: float64(lo), Max: float64(hi)}
	}
	if floats, dec, ok := allFloats(vals); ok {
		lo, hi := floats[0], floats[0]
		for _, f := range floats {
			if f < lo {
				lo = f
			}
			if f > hi {
				hi = f
			}
		}
		return engine.CSVColumn{Kind: "float", Min: lo, Max: hi, Decimals: dec}
	}
	if allMatch(vals, isoDateRe) {
		return engine.CSVColumn{Kind: "date"}
	}
	switch {
	case strings.Contains(header, "email"):
		return engine.CSVColumn{Kind: "email"}
	case strings.Contains(header, "first") && strings.Contains(header, "name"):
		return engine.CSVColumn{Kind: "first"}
	case strings.Contains(header, "last") && strings.Contains(header, "name"):
		return engine.CSVColumn{Kind: "last"}
	case (header == "name" || strings.Contains(header, "full") || strings.Contains(header, "employee") ||
		strings.Contains(header, "customer")) && looksLikePeople(vals):
		return engine.CSVColumn{Kind: "name"}
	case header == "city" || header == "location":
		return engine.CSVColumn{Kind: "city"}
	case strings.Contains(header, "company"):
		return engine.CSVColumn{Kind: "company"}
	}
	seen := map[string]bool{}
	choices := []string{}
	for _, v := range vals {
		if !seen[v] && len(choices) < 50 {
			seen[v] = true
			choices = append(choices, v)
		}
	}
	return engine.CSVColumn{Kind: "choice", Values: choices}
}

func allInts(vals []string) ([]int64, bool) {
	out := make([]int64, 0, len(vals))
	for _, v := range vals {
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, len(out) > 0
}

func allFloats(vals []string) ([]float64, int, bool) {
	out := make([]float64, 0, len(vals))
	dec := 0
	for _, v := range vals {
		v = strings.TrimSpace(v)
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, 0, false
		}
		if i := strings.IndexByte(v, '.'); i >= 0 && len(v)-i-1 > dec {
			dec = len(v) - i - 1
		}
		out = append(out, f)
	}
	return out, dec, len(out) > 0
}

func allMatch(vals []string, re *regexp.Regexp) bool {
	for _, v := range vals {
		if !re.MatchString(strings.TrimSpace(v)) {
			return false
		}
	}
	return len(vals) > 0
}

// looksLikePeople reports whether most values are "First Last" with a known
// first name (so product names such as "Blue Widget" stay a choice column).
func looksLikePeople(vals []string) bool {
	first := map[string]bool{}
	for _, n := range engine.FirstNames() {
		first[n] = true
	}
	n := 0
	for _, v := range vals {
		if f := strings.Fields(v); len(f) == 2 && first[f[0]] {
			n++
		}
	}
	return n*2 >= len(vals)
}

func minMax(v []int64) (int64, int64) {
	lo, hi := v[0], v[0]
	for _, x := range v {
		if x < lo {
			lo = x
		}
		if x > hi {
			hi = x
		}
	}
	return lo, hi
}
