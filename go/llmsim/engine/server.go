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
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options configure an Engine.
type Options struct {
	ProfileDir  string // directory with profile JSON files
	Profile     string // profile name to start with
	ScenarioDir string // directory with recorded cassettes
	Library     *Library
	AuthOff     bool
	TraceFile   string
	TraceSample float64
}

// Engine is the simulator: profiles, cassettes, router state and stats.
type Engine struct {
	prof     atomic.Pointer[runtimeProfile]
	profMtx  sync.Mutex
	profiles map[string]*Profile
	lib      *Library
	stats    *Stats
	router   router
	limiter  rateLimiter
	trace    *tracer
	authOff  bool
}

// New loads the profiles and the cassettes.
func New(o Options) (*Engine, error) {
	profiles, err := LoadProfiles(o.ProfileDir)
	if err != nil {
		return nil, err
	}
	name := o.Profile
	if name == "" {
		name = "default"
	}
	name = strings.TrimSuffix(filepath.Base(name), ".json")
	p, ok := profiles[name]
	if !ok {
		return nil, errors.New("unknown profile " + name)
	}
	rp, err := compileProfile(cloneProfile(p))
	if err != nil {
		return nil, err
	}
	lib := o.Library
	if lib == nil {
		if lib, err = LoadLibrary(o.ScenarioDir); err != nil {
			return nil, err
		}
	}
	tr, err := newTracer(o.TraceFile, o.TraceSample)
	if err != nil {
		return nil, err
	}
	e := &Engine{profiles: profiles, lib: lib, stats: newStats(), trace: tr, authOff: o.AuthOff}
	e.prof.Store(rp)
	return e, nil
}

// Library returns the loaded cassettes.
func (e *Engine) Library() *Library { return e.lib }

// Stats returns the counters.
func (e *Engine) Stats() *Stats { return e.stats }

// Close flushes the trace log.
func (e *Engine) Close() { e.trace.close() }

// Handler returns the HTTP routes of the simulator.
func (e *Engine) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		e.serveChat(w, r, APIOpenAI)
	})
	mux.HandleFunc("POST /chat/completions", func(w http.ResponseWriter, r *http.Request) {
		e.serveChat(w, r, APIOpenAI)
	})
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		e.serveChat(w, r, APIAnthropic)
	})
	mux.HandleFunc("POST /messages", func(w http.ResponseWriter, r *http.Request) {
		e.serveChat(w, r, APIAnthropic)
	})
	mux.HandleFunc("POST /v1/search", e.serveSearch)
	mux.HandleFunc("POST /v1/fetch", e.serveFetch)
	mux.HandleFunc("GET /v1/models", e.serveModels)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeRaw(w, http.StatusOK, []byte(`{"status":"ok"}`+"\n"))
	})
	mux.HandleFunc("GET /sim/stats", e.serveStats)
	mux.HandleFunc("GET /metrics", e.serveMetrics)
	mux.HandleFunc("GET /sim/profile", e.serveProfileGet)
	mux.HandleFunc("PUT /sim/profile", e.serveProfilePut)
	return mux
}

// serveChat is the hot path shared by the OpenAI and Anthropic APIs.
func (e *Engine) serveChat(w http.ResponseWriter, r *http.Request, api string) {
	start := time.Now()
	e.stats.total.Add(1)
	e.stats.inflight.Add(1)
	defer e.stats.inflight.Add(-1)
	e.stats.byAPI.inc(api)
	p := e.prof.Load()

	body, err := readBody(r)
	if err != nil {
		e.fail(w, api, start, http.StatusBadRequest, err.Error(), 0)
		return
	}
	var req *Request
	if api == APIAnthropic {
		req, err = ParseAnthropic(body.Bytes())
	} else {
		req, err = ParseOpenAI(body.Bytes())
	}
	putBuffer(body)
	if err != nil {
		e.fail(w, api, start, http.StatusBadRequest, err.Error(), 0)
		return
	}
	key, status, msg := e.authorize(p, r, api)
	if status != 0 {
		e.fail(w, api, start, status, msg, 0)
		return
	}
	e.stats.byModel.inc(req.Model)
	pool, reported, known, ok := e.router.acquire(p, req.Model)
	if !known {
		e.fail(w, api, start, http.StatusNotFound, "model '"+req.Model+"' not found", 0)
		return
	}
	if !ok {
		e.stats.rejected.Add(1)
		e.fail(w, api, start, p.RejectStatus, "the model is at capacity, retry later", p.RetryAfterSeconds)
		return
	}
	defer e.router.release(pool)
	promptTokens := estimateTokens(p, req.BodyLen)
	if allowed, retry := e.limiter.allow(key, promptTokens); !allowed {
		e.stats.rejected.Add(1)
		e.fail(w, api, start, http.StatusTooManyRequests, "rate limit reached for this key", retry)
		return
	}
	fault := drawFault(p)
	if fault != "" && !isReplyFault(fault) {
		if e.serveFault(w, r, api, start, p, fault) {
			return
		}
	}
	if req.MaxTokens > 0 && promptTokens+req.MaxTokens > p.NCtx {
		e.fail(w, api, start, http.StatusBadRequest, contextExceededMessage, 0)
		return
	}
	seed := ConversationSeed(req)
	rep, how, subs := answer(p, e.lib, req, seed)
	if fault != "" && applyReplyFault(fault, rep) {
		rep.Fault = fault
		e.stats.byFault.inc(fault)
	}
	e.countReply(req, rep, how, subs)
	e.limiter.charge(key, rep.CompletionTokens)
	out := getBuffer()
	if api == APIAnthropic {
		err = EncodeAnthropic(out, reported, seed, rep)
	} else {
		err = EncodeOpenAI(out, reported, seed, rep)
	}
	if err != nil {
		putBuffer(out)
		e.fail(w, api, start, http.StatusInternalServerError, err.Error(), 0)
		return
	}
	n := out.Len()
	writeRaw(w, http.StatusOK, out.Bytes())
	putBuffer(out)
	e.stats.observe(time.Since(start), http.StatusOK, n)
	e.trace.record(req, seed, rep, how, http.StatusOK)
}

func (e *Engine) countReply(req *Request, rep *Reply, how string, subs int) {
	e.stats.byFlavor.inc(rep.Flavor)
	e.stats.byBind.inc(how)
	e.stats.tokensOut.Add(int64(rep.CompletionTokens))
	if how == BindMix || how == BindNone {
		e.stats.unmatched.Add(1)
	}
	if how == BindCompaction {
		e.stats.compaction.Add(1)
	}
	if subs > 0 {
		e.stats.toolSubs.Add(int64(subs))
	}
	if len(rep.Calls) > 0 {
		e.stats.toolSteps.Add(1)
	}
	if req.NoTools {
		e.stats.noTools.Add(1)
	}
}

// serveFault answers with an error fault. It returns false when the fault
// only delays the request, which then continues normally.
func (e *Engine) serveFault(w http.ResponseWriter, r *http.Request, api string, start time.Time,
	p *runtimeProfile, fault string) bool {
	e.stats.byFault.inc(fault)
	switch fault {
	case FaultHTTP429:
		e.fail(w, api, start, http.StatusTooManyRequests, "rate limit reached (injected)", p.RetryAfterSeconds)
	case FaultHTTP503:
		e.fail(w, api, start, http.StatusServiceUnavailable, "the model is overloaded (injected)", p.RetryAfterSeconds)
	case FaultHTTP500:
		e.fail(w, api, start, http.StatusInternalServerError, "internal server error (injected)", 0)
	case FaultHTTP400:
		e.fail(w, api, start, http.StatusBadRequest, "invalid request (injected)", 0)
	case FaultContextExceeded:
		e.fail(w, api, start, http.StatusBadRequest, contextExceededMessage, 0)
	case FaultDropConnection:
		dropConnection(w)
		e.stats.observe(time.Since(start), 0, 0)
	case FaultSlowResponse:
		select {
		case <-time.After(time.Duration(p.SlowResponseMs) * time.Millisecond):
		case <-r.Context().Done():
		}
		return false
	}
	return true
}

// authorize checks the API key. It returns the key config (nil when auth is
// off) or an error status and message.
func (e *Engine) authorize(p *runtimeProfile, r *http.Request, api string) (*KeyConfig, int, string) {
	if e.authOff || !p.Auth {
		return nil, 0, ""
	}
	key := r.Header.Get("x-api-key")
	if auth := r.Header.Get("Authorization"); key == "" && strings.HasPrefix(auth, "Bearer ") {
		key = strings.TrimSpace(auth[len("Bearer "):])
	}
	if key == "" {
		return nil, http.StatusUnauthorized, "missing API key"
	}
	k := p.keys[key]
	if k == nil {
		return nil, http.StatusUnauthorized, "invalid API key"
	}
	if !k.keyAllows(api) {
		return nil, http.StatusUnauthorized, "this key is not valid for the " + api + " API"
	}
	return k, 0, ""
}

// fail writes an API-specific error body.
func (e *Engine) fail(w http.ResponseWriter, api string, start time.Time, status int, msg string, retryAfter int) {
	buf := getBuffer()
	switch api {
	case APIAnthropic:
		EncodeAnthropicError(buf, status, msg)
	case APIOpenAI:
		EncodeOpenAIError(buf, status, msg)
	default:
		writeSimpleError(buf, msg)
	}
	if retryAfter > 0 {
		w.Header().Set("Retry-After", itoa(retryAfter))
	}
	n := buf.Len()
	writeRaw(w, status, buf.Bytes())
	putBuffer(buf)
	e.stats.observe(time.Since(start), status, n)
}

func (e *Engine) serveModels(w http.ResponseWriter, r *http.Request) {
	p := e.prof.Load()
	data := []map[string]interface{}{}
	for _, m := range p.Models {
		data = append(data, map[string]interface{}{"id": m.Alias, "object": "model", "owned_by": "llmsim",
			"meta": map[string]interface{}{"n_ctx_train": p.NCtx, "pool": m.Pool}})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"object": "list", "data": data})
}

func (e *Engine) serveStats(w http.ResponseWriter, r *http.Request) {
	snap := e.stats.Snapshot(e.router.snapshot())
	snap["profile"] = e.prof.Load().Name
	snap["cassettes"] = e.lib.Size()
	writeJSON(w, http.StatusOK, snap)
}

func (e *Engine) serveMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(e.stats.Prometheus(e.router.snapshot())))
}
