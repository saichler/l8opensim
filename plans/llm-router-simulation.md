# Plan: LLM Router Simulator

## Overview

Add an **LLM router simulator** (`llmsim`) to l8opensim. It is an OpenAI-compatible and Anthropic-compatible LLM
endpoint that answers with **recorded, real LLM conversations**, **flavored** per session, and as close to
**instantly** as possible. It is built to be used by the **Tanto AI agent** (`../l8ai`) at up to **~20,000
concurrent sessions**, and it is registered as an LLM provider and profiles in probler's `probler.json` so Tanto can
select it.

**Scope is the simulator only.** Running scale tests, load drivers, and any change to Tanto, probler or kind are out
of scope.

**Responses are instant.** The simulator adds no token timing, no time-to-first-token delay and no generation
pacing. Each response is written as soon as it is rendered, so the LLM is never what slows the agent down.

Work happens in three stages:

1. **Record:** run a catalog of common tasks against the real `gpt-oss-120b` / `muse-glimmer-30b` on Halo and save
   each conversation as a *cassette*. Tasks include data questions, CSV exports, reports, record changes, web
   research, coding a simple app, code review, CSV generation and general chat.
2. **Flavor:** turn every cassette into a template whose names, numbers, CSV rows and identifiers vary per session
   from a seeded random generator. The structure and length stay realistic.
3. **Replay:** serve the cassettes instantly through a router that models multiple models, concurrency caps, rate
   limits and failures.

"Flavorable" here means what it means in `plans/nayax-cloud-api-simulation.md` (Phase 5): every simulated session
receives a plausible, schema-identical but **different** response. It does not mean every session gets the same
canned answer.

---

## What the Simulator Must Be Compatible With (Tanto's LLM client)

These facts come from `../l8ai/go/ai/agent/llm/` and `tools/`, and shape the simulator's design.

| Fact about Tanto's client | Simulator requirement |
|---|---|
| OpenAI-compatible kind: **non-streaming** `POST {baseUrl}/chat/completions` with `model`, `max_tokens`, `messages`, `tools`, optional `tool_choice: "none"` | Serve non-streaming chat completions |
| Anthropic kind: `POST {baseUrl}/messages` with `x-api-key`, `anthropic-version`, `cache_control` blocks | Serve the Anthropic Messages API |
| Reads `choices[0].message.content`, `tool_calls`, `finish_reason` (`length` = max tokens) and `usage` | Exact response shapes, usage always present |
| No session or conversation id is sent; system prompt and conversation summary are merged into one system message; earlier turns' tool calls are not resent | Matching must be **stateless**, driven by request content (Phase 4.2) |
| Tool names: `l8query`, `describe_model`, `list_modules`, `create_record`, `update_record`, `delete_record`, `export_csv`, `create_file`, `web_search`, `fetch_url`; `model` parameters carry an `enum` of allowed models | Replayed tool calls use these names and only model names from the request's own enum |
| Retries 429 and 5xx (up to 3, honoring `Retry-After`); other 4xx fail | Error responses carry correct status codes and `Retry-After` |
| Compacts long conversations through the LLM with a fixed compaction prompt | Compaction is its own flavor |
| Web tools are `POST /v1/search` and `POST /v1/fetch` with a bearer key | Serve the web tools too |

---

## Scope

### In scope

| Surface | Endpoints |
|---|---|
| OpenAI chat (non-streaming) | `POST /v1/chat/completions` |
| Anthropic Messages (non-streaming) | `POST /v1/messages` |
| Web tools | `POST /v1/search`, `POST /v1/fetch` |
| Discovery | `GET /v1/models`, `GET /health` |
| Router behavior | Model routing, per-model concurrency caps, per-key rate limits, fallbacks, fault injection, runtime admin API |
| Recorder | Catalog recording against Halo; optional recording proxy |
| probler registration | llmsim providers and profiles in `probler.json` |

### Out of scope

- Running scale tests or any test of Tanto; load drivers.
- Changes to Tanto (l8ai), probler code, probler deployment, or kind.
- Real inference and real LLM latency.
- Semantic correctness for prompts that were never recorded (unmatched requests get the closest flavor's answer).
- Streaming (SSE), `/v1/responses` and embeddings. Tanto doesn't use them; they can be added later.

---

## Architecture

```
                 ┌─────────────────────────── llmsim (standalone process) ───────────────────────────┐
 LLM client ───▶ │  HTTP front: /v1/chat/completions · /v1/messages · /v1/search · /v1/fetch           │
 (e.g. Tanto)    │     │ auth (sim keys) → rate limit (per key RPM/TPM)                                 │
                 │     ▼                                                                               │
                 │  Router: model alias → pool → concurrency cap / 429                                 │
                 │     ▼                                                                               │
                 │  Matcher: last user message → scenario → tool step                                  │
                 │     ▼                                                                               │
                 │  Flavorer: cassette step + seed → concrete reply (tool calls fitted to request)     │
                 │     ▼                                                                               │
                 │  Writer: OpenAI or Anthropic JSON, written immediately                              │
                 │  Fault injector · Stats/metrics · Admin API                                         │
                 └─────────────────────────────────────────────────────────────────────────────────────┘
                                         ▲ cassettes (JSON)
 Recorder (offline): catalog ─▶ Halo gpt-oss-120b / muse ─▶ cassettes
```

---

## Phase 0: Package Layout and Conventions

The engine is a library package used by a standalone binary. All code stays inside the existing module
`github.com/saichler/l8opensim/go`. **No file exceeds 500 lines**; split by concern as shown.

```
go/llmsim/
├── cmd/llmsim/main.go            # CLI: serve | record | proxy-record | flavor-check
├── engine/
│   ├── server.go                 # HTTP front, optional TLS, route registration
│   ├── openai_chat.go            # /v1/chat/completions
│   ├── anthropic_messages.go     # /v1/messages
│   ├── neutral.go                # provider-neutral request/response model both APIs map to
│   ├── webtools.go               # /v1/search, /v1/fetch
│   ├── router.go                 # model aliases, pools, concurrency caps
│   ├── ratelimit.go              # per-key RPM/TPM token buckets
│   ├── matcher.go                # last user message → scenario → tool step
│   ├── classifier.go             # prompt → flavor rules, #sim: marker
│   ├── toolfit.go                # fit cassette tool calls to the request's tools and model enums
│   ├── flavor.go                 # placeholder engine + seeded RNG
│   ├── flavor_generators.go      # names, CSV rows, code identifiers, numbers, dates
│   ├── writer.go                 # instant response writer, buffer pooling
│   ├── faults.go                 # error injection
│   ├── stats.go                  # counters, histograms, /sim/stats, /metrics
│   ├── admin.go                  # runtime profile changes
│   └── cassette.go               # cassette schema, loader, index
├── recorder/
│   ├── catalog_runner.go         # runs the catalog against Halo, multi-turn and tool loops
│   ├── tool_fixtures.go          # answers recorded tool calls with fixture results
│   ├── proxy.go                  # optional transparent recording proxy
│   ├── sanitize.go               # strips keys, normalizes, marks placeholders
│   └── validators.go             # CSV parse, JSON, code compile checks
├── scenarios/
│   ├── tanto_tools.json          # Tanto's tool definitions and a system prompt, used when recording
│   ├── fixtures/                 # tool results the recorder returns (query rows, model descriptions, search results)
│   ├── catalog/<flavor>.json     # catalog user messages + variant parameters
│   └── recorded/<flavor>/<id>.json  # cassettes
└── profiles/
    ├── default.json              # unlimited capacity, no faults
    ├── limited.json              # concurrency caps + rate limits → 429s
    └── chaos.json                # fault injection enabled
```

Unit tests live in `go/tests/` (`TestLLMSim_test.go` plus helpers), consistent with the existing test layout.

Halo credentials for recording come only from the environment (`L8_LLM_KEY`) and the CA from `llm/halo-ca.crt`.
Nothing under `llm/` is read at build time or committed.

---

## Phase 1: Scenario Catalog (the "flavors")

Each flavor has **12–15 catalog user messages** with parameter slots so recording produces variants.

### 1.1 Tanto tool flavors

| # | Flavor | Example user messages | Tool loop | Final output |
|---|---|---|---|---|
| 1 | `data_query` | "Which network devices are down?", "Show the GPUs on host {{host}}" | `describe_model` → `l8query` | Table/list answer |
| 2 | `data_aggregate` | "How many devices per vendor?", "Average GPU temperature per server" | `l8query` with `count/sum/avg` + `group-by` | Numbers + short analysis |
| 3 | `csv_export` | "Export all {{entity}} in {{site}} to CSV" | `describe_model` → `export_csv` | Answer referencing the attached file |
| 4 | `report_file` | "Write a markdown report of today's critical alarms", "Create a CSV summary of pod restarts" | `l8query` → `create_file` | Answer + attached file |
| 5 | `record_crud` | "Add a note to device {{device}}", "Delete the test records" | `create_record` / `update_record`; `delete_record` only after a confirmation turn | Multi-turn with confirmation |
| 6 | `explore_models` | "What data can you show me?", "What fields does a GPU have?" | `list_modules` / `describe_model` | Explanation |
| 7 | `web_research` | "@web What is the latest Kubernetes release?" | `web_search` → `fetch_url` | Answer with sources |
| 8 | `compaction` | (Tanto's fixed compaction prompt over a long conversation) | none | Summary ≤ 1.5K tokens |

### 1.2 General LLM flavors

| # | Flavor | Example user messages | Typical output |
|---|---|---|---|
| 9 | `code_app` | "Write a simple TODO REST API in Go", "Python CLI that converts units", "HTML+JS tip calculator"; follow-ups "add tests", "add a Dockerfile" | 1.5K–4K tokens, code blocks |
| 10 | `code_review` | Review a pasted diff or file for bugs, style, security | 600–1.5K |
| 11 | `code_debug` | Stack trace + snippet → root cause + fix | 400–1.2K |
| 12 | `csv_generate` | "Generate a CSV of 50 fictitious {{entity}}" (inline, or via `create_file` with `.csv`) | 1K–3K, strict CSV |
| 13 | `csv_analyze` | Pasted CSV → totals, anomalies, answers | 300–1K |
| 14 | `summarize_long` | Summarize a long pasted document | 300–800 |
| 15 | `writing` | Email, release notes, incident summary, status update | 300–1K |
| 16 | `qa_chat` | General multi-turn conversation / knowledge questions | 100–600 per turn |
| 17 | `reasoning_math` | Word problems, capacity planning puzzles | 2K–6K mostly reasoning |
| 18 | `edge_cases` | Ambiguous request → clarifying question; disallowed → refusal; `max_tokens` hit → `finish_reason: length`; malformed tool-call JSON; a model name outside the enum | varies |

Catalog file shape (`scenarios/catalog/csv_export.json`):

```json
{
  "flavor": "csv_export",
  "validators": ["tool_sequence"],
  "messages": [
    {
      "id": "csv_export/devices-by-site",
      "params": { "entity": ["network devices", "GPU servers", "pods"], "site": ["Austin", "Dallas"] },
      "turns": ["Export all {{entity}} in {{site}} to CSV"]
    }
  ],
  "samples_per_variant": 3,
  "muse_fraction": 0.1
}
```

Some **intentionally imperfect recordings** are kept (a CSV with a stray header line, code that doesn't compile, a
query with a wrong field name). Real LLMs produce these. The validators tag each one (`quality: "flawed"`) instead of
discarding it, and profiles control the flawed fraction.

---

## Phase 2: Recorder

### 2.1 Catalog mode (`llmsim record --catalog scenarios/catalog --out scenarios/recorded`)

- Expands each catalog message × params × `samples_per_variant` and runs it against
  `https://192.168.1.124:8080/v1` (and a `muse_fraction` subset against 8082), using the CA in `llm/halo-ca.crt`.
- Tanto flavors are sent with the system prompt and tool definitions from `scenarios/tanto_tools.json`, so the
  model produces Tanto-shaped tool calls.
- When the model calls a tool, the recorder answers from `scenarios/fixtures/` (query rows, model descriptions,
  export results, search and fetch results) and continues the loop until a final answer.
- Multi-turn flavors: feeds the model's own reply back and sends the next scripted user turn, including the
  confirmation turn for `record_crud`.
- Captures content, tool calls and `usage`. Timing is **not** recorded, because replay is instant.
- **Resumable and polite:** Halo has only 2 gpt-oss slots and 1 Muse slot, shared with codex/agent1/agent2.
  Default concurrency is 1 (`--concurrency`), with a done-file so interrupted runs continue.

**Recording budget estimate:** 18 flavors × ~12 messages × ~3 samples ≈ **650 conversations**, ~2–4 LLM calls
each ≈ 2,000 calls at ~10–20 s ≈ **3–6 h at 2 slots**, plus the 10% Muse subset. One overnight run.

### 2.2 Proxy mode (optional): `llmsim proxy-record --upstream https://192.168.1.124:8080/v1 --listen :9090`

- A transparent proxy: any OpenAI-compatible client pointed at it is forwarded verbatim to Halo, and each
  request/response pair is written as a cassette, keyed exactly as replay keys it (Phase 4.2).
- Available as a recorder feature. Using it is up to whoever runs the client.

### 2.3 Sanitization and templating (`recorder/sanitize.go`)

- Strip `Authorization` headers, keys and host-specific URLs; normalize timestamps and ids.
- Keep masking tokens (`[NAME_…]`, `[MONEY_…]`) as they are.
- Auto-mark flavorable spans: CSV data rows, numbers in tables, person and company names (dictionary match),
  dates, UUIDs, entity identifiers from the catalog params. Manual overrides go in the cassette.

### 2.4 Cassette format (`scenarios/recorded/<flavor>/<id>.json`), provider-neutral

```json
{
  "id": "csv_export/devices-by-site/austin/s2",
  "flavor": "csv_export",
  "model": "gpt-oss-120b",
  "quality": "ok",
  "recorded_at": "2026-11-25T02:14:00Z",
  "params": { "site": ["Austin", "Dallas", "Houston"] },
  "turns": [{
    "user_message": "Export all network devices in Austin to CSV",
    "steps": [
      {
        "tool_calls": [{ "name": "describe_model", "arguments": { "model": "NetworkDevice" } }],
        "tool_results": ["{\"model\":\"NetworkDevice\",\"fields\":[...]}"],
        "usage": { "prompt_tokens": 6120, "completion_tokens": 64 }
      },
      {
        "tool_calls": [{ "name": "export_csv",
                         "arguments": { "query": "select * from NetworkDevice where equipmentinfo.location={{p:site}}" } }],
        "usage": { "prompt_tokens": 6480, "completion_tokens": 88 }
      },
      {
        "text": "I exported {{int:20:400}} network devices in {{p:site}} to the attached CSV file ...",
        "finish_reason": "stop",
        "usage": { "prompt_tokens": 6610, "completion_tokens": 142 },
        "chars": 180
      }
    ]
  }]
}
```

(As implemented: a cassette holds one or more `turns`, so multi-turn conversations replay turn by turn.)

---

## Phase 3: Flavoring (per-session variance)

- **Seeded RNG per conversation:** seed = hash(stateless conversation key from Phase 4.2, cassette id). A retried
  request gets the same text; different conversations get different text.
- **Placeholder engine** (`flavor.go`, same `{{.X}}` style as the Nayax Phase 5 placeholders):
  `{{name}}`, `{{company}}`, `{{city}}`, `{{int:min:max}}`, `{{money:min:max}}`, `{{date:range}}`, `{{uuid}}`,
  `{{ident:snake|camel}}` (code identifiers), and `{{csv_rows:<entity>:<n>}}` (regenerates CSV rows that match the
  recorded header and column types).
- **Code flavors:** vary app/entity names, identifiers, comments and string literals consistently within the
  response (one rename map per conversation), so the code stays internally coherent.
- **Choosing among samples:** most variety comes from the 3 samples per variant. Flavoring adds per-conversation
  uniqueness on top.
- **Token accounting:** after substitution, `completion_tokens` is rescaled by the measured chars-per-token ratio
  of the original cassette.
- **Render cost:** templates are pre-parsed at load time; rendering is a single pass over pre-split segments (no
  regex at request time).
- **`flavor-check` command:** renders N conversations of a cassette and asserts they differ, stay valid (CSV
  parses, JSON parses, code-block languages unchanged), and stay within ±10% of the original length.

---

## Phase 4: Replay Server

### 4.1 Endpoints and wire compatibility

- **OpenAI** `POST /v1/chat/completions`, non-streaming: `choices[0].message` with `content` and/or `tool_calls`
  (`id`, `type: function`, `function.name`, `function.arguments` as a JSON string), `finish_reason`
  (`stop` / `tool_calls` / `length`) and `usage.prompt_tokens` / `completion_tokens`.
- **Anthropic** `POST /v1/messages`: `content` blocks (`text`, `tool_use` with `id`, `name`, `input`), `stop_reason`
  (`end_turn` / `tool_use` / `max_tokens`), `usage.input_tokens` / `output_tokens`. Accepts `cache_control`, the
  `x-api-key` header and `anthropic-version`.
- Both APIs map to one provider-neutral request/response model (`neutral.go`), so every cassette serves both.
- `tool_choice: "none"` always gets a text answer: the cassette's final step, or the flavor's generic wrap-up if the
  cassette is still mid tool loop.
- `/v1/models` lists the configured model aliases; `/health` needs no key.

### 4.2 Stateless conversation key and tool step

- **Conversation key** = hash of the normalized **last user message** (masking tokens and whitespace normalized)
  plus the count of user messages in the request. The same request maps to the same key on any llmsim instance.
- **Tool step** = number of assistant tool-call rounds after that last user message (`tool` messages for OpenAI,
  `tool_result` blocks for Anthropic). Step N returns the cassette's step N; past the last step, the final text step
  is returned.
- **Compaction requests** are recognized by Tanto's fixed compaction prompt and routed to the `compaction` flavor.

### 4.3 Scenario binding (deterministic from the key)

1. **Marker:** a `#sim:<flavor>` or `#sim:<cassette id>` tag in the user message selects it explicitly. llmsim
   strips the tag from echoed text.
2. **Exact match:** the normalized user message equals a recorded `user_message`.
3. **Classifier** (`classifier.go`): ordered keyword/regex rules from the profile map the user message to a flavor
   ("export" + "csv" → `csv_export`, "how many" / "count" → `data_aggregate`, "review" + code fence →
   `code_review`, "@web" → `web_research`, and so on). Rules are compiled once at load time.
4. **Weighted default mix:** profile-level flavor weights for anything else.

Within a flavor, the cassette is chosen with the conversation seed, weighted by `quality` according to the
profile's `flawed_fraction`.

### 4.4 Fitting tool calls to the request (`toolfit.go`)

- A tool call is replayed only if its name is among the request's `tools`. Otherwise it is remapped to the closest
  request tool, or the step degrades to a text answer.
- The `model` argument of `l8query`, `describe_model`, `create_record`, `update_record`, `delete_record` and
  `export_csv`, and the `from <Model>` in queries, must be in the request's `enum` for that parameter. If the
  recorded model isn't allowed, the nearest allowed model is substituted and model-specific `where` clauses are
  dropped to keep the query valid.
- Tool-call ids are generated per conversation (deterministic from the seed).

### 4.5 Instant delivery (`writer.go`)

- **No artificial delay anywhere.** The response is rendered and written as soon as the request is matched.
- `prompt_tokens` / `input_tokens` are estimated from the request (chars/4, calibrated). They drive `usage` and
  `context length exceeded` errors against the profile's `n_ctx` (131,072).
- The only way a response is ever slowed down is the explicitly enabled delay fault (Phase 5.4), which is off in
  the `default` profile.

---

## Phase 5: Router Behavior and Fault Injection

### 5.1 Model routing

- Profile-defined model aliases → pools. Defaults: `gpt-oss-120b` → pool `coder`, `muse-glimmer-30b` → pool `muse`,
  `claude-sim` for the Anthropic API. Any alias can be added.
- A single port serves every API and routes by the `model` field.
- Fallback chains: if pool A is over its cap or failing, route to pool B per profile, and report the routed model.

### 5.2 Capacity: concurrency caps (no queueing delay)

- `default`: unlimited capacity; every request is answered instantly.
- `limited`: each pool has a `max_concurrent` cap and per-key RPM/TPM. Requests over the cap are **rejected
  immediately** with `429` + `Retry-After` (or `503`, per profile) instead of being queued.

### 5.3 Auth and rate limits

- Simulated keys (`sk-sim-<name>-<n>`) are defined per profile. They are never real Halo keys. OpenAI requests use
  `Authorization: Bearer`; Anthropic requests use `x-api-key`. `--auth off` is available.
- Per-key RPM and TPM token buckets return `429` with an OpenAI- or Anthropic-style error body.

### 5.4 Fault injection (`faults.go`): rates per profile, changeable at runtime

All faults are **off in the `default` profile**.

| Fault | Behavior |
|---|---|
| `http_429` / `http_503` / `http_500` | Immediate error with realistic body and `Retry-After` |
| `http_400` | Immediate non-retryable error |
| `slow_response` | Opt-in only: delay a fraction of responses by T seconds |
| `drop_connection` | Close the connection without a response |
| `truncated_length` | `finish_reason: length` early |
| `malformed_tool_json` | Tool-call arguments invalid JSON |
| `bad_model_in_tool` | A model outside the request's enum |
| `context_exceeded` | 400 `context length exceeded` |
| `empty_answer` | Empty `content`, no tool calls |

### 5.5 Admin API (`admin.go`)

`GET/PUT /sim/profile` switches the profile or patches individual knobs (fault rates, concurrency caps, rate
limits, flavor mix) without a restart.

---

## Phase 6: Register llmsim in `probler.json`

Add llmsim to `../l8secure/go/secure/plugin/probler/probler.json`, next to the existing `testllm` / `testmuse`
providers and the `default` / `muse` profiles, following their structure:

```json
"llmProviders": {
  "llmsim": {
    "providerId": "llmsim",
    "name": "LLM Simulator",
    "kind": "L8_LLM_PROVIDER_KIND_OPENAI_COMPATIBLE",
    "baseUrl": "http://<llmsim-host>:9080/v1",
    "apiKey": "sk-sim-tanto-1",
    "models": [
      { "modelId": "gpt-oss-120b", "displayName": "gpt-oss-120b (simulated)" },
      { "modelId": "muse-glimmer-30b", "displayName": "muse-glimmer-30b (simulated)" }
    ],
    "description": "l8opensim LLM simulator (instant replies)"
  },
  "llmsim-anthropic": {
    "providerId": "llmsim-anthropic",
    "name": "LLM Simulator (Anthropic API)",
    "kind": "L8_LLM_PROVIDER_KIND_ANTHROPIC",
    "baseUrl": "http://<llmsim-host>:9080/v1",
    "apiKey": "sk-sim-tanto-2",
    "models": [ { "modelId": "claude-sim", "displayName": "claude-sim (simulated)" } ],
    "description": "l8opensim LLM simulator, Anthropic Messages API"
  }
},
"llmProfiles": {
  "llmsim":           { "profileId": "llmsim", "name": "Simulator", "providerId": "llmsim",
                        "modelId": "gpt-oss-120b", "isDefault": false,
                        "description": "Instant simulated LLM" },
  "llmsim-muse":      { "profileId": "llmsim-muse", "name": "Simulator Muse", "providerId": "llmsim",
                        "modelId": "muse-glimmer-30b", "isDefault": false,
                        "description": "Simulated reasoning model" },
  "llmsim-anthropic": { "profileId": "llmsim-anthropic", "name": "Simulator (Anthropic)",
                        "providerId": "llmsim-anthropic", "modelId": "claude-sim", "isDefault": false,
                        "description": "Simulated LLM over the Anthropic API" }
}
```

- `isDefault` stays on the existing `default` profile; the new profiles are selectable.
- The keys are simulator keys, not secrets. The real Halo key never goes into `probler.json`.
- llmsim serves **plain HTTP** on these base URLs by default (TLS optional).
- `<llmsim-host>` is filled in with the host that will run llmsim (Open Question 1).
- If l8secure seeds providers from `credentials` entries (the existing providers say "Seeded from the TestLLM chat
  credential"), a matching `LLMSim` credential entry is added too. To be confirmed against l8secure's seeding code.

---

## Phase 7: Web Tools

`/v1/search` and `/v1/fetch` return the results recorded in `web_research` cassettes for the matching
conversation. Other requests get synthetic but well-formed results (titles, URLs, snippets, paged text with
`next_start`). Blocking of private and LAN URLs is mirrored (400 `blocked: …`). Returned instantly, using the same
sim keys.

---

## Phase 8: Capacity for 20K Concurrent Sessions

- **Throughput design:** responses are instant, so the load is request rate. Per request: parse only the fields
  matching needs, match with map lookups, render with a single pass into a pooled buffer, and write one JSON body.
  No per-request timers or regexes.
- **Large request bodies:** Tanto's system prompt, catalog and tool definitions are ~6–10K tokens. The parser reads
  the body once, skips the system prompt and tool descriptions, and extracts only the last user message, the
  tool-step count and the tool names/enums.
- **Memory:** cassettes are loaded once and shared read-only; buffers come from `sync.Pool`. Target < 2 GB RSS.
- **OS:** check `ulimit -n` at startup with a clear error; HTTP/1.1 keep-alive; raised listen backlog.
- **Multiple instances:** matching is stateless, so several llmsim instances return identical answers for the same
  request (`SO_REUSEPORT` multi-process mode, or behind any L4 balancer).

---

## Phase 9: Observability

`GET /sim/stats` (JSON) and `GET /metrics` (Prometheus text) report:

- requests/s and in-flight requests, per API (OpenAI / Anthropic / web tools), per model, per flavor
- tool steps served, `tool_choice: none` requests, compaction requests
- rejected requests (429/503) and injected faults by type
- simulator response latency histogram (request received → last byte written)
- unmatched requests (classifier fallbacks) and tool-fit substitutions

Plus an optional sampled trace log per conversation key (cassette id, step, faults applied).

---

## Phase 10: Simulator Unit Tests

`go/tests/TestLLMSim_test.go` plus helpers, testing the simulator only:

- conversation key and tool step for OpenAI and Anthropic message shapes
- binding priority (`#sim:` marker, exact match, classifier, default mix) and compaction detection
- tool fitting (missing tools, model enum substitution, query rewrite)
- flavor determinism and validity (same seed → same text, different seeds → different text, CSV/JSON still parse)
- response shapes for both APIs, including `tool_choice: none`, `finish_reason` / `stop_reason` and usage
- rate limiter, concurrency caps and fault selection rates
- each response is returned without added delay in the `default` profile

---

## Phase 11: Usage Guide for Agents (`go/llmsim/LLMSIM-USAGE.md`)

A Markdown guide written for **another agent** (or a person) that has to use llmsim: start it, point an LLM client
at it, steer which scenario answers, change its behavior at runtime, and read its stats. It follows the style of
`llm/llm-api.md`: short sections, tables, copy-paste examples. It contains no real keys. It is written alongside
Phases 4–9 and kept in sync with the actual flags and endpoints. Its content:

~~~markdown
# LLM Simulator (llmsim) — guide for agents

llmsim is an OpenAI- and Anthropic-compatible LLM endpoint that answers **instantly** with recorded, flavored
conversations. Use it wherever a real LLM would be too slow or too expensive. It does no real inference: answers
are realistic in shape, but they are not true answers to your exact question.

## Start it

```bash
cd go/llmsim && go build -o llmsim ./cmd/llmsim
./llmsim serve --listen :9080 --profile profiles/default.json --scenarios scenarios/recorded
# optional: --tls-cert/--tls-key for HTTPS, --auth off to skip key checks
curl http://<host>:9080/health          # → 200
```

## Endpoints (one port, routed by the `model` field)

| Call | Purpose |
|---|---|
| `POST /v1/chat/completions` | OpenAI chat (non-streaming), with function tools |
| `POST /v1/messages` | Anthropic Messages, with tool_use |
| `POST /v1/search`, `POST /v1/fetch` | Web tools (same bodies as the Halo web-tools service) |
| `GET /v1/models`, `GET /health` | Discovery / liveness (health needs no key) |
| `GET /sim/stats`, `GET /metrics` | Simulator statistics (JSON / Prometheus) |
| `GET/PUT /sim/profile` | Read or change the active profile at runtime |

Models: `gpt-oss-120b`, `muse-glimmer-30b` (OpenAI API), `claude-sim` (Anthropic API), plus any profile aliases.

## Keys

Send `Authorization: Bearer <key>` (OpenAI, web tools) or `x-api-key: <key>` (Anthropic). Keys are listed in the
active profile (for example `sk-sim-tanto-1`). Wrong or missing key → 401, unless the server runs with `--auth off`.

## Examples

```bash
curl http://<host>:9080/v1/chat/completions -H "Authorization: Bearer sk-sim-tanto-1" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-oss-120b","messages":[{"role":"user","content":"Write a Go program that counts lines, words and characters in files, like wc."}]}'

curl http://<host>:9080/v1/messages -H "x-api-key: sk-sim-tanto-2" -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-sim","max_tokens":2000,"messages":[{"role":"user","content":"Generate a CSV of 20 employees"}]}'
```

Python (openai SDK): `OpenAI(base_url="http://<host>:9080/v1", api_key="sk-sim-tanto-1")`, used as with any
OpenAI-compatible server.

## Using it from Tanto (probler)

Select the LLM profile `llmsim` (OpenAI API), `llmsim-muse` or `llmsim-anthropic` (Anthropic API) for a conversation.
These are defined in `probler.json`. Nothing else changes in Tanto.

## Choosing what answers

| Method | How |
|---|---|
| Explicit flavor | Put `#sim:<flavor>` in the user message, e.g. `#sim:csv_export Export all GPUs` |
| Explicit cassette | `#sim:<cassette id>`, e.g. `#sim:csv_export/devices-by-site/austin/s2` |
| Exact catalog message | Send a catalog user message verbatim (see `scenarios/catalog/`) |
| Anything else | A keyword classifier picks the flavor; otherwise the profile's weighted mix |

Flavors: `data_query`, `data_aggregate`, `csv_export`, `report_file`, `record_crud`, `explore_models`,
`web_research`, `compaction`, `code_app`, `code_review`, `code_debug`, `csv_generate`, `csv_analyze`,
`summarize_long`, `writing`, `qa_chat`, `reasoning_math`, `edge_cases`.

## Behavior you can rely on

- **Instant:** no generation delay unless the `slow_response` fault is enabled.
- **Deterministic:** the same request returns the same answer on any llmsim instance; different conversations get
  different (flavored) answers.
- **Tool loops:** if the request has `tools`, the reply may contain tool calls. Return the tool results in the next
  request (`role: tool` / `tool_result`) and llmsim continues with the next step, then a final text answer.
  Tool calls only use tool names present in your request, and only model names from your tool's `enum`.
- `tool_choice: "none"` always returns text.
- `usage` is always present; token counts are estimates.
- Context limit 131,072 tokens → 400 `context length exceeded` above it.

## Profiles and runtime changes

| Profile | Behavior |
|---|---|
| `default` | Unlimited capacity, no faults |
| `limited` | Per-model concurrency caps and per-key RPM/TPM; over the limit → immediate 429 with `Retry-After` |
| `chaos` | Fault injection on (429/500/503, dropped connections, truncated or malformed replies, …) |

```bash
curl http://<host>:9080/sim/profile                                   # show the active profile
curl -X PUT http://<host>:9080/sim/profile -d '{"name":"limited"}'    # switch profile
curl -X PUT http://<host>:9080/sim/profile -d '{"faults":{"http_429":0.05}}'   # patch one knob
```

## Stats

`GET /sim/stats` returns requests/s, in-flight requests, per-flavor and per-model counts, rejected requests,
injected faults, unmatched requests and the simulator latency histogram. If the latency grows, the simulator, not
your client, is the bottleneck: add an instance (`--reuseport` or another host).

## Recording new scenarios (needs Halo access)

```bash
L8_LLM_KEY=<halo chat key> ./llmsim record --catalog scenarios/catalog --out scenarios/recorded \
  --upstream https://192.168.1.124:8080/v1 --ca ../../llm/halo-ca.crt --concurrency 1
./llmsim flavor-check --scenarios scenarios/recorded      # validate the new cassettes
```

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| 401 | Wrong key or wrong header (`Authorization: Bearer` vs `x-api-key`) |
| 404 model | Model not in the active profile's aliases; check `GET /v1/models` |
| 429 | `limited` or `chaos` profile active; check `GET /sim/profile` |
| Same answer for different questions | Classifier fell back to the default mix; use `#sim:<flavor>` |
| Text instead of a tool call | The cassette's tool isn't in your request's `tools`, or `tool_choice` is `none` |
| `too many open files` at start | Raise `ulimit -n` (see `increase_file_limits.sh`) |
~~~

---

## Traceability Matrix

| # | Section | Action item | Phase |
|---|---|---|---|
| 1 | Layout | Create `go/llmsim/{cmd,engine,recorder,scenarios,profiles}` with the 500-line cap | 0 |
| 2 | Catalog | 8 Tanto tool flavors + 10 general flavors, 12–15 messages each, params and validators | 1 |
| 3 | Recorder (catalog) | Run catalog against Halo with Tanto tool definitions, fixture tool results, multi-turn, resume | 2.1 |
| 4 | Recorder (proxy) | Optional transparent recording proxy | 2.2 |
| 5 | Sanitize | Key stripping, masking tokens kept, auto-placeholder marking | 2.3 |
| 6 | Cassette schema | Provider-neutral steps (tool calls / text / usage), loader, index | 2.4 |
| 7 | Flavoring | Seeded, pre-parsed placeholder engine, CSV/code generators, token rescaling, `flavor-check` | 3 |
| 8 | API surface | OpenAI chat and Anthropic Messages via one neutral model; `tool_choice: none`; models; health | 4.1 |
| 9 | Matching | Stateless key + tool step, compaction detection, `#sim:` marker, classifier, default mix | 4.2–4.3 |
| 10 | Tool fitting | Tool-name check, model enum substitution, query rewrite | 4.4 |
| 11 | Instant delivery | Zero-delay writer, token estimation, context limit | 4.5 |
| 12 | Router | Aliases incl. `claude-sim`, pools, caps with immediate 429, fallbacks | 5.1–5.2 |
| 13 | Auth + limits | Bearer and `x-api-key` sim keys, RPM/TPM | 5.3 |
| 14 | Faults + admin | Fault catalog (delay opt-in only), runtime profile API | 5.4–5.5 |
| 15 | probler.json | `llmsim` and `llmsim-anthropic` providers; `llmsim`, `llmsim-muse`, `llmsim-anthropic` profiles | 6 |
| 16 | Web tools | Recorded/synthetic search and fetch | 7 |
| 17 | Capacity | Lean parsing, pooling, fd check, multi-instance mode | 8 |
| 18 | Observability | `/sim/stats`, `/metrics`, traces | 9 |
| 19 | Unit tests | `go/tests/TestLLMSim_test.go` | 10 |
| 20 | Usage guide | `go/llmsim/LLMSIM-USAGE.md` for agents: start, endpoints, keys, examples, steering, profiles, stats, recording, troubleshooting | 11 |

---

## Risks and Open Questions

1. **llmsim host and port.** *Resolved during implementation:* `probler.json` uses `http://192.168.1.176:9080/v1`
   (the development host's LAN IP). Change `baseUrl` in both llmsim providers if llmsim runs elsewhere. Port 9090 is
   the optional recording proxy; 8080 stays with opensim's control API.
2. **Seeding in l8secure.** *Resolved:* `ActivateLlmProviders` seeds directly from `llmProviders` /
   `llmProfiles`, so no `credentials` entry is needed. The entries load with l8secure's own `L8SecureConfig` types.
3. **Tanto's system prompt and tools for recording.** `scenarios/tanto_tools.json` is copied from l8ai's
   `tools/definitions.go` and system prompt. If Tanto's tools change, the copy and the cassettes need refreshing.
4. **Halo availability.** Recording uses Halo's gpt-oss slots for several hours and competes with
   codex/agent1/agent2. Run it overnight; the recorder defaults to concurrency 1.
5. **Semantic fidelity limits.** Replayed answers are realistic in shape and content but are not answers to the
   exact question asked, and they don't reflect the actual tool results the client gets back.

---

## Estimated Effort

| Phase | Effort | Notes |
|---|---|---|
| Phase 0 | 0.5 day | Layout, CLI skeleton |
| Phase 1 | 2 days | 18 flavors, Tanto tool definitions copy, fixtures, validators |
| Phase 2 | 2.5 days | Catalog recorder with tool fixtures, optional proxy, sanitize; plus an overnight recording run |
| Phase 3 | 2 days | Placeholder engine, CSV/code generators, `flavor-check` |
| Phase 4 | 3.5 days | OpenAI + Anthropic wire formats, neutral model, stateless matcher, tool fitting |
| Phase 5 | 2 days | Router pools, caps, limits, faults, admin API |
| Phase 6 | 0.5 day | `probler.json` entries |
| Phase 7 | 0.5 day | Web tools |
| Phase 8 | 1 day | Lean parsing, pooling |
| Phase 9 | 1 day | Stats, metrics, traces |
| Phase 10 | 1.5 days | Unit tests |
| Phase 11 | 0.5 day | Agent usage guide |
| **Total** | **~17.5 days** | |

**Suggested first milestone (~6 days):** Phases 0, 1 (5 flavors: `data_query`, `csv_export`, `report_file`,
`code_app`, `qa_chat`), 2.1, a minimal 3, 4 (OpenAI path only) and 6 with the `default` profile.
