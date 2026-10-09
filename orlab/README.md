# orlab

orlab sends the same prompt through OpenRouter's three API formats — `/chat/completions`, `/responses`, and `/messages` — and compares what each one does with it: which parameters are passed on, renamed, or silently dropped, and how tokens, cost, latency, stop reasons, and errors differ. It is a learning tool, not a benchmark.

What it has found against the live API is in [docs/findings.md](docs/findings.md).

## Run

You need Go 1.23+ and an OpenRouter key (`sk-or-...`) in the environment:

```sh
export OPENROUTER_API_KEY='sk-or-...'

go run ./cmd/orlab -dry-run                        # build the requests, send nothing; no key needed
go run ./cmd/orlab -cheap -models x-ai/grok-4.6    # one model, free or cheapest variant
go run ./cmd/orlab                                 # full run, default models
```

Each run writes everything to `runs/<timestamp>/`: the reports in `reports/` (`ANALYSIS.md`, `CAPABILITY-MATRIX.md`, `STREAMING-GRAMMAR.md`), plus the phase transcript, the JSONL records, and the log.

Other flags: `-suite` (your own prompt file), `-repeat N`, `-no-stream`, `-echo-upstream=false`, `-concurrency`, `-timeout`, `-out`. Run with `-h` for the full list.

To try it with no key and no spend, replay the committed recording. It is synthetic, so it shows the report format, not real OpenRouter behavior:

```sh
go run ./cmd/orlab -replay testdata/cassettes -models test/model
```

You can also point `-replay` at one of your own `runs/<timestamp>/` directories. Replay only works while the prompt suite is unchanged, because requests are matched by their exact bytes.

## How it works

Each test prompt is described once, in a neutral form, and code translates it into the three API formats. Any difference in the results then comes from the API format, not from the prompt.

| What | Where |
|---|---|
| The neutral prompt description (`PromptSpec`) and shared result (`Result`) | `internal/dialect/dialect.go` |
| The prompts that get sent | `internal/prompts/suite.json` |
| The three translators | `internal/dialect/chat.go`, `responses.go`, `messages.go` |
| Running the phases | `internal/runner/` |
| Building the reports | `internal/analyze/` |
| HTTP client, credential scrubbing, recording and replay | `internal/transport/`, `redact/`, `record/` |

Requests are streamed by default, because OpenRouter only returns the upstream request body (`debug.echo_upstream_body`) on streamed calls.

A run goes through nine phases for every prompt, model, and format:

| Phase | What it does |
|---|---|
| Preflight | Checks each model exists and records its declared supported parameters |
| Build | Translates the prompt into three request bodies and diffs them |
| Send | Sends the request and starts the clock |
| Receive | Captures status, headers, body, and timings |
| Echo | Reads the request body OpenRouter actually sent to the provider |
| Metadata | Reads which provider was chosen and whether fallbacks or retries fired |
| Normalize | Maps the response into the shared `Result` |
| Reconcile | Fetches the authoritative token counts and cost from `/generation` |
| Analyze | Writes the reports |

## Safety

- The key is read only from `OPENROUTER_API_KEY`, never from a flag or a config file.
- Credentials are scrubbed from every record before it is written. A record that still looks like it contains a key is dropped, and the drop count is reported at the end of the run.
- `echo_upstream_body` is on by default, so run records hold full request content. `runs/` is ignored by git; do not commit a live run.

## Limitations

- A parameter is reported as `dropped` when it is missing from the upstream body under any of the names orlab knows to look for. A provider that renames it to something else will be misreported as dropped.
- `/messages` does not support the upstream echo, so parameter fates there show as `no-echo`.
- The mapping from a reasoning effort level to a `/messages` thinking budget is orlab's own convention, not an API behavior.
- `-repeat` results are only as meaningful as the provider's own determinism.

## Development

Standard library only.

```sh
make check                  # vet, race tests, InsecureSkipVerify grep
make test                   # go test ./...
make staticcheck vuln sec
```

Golden files pin the three built request bodies; regenerate them with `go test ./internal/dialect/ -update`. Regenerate the synthetic recording with `go test ./internal/runner/ -run TestGenerateSampleCassette -update-cassettes`.
