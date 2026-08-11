# pareto-lab

A small Go CLI for learning how OpenRouter's [Pareto Code router](https://openrouter.ai/docs/guides/routing/routers/pareto-router)
(`openrouter/pareto-code`) makes routing decisions. It sends real requests
through the router, watches which concrete model gets selected, and records
cost, latency, and token usage for every request.

## What it tests

Each run executes two phases:

1. **Score sweep** — sends a simple and a complex coding prompt at each
   `min_coding_score` in `{0.3, 0.6, 0.8, 1.0}` and logs which model the
   router picks. The router maps the score to one of three quality tiers
   (`>= 0.66` high, `>= 0.33` medium, `< 0.33` low) and routes to the
   cheapest available model in that tier.
2. **Sticky sessions** — opens a session with a `session_id`, sends a
   follow-up on the same session (should stay on the same model), and sends a
   control request without a session id (routed fresh).

## Requirements

- Go 1.25+ (uses the official [OpenRouterTeam/go-sdk](https://github.com/OpenRouterTeam/go-sdk))
- An OpenRouter API key exported as `OPENROUTER_API_KEY` (must start with
  `sk-or-`; validated before any request is sent, never logged)

## Run

```sh
go run .
```

Progress prints to the console in real time, one line per request:

```
Min Score: 0.8 | Prompt: simple | Model: openai/gpt-5.6-sol | Cost: $0.004175 | Tokens: 31/134 | TTFT: 2175ms | Total: 3.65s
```

Test configuration is hardcoded at the top of `main.go`:

| Const        | Default | Meaning                                    |
| ------------ | ------- | ------------------------------------------ |
| `iterations` | 1       | full test passes per run                   |
| `maxTokens`  | 1600    | completion-token cap per request           |
| `outDir`     | `.`     | where the results files are written        |
| `scoreSweep` | 0.3–1.0 | the `min_coding_score` values under test   |

A full run makes 11 requests and costs roughly $0.05–0.10 depending on which
models the router selects.

## Output files

All three files append across runs, so history accumulates:

- `results.csv` — one row per request: timestamp, min_coding_score,
  prompt_type, model_selected, cost, input_tokens, output_tokens,
  response_time_ms, plus ttft_ms, phase, session_id, and error columns
- `results.json` — the same requests with richer detail (full response text,
  reasoning volume, finish reason), kept as a single valid JSON array and
  written atomically (temp file + rename)
- `learning_log.txt` — timestamped human-readable narrative of what was
  observed at each step, followed by a numbered **findings summary** for that
  run: tier routing, failures, responses truncated by the `max_tokens` cap,
  cost and latency per tier, sticky-session outcome, and a takeaway. The
  summary is computed from the run's own results and is always written to the
  log, not just the console (`summary.go`)

## Security notes

- The API key comes only from the environment, is format-checked before use
  (OWASP A07), and is redacted from all console/file output (A01/A09).
- The SDK's default HTTPS endpoint is used as-is; no server override (A02).
- Responses are validated (model present, non-empty output, mid-stream
  provider errors surfaced) rather than trusted blindly (A08).
- Prompts are hardcoded, but still length/emptiness-validated so the guard
  exists if they ever become configurable (A03).
