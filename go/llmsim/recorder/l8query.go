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
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// A small L8Query evaluator over the fixtures, enough for the model to get
// realistic answers (and realistic errors) while recording:
// select <cols|aggregates> from <Model> [where a=b and c>d] [group-by f] [sort-by f [descending]] [limit n]

type parsedQuery struct {
	cols    []string
	model   string
	where   [][]cond // OR of AND groups
	groupBy []string
	sortBy  string
	desc    bool
	limit   int
}

type cond struct {
	field, op, value string
}

type queryResult struct {
	model *fixtureModel
	rows  []map[string]interface{}
	total int
	err   string
}

var clauseWords = []string{" where ", " group-by ", " sort-by ", " limit "}

func parseL8Query(q string) (*parsedQuery, error) {
	q = strings.TrimSpace(q)
	lower := strings.ToLower(q)
	if !strings.HasPrefix(lower, "select ") {
		return nil, fmt.Errorf("an L8Query starts with 'select'")
	}
	from := strings.Index(lower, " from ")
	if from < 0 {
		return nil, fmt.Errorf("missing 'from <Model>'")
	}
	pq := &parsedQuery{}
	for _, c := range strings.Split(q[len("select "):from], ",") {
		if c = strings.TrimSpace(c); c != "" {
			pq.cols = append(pq.cols, c)
		}
	}
	rest := q[from+len(" from "):]
	clauses := splitClauses(" " + rest)
	pq.model = strings.TrimSpace(clauses[""])
	if w, ok := clauses["where"]; ok {
		for _, orPart := range splitWord(w, " or ") {
			group := []cond{}
			for _, part := range splitWord(orPart, " and ") {
				c, err := parseCond(part)
				if err != nil {
					return nil, err
				}
				group = append(group, c)
			}
			pq.where = append(pq.where, group)
		}
	}
	if g, ok := clauses["group-by"]; ok {
		for _, f := range strings.Split(g, ",") {
			pq.groupBy = append(pq.groupBy, strings.TrimSpace(f))
		}
	}
	if s, ok := clauses["sort-by"]; ok {
		fields := strings.Fields(s)
		if len(fields) > 0 {
			pq.sortBy = fields[0]
			pq.desc = len(fields) > 1 && strings.HasPrefix(strings.ToLower(fields[1]), "desc")
		}
	}
	if l, ok := clauses["limit"]; ok {
		n, err := strconv.Atoi(strings.TrimSpace(l))
		if err != nil {
			return nil, fmt.Errorf("invalid limit '%s'", strings.TrimSpace(l))
		}
		pq.limit = n
	}
	return pq, nil
}

// splitClauses splits " Model where ... sort-by ..." into its clauses ("" is
// the model).
func splitClauses(s string) map[string]string {
	lower := strings.ToLower(s)
	type pos struct {
		word string
		at   int
	}
	found := []pos{}
	for _, w := range clauseWords {
		if i := strings.Index(lower, w); i >= 0 {
			found = append(found, pos{strings.TrimSpace(w), i})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].at < found[j].at })
	out := map[string]string{}
	end := len(s)
	if len(found) > 0 {
		end = found[0].at
	}
	out[""] = s[:end]
	for i, p := range found {
		start := p.at + len(p.word) + 2
		stop := len(s)
		if i+1 < len(found) {
			stop = found[i+1].at
		}
		if start > stop {
			start = stop
		}
		out[p.word] = strings.TrimSpace(s[start:stop])
	}
	return out
}

func splitWord(s, word string) []string {
	out := []string{}
	lower := strings.ToLower(s)
	for {
		i := strings.Index(lower, word)
		if i < 0 {
			return append(out, s)
		}
		out = append(out, s[:i])
		s, lower = s[i+len(word):], lower[i+len(word):]
	}
}

func parseCond(s string) (cond, error) {
	s = strings.TrimSpace(s)
	for _, op := range []string{">=", "<=", "!=", "=", ">", "<"} {
		if i := strings.Index(s, op); i > 0 {
			v := strings.Trim(strings.TrimSpace(s[i+len(op):]), `'"`)
			return cond{field: strings.TrimSpace(s[:i]), op: op, value: v}, nil
		}
	}
	return cond{}, fmt.Errorf("invalid condition '%s'", s)
}

// Query runs an L8Query and returns the tool result JSON.
func (f *Fixtures) Query(q string, unlimited bool) string {
	res := f.run(q, unlimited)
	if res.err != "" {
		return toJSON(map[string]string{"error": res.err})
	}
	pq, _ := parseL8Query(q)
	if aggs := aggregates(pq.cols); len(aggs) > 0 {
		return toJSON(map[string]interface{}{"model": res.model.Name, "aggregates": aggregate(res.rows, pq, aggs)})
	}
	rows := res.rows
	if pq.limit > 0 && len(rows) > pq.limit {
		rows = rows[:pq.limit]
	}
	out := make([]interface{}, 0, len(rows))
	for _, r := range rows {
		out = append(out, nest(project(r, pq.cols)))
	}
	return truncate(toJSON(map[string]interface{}{"model": res.model.Name, "count": res.total, "records": out}))
}

func (f *Fixtures) run(q string, unlimited bool) queryResult {
	pq, err := parseL8Query(q)
	if err != nil {
		return queryResult{err: err.Error()}
	}
	m := f.models[pq.model]
	if m == nil {
		return queryResult{err: "unknown model '" + pq.model + "'; use list_modules to see the models"}
	}
	known := map[string]bool{}
	for _, fld := range m.Fields {
		known[fld.Path] = true
	}
	check := func(field string) string {
		if field == "*" || known[field] {
			return ""
		}
		return "unknown field '" + field + "' in model " + m.Name + "; use describe_model"
	}
	for _, g := range pq.where {
		for _, c := range g {
			if e := check(c.field); e != "" {
				return queryResult{err: e}
			}
		}
	}
	for _, c := range pq.cols {
		if e := check(aggField(c)); e != "" {
			return queryResult{err: e}
		}
	}
	if pq.sortBy != "" {
		if e := check(pq.sortBy); e != "" {
			return queryResult{err: e}
		}
	}
	if !unlimited && len(aggregates(pq.cols)) == 0 && len(pq.cols) == 1 && pq.cols[0] == "*" &&
		len(pq.where) == 0 && (pq.limit <= 0 || pq.limit > 100) {
		return queryResult{err: "a 'select *' needs an id filter or 'limit <n>' (n <= 100)"}
	}
	rows := []map[string]interface{}{}
	for _, r := range f.records[m.Name] {
		if matchesWhere(r, pq.where) {
			rows = append(rows, r)
		}
	}
	if pq.sortBy != "" {
		sort.SliceStable(rows, func(i, j int) bool {
			less := compareValues(rows[i][pq.sortBy], rows[j][pq.sortBy]) < 0
			if pq.desc {
				return compareValues(rows[i][pq.sortBy], rows[j][pq.sortBy]) > 0
			}
			return less
		})
	}
	return queryResult{model: m, rows: rows, total: len(rows)}
}

func matchesWhere(r map[string]interface{}, where [][]cond) bool {
	if len(where) == 0 {
		return true
	}
	for _, group := range where {
		ok := true
		for _, c := range group {
			if !matchCond(r[c.field], c) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func matchCond(v interface{}, c cond) bool {
	s := fmt.Sprint(v)
	switch c.op {
	case "=", "!=":
		eq := strings.EqualFold(s, c.value) || strings.HasSuffix(strings.ToUpper(s), "_"+strings.ToUpper(c.value))
		if strings.Contains(c.value, "*") {
			eq = wildcard(strings.ToLower(s), strings.ToLower(c.value))
		}
		return eq == (c.op == "=")
	}
	cmp := compareValues(v, c.value)
	switch c.op {
	case ">":
		return cmp > 0
	case "<":
		return cmp < 0
	case ">=":
		return cmp >= 0
	case "<=":
		return cmp <= 0
	}
	return false
}

func wildcard(s, pattern string) bool {
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1:] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(pattern, "*") || s == ""
}

func compareValues(a, b interface{}) int {
	fa, errA := strconv.ParseFloat(fmt.Sprint(a), 64)
	fb, errB := strconv.ParseFloat(fmt.Sprint(b), 64)
	if errA == nil && errB == nil {
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		}
		return 0
	}
	return strings.Compare(strings.ToLower(fmt.Sprint(a)), strings.ToLower(fmt.Sprint(b)))
}
