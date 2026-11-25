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
	"net/http"
)

func (e *Engine) serveProfileGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, e.prof.Load().Profile)
}

// serveProfilePut switches to another loaded profile ({"name":"limited"}) or
// patches the active profile with the fields given (maps are merged, other
// fields replaced).
func (e *Engine) serveProfilePut(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	data := append([]byte(nil), body.Bytes()...)
	putBuffer(body)
	p, err := e.ApplyProfile(data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// ApplyProfile switches or patches the active profile from a JSON document.
func (e *Engine) ApplyProfile(data []byte) (*Profile, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, errors.New("invalid JSON: " + err.Error())
	}
	e.profMtx.Lock()
	defer e.profMtx.Unlock()
	var next *Profile
	if nameRaw, ok := fields["name"]; ok && len(fields) == 1 {
		name := ""
		_ = json.Unmarshal(nameRaw, &name)
		base, found := e.profiles[name]
		if !found {
			return nil, errors.New("unknown profile " + name)
		}
		next = cloneProfile(base)
	} else {
		next = cloneProfile(e.prof.Load().Profile)
		if err := json.Unmarshal(data, next); err != nil {
			return nil, errors.New("invalid profile patch: " + err.Error())
		}
	}
	rp, err := compileProfile(next)
	if err != nil {
		return nil, err
	}
	e.prof.Store(rp)
	return rp.Profile, nil
}

// ProfileName returns the active profile's name.
func (e *Engine) ProfileName() string { return e.prof.Load().Name }
