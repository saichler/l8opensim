# LLM Simulator (llmsim) — guide for agents

llmsim is an OpenAI- and Anthropic-compatible LLM endpoint that answers **instantly** with recorded, flavored
conversations. Use it wherever a real LLM would be too slow or too expensive, for example to put load on an AI agent.
It does no real inference: answers are realistic in shape, size and tool usage, but they are not true answers to
your exact question.

- Code: `go/llmsim/` (engine in `engine/`, recorder in `recorder/`, CLI in `cmd/llmsim/`)
- Plan: `plans/llm-router-simulation.md`
- All commands below run from `go/llmsim/`, where the default paths (`profiles/`, `scenarios/`) resolve.

## Start it

```bash
cd go/llmsim
go build -o llmsim ./cmd/llmsim
./llmsim serve                                  # :9080, profile "default", cassettes in scenarios/recorded
curl http://localhost:9080/health               # → {"status":"ok"}
```

`serve` flags:

| Flag | Default | Meaning |
|---|---|---|
| `-listen` | `:9080` | Listen address |
| `-profiles` | `profiles` | Directory with the profile JSON files |
| `-profile` | `default` | Profile to start with |
| `-scenarios` | `scenarios/recorded` | Directory with the recorded cassettes |
| `-auth` | `on` | `off` skips API key checks |
| `-tls-cert`, `-tls-key` | — | Serve HTTPS with this certificate and key (plain HTTP otherwise) |
| `-reuseport` | `false` | SO_REUSEPORT: run several llmsim processes on the same port |
| `-min-fds` | `65536` | Fail at start if the open-files limit is lower (0 = no check) |
| `-trace-file`, `-trace-sample` | —, `0.01` | Sampled JSON-lines trace of matched requests |

## Endpoints (one port, routed by the `model` field)

| Call | Purpose |
|---|---|
| `POST /v1/chat/completions` | OpenAI chat completions (non-streaming), with function tools |
| `POST /v1/messages` | Anthropic Messages (non-streaming), with `tool_use` |
| `POST /v1/search`, `POST /v1/fetch` | Web tools (same bodies and results as the Halo web-tools service) |
| `GET /v1/models` | Configured model aliases |
| `GET /health` | Liveness (no key needed) |
| `GET /sim/stats` | Simulator statistics (JSON) |
| `GET /metrics` | Simulator statistics (Prometheus text) |
| `GET /sim/profile`, `PUT /sim/profile` | Read or change the active profile at runtime (no key needed) |

`/chat/completions` and `/messages` without the `/v1` prefix work too. Streaming (`"stream": true`) is rejected
with 400.

Models (profile `default`): `gpt-oss-120b` and `claude-sim` (pool `coder`), `muse-glimmer-30b` (pool `muse`).
Any model alias works with both APIs.

## Keys

Send `Authorization: Bearer <key>` (OpenAI API, web tools) or `x-api-key: <key>` (Anthropic API). Keys are listed in
the active profile:

| Key | APIs |
|---|---|
| `sk-sim-tanto-1` | OpenAI, web tools |
| `sk-sim-tanto-2` | Anthropic, web tools |
| `sk-sim-agent-1`, `sk-sim-agent-2` | all |

These are simulator keys, not secrets. A wrong or missing key, or a key used on an API it is not listed for, gets
401, unless the server runs with `-auth off`.

## Examples

```bash
curl http://localhost:9080/v1/chat/completions -H "Authorization: Bearer sk-sim-tanto-1" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-oss-120b","messages":[{"role":"user","content":"Write a simple TODO REST API in Go"}]}'

curl http://localhost:9080/v1/messages -H "x-api-key: sk-sim-tanto-2" -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-sim","max_tokens":2000,"messages":[{"role":"user","content":"#sim:csv_generate employees please"}]}'

curl http://localhost:9080/v1/search -H "Authorization: Bearer sk-sim-tanto-1" -d '{"query":"latest kubernetes release"}'
```

Python (openai SDK):

```python
from openai import OpenAI
llm = OpenAI(base_url="http://localhost:9080/v1", api_key="sk-sim-tanto-1")
r = llm.chat.completions.create(model="gpt-oss-120b", messages=[{"role": "user", "content": "What is BGP?"}])
print(r.choices[0].message.content)
```

## Using it from Tanto (probler)

`../l8secure/go/secure/plugin/probler/probler.json` defines these:

| Profile | Provider | API | Model |
|---|---|---|---|
| `llmsim` | `llmsim` | OpenAI | `gpt-oss-120b` |
| `llmsim-muse` | `llmsim` | OpenAI | `muse-glimmer-30b` |
| `llmsim-anthropic` | `llmsim-anthropic` | Anthropic | `claude-sim` |

Both providers point at `http://192.168.1.176:9080/v1` (plain HTTP, so no CA is needed). If llmsim runs on another
host, change `baseUrl` in both providers. Select one of the profiles for a Tanto conversation. The `default`
profile (the real LLM) stays the default.

## Choosing what answers

Every answer comes from a recorded conversation (a *cassette*) of one *flavor*. llmsim picks it from the request
alone, without session state:

| Priority | Method | How |
|---|---|---|
| 1 | Compaction | A system prompt containing Tanto's compaction prompt → flavor `compaction` |
| 2 | Explicit flavor | `#sim:<flavor>` anywhere in the last user message, e.g. `#sim:csv_export Export all GPUs` |
| 2 | Explicit cassette | `#sim:<cassette id>`, e.g. `#sim:csv_export/devices-by-site/v0/s1` |
| 3 | Exact catalog message | Send a catalog user message verbatim (see `scenarios/catalog/*.json`). A follow-up shared by several recordings (e.g. "Yes, go ahead.") binds to the recording that starts with the conversation's first message |
| 3 | Session continuation | Turn N of a conversation whose first message started a recorded multi-turn session, even when turn N is worded differently: llmsim answers with that session's turn N |
| 4 | Classifier | Keyword rules in the profile (`classifier`) map the user message to a flavor |
| 5 | Default mix | The profile's weighted `flavor_mix` |

The `#sim:` tag is removed before matching and never echoed back.

Flavors: `data_query`, `data_aggregate`, `csv_export`, `report_file`, `record_crud`, `explore_models`,
`web_research`, `compaction`, `code_app`, `code_review`, `code_debug`, `csv_generate`, `csv_analyze`,
`summarize_long`, `writing`, `qa_chat`, `reasoning_math`, `edge_cases`, `conversation`.

### Real-life sessions (`conversation` flavor)

`scenarios/catalog/conversation.json` holds 20 scripted operator sessions of 6–9 user turns each that mix data
queries, alarms, CRUD with confirmations, CSV export, code, reviews, writing and small talk (e.g.
`noc-morning-check`, `outage-investigation`, `k8s-triage`, `weekly-report`, `short-chatty`). To drive realistic
multi-request sessions, send the turns of one script in order, appending each llmsim reply (and the tool
results) to the history before the next turn. Every turn costs Tanto one llmsim request per tool round plus the
final answer. Within one session the flavored values (names, numbers, sites, CSV rows) stay the same across
turns. Different sessions differ when their first message differs or when they set the OpenAI `user` /
Anthropic `metadata.user_id` field.

## Behavior you can rely on

- **Instant:** no generation delay, unless the opt-in `slow_response` fault is enabled.
- **Deterministic:** the same request returns the same answer, on any llmsim instance. The key is the normalized
  last user message, the number of user messages and the first user message, plus the OpenAI `user` field or the
  Anthropic `metadata.user_id` when present. Different conversations get different, flavored answers (names,
  numbers, CSV rows, sites, identifiers).
- **Follows the question:** when the user message names a value the recording was parameterized with (for
  example a site such as "Dallas"), the answer uses it.
- **Tool loops:**
  - If the request defines `tools`, the reply may contain tool calls.
  - Send the tool results in the next request (`role: tool` for OpenAI, `tool_result` blocks for Anthropic).
    llmsim answers with the next recorded step, and finally with a text answer.
  - Tool calls only use tool names defined in your request. Unknown ones are remapped to a similar tool, or the
    step becomes a text answer.
  - A `model` argument always comes from your tool's `enum`. Queries (`... from <Model> ...`) are rewritten to an
    allowed model, and their `where` clause is dropped when the model changes.
- **`tool_choice: "none"`** (OpenAI) or `{"type":"none"}` (Anthropic) always returns text.
- **Usage:** `usage` is always present. Prompt tokens are estimated as request bytes / 4. Completion tokens are
  the recorded count scaled to the flavored text.
- **Context limit:** above 131,072 tokens (prompt estimate + `max_tokens`) you get a 400 "context length exceeded".
- **Muse:** requests for `muse-glimmer-30b` prefer cassettes recorded with Muse.

## Profiles and runtime changes

| Profile | Behavior |
|---|---|
| `default` | Unlimited capacity, no faults, 5% flawed recordings |
| `limited` | Pool caps (`coder` 500, `muse` 50 falling back to `coder`) and per-key RPM/TPM. Over a limit → immediate 429 with `Retry-After` (never queued) |
| `chaos` | Fault injection on, 15% flawed recordings |

```bash
curl http://localhost:9080/sim/profile                                           # show the active profile
curl -X PUT http://localhost:9080/sim/profile -d '{"name":"limited"}'            # switch profile
curl -X PUT http://localhost:9080/sim/profile -d '{"faults":{"http_429":0.05}}'  # patch one knob (maps merge)
curl -X PUT http://localhost:9080/sim/profile -d '{"pools":{"coder":{"max_concurrent":200}}}'
```

Faults (rates are fractions of requests; all off in `default`):

| Fault | Effect |
|---|---|
| `http_429`, `http_503` | Immediate error with `Retry-After` |
| `http_500`, `http_400` | Immediate error |
| `drop_connection` | Connection closed without a response |
| `slow_response` | Opt-in only: delay by `slow_response_ms` (default 5000) before answering |
| `context_exceeded` | 400 "context length exceeded" |
| `truncated_length` | Text cut, `finish_reason: length` / `stop_reason: max_tokens` |
| `malformed_tool_json` | First tool call's arguments are invalid JSON |
| `bad_model_in_tool` | A tool call uses the model `NoSuchModel` |
| `empty_answer` | Empty text, no tool calls |

Other profile fields:
- `n_ctx`
- `chars_per_token`
- `auth`
- `keys` (`key`, `name`, `apis`, `rpm`, `tpm`)
- `models` (`alias`, `pool`)
- `pools` (`max_concurrent`, `fallback`)
- `reject_status`
- `retry_after_seconds`
- `flawed_fraction`
- `flavor_mix`
- `classifier` (`flavor`, `all`, `any`, `tools`)
- `compaction_marker`

A profile file can `"extends": "<other profile>"`.

## Stats

`GET /sim/stats` reports:
- `requests_total` and `requests_per_second`
- `in_flight` and `in_flight_by_pool`
- `rejected`
- `unmatched` (answered by the default mix)
- `tool_steps_served`, `tool_choice_none`, `compaction_requests`
- `tool_fit_substitutions`
- `by_api`, `by_model`, `by_flavor`, `by_binding`, `by_fault`, `by_status`
- `latency_histogram` and `latency_percentiles_us` (simulator time from request received to response written)

`GET /metrics` exposes the same as Prometheus series named `llmsim_*`.

If the simulator latency grows, the simulator is the bottleneck, not your client. Add instances: several
processes with `-reuseport` on one host, or several hosts behind a balancer. Matching is stateless, so every
instance gives the same answers.

For high session counts, raise the open-files limit (`ulimit -n`, or `increase_file_limits.sh` in the repo root).

## Recording new scenarios (needs Halo access)

```bash
export L8_LLM_KEY=<Halo chat key>              # from llm/llm-api.md (never committed)
./llmsim record                                # every catalog job without a cassette yet (resumable)
./llmsim record -only csv_export -limit 5      # a subset
./llmsim flavor-check                          # validate the cassettes' flavoring
```

`record` runs each catalog conversation against Halo with Tanto's system prompt and tools
(`scenarios/tanto_tools.json`):
- Data tools are answered from `scenarios/fixtures/models.json`.
- Web tools go to Halo's web-tools service (port 8083), falling back to synthetic results.
- Recordings that fail validation (no CSV, unparsable code, missing expected tool, empty answer) are kept with
  `quality: "flawed"`.

Main flags:

| Flag | Default |
|---|---|
| `-upstream` | `https://192.168.1.124:8080/v1` |
| `-muse-upstream` | `https://192.168.1.124:8082/v1` |
| `-web-upstream` | `https://192.168.1.124:8083` |
| `-ca` | `../../llm/halo-ca.crt` |
| `-concurrency` | 1 |
| `-max-rounds` | 8 |
| `-max-tokens` | 8192 |
| `-only` | — |
| `-limit` | — |
| `-out` | `scenarios/recorded` |

An existing cassette file marks its job as done, so an interrupted run resumes where it stopped.

Recording proxy (records any OpenAI-compatible client's real traffic):

```bash
./llmsim proxy-record -listen :9090 -flavor my_agent   # point the client at http://<host>:9090/v1
```

Each conversation is written to `scenarios/recorded/<flavor>/proxy-<key>.json`.

### File formats

Catalog (`scenarios/catalog/<flavor>.json`):

```json
{"flavor": "csv_export", "mode": "tanto", "validators": ["tool_sequence"], "expect_tools": ["export_csv"],
 "samples_per_variant": 3, "variants_per_message": 1, "muse_fraction": 0.1,
 "messages": [{"id": "devices-by-site", "params": {"site": ["Austin", "Dallas"]},
               "turns": ["Export all network devices in {{site}} to CSV."]}]}
```

- `mode` is `tanto`, `tanto_web` (adds the web tools) or `compaction`.
- `validators` can be `csv`, `code` and `tool_sequence`.

Cassette (`scenarios/recorded/<flavor>/<id>.json`):
- `id`, `flavor`, `model`, `quality`
- `params`: the value pools of `{{p:<name>}}`
- `csv`: the column specs of `{{csv:<n>}}`
- `turns`: each turn has `user_message` and `steps`
- each step has `text`, `reasoning`, `tool_calls` (`name`, `arguments`), `tool_results`, `finish_reason`,
  `usage` and `chars` (the original size)

Placeholders in texts and string arguments:
- `{{name:N}}`, `{{first}}`, `{{last}}`, `{{company}}`, `{{city:N}}`
- `{{int:min:max}}`, `{{money:min:max}}`, `{{date:N}}`, `{{uuid:N}}`
- `{{p:<param>[:title|upper|lower|snake|camel]}}`
- `{{csv:<n>}}`

A literal `{{` is written `{{{{`.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| 401 | Wrong key, wrong header (`Authorization: Bearer` vs `x-api-key`), or key not allowed on that API |
| 404 model | The model is not in the active profile; check `GET /v1/models` |
| 429 / 503 without load | `limited` or `chaos` profile active, or a fault patched in; check `GET /sim/profile` |
| 400 streaming | Send `"stream": false`; the simulator answers non-streaming only |
| Unrelated answer | The classifier fell back to the default mix (`by_binding.mix` in stats); add `#sim:<flavor>` or a classifier rule |
| Text instead of a tool call | The recorded tool is not in your request's `tools`, or `tool_choice` is `none` |
| `open files limit is …` at start | Raise `ulimit -n`, or start with `-min-fds 0` for small tests |
| `record`: key is empty | `export L8_LLM_KEY=<Halo chat key>` |
| `record`: `certificate verify failed` | Pass `-ca` with the Halo CA (`llm/halo-ca.crt`) |
