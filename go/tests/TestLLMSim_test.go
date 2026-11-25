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

package tests

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saichler/l8opensim/go/llmsim/engine"
)

func TestLLMSimParseOpenAI(t *testing.T) {
	body := `{"model":"gpt-oss-120b","max_tokens":100,"tool_choice":"none","messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":"first [NAME_AB12CD]"},
		{"role":"assistant","content":"ok"},
		{"role":"user","content":[{"type":"text","text":"second"}]},
		{"role":"assistant","content":null,"tool_calls":[{"id":"1","type":"function","function":{"name":"l8query","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"1","content":"{}"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"2","type":"function","function":{"name":"l8query","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"2","content":"{}"}],
		"tools":[{"type":"function","function":{"name":"describe_model","parameters":{"type":"object","properties":{"model":{"type":"string","enum":["A","B"]}}}}}]}`
	req, err := engine.ParseOpenAI([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if req.UserCount != 2 || req.ToolStep != 2 || req.LastUser != "second" || !req.NoTools || req.System != "sys" {
		t.Fatalf("unexpected parse: %+v", req)
	}
	if got := req.ModelEnum(); len(got) != 2 || got[0] != "A" {
		t.Fatalf("model enum: %v", got)
	}
	if engine.Normalize("First  [NAME_AB12CD] #sim:qa_chat") != engine.Normalize("first [NAME_ZZ99] ") {
		t.Fatal("normalization must ignore case, spacing, mask tokens and markers")
	}
	if _, err := engine.ParseOpenAI([]byte(`{"model":"x","stream":true,"messages":[{"role":"user","content":"hi"}]}`)); err == nil {
		t.Fatal("streaming must be rejected")
	}
}

func TestLLMSimParseAnthropic(t *testing.T) {
	body := `{"model":"claude-sim","max_tokens":100,"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],
		"tool_choice":{"type":"none"},
		"messages":[{"role":"user","content":"question"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"l8query","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"{}"}]}],
		"tools":[{"name":"l8query","input_schema":{"type":"object"}}]}`
	req, err := engine.ParseAnthropic([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if req.UserCount != 1 || req.ToolStep != 1 || req.LastUser != "question" || !req.NoTools || req.System != "sys" {
		t.Fatalf("unexpected parse: %+v", req)
	}
}

func TestLLMSimToolLoopOpenAI(t *testing.T) {
	_, srv := startLLMSim(t)
	url := srv.URL + "/v1/chat/completions"
	user := map[string]interface{}{"role": "user", "content": "Export the devices in Dallas to CSV"}
	msgs := []interface{}{map[string]string{"role": "system", "content": "You are Tanto"}, user}
	tools := tantoTools("GpuDevice", "Alarm")
	names := []string{}
	for step := 0; step < 3; step++ {
		status, _, out := llmsimPost(t, url, openAIHeaders(),
			map[string]interface{}{"model": "gpt-oss-120b", "messages": msgs, "tools": tools})
		if status != 200 {
			t.Fatalf("step %d: status %d %v", step, status, out)
		}
		choice := openAIChoice(t, out)
		msg := choice["message"].(map[string]interface{})
		calls, _ := msg["tool_calls"].([]interface{})
		if step < 2 {
			if choice["finish_reason"] != "tool_calls" || len(calls) != 1 {
				t.Fatalf("step %d: expected one tool call, got %v", step, choice)
			}
			fn := calls[0].(map[string]interface{})["function"].(map[string]interface{})
			names = append(names, fn["name"].(string))
			args := map[string]interface{}{}
			if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil {
				t.Fatalf("arguments are not JSON: %v", err)
			}
			if step == 0 && args["model"] != "GpuDevice" {
				t.Fatalf("model must be fitted to the request enum, got %v", args["model"])
			}
			if step == 1 {
				q := args["query"].(string)
				if !strings.Contains(q, "from GpuDevice") || strings.Contains(q, "where") || !strings.Contains(q, "limit 50") {
					t.Fatalf("query not fitted: %s", q)
				}
			}
			msgs = append(msgs, map[string]interface{}{"role": "assistant", "content": nil, "tool_calls": calls},
				map[string]interface{}{"role": "tool", "tool_call_id": "x", "content": "{}"})
			continue
		}
		text, _ := msg["content"].(string)
		if !strings.HasPrefix(text, "STEP-FINAL") || choice["finish_reason"] != "stop" {
			t.Fatalf("final step: %v", choice)
		}
		if !strings.Contains(text, "in Dallas") {
			t.Fatalf("the site named by the user must be used: %s", text)
		}
		usage := out["usage"].(map[string]interface{})
		if usage["prompt_tokens"].(float64) < 1 || usage["completion_tokens"].(float64) < 1 {
			t.Fatalf("usage missing: %v", usage)
		}
	}
	if names[0] != "describe_model" || names[1] != "export_csv" {
		t.Fatalf("tool sequence: %v", names)
	}
}

func TestLLMSimToolChoiceNoneAndMissingTools(t *testing.T) {
	_, srv := startLLMSim(t)
	url := srv.URL + "/v1/chat/completions"
	msgs := []interface{}{map[string]string{"role": "user", "content": "Export the devices in Austin to CSV"}}
	_, _, out := llmsimPost(t, url, openAIHeaders(), map[string]interface{}{"model": "gpt-oss-120b",
		"messages": msgs, "tools": tantoTools("GpuDevice"), "tool_choice": "none"})
	choice := openAIChoice(t, out)
	if text, _ := choice["message"].(map[string]interface{})["content"].(string); !strings.HasPrefix(text, "STEP-FINAL") {
		t.Fatalf("tool_choice none must return the final text: %v", choice)
	}
	_, _, out = llmsimPost(t, url, openAIHeaders(), map[string]interface{}{"model": "gpt-oss-120b", "messages": msgs})
	choice = openAIChoice(t, out)
	if choice["finish_reason"] != "stop" {
		t.Fatalf("without tools the reply must be text: %v", choice)
	}
}

func TestLLMSimAnthropic(t *testing.T) {
	_, srv := startLLMSim(t)
	status, _, out := llmsimPost(t, srv.URL+"/v1/messages", map[string]string{"x-api-key": llmsimKeyAnthropic},
		map[string]interface{}{"model": "claude-sim", "max_tokens": 1000,
			"messages": []interface{}{map[string]string{"role": "user", "content": "Export the devices in Austin to CSV"}},
			"tools": []interface{}{map[string]interface{}{"name": "describe_model", "input_schema": map[string]interface{}{
				"type": "object", "properties": map[string]interface{}{"model": map[string]interface{}{"type": "string", "enum": []string{"Alarm"}}}}}}})
	if status != 200 || out["stop_reason"] != "tool_use" || out["type"] != "message" {
		t.Fatalf("anthropic tool step: %d %v", status, out)
	}
	block := out["content"].([]interface{})[0].(map[string]interface{})
	if block["type"] != "tool_use" || block["input"].(map[string]interface{})["model"] != "Alarm" {
		t.Fatalf("tool_use block: %v", block)
	}
	if _, ok := out["usage"].(map[string]interface{})["output_tokens"]; !ok {
		t.Fatalf("usage missing: %v", out)
	}
}

func TestLLMSimBindingAndTurns(t *testing.T) {
	_, srv := startLLMSim(t)
	url := srv.URL + "/v1/chat/completions"
	ask := func(model string, msgs ...string) string {
		list := []interface{}{}
		for i, m := range msgs {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			list = append(list, map[string]string{"role": role, "content": m})
		}
		_, _, out := llmsimPost(t, url, openAIHeaders(), map[string]interface{}{"model": model, "messages": list})
		text, _ := openAIChoice(t, out)["message"].(map[string]interface{})["content"].(string)
		return text
	}
	if got := ask("gpt-oss-120b", "#sim:csv_generate please"); !strings.HasPrefix(got, "CSV-ANSWER") {
		t.Fatalf("marker binding: %q", got)
	}
	if got := ask("gpt-oss-120b", "What is BGP?"); !strings.HasPrefix(got, "QA-TURN-1") {
		t.Fatalf("exact binding: %q", got)
	}
	if got := ask("gpt-oss-120b", "What is BGP?", "...", "And OSPF?"); got != "QA-TURN-2" {
		t.Fatalf("second turn: %q", got)
	}
	if got := ask("muse-glimmer-30b", "something unrelated"); got != "MUSE-ANSWER" {
		t.Fatalf("model preference in the default mix: %q", got)
	}
	_, _, out := llmsimPost(t, url, openAIHeaders(), map[string]interface{}{"model": "gpt-oss-120b",
		"messages": []interface{}{map[string]string{"role": "system", "content": "You maintain the running summary of a conversation"},
			map[string]string{"role": "user", "content": "Current summary: x"}}})
	if text := openAIChoice(t, out)["message"].(map[string]interface{})["content"]; text != "COMPACTED summary" {
		t.Fatalf("compaction binding: %v", text)
	}
}

func TestLLMSimFlavorDeterminismAndCSV(t *testing.T) {
	_, srv := startLLMSim(t)
	url := srv.URL + "/v1/chat/completions"
	reply := func(first string) string {
		_, _, out := llmsimPost(t, url, openAIHeaders(), map[string]interface{}{"model": "gpt-oss-120b",
			"messages": []interface{}{map[string]string{"role": "user", "content": first},
				map[string]string{"role": "assistant", "content": "ok"},
				map[string]string{"role": "user", "content": "#sim:csv_generate generate"}}})
		return openAIChoice(t, out)["message"].(map[string]interface{})["content"].(string)
	}
	a1, a2, b := reply("conversation A"), reply("conversation A"), reply("conversation B")
	if a1 != a2 {
		t.Fatal("the same request must get the same answer")
	}
	if a1 == b {
		t.Fatal("different conversations must get different answers")
	}
	for _, text := range []string{a1, b} {
		body := strings.SplitN(strings.SplitN(text, "```csv\n", 2)[1], "\n```", 2)[0]
		r := csv.NewReader(strings.NewReader(body))
		r.FieldsPerRecord = 4
		rows, err := r.ReadAll()
		if err != nil || len(rows) != 21 {
			t.Fatalf("flavored CSV invalid (%v), %d rows:\n%s", err, len(rows), body)
		}
	}
}

func TestLLMSimAuthAndErrors(t *testing.T) {
	_, srv := startLLMSim(t)
	url := srv.URL + "/v1/chat/completions"
	body := map[string]interface{}{"model": "gpt-oss-120b", "messages": []interface{}{map[string]string{"role": "user", "content": "hi"}}}
	if s, _, _ := llmsimPost(t, url, nil, body); s != 401 {
		t.Fatalf("missing key: %d", s)
	}
	if s, _, _ := llmsimPost(t, url, map[string]string{"Authorization": "Bearer " + llmsimKeyAnthropic}, body); s != 401 {
		t.Fatalf("anthropic key on the OpenAI API: %d", s)
	}
	if s, _, out := llmsimPost(t, url, openAIHeaders(), map[string]interface{}{"model": "nope",
		"messages": []interface{}{map[string]string{"role": "user", "content": "hi"}}}); s != 404 || out["error"] == nil {
		t.Fatalf("unknown model: %d %v", s, out)
	}
	if s, _, _ := llmsimPost(t, url, openAIHeaders(), map[string]interface{}{"model": "gpt-oss-120b", "max_tokens": 200000,
		"messages": []interface{}{map[string]string{"role": "user", "content": "hi"}}}); s != 400 {
		t.Fatalf("context exceeded: %d", s)
	}
	limited := map[string]string{"Authorization": "Bearer sk-test-limited"}
	for i := 0; i < 2; i++ {
		if s, _, _ := llmsimPost(t, url, limited, body); s != 200 {
			t.Fatalf("request %d under the rate limit: %d", i, s)
		}
	}
	s, h, _ := llmsimPost(t, url, limited, body)
	if s != 429 || h.Get("Retry-After") == "" {
		t.Fatalf("rate limit: %d retry-after=%q", s, h.Get("Retry-After"))
	}
}

func TestLLMSimCapsAndProfileSwitch(t *testing.T) {
	e, srv := startLLMSim(t)
	if _, err := e.ApplyProfile([]byte(`{"name":"limited"}`)); err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/v1/chat/completions"
	body := map[string]interface{}{"model": "gpt-oss-120b", "messages": []interface{}{map[string]string{"role": "user", "content": "hi"}}}
	statuses := make([]int, 2)
	wg := sync.WaitGroup{}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			time.Sleep(time.Duration(i) * 100 * time.Millisecond)
			statuses[i], _, _ = llmsimPost(t, url, openAIHeaders(), body)
		}(i)
	}
	wg.Wait()
	if statuses[0] != 200 || statuses[1] != 429 {
		t.Fatalf("pool cap of 1 with a slow first request: %v", statuses)
	}
	if _, err := e.ApplyProfile([]byte(`{"faults":{"http_503":1}}`)); err != nil {
		t.Fatal(err)
	}
	if s, h, _ := llmsimPost(t, url, openAIHeaders(), body); s != 503 || h.Get("Retry-After") == "" {
		t.Fatalf("injected 503: %d", s)
	}
	if _, err := e.ApplyProfile([]byte(`{"name":"default"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyProfile([]byte(`{"faults":{"empty_answer":1}}`)); err != nil {
		t.Fatal(err)
	}
	_, _, out := llmsimPost(t, url, openAIHeaders(), body)
	if text := openAIChoice(t, out)["message"].(map[string]interface{})["content"]; text != "" {
		t.Fatalf("empty_answer fault: %v", text)
	}
	if _, err := e.ApplyProfile([]byte(`{"faults":{"no_such_fault":1}}`)); err == nil {
		t.Fatal("unknown faults must be rejected")
	}
}

func TestLLMSimNoAddedDelay(t *testing.T) {
	_, srv := startLLMSim(t)
	body := map[string]interface{}{"model": "gpt-oss-120b", "messages": []interface{}{map[string]string{"role": "user", "content": "What is BGP?"}}}
	llmsimPost(t, srv.URL+"/v1/chat/completions", openAIHeaders(), body)
	start := time.Now()
	for i := 0; i < 50; i++ {
		llmsimPost(t, srv.URL+"/v1/chat/completions", openAIHeaders(), body)
	}
	if per := time.Since(start) / 50; per > 20*time.Millisecond {
		t.Fatalf("responses must be instant, got %v per request", per)
	}
}

func TestLLMSimWebTools(t *testing.T) {
	_, srv := startLLMSim(t)
	s, _, out := llmsimPost(t, srv.URL+"/v1/search", openAIHeaders(), map[string]interface{}{"query": "latest go release"})
	results, _ := out["results"].([]interface{})
	if s != 200 || len(results) != 1 || results[0].(map[string]interface{})["title"] != "Go 1.26" {
		t.Fatalf("recorded search: %d %v", s, out)
	}
	_, _, out = llmsimPost(t, srv.URL+"/v1/search", openAIHeaders(), map[string]interface{}{"query": "anything else", "max_results": 3})
	if results, _ = out["results"].([]interface{}); len(results) != 3 {
		t.Fatalf("synthetic search: %v", out)
	}
	s, _, out = llmsimPost(t, srv.URL+"/v1/fetch", openAIHeaders(), map[string]interface{}{"url": "https://example.com/x", "max_chars": 500})
	if s != 200 || out["truncated"] != true || out["next_start"].(float64) != 500 {
		t.Fatalf("fetch paging: %d %v", s, out)
	}
	if s, _, _ = llmsimPost(t, srv.URL+"/v1/fetch", openAIHeaders(), map[string]interface{}{"url": "http://192.168.1.1/"}); s != 400 {
		t.Fatalf("LAN fetch must be blocked: %d", s)
	}
}

func TestLLMSimStatsAndFlavorCheck(t *testing.T) {
	e, srv := startLLMSim(t)
	llmsimPost(t, srv.URL+"/v1/chat/completions", openAIHeaders(), map[string]interface{}{"model": "gpt-oss-120b",
		"messages": []interface{}{map[string]string{"role": "user", "content": "What is BGP?"}}})
	resp, err := http.Get(srv.URL + "/sim/stats")
	if err != nil {
		t.Fatal(err)
	}
	stats := map[string]interface{}{}
	_ = json.NewDecoder(resp.Body).Decode(&stats)
	resp.Body.Close()
	if stats["requests_total"].(float64) < 1 || stats["by_flavor"].(map[string]interface{})["qa_chat"] == nil {
		t.Fatalf("stats: %v", stats)
	}
	resp, err = http.Get(srv.URL + "/metrics")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("metrics: %v", err)
	}
	resp.Body.Close()
	rep := engine.FlavorCheck(e.Library(), 4)
	if len(rep.Failures) > 0 {
		t.Fatalf("flavor check: %v", rep.Failures)
	}
}

func TestLLMSimSessionContinuity(t *testing.T) {
	_, srv := startLLMSim(t)
	url := srv.URL + "/v1/chat/completions"
	ask := func(msgs ...string) string {
		list := []interface{}{}
		for i, m := range msgs {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			list = append(list, map[string]string{"role": role, "content": m})
		}
		_, _, out := llmsimPost(t, url, openAIHeaders(), map[string]interface{}{"model": "gpt-oss-120b", "messages": list})
		text, _ := openAIChoice(t, out)["message"].(map[string]interface{})["content"].(string)
		return text
	}
	t1 := ask("Any critical alarms?")
	// "Yes, go ahead." is in two sessions: the first message picks the right one.
	for i := 0; i < 5; i++ {
		if got := ask("Any critical alarms?", t1, "Yes, go ahead."); !strings.HasPrefix(got, "S1-T2") {
			t.Fatalf("shared follow-up bound to the wrong session: %q", got)
		}
		if got := ask("Clear the fan alarms.", "S2-T1", "Yes, go ahead."); got != "S2-T2" {
			t.Fatalf("shared follow-up bound to the wrong session: %q", got)
		}
	}
	// A reworded follow-up continues the recorded session.
	t3 := ask("Any critical alarms?", t1, "ok", "x", "please write me a status update")
	if !strings.HasPrefix(t3, "S1-T3") {
		t.Fatalf("session continuation: %q", t3)
	}
	// Flavored values stay the same across the turns of a session.
	site := strings.TrimPrefix(t1, "S1-T1 site ")
	if !strings.HasSuffix(t3, "site "+site) {
		t.Fatalf("flavored value changed within a session: %q vs %q", t1, t3)
	}
}
