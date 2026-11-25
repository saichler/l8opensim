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
	"time"
)

// rateLimiter keeps a requests-per-minute and a tokens-per-minute bucket per
// key. Limits are read from the key config on every call, so profile changes
// take effect immediately.
type rateLimiter struct {
	buckets sync.Map // key → *bucket
}

type bucket struct {
	mtx    sync.Mutex
	req    float64
	tok    float64
	last   time.Time
	primed bool
}

// allow takes one request and the prompt tokens from the key's buckets. It
// returns false, with the seconds until a retry can succeed, when either
// bucket is empty.
func (rl *rateLimiter) allow(k *KeyConfig, tokens int) (bool, int) {
	if k == nil || (k.RPM <= 0 && k.TPM <= 0) {
		return true, 0
	}
	v, _ := rl.buckets.LoadOrStore(k.Key, &bucket{})
	b := v.(*bucket)
	b.mtx.Lock()
	defer b.mtx.Unlock()
	now := time.Now()
	if !b.primed {
		b.req, b.tok, b.last, b.primed = k.RPM, k.TPM, now, true
	}
	minutes := now.Sub(b.last).Minutes()
	b.last = now
	if k.RPM > 0 {
		b.req = minf(k.RPM, b.req+minutes*k.RPM)
	}
	if k.TPM > 0 {
		b.tok = minf(k.TPM, b.tok+minutes*k.TPM)
	}
	if k.RPM > 0 && b.req < 1 {
		return false, retrySeconds((1 - b.req) / k.RPM)
	}
	if k.TPM > 0 && b.tok < float64(tokens) && b.tok < k.TPM {
		need := float64(tokens) - b.tok
		if need > k.TPM {
			need = k.TPM
		}
		return false, retrySeconds(need / k.TPM)
	}
	if k.RPM > 0 {
		b.req--
	}
	if k.TPM > 0 {
		b.tok -= float64(tokens)
	}
	return true, 0
}

// charge adds the completion tokens after the reply is known.
func (rl *rateLimiter) charge(k *KeyConfig, tokens int) {
	if k == nil || k.TPM <= 0 {
		return
	}
	if v, ok := rl.buckets.Load(k.Key); ok {
		b := v.(*bucket)
		b.mtx.Lock()
		b.tok -= float64(tokens)
		b.mtx.Unlock()
	}
}

func retrySeconds(minutes float64) int {
	s := int(minutes*60) + 1
	if s < 1 {
		s = 1
	}
	return s
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
