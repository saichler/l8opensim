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
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/saichler/l8opensim/go/llmsim/engine"
)

// Helpers for TestLLMSim_test.go: an in-memory cassette library, a profile
// directory and an httptest server running the simulator.

const llmsimKeyOpenAI = "sk-test-openai"
const llmsimKeyAnthropic = "sk-test-anthropic"

var llmsimProfile = map[string]interface{}{
	"name": "default", "n_ctx": 131072, "auth": true,
	"keys": []map[string]interface{}{
		{"key": llmsimKeyOpenAI, "name": "openai", "apis": []string{"openai", "webtools"}},
		{"key": llmsimKeyAnthropic, "name": "anthropic", "apis": []string{"anthropic"}},
		{"key": "sk-test-limited", "name": "limited", "rpm": 2},
	},
	"models": []map[string]string{
		{"alias": "gpt-oss-120b", "pool": "coder"}, {"alias": "claude-sim", "pool": "coder"},
		{"alias": "muse-glimmer-30b", "pool": "muse"},
	},
	"pools":      map[string]interface{}{"coder": map[string]int{"max_concurrent": 0}, "muse": map[string]int{"max_concurrent": 1}},
	"flavor_mix": map[string]float64{"qa_chat": 1},
	"classifier": []map[string]interface{}{
		{"flavor": "csv_export", "all": []string{"export"}, "any": []string{"csv"}},
		{"flavor": "web_research", "any": []string{"@web"}},
	},
}

// llmsimCassettes builds the test library: a two-step tool flavor, a CSV
// flavor, a chat flavor, a compaction flavor and a web flavor.
func llmsimCassettes() []*engine.Cassette {
	return []*engine.Cassette{
		{ID: "csv_export/t1", Flavor: "csv_export", Model: "gpt-oss-120b", Quality: "ok",
			Params: map[string][]string{"site": {"Austin", "Dallas", "Seattle"}},
			Turns: []engine.Turn{{UserMessage: "Export the devices in Austin to CSV", Steps: []engine.Step{
				{ToolCalls: []engine.RecordedCall{{Name: "describe_model", Arguments: map[string]interface{}{"model": "NetworkDevice"}}},
					Usage: engine.Usage{CompletionTokens: 20}, Chars: 40},
				{ToolCalls: []engine.RecordedCall{{Name: "export_csv", Arguments: map[string]interface{}{
					"query": "select * from NetworkDevice where equipmentinfo.location={{p:site}} limit 50"}}},
					Usage: engine.Usage{CompletionTokens: 30}, Chars: 90},
				{Text: "STEP-FINAL exported {{int:10:400}} devices in {{p:site}} for {{name:1}}.", FinishReason: "stop",
					Usage: engine.Usage{CompletionTokens: 25}, Chars: 60},
			}}}},
		{ID: "csv_generate/t1", Flavor: "csv_generate", Model: "gpt-oss-120b", Quality: "ok",
			CSV: []engine.CSVSpec{{Header: []string{"id", "name", "salary", "hired"}, Rows: 20, Columns: []engine.CSVColumn{
				{Kind: "seq", Min: 1}, {Kind: "name"}, {Kind: "int", Min: 40000, Max: 150000}, {Kind: "date"}}}},
			Turns: []engine.Turn{{UserMessage: "Generate a CSV of employees", Steps: []engine.Step{
				{Text: "CSV-ANSWER\n```csv\nid,name,salary,hired\n{{csv:0}}\n```", Usage: engine.Usage{CompletionTokens: 400}}}}}},
		{ID: "qa_chat/t1", Flavor: "qa_chat", Model: "gpt-oss-120b", Quality: "ok",
			Turns: []engine.Turn{
				{UserMessage: "What is BGP?", Steps: []engine.Step{{Text: "QA-TURN-1 about {{city:1}}", Reasoning: "think"}}},
				{UserMessage: "And OSPF?", Steps: []engine.Step{{Text: "QA-TURN-2"}}}}},
		{ID: "conversation/s1", Flavor: "conversation", Model: "gpt-oss-120b", Quality: "ok",
			Turns: []engine.Turn{
				{UserMessage: "Any critical alarms?", Steps: []engine.Step{{Text: "S1-T1 site {{city:1}}"}}},
				{UserMessage: "Yes, go ahead.", Steps: []engine.Step{{Text: "S1-T2 site {{city:1}}"}}},
				{UserMessage: "Write a status update.", Steps: []engine.Step{{Text: "S1-T3 site {{city:1}}"}}}}},
		{ID: "conversation/s2", Flavor: "conversation", Model: "gpt-oss-120b", Quality: "ok",
			Turns: []engine.Turn{
				{UserMessage: "Clear the fan alarms.", Steps: []engine.Step{{Text: "S2-T1"}}},
				{UserMessage: "Yes, go ahead.", Steps: []engine.Step{{Text: "S2-T2"}}}}},
		{ID: "qa_chat/muse", Flavor: "qa_chat", Model: "muse-glimmer-30b", Quality: "ok",
			Turns: []engine.Turn{{UserMessage: "Muse question", Steps: []engine.Step{{Text: "MUSE-ANSWER"}}}}},
		{ID: "compaction/t1", Flavor: "compaction", Model: "gpt-oss-120b", Quality: "ok",
			Turns: []engine.Turn{{UserMessage: "Current summary:", Steps: []engine.Step{{Text: "COMPACTED summary"}}}}},
		{ID: "web_research/t1", Flavor: "web_research", Model: "gpt-oss-120b", Quality: "ok",
			Turns: []engine.Turn{{UserMessage: "@web latest go", Steps: []engine.Step{
				{ToolCalls: []engine.RecordedCall{{Name: "web_search", Arguments: map[string]interface{}{"query": "latest go release"}}},
					ToolResults: []string{`{"results":[{"title":"Go 1.26","url":"https://go.dev/doc/devel/release","snippet":"Go 1.26 released"}]}`}},
				{Text: "WEB-ANSWER Go 1.26"}}}}},
	}
}

// startLLMSim starts the simulator on an httptest server.
func startLLMSim(t *testing.T) (*engine.Engine, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	data, _ := json.Marshal(llmsimProfile)
	if err := os.WriteFile(filepath.Join(dir, "default.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	limited := `{"name":"limited","extends":"default","pools":{"coder":{"max_concurrent":1}},
		"faults":{"slow_response":1},"slow_response_ms":400}`
	if err := os.WriteFile(filepath.Join(dir, "limited.json"), []byte(limited), 0o644); err != nil {
		t.Fatal(err)
	}
	lib, err := engine.NewLibrary(llmsimCassettes())
	if err != nil {
		t.Fatal(err)
	}
	e, err := engine.New(engine.Options{ProfileDir: dir, Profile: "default", Library: lib})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e.Handler())
	t.Cleanup(srv.Close)
	return e, srv
}

// llmsimPost sends a JSON body and returns the status, headers and decoded body.
func llmsimPost(t *testing.T, url string, headers map[string]string, body interface{}) (int, http.Header, map[string]interface{}) {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]interface{}{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, resp.Header, out
}

func openAIHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + llmsimKeyOpenAI}
}

// tantoTools returns Tanto-like tool definitions with a model enum.
func tantoTools(models ...string) []map[string]interface{} {
	model := map[string]interface{}{"type": "string", "enum": models}
	fn := func(name string, props map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": name,
			"parameters": map[string]interface{}{"type": "object", "properties": props}}}
	}
	return []map[string]interface{}{
		fn("l8query", map[string]interface{}{"query": map[string]string{"type": "string"}}),
		fn("describe_model", map[string]interface{}{"model": model}),
		fn("export_csv", map[string]interface{}{"query": map[string]string{"type": "string"}}),
	}
}

// openAIChoice returns choices[0] of an OpenAI response.
func openAIChoice(t *testing.T, out map[string]interface{}) map[string]interface{} {
	t.Helper()
	choices, ok := out["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		t.Fatalf("no choices in %v", out)
	}
	return choices[0].(map[string]interface{})
}
