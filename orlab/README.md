# orlab

orlab sends one logical prompt through all three of OpenRouter's API dialects — `/chat/completions` (OpenAI Chat Completions), `/responses` (OpenAI Responses), and `/messages` (Anthropic Messages) — and reports, phase by phase, what each one actually does with it. It answers questions the documentation does not: whether OpenRouter sends the same bytes upstream to the provider regardless of which dialect you speak, which of your parameters are passed through, renamed, transformed, silently dropped, or rejected, and how token counts, cost, latency, stop reasons, and error envelopes differ for identical inputs. It is a learning and diagnostic harness, not a benchmark — the output is a set of comparison tables and a plain-English writeup, not a score.

## Quickstart

You need Go 1.23+ and an OpenRouter API key. OpenRouter keys begin with `sk-or-`; a bare `sk-` key is an OpenAI key and will not route.

```sh
export OPENROUTER_API_KEY='sk-or-...'   # environment only — never a flag or a config file
```

Start with a dry run. It builds all three request bodies for every suite entry, runs the Build-phase diff, and sends nothing, so it costs nothing and needs no key:

```sh
go run ./cmd/orlab -dry-run
```

Then a cheap real run, which swaps in `:free` or lowest-cost model variants where the catalog has them:

```sh
go run ./cmd/orlab -cheap -models x-ai/grok-4.6
```

Then the full run across the default model set:

```sh
go run ./cmd/orlab
```

Reports land in `reports/` (regenerated every run) and an archival copy in `runs/<timestamp>/reports/`. The phase transcript, the JSONL records, and the structured log land in `runs/<timestamp>/`.

Useful flags: `-models` (comma-separated slugs), `-suite` (path to your own suite file), `-repeat N` (determinism probe), `-no-stream` (run the whole suite buffered), `-echo-upstream=false`, `-concurrency`, `-timeout`, `-out`.

## Architecture

- **One canonical request.** A `PromptSpec` describes the prompt independently of any dialect. Each of the three `Dialect` implementations translates it to its own wire format and returns `BuildNotes` recording what it renamed, transformed, or could not express.
- **One shared result.** Each dialect parses its response into a `Result`. Fields a dialect cannot populate go in an explicit `Unsupported` map, so a missing value is never mistaken for a real zero.
- **Streaming first.** `debug.echo_upstream_body` only works on streamed requests, so every suite entry runs streamed by default. Buffered mode exists for comparison.
- **Supporting pieces.** A hardened transport (TLS 1.2 floor, pinned redirects, backoff that honors `Retry-After`), a redactor that scrubs credentials before anything reaches disk, a runner that emits an event per phase, and an analyzer that builds the reports.

## The nine phases

**Preflight** fetches `GET /models` to confirm every requested slug exists and to capture its declared `supported_parameters`, which the parameter-fate table later cross-references. An unknown slug aborts the run with the closest matches suggested. The API key is deliberately *not* validated here — if it is bad, the first real request fails and that error becomes a data point in the error taxonomy.

```
[test/model] PREFLIGHT  ok: context=8192, 3 supported parameters
```

**Build** translates the canonical spec into three request bodies and diffs them. This is the highest-value teaching artifact in the tool: three wire formats for one intent, side by side.

```
[plain test/model chat] BUILD      /chat/completions, 193 bytes
[plain test/model] BUILD.DIFF shared keys: debug,model,stream | dialect-specific:
                              input,max_completion_tokens,max_output_tokens,max_tokens,messages
```

**Send** records the exact outbound headers (redacted), the body size, and starts the clock.

```
[plain test/model chat] SEND  POST /chat/completions (193 bytes), headers:
  map[Authorization:[[REDACTED]] Content-Type:[application/json] X-Openrouter-Metadata:[enabled]]
```

**Receive** captures the status code, response headers including rate-limit headers, the raw body, TTFB, and total duration.

```
[plain test/model chat] RECEIVE    status 200, rate-limit remaining=""
```

**Echo** parses `debug.echo_upstream_body` — the transformed request body OpenRouter actually sent upstream. When provider fallbacks fire, one debug chunk arrives per attempted provider, so this is a list, not a single value.

```
[plain test/model chat] ECHO       1 upstream body(ies) captured
```

**Metadata** parses `openrouter_metadata`, the second and independent observability channel, which reports the routing strategy, which provider was actually selected, and whether fallbacks or retries fired.

```
[plain test/model chat] METADATA   strategy=direct attempt=1
```

**Normalize** maps the dialect-specific response into the shared `Result`, recording explicit unsupported markers for anything this dialect cannot report.

```
[plain test/model chat] NORMALIZE  stop=stop/stop text=16B reasoning=0B tools=0
                                   usage=p=10 c=4 r=- cached=- cost=4e-05 ttfb=0s total=0s
```

**Reconcile** polls `GET /generation?id=` with bounded backoff for the authoritative native token counts and cost, since that record populates asynchronously. Any disagreement with the inline usage block is flagged in the ledger.

```
[plain test/model chat] RECONCILE  ok: provider=FakeProv native_prompt=11 native_completion=3 cost=5e-05
```

**Analyze** builds the cross-dialect matrices and writes `reports/ANALYSIS.md`, `reports/CAPABILITY-MATRIX.md`, and `reports/STREAMING-GRAMMAR.md`.

## Sample output

These samples come from the committed replay cassette, which is **synthetic** — recorded against the in-repo fake OpenRouter server, not from a live run. They show the shape of the output, not real findings about OpenRouter. What has been measured against the live API is recorded in [docs/findings.md](docs/findings.md).

Capability matrix (excerpt):

| entry | kind | chat | responses | messages |
|---|---|---|---|---|
| plain | plain | ✅ 1/1 | ✅ 1/1 | ✅ 1/1 |
| tools | tools | ✅ 1/1 | ✅ 1/1 | ✅ 1/1 |
| buffered | buffered | ✅ 1/1 | ✅ 1/1 | ✅ 1/1 |
| err_bad_model | error_probe | ❌ 404 not_found ×1 | ❌ 404 not_found ×1 | ❌ 404 not_found ×1 |

Parameter fates (excerpt). Each cell is the fate of that parameter on that dialect, judged from the request body, the `BuildNotes`, and the echoed upstream body:

| param | chat | responses | messages | catalog says |
|---|---|---|---|---|
| top_k | dropped ×2 | dropped ×2 | no-echo ×2 | 0/1 models |
| seed | dropped ×2 | dropped ×2 | unexpressible ×2 | 1/1 models |
| max_tokens | no-echo ×1; renamed(max_completion_tokens→max_tokens) ×13 | dropped ×13; no-echo ×1 | no-echo ×14 | 0/1 models |

`unexpressible` means the dialect has no wire field for it and the build said so. `dropped` means it was sent but is absent from the upstream body. `no-echo` means no upstream body was available to check — which on `/messages` is the expected state, since echo is not supported there.

## Replay mode

Every HTTP exchange is recorded as a redacted cassette, so a full run can be replayed offline with no key and no spend. A synthetic cassette is committed, so the repo is runnable by anyone reviewing it:

```sh
go run ./cmd/orlab -replay testdata/cassettes -models test/model
```

That produces the complete set of reports without touching the network. To replay one of your own runs, point `-replay` at its `runs/<timestamp>/` directory. Replay matches a rebuilt request to its recorded response by hashing the method, path, and request body, so a cassette only replays a suite that builds byte-identical requests — change the suite and you need a new recording.

Regenerate the committed synthetic cassette with:

```sh
go test ./internal/runner/ -run TestGenerateSampleCassette -update-cassettes
```

## Security model

The API key is read from `OPENROUTER_API_KEY` and nowhere else — never a CLI flag, where it would be visible in `ps` and shell history, and never a config file field. The `sk-or-` prefix is validated at startup. When a key is identified in logs it appears only as a fingerprint: the last four characters plus a short SHA-256 prefix.

Redaction happens before the write, not as a cleanup pass afterwards. A scrubber strips `Authorization`, `Cookie`, `Set-Cookie`, and any `sk-or-…` pattern from every record, then verifies the result; if anything credential-shaped survives, the record is dropped rather than written, and the drop is counted and reported at the end of the run. The redactor has its own adversarial test suite covering keys embedded in JSON bodies, URLs, SSE data lines, truncated records, and non-canonical header casing.

Run directories are created with mode 0700 and files with 0600, at creation time rather than by a later `chmod`. Every response body read is bounded by a configurable cap that fails loudly rather than truncating silently. TLS is pinned to 1.2 minimum, `InsecureSkipVerify` appears nowhere in the codebase and CI fails the build if it ever does, and the redirect policy refuses any redirect to a host outside the configured API host so the `Authorization` header cannot leak cross-origin. `-base-url` must be `https://` unless `-allow-insecure-localhost` is passed and the host actually resolves to loopback.

`echo_upstream_body` is enabled by default. OpenRouter's documentation notes it is not intended for production use, since it surfaces the full request content sent upstream; that is acceptable here because this is a throwaway diagnostic harness running synthetic prompts, and the credential redaction above still applies to everything written. Disable it with `-echo-upstream=false`.

`runs/`, `reports/`, `*.jsonl`, and `.env` are ignored by the repo-root `.gitignore`, which also re-includes orlab's JSON fixtures and embedded suite (the root file ignores `*.json` repo-wide for the Python projects alongside this one). Before committing any sample output, confirm it came from a scrubbed or synthetic run.

## Limitations and known unknowns

The parameter-fate classifier searches the echoed upstream body recursively for a set of candidate wire names per parameter, so a provider that renames a field to something outside that candidate list will be reported as `dropped` when it was really renamed — the candidate lists are a maintenance surface. The effort-to-thinking-budget mapping used when translating a reasoning effort level onto `/messages` is this harness's own convention, not an OpenRouter or Anthropic one, and it is recorded in every `BuildNotes` so it is never mistaken for an API behavior. Determinism results are only as meaningful as the provider's own determinism guarantees, which for most models are weak regardless of seed. Replay cassettes are keyed on exact request bytes, so they are tied to the suite that produced them. And the committed sample cassette is synthetic: it demonstrates the report shape, not OpenRouter's behavior.

## Dependencies

Standard library only. No third-party runtime or test dependencies — the JSON diff, the SSE parser, the CLI flags, the redactor, and the report writer are all stdlib. `staticcheck`, `govulncheck`, and `gosec` run via `go run` in CI without entering `go.mod`.

## Development

```sh
make check      # vet, race tests, InsecureSkipVerify grep
make test       # go test ./...
make staticcheck vuln sec
```

Golden files pin all three built request bodies, so any change in translation shows up as a diff; regenerate them deliberately with `go test ./internal/dialect/ -update`. Before committing, confirm nothing under `runs/` or `reports/` is staged.
