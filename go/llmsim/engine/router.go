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
	"sync"
	"sync/atomic"
)

// router tracks in-flight requests per pool. Counters live outside the
// profile so they survive runtime profile changes.
type router struct {
	inflight sync.Map // pool name → *atomic.Int64
}

func (r *router) counter(pool string) *atomic.Int64 {
	if v, ok := r.inflight.Load(pool); ok {
		return v.(*atomic.Int64)
	}
	v, _ := r.inflight.LoadOrStore(pool, &atomic.Int64{})
	return v.(*atomic.Int64)
}

// acquire admits a request for a model: it tries the model's pool, then the
// pool's fallback chain. It returns the pool admitted to, the model alias to
// report, and false when the model is unknown or every pool is full.
func (r *router) acquire(p *runtimeProfile, model string) (pool, reported string, known, ok bool) {
	pool, known = p.modelPool[model]
	if !known {
		return "", "", false, false
	}
	reported = model
	seen := map[string]bool{}
	for pool != "" && !seen[pool] {
		seen[pool] = true
		cfg := p.Pools[pool]
		c := r.counter(pool)
		n := c.Add(1)
		if cfg == nil || cfg.MaxConcurrent <= 0 || n <= cfg.MaxConcurrent {
			return pool, reported, true, true
		}
		c.Add(-1)
		if cfg.Fallback == "" {
			break
		}
		pool = cfg.Fallback
		reported = firstAlias(p, pool)
	}
	return "", "", true, false
}

func (r *router) release(pool string) {
	if pool != "" {
		r.counter(pool).Add(-1)
	}
}

func firstAlias(p *runtimeProfile, pool string) string {
	for _, m := range p.Models {
		if m.Pool == pool {
			return m.Alias
		}
	}
	return pool
}

// snapshot returns the in-flight count per pool.
func (r *router) snapshot() map[string]int64 {
	out := map[string]int64{}
	r.inflight.Range(func(k, v interface{}) bool {
		out[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	return out
}
