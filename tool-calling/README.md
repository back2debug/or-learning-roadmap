# Tool Calling Explorer (Go)

A progressive, self-documenting exploration of **tool calling via the chat
completions API** in Go, comparing an explicitly pinned model (Claude Sonnet 5)
against the two model routers (`openrouter/auto` and `openrouter/auto-beta`).

Every API call prints the full request JSON, the full response JSON, a
field-by-field inspection of the parsed Go structs, and a plain-English
explanation of what the model did — so you can watch the tool-calling
protocol work on the wire.

Built with the **standard library only** (`net/http`, `encoding/json`,
`log/slog`). No SDK, no third-party dependencies.

## Prerequisites

- Go 1.21+ (uses `log/slog`; developed on 1.26)
- An OpenRouter API key exported as an environment variable:

```sh
export OPENROUTER_API_KEY="sk-or-v1-..."
```

The program **panics at startup** if the key is missing — it never falls back
to an empty credential.

## Run

```sh
go run .            # runs all 9 parts + comparison (makes real, billed API calls)
LOG_LEVEL=debug go run .   # verbose structured logs
```

Every run also writes a complete transcript — structured log events plus all
pretty-printed JSON, struct inspections, and the comparison — to
**`./tool-calling.log`** in the directory you run from (the project root).
The file is truncated at the start of each run and its absolute path is
logged on startup. It is covered by the repo's `*.log` gitignore rule, so it
won't be committed.

## Test

Unit tests never touch the network — the client depends on a small `HTTPDoer`
interface, and tests inject a mock transport with canned responses:

```sh
go test ./...       # or: go test -v ./...
```

## The nine parts

| Part | Model | What it demonstrates |
|---|---|---|
| 1 | `anthropic/claude-sonnet-5` | Single request with an `add_numbers` tool; inspect whether the model calls the tool or answers in text |
| 2 | `anthropic/claude-sonnet-5` | Full tool loop: execute the tool, send the result back, get the final text answer |
| 3 | `anthropic/claude-sonnet-5` | Two tools; the model chains `multiply_numbers(7,6)` → `add_numbers(42,8)` across turns |
| 4–6 | `openrouter/auto` | Same three experiments through the Auto Router |
| 7–9 | `openrouter/auto-beta` | Same three experiments through the Auto Router (Beta) |
| — | | Comparison table + observations: models served, providers, routing consistency, token usage |

> **Why `openrouter/auto`?** A request with no `model` field requires a
> default configured in your dashboard. The router pseudo-model `openrouter/auto`
> (or its successor `openrouter/auto-beta`) lets the API decide.

## Findings from a sample run (2026-08-04)

- **Routing is per-request.** `openrouter/auto` served turn 1 with
  `google/gemini-3.6-flash` and turn 2 with `openai/gpt-5.6-sol`. Full message
  history travels with each request, so it works — but pin the model if you
  need consistency.
- **`openrouter/auto-beta` routed to `z-ai/glm-5.2` consistently** across all
  turns in our run, unlike the less-consistent `auto`.
- **Token counts vary by model.** Explicit Sonnet 5 runs report ~3× the tokens
  of routed runs for the same conversation (tokenizer differences and Claude's
  larger tool-schema serialization, not efficiency).
- **Wire-format quirks:** Claude emits `toolu_...` IDs and arguments in schema
  order; Gemini reverses them. Arguments must be parsed as JSON, never matched
  as strings. `native_finish_reason` preserves the upstream provider's stop
  reason under the normalized `finish_reason`.

## Project layout

| File | Responsibility |
|---|---|
| `main.go` | Entry point; part definitions; the shared conversation loop; inspection, explanation, and comparison output |
| `client.go` | Hardened OpenRouter client: `HTTPDoer` interface, retries with backoff, per-attempt timeouts, status validation |
| `models.go` | Typed structs mirroring the OpenRouter wire format (request, response, tool calls, usage, error envelope) |
| `tools.go` | `add_numbers` / `multiply_numbers` definitions (JSON Schema) and strict argument validation + execution |
| `const.go` | All model names, header names, timeouts, and limits as typed constants |
| `logger.go` | `log/slog` setup, API-key redaction, JSON pretty-printing, section separators |
| `client_test.go` | Unit tests with a mocked HTTP transport (parse, retry-on-500, no-retry-on-4xx, argument validation) |

## Security practices demonstrated

- **API key handling** — loaded from `OPENROUTER_API_KEY` (panic if unset);
  sent only in the `Authorization` header, never in the body or URL; only
  ever logged through `redactKey()` as `sk-or-v1...***`.
- **HTTP client hardening** — 30 s per-attempt timeout, 60 s dial timeout;
  redirects refused via `CheckRedirect` (following one could leak the bearer
  token to another host); TLS 1.2+ with certificate verification **on**;
  `MaxConnsPerHost` limit; identifying `User-Agent`.
- **Retries** — up to 3 retries with exponential backoff (1 s → 2 s → 4 s),
  but only for transient failures (429, 5xx, transport errors). 4xx errors
  and context cancellation are never retried.
- **Input validation** — tool-call arguments are model output and therefore
  untrusted: they are decoded with `DisallowUnknownFields`, pointer fields
  distinguish *missing* from *zero*, and unknown tool names are rejected.
- **Bounded reads** — response bodies are capped at 10 MiB via
  `io.LimitReader`; bodies are always closed.
- **Error hygiene** — non-2xx responses become a typed `*APIStatusError`
  carrying status, `x-request-id`, and a truncated message (never the full
  body); errors are wrapped with `%w` and classified with `errors.As`.
- **Graceful shutdown** — `signal.NotifyContext` cancels in-flight requests
  and aborts retry waits on Ctrl-C / SIGTERM.

## Go idioms demonstrated

- Small, focused interface (`HTTPDoer`) for testability
- Custom types for semantic clarity (`ModelName`, `ToolChoice`) instead of
  bare strings; constants for every magic value
- `context.Context` as the first parameter on all I/O paths, with
  `context.WithTimeout` per attempt
- `json.RawMessage` for byte-exact JSON Schema instead of `interface{}` maps
- Structured logging with `log/slog` (Info for flow, Debug for detail,
  Warn/Error for failures)
- Errors as values: custom error type implementing `error`, wrapped with
  `fmt.Errorf("...: %w", err)`, inspected with `errors.Is` / `errors.As`
