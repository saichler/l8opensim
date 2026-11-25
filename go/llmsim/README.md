# llmsim — quick start

llmsim is a fake LLM server for load testing AI agents. It answers OpenAI and Anthropic API calls instantly with
replies recorded from a real model (coding, code review, CSV, data queries, reports, web research, multi-turn
operator sessions). Every conversation gets different but plausible values (names, numbers, sites, CSV rows), so
thousands of concurrent sessions don't all receive the same text. No GPU or real LLM is needed.

Full reference: [LLMSIM-USAGE.md](LLMSIM-USAGE.md).

## 1. Run it

Needs Go 1.26+.

```bash
git clone https://github.com/saichler/l8opensim
cd l8opensim/go/llmsim
go build -o llmsim ./cmd/llmsim
ulimit -n 65536          # required: serve refuses to start below 65536 open files (or pass -min-fds 0)
./llmsim serve           # listens on :9080
curl http://localhost:9080/health    # {"status":"ok"}
```

Useful flags: `-listen :9080`, `-profile default|limited|chaos`, `-auth off` (no API keys),
`-tls-cert/-tls-key` (HTTPS).

## 2. Point your client at it

| Setting | OpenAI-compatible client | Anthropic client |
|---|---|---|
| Base URL | `http://<host>:9080/v1` | `http://<host>:9080` (calls `/v1/messages`) |
| API key | `sk-sim-tanto-1` (header `Authorization: Bearer`) | `sk-sim-tanto-2` (header `x-api-key`) |
| Model | `gpt-oss-120b` or `muse-glimmer-30b` | `claude-sim` |

`sk-sim-agent-1` and `sk-sim-agent-2` work with both APIs. These are simulator keys, not secrets.

```bash
curl http://localhost:9080/v1/chat/completions -H "Authorization: Bearer sk-sim-tanto-1" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-oss-120b","messages":[{"role":"user","content":"Write a Go program that counts lines, words and characters in files, like wc."}]}'
```

```python
from openai import OpenAI
llm = OpenAI(base_url="http://localhost:9080/v1", api_key="sk-sim-tanto-1")
print(llm.chat.completions.create(model="gpt-oss-120b",
      messages=[{"role": "user", "content": "What is BGP?"}]).choices[0].message.content)
```

## 3. What to expect

- **Non-streaming only.** `"stream": true` gets a 400.
- **Instant.** There is no generation delay.
- **Tools work.** If you send `tools`, replies may contain tool calls. Send the tool results back and you get the
  next step, then a final text answer. Calls only use the tool names (and `enum` values) you defined.
- **Deterministic.** The same request always gets the same answer. To make each session unique, set the OpenAI
  `user` field or Anthropic `metadata.user_id` to a per-session id. Without it, sessions that start with the same
  message get the same replies.
- **Multi-turn sessions.** The replies follow the conversation, and a session keeps the same values across its
  turns. Real-life 6–9 turn scripts are in `scenarios/catalog/conversation.json`. Send their turns in order for
  realistic sessions.
- **Choosing the task.** Any message gets a reply. To force a kind of reply, add `#sim:<flavor>` to the message,
  e.g. `#sim:csv_generate`. Flavors include `code_app`, `code_review`, `csv_generate`, `csv_export`,
  `data_query`, `report_file`, `web_research`, `writing`, `qa_chat`, `conversation` (full list in
  LLMSIM-USAGE.md).

## 4. Load-test knobs

Pick the profile at start with `-profile`, or switch it while running:

```bash
curl -X PUT http://localhost:9080/sim/profile -d '{"name":"limited"}'            # caps + rate limits → 429s
curl -X PUT http://localhost:9080/sim/profile -d '{"name":"chaos"}'              # random errors and bad outputs
curl -X PUT http://localhost:9080/sim/profile -d '{"faults":{"http_429":0.05}}'  # change one setting
```

| Profile | Behavior |
|---|---|
| `default` | Unlimited capacity, no faults |
| `limited` | Concurrency caps per model pool and per-key RPM/TPM limits. Over a limit you get an immediate 429 with `Retry-After` |
| `chaos` | Injected 429/500/503, dropped connections, truncated answers, malformed tool calls |

Watch it with `GET /sim/stats` (JSON) or `GET /metrics` (Prometheus). For very high load, run several processes
on one port with `-reuseport`.
