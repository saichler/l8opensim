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

// classify maps the last user message to a flavor with the profile's ordered
// keyword rules. Keywords were lowercased when the profile was compiled.
func classify(p *runtimeProfile, req *Request) string {
	if len(p.Classifier) == 0 {
		return ""
	}
	text := strings.ToLower(req.LastUser)
	for i := range p.Classifier {
		r := &p.Classifier[i]
		if ruleMatches(r, text, req) {
			return r.Flavor
		}
	}
	return ""
}

func ruleMatches(r *ClassifierRule, text string, req *Request) bool {
	for _, kw := range r.All {
		if !strings.Contains(text, kw) {
			return false
		}
	}
	if len(r.Any) > 0 {
		found := false
		for _, kw := range r.Any {
			if strings.Contains(text, kw) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, t := range r.Tools {
		if req.Tools[t] == nil {
			return false
		}
	}
	return len(r.All) > 0 || len(r.Any) > 0 || len(r.Tools) > 0
}
