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
	"fmt"
	"strconv"
	"strings"
	"time"
)

var firstNames = []string{"James", "Mary", "Robert", "Patricia", "John", "Jennifer", "Michael", "Linda", "David",
	"Elizabeth", "William", "Barbara", "Richard", "Susan", "Joseph", "Jessica", "Thomas", "Sarah", "Daniel", "Karen",
	"Matthew", "Nancy", "Anthony", "Lisa", "Mark", "Betty", "Steven", "Sandra", "Paul", "Ashley", "Andrew", "Emily",
	"Joshua", "Donna", "Kevin", "Michelle", "Brian", "Carol", "George", "Amanda", "Noa", "Yael", "Omer", "Priya",
	"Arjun", "Wei", "Mei", "Hiroshi", "Sofia", "Lucas", "Mateo", "Elena", "Ahmed", "Fatima", "Olga", "Ivan"}

var lastNames = []string{"Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller", "Davis", "Rodriguez",
	"Martinez", "Hernandez", "Lopez", "Gonzalez", "Wilson", "Anderson", "Thomas", "Taylor", "Moore", "Jackson",
	"Martin", "Lee", "Perez", "Thompson", "White", "Harris", "Sanchez", "Clark", "Ramirez", "Lewis", "Robinson",
	"Walker", "Young", "Allen", "King", "Wright", "Scott", "Torres", "Nguyen", "Hill", "Flores", "Cohen", "Levi",
	"Patel", "Sharma", "Chen", "Wang", "Tanaka", "Sato", "Rossi", "Muller", "Novak", "Kowalski", "Ivanov"}

var companies = []string{"Acme Corp", "Globex", "Initech", "Umbrella Systems", "Stark Industries", "Wayne Logistics",
	"Hooli", "Vandelay Imports", "Soylent Foods", "Cyberdyne", "Tyrell Labs", "Wonka Industries", "Aperture Science",
	"Massive Dynamic", "Pied Piper", "Oscorp", "Northwind Traders", "Contoso", "Fabrikam", "Adventure Works",
	"Blue Harbor", "Silverline Networks", "Redwood Analytics", "Summit Retail", "Bluepeak Energy"}

var cities = []string{"Austin", "Dallas", "Houston", "San Antonio", "Seattle", "Portland", "Denver", "Phoenix",
	"Chicago", "Boston", "New York", "Atlanta", "Miami", "San Jose", "San Diego", "Los Angeles", "Nashville",
	"Raleigh", "Minneapolis", "Detroit", "Toronto", "London", "Berlin", "Paris", "Madrid", "Amsterdam", "Tel Aviv",
	"Tokyo", "Singapore", "Sydney", "Mumbai", "Dublin", "Stockholm", "Zurich", "Vienna", "Prague", "Warsaw"}

var loremWords = strings.Fields(`the system network device cluster service request response latency throughput
configuration release update security policy storage compute memory interface traffic monitoring alert metric
performance capacity version support guide example overview install upgrade feature improvement documentation
data model query result analysis report summary user team project deployment environment production staging
availability reliability scale node pod container image registry controller operator resource limit quota`)

// hashString is FNV-1a 64.
func hashString(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// mix combines two values with a splitmix64 finalizer.
func mix(a, b uint64) uint64 {
	z := a + 0x9e3779b97f4a7c15*(b+1)
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func pick(list []string, h uint64) string {
	return list[h%uint64(len(list))]
}

func unitFloat(h uint64) float64 {
	return float64(h>>11) / float64(1<<53)
}

func dateBack(days int) string {
	return time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
}

func uuidFrom(a, b uint64) string {
	return fmt.Sprintf("%08x-%04x-4%03x-%04x-%012x", uint32(a>>32), uint16(a>>16), uint16(a)&0xfff,
		(uint16(b>>48)&0x3fff)|0x8000, b&0xffffffffffff)
}

// generateCSVRows regenerates the data rows of a CSV block (no header, no
// trailing newline) from its column spec.
func generateCSVRows(spec *CSVSpec, seed uint64) string {
	b := strings.Builder{}
	w := csv.NewWriter(&b)
	row := make([]string, len(spec.Columns))
	for r := 0; r < spec.Rows; r++ {
		rowSeed := mix(seed, uint64(r))
		names := map[int]string{}
		for c := range spec.Columns {
			row[c] = csvCell(&spec.Columns[c], r, mix(rowSeed, uint64(c)), names, rowSeed)
		}
		_ = w.Write(row)
	}
	w.Flush()
	return strings.TrimRight(b.String(), "\n")
}

func csvCell(col *CSVColumn, row int, h uint64, names map[int]string, rowSeed uint64) string {
	switch col.Kind {
	case "seq":
		return strconv.FormatInt(int64(col.Min)+int64(row), 10)
	case "int":
		lo, hi := int64(col.Min), int64(col.Max)
		if hi <= lo {
			return strconv.FormatInt(lo, 10)
		}
		return strconv.FormatInt(lo+int64(h%uint64(hi-lo+1)), 10)
	case "float":
		v := col.Min + (col.Max-col.Min)*unitFloat(h)
		return strconv.FormatFloat(v, 'f', col.Decimals, 64)
	case "date":
		return dateBack(int(h % 1500))
	case "name":
		n := pick(firstNames, rowSeed) + " " + pick(lastNames, mix(rowSeed, 3))
		names[0] = n
		return n
	case "first":
		return pick(firstNames, rowSeed)
	case "last":
		return pick(lastNames, mix(rowSeed, 3))
	case "email":
		first := strings.ToLower(pick(firstNames, rowSeed))
		last := strings.ToLower(pick(lastNames, mix(rowSeed, 3)))
		return first + "." + last + "@example.com"
	case "city":
		return pick(cities, h)
	case "company":
		return pick(companies, h)
	case "choice":
		if len(col.Values) > 0 {
			return pick(col.Values, h)
		}
	}
	return ""
}

// SyntheticText builds n characters of plausible prose from a seed.
func SyntheticText(seed uint64, n int) string {
	b := strings.Builder{}
	b.Grow(n + 32)
	h := seed
	sentence := 0
	for b.Len() < n {
		h = mix(h, uint64(b.Len()))
		words := 8 + int(h%10)
		for i := 0; i < words; i++ {
			w := pick(loremWords, mix(h, uint64(i)))
			if i == 0 {
				w = strings.ToUpper(w[:1]) + w[1:]
			}
			b.WriteString(w)
			if i < words-1 {
				b.WriteByte(' ')
			}
		}
		b.WriteString(". ")
		sentence++
		if sentence%5 == 0 {
			b.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(b.String()[:n])
}

// FirstNames returns the first-name dictionary (used by the recorder to find
// person names to flavor).
func FirstNames() []string { return firstNames }

// Cities returns the city dictionary.
func Cities() []string { return cities }

// Pick returns a deterministic element of list for a seed.
func Pick(list []string, seed uint64) string { return pick(list, seed) }

// Mix combines two hash values.
func Mix(a, b uint64) uint64 { return mix(a, b) }

// HashString is FNV-1a 64 of s.
func HashString(s string) uint64 { return hashString(s) }

// Normalize is the normalization used for matching user messages.
func Normalize(s string) string { return normalize(s) }
