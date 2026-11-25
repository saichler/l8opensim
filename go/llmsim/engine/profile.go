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
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Profile configures the router: models, pools, keys, limits, faults and the
// flavor selection. A profile may extend another profile by name.
type Profile struct {
	Name              string             `json:"name"`
	Extends           string             `json:"extends,omitempty"`
	NCtx              int                `json:"n_ctx"`
	CharsPerToken     float64            `json:"chars_per_token"`
	Auth              bool               `json:"auth"`
	Keys              []KeyConfig        `json:"keys"`
	Models            []ModelConfig      `json:"models"`
	Pools             map[string]*Pool   `json:"pools"`
	RejectStatus      int                `json:"reject_status"`
	RetryAfterSeconds int                `json:"retry_after_seconds"`
	Faults            map[string]float64 `json:"faults"`
	SlowResponseMs    int                `json:"slow_response_ms"`
	FlawedFraction    float64            `json:"flawed_fraction"`
	FlavorMix         map[string]float64 `json:"flavor_mix"`
	Classifier        []ClassifierRule   `json:"classifier"`
	CompactionMarker  string             `json:"compaction_marker"`
}

// KeyConfig is one simulated API key with its limits (0 = unlimited).
type KeyConfig struct {
	Key  string   `json:"key"`
	Name string   `json:"name"`
	APIs []string `json:"apis,omitempty"` // openai, anthropic, webtools; empty = all
	RPM  float64  `json:"rpm,omitempty"`
	TPM  float64  `json:"tpm,omitempty"`
}

// ModelConfig maps a model alias to a pool.
type ModelConfig struct {
	Alias string `json:"alias"`
	Pool  string `json:"pool"`
}

// Pool is a group of capacity shared by its model aliases.
type Pool struct {
	MaxConcurrent int64  `json:"max_concurrent"`
	Fallback      string `json:"fallback,omitempty"`
}

// ClassifierRule maps a user message to a flavor: every All keyword must be
// present, at least one Any keyword (when given), and every Tools tool must be
// defined in the request.
type ClassifierRule struct {
	Flavor string   `json:"flavor"`
	All    []string `json:"all,omitempty"`
	Any    []string `json:"any,omitempty"`
	Tools  []string `json:"tools,omitempty"`
}

// runtimeProfile is a validated profile with its lookup tables.
type runtimeProfile struct {
	*Profile
	modelPool map[string]string
	keys      map[string]*KeyConfig
	mixNames  []string
	mixTotal  float64
}

// LoadProfiles reads every *.json profile in dir, resolving "extends".
func LoadProfiles(dir string) (map[string]*Profile, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	raw := map[string][]byte{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		head := struct {
			Name string `json:"name"`
		}{}
		if err = json.Unmarshal(data, &head); err != nil {
			return nil, fmt.Errorf("profile %s: %w", f, err)
		}
		if head.Name == "" {
			head.Name = strings.TrimSuffix(filepath.Base(f), ".json")
		}
		raw[head.Name] = data
	}
	out := map[string]*Profile{}
	for name := range raw {
		p, err := resolveProfile(name, raw, map[string]bool{})
		if err != nil {
			return nil, err
		}
		out[name] = p
	}
	if len(out) == 0 {
		return nil, errors.New("no profiles in " + dir)
	}
	return out, nil
}

func resolveProfile(name string, raw map[string][]byte, seen map[string]bool) (*Profile, error) {
	if seen[name] {
		return nil, errors.New("profile extends loop at " + name)
	}
	seen[name] = true
	data, ok := raw[name]
	if !ok {
		return nil, errors.New("unknown profile " + name)
	}
	head := struct {
		Extends string `json:"extends"`
	}{}
	_ = json.Unmarshal(data, &head)
	p := &Profile{}
	if head.Extends != "" {
		base, err := resolveProfile(head.Extends, raw, seen)
		if err != nil {
			return nil, err
		}
		p = cloneProfile(base)
	}
	if err := json.Unmarshal(data, p); err != nil {
		return nil, fmt.Errorf("profile %s: %w", name, err)
	}
	p.Name = name
	return p, nil
}

func cloneProfile(p *Profile) *Profile {
	data, _ := json.Marshal(p)
	out := &Profile{}
	_ = json.Unmarshal(data, out)
	return out
}

// compileProfile validates a profile and builds its lookup tables.
func compileProfile(p *Profile) (*runtimeProfile, error) {
	if p.NCtx <= 0 {
		p.NCtx = 131072
	}
	if p.CharsPerToken <= 0 {
		p.CharsPerToken = 4
	}
	if p.RejectStatus == 0 {
		p.RejectStatus = 429
	}
	if p.RetryAfterSeconds <= 0 {
		p.RetryAfterSeconds = 1
	}
	if p.SlowResponseMs <= 0 {
		p.SlowResponseMs = 5000
	}
	if p.CompactionMarker == "" {
		p.CompactionMarker = "You maintain the running summary of a conversation"
	}
	if p.Pools == nil {
		p.Pools = map[string]*Pool{}
	}
	rp := &runtimeProfile{Profile: p, modelPool: map[string]string{}, keys: map[string]*KeyConfig{}}
	for _, m := range p.Models {
		if m.Alias == "" || m.Pool == "" {
			return nil, errors.New("model entries need alias and pool")
		}
		if _, ok := p.Pools[m.Pool]; !ok {
			p.Pools[m.Pool] = &Pool{}
		}
		rp.modelPool[m.Alias] = m.Pool
	}
	for name, pool := range p.Pools {
		if pool == nil {
			p.Pools[name] = &Pool{}
			continue
		}
		if pool.Fallback != "" {
			if _, ok := p.Pools[pool.Fallback]; !ok {
				return nil, fmt.Errorf("pool %s falls back to unknown pool %s", name, pool.Fallback)
			}
		}
	}
	for i := range p.Keys {
		rp.keys[p.Keys[i].Key] = &p.Keys[i]
	}
	if p.Auth && len(rp.keys) == 0 {
		return nil, errors.New("auth is on but the profile defines no keys")
	}
	for name := range p.Faults {
		if !knownFault(name) {
			return nil, errors.New("unknown fault " + name)
		}
	}
	for i := range p.Classifier {
		r := &p.Classifier[i]
		r.All = lowerAll(r.All)
		r.Any = lowerAll(r.Any)
	}
	for name, w := range p.FlavorMix {
		if w > 0 {
			rp.mixNames = append(rp.mixNames, name)
			rp.mixTotal += w
		}
	}
	sortStrings(rp.mixNames)
	return rp, nil
}

func lowerAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// keyAllows reports whether the key may call the api.
func (k *KeyConfig) keyAllows(api string) bool {
	if len(k.APIs) == 0 {
		return true
	}
	for _, a := range k.APIs {
		if a == api {
			return true
		}
	}
	return false
}
