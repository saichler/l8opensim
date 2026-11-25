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
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saichler/l8opensim/go/llmsim/engine"
)

// RunConfig configures a catalog recording run.
type RunConfig struct {
	CatalogDir   string
	OutDir       string
	TantoFile    string
	FixturesFile string
	Upstream     *Upstream
	ChatURL      string // e.g. https://192.168.1.124:8080/v1
	MuseURL      string // e.g. https://192.168.1.124:8082/v1
	WebURL       string // e.g. https://192.168.1.124:8083
	ChatModel    string
	MuseModel    string
	Concurrency  int
	MaxRounds    int
	MaxTokens    int
	Only         string // record only this flavor
	Limit        int    // record at most this many new conversations
}

type runner struct {
	cfg   RunConfig
	tanto *TantoSetup
	fix   *Fixtures
	done  atomic.Int64
	fail  atomic.Int64
	total int
}

// Run records every catalog job that has no cassette yet (an existing output
// file marks a job done, so interrupted runs resume).
func Run(ctx context.Context, cfg RunConfig) error {
	cats, err := LoadCatalogs(cfg.CatalogDir)
	if err != nil {
		return err
	}
	tanto, err := LoadTanto(cfg.TantoFile)
	if err != nil {
		return err
	}
	fix, err := LoadFixtures(cfg.FixturesFile)
	if err != nil {
		return err
	}
	if cfg.MaxRounds <= 0 {
		cfg.MaxRounds = 8
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 8192
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	museUp := cfg.MuseURL != "" && cfg.Upstream.Healthy(ctx, cfg.MuseURL)
	if !museUp {
		log.Printf("muse is not reachable at %s; its conversations are recorded with %s", cfg.MuseURL, cfg.ChatModel)
	}
	pending := []Job{}
	for _, j := range Expand(cats) {
		if cfg.Only != "" && j.Flavor != cfg.Only {
			continue
		}
		if _, err := os.Stat(filepath.Join(cfg.OutDir, j.File)); err == nil {
			continue
		}
		if !museUp {
			j.Muse = false
		}
		pending = append(pending, j)
	}
	if cfg.Limit > 0 && len(pending) > cfg.Limit {
		pending = pending[:cfg.Limit]
	}
	r := &runner{cfg: cfg, tanto: tanto, fix: fix, total: len(pending)}
	log.Printf("recording %d conversations (concurrency %d, muse %v)", len(pending), cfg.Concurrency, museUp)
	chatJobs, museJobs := []Job{}, []Job{}
	for _, j := range pending {
		if j.Muse {
			museJobs = append(museJobs, j)
		} else {
			chatJobs = append(chatJobs, j)
		}
	}
	// The two models share the upstream GPU: running them at the same time
	// slows both down several times, so the Muse conversations (one slot)
	// are recorded after the chat model's.
	r.phase(ctx, chatJobs, cfg.Concurrency)
	r.phase(ctx, museJobs, 1)
	log.Printf("done: %d recorded, %d failed", r.done.Load(), r.fail.Load())
	if r.fail.Load() > 0 {
		return fmt.Errorf("%d conversations failed (rerun to retry them)", r.fail.Load())
	}
	return nil
}

// phase records jobs with n parallel workers.
func (r *runner) phase(ctx context.Context, jobs []Job, n int) {
	ch := make(chan Job)
	wg := sync.WaitGroup{}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.worker(ctx, ch) }()
	}
	feed(ctx, ch, jobs)
	wg.Wait()
}

func feed(ctx context.Context, ch chan<- Job, jobs []Job) {
	defer close(ch)
	for _, j := range jobs {
		select {
		case ch <- j:
		case <-ctx.Done():
			return
		}
	}
}

func (r *runner) worker(ctx context.Context, jobs <-chan Job) {
	for j := range jobs {
		start := time.Now()
		c, err := r.record(ctx, j)
		if err == nil {
			err = r.save(j, c)
		}
		n := r.done.Load() + r.fail.Load() + 1
		if err != nil {
			r.fail.Add(1)
			log.Printf("[%d/%d] FAIL %s: %v", n, r.total, j.ID, err)
			continue
		}
		r.done.Add(1)
		steps := 0
		for _, t := range c.Turns {
			steps += len(t.Steps)
		}
		log.Printf("[%d/%d] %s %s steps=%d quality=%s %s", n, r.total, j.ID, c.Model, steps, c.Quality,
			time.Since(start).Round(time.Second))
	}
}

// record runs one scripted conversation against the upstream model.
func (r *runner) record(ctx context.Context, j Job) (*engine.Cassette, error) {
	model, base := r.cfg.ChatModel, r.cfg.ChatURL
	if j.Muse {
		model, base = r.cfg.MuseModel, r.cfg.MuseURL
	}
	system := r.tanto.SystemPrompt
	tools := r.tanto.Tools
	switch j.Mode {
	case ModeTantoWeb:
		tools = append(append([]json.RawMessage{}, r.tanto.Tools...), r.tanto.WebTools...)
	case ModeCompaction:
		system, tools = r.tanto.CompactionPrompt, nil
	}
	c := &engine.Cassette{ID: j.ID, Flavor: j.Flavor, Model: model, Quality: "ok",
		RecordedAt: time.Now().UTC().Format(time.RFC3339)}
	history := []map[string]interface{}{{"role": "system", "content": system}}
	for _, userText := range j.Turns {
		history = append(history, map[string]interface{}{"role": "user", "content": userText})
		work := append([]map[string]interface{}{}, history...)
		turn := engine.Turn{UserMessage: userText}
		for round := 0; ; round++ {
			body := map[string]interface{}{"model": model, "messages": work, "max_tokens": r.cfg.MaxTokens}
			if len(tools) > 0 {
				body["tools"] = tools
				if round >= r.cfg.MaxRounds {
					body["tool_choice"] = "none"
				}
			}
			res, err := r.cfg.Upstream.Chat(ctx, base, body)
			if err != nil {
				return nil, err
			}
			step := engine.Step{Reasoning: res.Reasoning, Text: res.Content, FinishReason: res.Finish,
				Usage: engine.Usage{PromptTokens: res.PromptTokens, CompletionTokens: res.CompletionTokens}}
			if len(res.Calls) == 0 || round >= r.cfg.MaxRounds {
				turn.Steps = append(turn.Steps, step)
				history = append(history, map[string]interface{}{"role": "assistant", "content": res.Content})
				break
			}
			assistant := map[string]interface{}{"role": "assistant", "content": res.Content}
			calls := []map[string]interface{}{}
			for _, call := range res.Calls {
				calls = append(calls, map[string]interface{}{"id": call.ID, "type": "function",
					"function": map[string]string{"name": call.Name, "arguments": call.Arguments}})
			}
			assistant["tool_calls"] = calls
			work = append(work, assistant)
			for _, call := range res.Calls {
				args := map[string]interface{}{}
				if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
					args = map[string]interface{}{"_raw": call.Arguments}
				}
				result := r.runTool(ctx, call.Name, args)
				step.ToolCalls = append(step.ToolCalls, engine.RecordedCall{Name: call.Name, Arguments: args})
				step.ToolResults = append(step.ToolResults, result)
				work = append(work, map[string]interface{}{"role": "tool", "tool_call_id": call.ID, "content": result})
			}
			turn.Steps = append(turn.Steps, step)
		}
		c.Turns = append(c.Turns, turn)
	}
	return c, nil
}

// runTool answers a tool call: web tools go to the upstream web-tools
// service (falling back to synthetic results), data tools to the fixtures.
func (r *runner) runTool(ctx context.Context, name string, args map[string]interface{}) string {
	if _, bad := args["_raw"]; bad {
		return toJSON(map[string]string{"error": "invalid tool arguments: not a JSON object"})
	}
	switch name {
	case "web_search", "fetch_url":
		path := "/v1/search"
		if name == "fetch_url" {
			path = "/v1/fetch"
			if _, ok := args["max_chars"]; !ok {
				args["max_chars"] = 8000
			}
		}
		if r.cfg.WebURL != "" {
			if out, err := r.cfg.Upstream.WebTool(ctx, r.cfg.WebURL, path, args); err == nil {
				return truncate(out)
			}
		}
		return syntheticWeb(name, args)
	}
	return r.fix.Answer(name, args)
}

func syntheticWeb(name string, args map[string]interface{}) string {
	if name == "web_search" {
		q, _ := args["query"].(string)
		return toJSON(map[string]interface{}{"results": engine.SyntheticSearch(q, 5)})
	}
	u, _ := args["url"].(string)
	title, text := engine.SyntheticPage(u)
	return toJSON(engine.PageSlice(title, text, 0, 8000))
}

// save validates, templates and writes a cassette.
func (r *runner) save(j Job, c *engine.Cassette) error {
	quality, notes := Validate(c, j.Validators, j.ExpectTools)
	c.Quality = quality
	if len(notes) > 0 {
		log.Printf("%s flawed: %s", j.ID, strings.Join(notes, "; "))
	}
	Sanitize(c, j.Chosen, j.Pools)
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(r.cfg.OutDir, j.File)
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
