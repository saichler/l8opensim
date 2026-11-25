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
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// latencyBounds are the histogram bucket upper bounds in microseconds.
var latencyBounds = []int64{100, 250, 500, 1000, 2500, 5000, 10000, 25000, 50000, 100000, 250000, 1000000}

// Stats are the simulator counters, all lock-free on the hot path.
type Stats struct {
	start      time.Time
	total      atomic.Int64
	inflight   atomic.Int64
	rejected   atomic.Int64
	unmatched  atomic.Int64
	toolSubs   atomic.Int64
	toolSteps  atomic.Int64
	noTools    atomic.Int64
	compaction atomic.Int64
	bytesOut   atomic.Int64
	tokensOut  atomic.Int64
	latSum     atomic.Int64
	latBuckets []atomic.Int64
	rate       atomic.Uint64 // float64 bits, requests/s over the last second

	byAPI    counterMap
	byModel  counterMap
	byFlavor counterMap
	byFault  counterMap
	byStatus counterMap
	byBind   counterMap
}

type counterMap struct{ m sync.Map }

func (c *counterMap) inc(key string) {
	if key == "" {
		return
	}
	if v, ok := c.m.Load(key); ok {
		v.(*atomic.Int64).Add(1)
		return
	}
	v, _ := c.m.LoadOrStore(key, &atomic.Int64{})
	v.(*atomic.Int64).Add(1)
}

func (c *counterMap) snapshot() map[string]int64 {
	out := map[string]int64{}
	c.m.Range(func(k, v interface{}) bool {
		out[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	return out
}

func newStats() *Stats {
	s := &Stats{start: time.Now(), latBuckets: make([]atomic.Int64, len(latencyBounds)+1)}
	go s.sampleRate()
	return s
}

func (s *Stats) sampleRate() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	prev := s.total.Load()
	for range t.C {
		cur := s.total.Load()
		s.rate.Store(math.Float64bits(float64(cur - prev)))
		prev = cur
	}
}

// observe records one finished request.
func (s *Stats) observe(d time.Duration, status int, bytes int) {
	us := d.Microseconds()
	s.latSum.Add(us)
	i := sort.Search(len(latencyBounds), func(i int) bool { return latencyBounds[i] >= us })
	s.latBuckets[i].Add(1)
	s.byStatus.inc(fmt.Sprint(status))
	s.bytesOut.Add(int64(bytes))
}

// Snapshot returns the statistics as a JSON-friendly map.
func (s *Stats) Snapshot(pools map[string]int64) map[string]interface{} {
	hist := map[string]int64{}
	for i := range s.latBuckets {
		label := "+Inf"
		if i < len(latencyBounds) {
			label = fmt.Sprintf("le_%dus", latencyBounds[i])
		}
		hist[label] = s.latBuckets[i].Load()
	}
	total := s.total.Load()
	avg := 0.0
	if total > 0 {
		avg = float64(s.latSum.Load()) / float64(total)
	}
	return map[string]interface{}{
		"uptime_seconds":         int64(time.Since(s.start).Seconds()),
		"requests_total":         total,
		"requests_per_second":    math.Float64frombits(s.rate.Load()),
		"in_flight":              s.inflight.Load(),
		"in_flight_by_pool":      pools,
		"rejected":               s.rejected.Load(),
		"unmatched":              s.unmatched.Load(),
		"tool_fit_substitutions": s.toolSubs.Load(),
		"tool_steps_served":      s.toolSteps.Load(),
		"tool_choice_none":       s.noTools.Load(),
		"compaction_requests":    s.compaction.Load(),
		"bytes_out":              s.bytesOut.Load(),
		"completion_tokens_out":  s.tokensOut.Load(),
		"latency_avg_us":         avg,
		"latency_histogram":      hist,
		"by_api":                 s.byAPI.snapshot(),
		"by_model":               s.byModel.snapshot(),
		"by_flavor":              s.byFlavor.snapshot(),
		"by_binding":             s.byBind.snapshot(),
		"by_fault":               s.byFault.snapshot(),
		"by_status":              s.byStatus.snapshot(),
		"latency_percentiles_us": s.percentiles(),
	}
}

// percentiles estimates p50/p90/p99 from the histogram (bucket upper bounds).
func (s *Stats) percentiles() map[string]int64 {
	counts := make([]int64, len(s.latBuckets))
	var total int64
	for i := range s.latBuckets {
		counts[i] = s.latBuckets[i].Load()
		total += counts[i]
	}
	out := map[string]int64{}
	for _, q := range []struct {
		name string
		v    float64
	}{{"p50", 0.5}, {"p90", 0.9}, {"p99", 0.99}} {
		if total == 0 {
			out[q.name] = 0
			continue
		}
		target := int64(math.Ceil(q.v * float64(total)))
		var acc int64
		for i, c := range counts {
			acc += c
			if acc >= target {
				if i < len(latencyBounds) {
					out[q.name] = latencyBounds[i]
				} else {
					out[q.name] = -1
				}
				break
			}
		}
	}
	return out
}

// Prometheus renders the counters in the Prometheus text format.
func (s *Stats) Prometheus(pools map[string]int64) string {
	b := strings.Builder{}
	gauge := func(name, help string, v interface{}) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %v\n", name, help, name, name, v)
	}
	counter := func(name, help string, v int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
	}
	labeled := func(name, help, label string, m map[string]int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s{%s=%q} %d\n", name, label, k, m[k])
		}
	}
	counter("llmsim_requests_total", "Requests served", s.total.Load())
	gauge("llmsim_requests_per_second", "Requests in the last second", math.Float64frombits(s.rate.Load()))
	gauge("llmsim_in_flight", "Requests in flight", s.inflight.Load())
	counter("llmsim_rejected_total", "Requests rejected by caps or rate limits", s.rejected.Load())
	counter("llmsim_unmatched_total", "Requests answered by the default mix", s.unmatched.Load())
	counter("llmsim_tool_fit_substitutions_total", "Tool call substitutions", s.toolSubs.Load())
	counter("llmsim_tool_steps_total", "Tool-call steps served", s.toolSteps.Load())
	counter("llmsim_tool_choice_none_total", "Requests with tool_choice none", s.noTools.Load())
	counter("llmsim_compaction_total", "Compaction requests", s.compaction.Load())
	counter("llmsim_bytes_out_total", "Response bytes written", s.bytesOut.Load())
	counter("llmsim_completion_tokens_total", "Completion tokens reported", s.tokensOut.Load())
	labeled("llmsim_requests_by_api_total", "Requests per API", "api", s.byAPI.snapshot())
	labeled("llmsim_requests_by_model_total", "Requests per model", "model", s.byModel.snapshot())
	labeled("llmsim_requests_by_flavor_total", "Requests per flavor", "flavor", s.byFlavor.snapshot())
	labeled("llmsim_faults_total", "Injected faults", "fault", s.byFault.snapshot())
	labeled("llmsim_responses_by_status_total", "Responses per HTTP status", "status", s.byStatus.snapshot())
	fmt.Fprintf(&b, "# HELP llmsim_pool_in_flight In-flight requests per pool\n# TYPE llmsim_pool_in_flight gauge\n")
	for k, v := range pools {
		fmt.Fprintf(&b, "llmsim_pool_in_flight{pool=%q} %d\n", k, v)
	}
	fmt.Fprintf(&b, "# HELP llmsim_latency_us Simulator latency\n# TYPE llmsim_latency_us histogram\n")
	var acc int64
	for i := range s.latBuckets {
		acc += s.latBuckets[i].Load()
		le := "+Inf"
		if i < len(latencyBounds) {
			le = fmt.Sprint(latencyBounds[i])
		}
		fmt.Fprintf(&b, "llmsim_latency_us_bucket{le=%q} %d\n", le, acc)
	}
	fmt.Fprintf(&b, "llmsim_latency_us_sum %d\nllmsim_latency_us_count %d\n", s.latSum.Load(), acc)
	return b.String()
}
