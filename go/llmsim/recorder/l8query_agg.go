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

const maxToolResultChars = 16000

// aggregates returns the aggregate columns (count(*), sum(f), ...).
func aggregates(cols []string) []string {
	out := []string{}
	for _, c := range cols {
		l := strings.ToLower(c)
		for _, fn := range []string{"count(", "sum(", "avg(", "min(", "max("} {
			if strings.HasPrefix(l, fn) && strings.HasSuffix(l, ")") {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// aggField returns the field inside an aggregate, or the column itself.
func aggField(col string) string {
	if i := strings.Index(col, "("); i >= 0 && strings.HasSuffix(col, ")") {
		return strings.TrimSpace(col[i+1 : len(col)-1])
	}
	return col
}

func aggregate(rows []map[string]interface{}, pq *parsedQuery, aggs []string) []map[string]interface{} {
	groups := map[string][]map[string]interface{}{}
	keys := []string{}
	for _, r := range rows {
		parts := []string{}
		for _, g := range pq.groupBy {
			parts = append(parts, fmt.Sprint(r[g]))
		}
		k := strings.Join(parts, "\x00")
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], r)
	}
	if len(pq.groupBy) == 0 && len(keys) == 0 {
		keys = append(keys, "")
	}
	sort.Strings(keys)
	out := []map[string]interface{}{}
	for _, k := range keys {
		members := groups[k]
		entry := map[string]interface{}{}
		if len(pq.groupBy) > 0 {
			vals := strings.Split(k, "\x00")
			group := map[string]string{}
			for i, g := range pq.groupBy {
				if i < len(vals) {
					group[g] = vals[i]
				}
			}
			entry["group"] = group
		}
		for _, a := range aggs {
			entry[a] = aggValue(strings.ToLower(a[:strings.Index(a, "(")]), aggField(a), members)
		}
		out = append(out, entry)
	}
	return out
}

func aggValue(fn, field string, rows []map[string]interface{}) interface{} {
	if fn == "count" {
		return len(rows)
	}
	vals := []float64{}
	for _, r := range rows {
		if f, err := strconv.ParseFloat(fmt.Sprint(r[field]), 64); err == nil {
			vals = append(vals, f)
		}
	}
	if len(vals) == 0 {
		return 0
	}
	sum, lo, hi := 0.0, vals[0], vals[0]
	for _, v := range vals {
		sum += v
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	switch fn {
	case "sum":
		return sum
	case "avg":
		return float64(int(sum/float64(len(vals))*100)) / 100
	case "min":
		return lo
	}
	return hi
}

// project keeps the selected columns of a flat record.
func project(r map[string]interface{}, cols []string) map[string]interface{} {
	if len(cols) == 0 || (len(cols) == 1 && cols[0] == "*") {
		return r
	}
	out := map[string]interface{}{}
	for _, c := range cols {
		if v, ok := r[c]; ok {
			out[c] = v
		}
	}
	return out
}

// nest turns a flat path → value record into nested objects.
func nest(flat map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for path, v := range flat {
		parts := strings.Split(path, ".")
		cur := out
		for _, p := range parts[:len(parts)-1] {
			next, ok := cur[p].(map[string]interface{})
			if !ok {
				next = map[string]interface{}{}
				cur[p] = next
			}
			cur = next
		}
		cur[parts[len(parts)-1]] = v
	}
	return out
}

func truncate(s string) string {
	if len(s) <= maxToolResultChars {
		return s
	}
	return s[:maxToolResultChars] + "\n[truncated: more rows; refine the query]"
}
