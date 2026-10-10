# autorouter-lab

An experiment harness that compares OpenRouter's two Auto Router tracks across a
matrix of payload configurations: `openrouter/auto`, the standard track, and
`openrouter/auto-beta`, the early-access track where new routing behaviour lands
first. **Neither router is deprecated.** The question this project asks is what
has arrived on the beta track that has not reached stable yet, not which one is
better.

This is a learning project. It makes real, billed API calls.

**Status:** Phase 3 of 5. Both transports, the matrix runner and the assertion
layer work for both routers. Backfill and the full analysis report are not
built yet.

## Setup

```bash
export OPENROUTER_API_KEY=...
```

That is the whole setup. This project deliberately uses **no `.env` file** and
has no dotenv dependency: the key is read from the environment and nowhere else,
never from a flag or a config file. Do not add one.


### Check "Prevent overrides" first

In OpenRouter, open **Settings → Routing** and make sure the **Prevent
overrides** toggle is **off**. When it is on, your saved account defaults are
final and every request-level `plugins` block is ignored, which invalidates
every experiment here. The saved defaults apply to both routers, so the effect
would be to flatten the differences between the tracks rather than skew one.

## Router → plugin id

Each router reads per-request settings only under its own plugin id:

| `router` | model slug | plugin `id` |
| --- | --- | --- |
| `auto` | `openrouter/auto` | `auto-router` |
| `auto-beta` | `openrouter/auto-beta` | `auto-beta-router` |

Settings sent under the other router's id are **accepted and silently
ignored**. Measured on 2026-10-09 with an allow-list that no model can match:

| model slug | plugin id sent | result |
| --- | --- | --- |
| `openrouter/auto` | `auto-router` | 404 `No models match your request and model restrictions` |
| `openrouter/auto` | `auto-beta-router` | 200, routed to an ordinary model |
| `openrouter/auto-beta` | `auto-router` | 200, routed to an ordinary model |

So a mismatch looks exactly like a normal routing decision. To keep it
unreachable:

- `internal/config/router.go` is the only place either string is written; a
  test fails if any other non-test file spells one out.
- The YAML cannot set a plugin id.
- The request builder checks the id the SDK rendered against the mapping, and a
  guard in the HTTP layer re-checks the final bytes before anything is sent.

`go test -tags live -run TestLiveMismatchedPluginID -v ./internal/transport/`
reproduces the table above (two short billed requests plus one free 404).

## Cost settings

`cost_tier` is the current setting: one of `low`, `medium`, `high`, `xhigh`,
`max`. A tier is a band, not a ceiling, so it excludes models cheaper than the
band as well as more expensive ones.

`cost_quality_tradeoff` is the **deprecated** numeric dial (0–10, higher favours
cheaper models). It is still accepted, and **`cost_tier` wins when both are
sent**. The config refuses a cell that sets both unless it also sets
`allow_cost_conflict: true`, which is reserved for the precedence experiment.

## Pinned SDK

`github.com/OpenRouterTeam/go-sdk` is pinned to **v0.9.40**. The SDK is
generated from the OpenAPI spec and is in beta; breaking changes ship in minor
`0.x` releases, so upgrade deliberately and re-run the tests. Things this
project relies on, or works around, in that version:

- Its built-in retry (5xx, backing off for up to an hour) is turned off; the
  harness owns retries and spend.
- The plugin constructors overwrite the plugin `id` with their own constant.
- It reads `OPENROUTER_X_TITLE` and `OPENROUTER_HTTP_REFERER` from the
  environment. This project sends no app title or referer, and the HTTP layer
  blocks any request that carries one.

## Two transports

- `chat_completions` uses the OpenRouter Go SDK's `Chat.Send`.
- `messages` posts to `https://openrouter.ai/api/v1/messages`, OpenRouter's
  Anthropic-compatible endpoint. The OpenRouter Go SDK defines the Messages
  request type but has no method that sends one, so the body is built with the
  SDK's type and sent with `net/http`. This is the one place harness code sets
  the `Authorization` header itself.

A test asserts that both transports send identical `model`, `plugins`,
`provider` and `session_id` for the same cell, so a difference between surfaces
is not the harness's doing. Measured on 2026-10-09: `plugins` and `provider`
are both honoured on the Messages endpoint, for both routers.

## Reading a result: `model` is not always the routing decision

`resolved_to` is the model the router chose. `model` is the model that
answered. They differ when the chosen model fails upstream and one of the
router's `fallback_models` answers instead; the record then has
`model_fallback: true`. Use `resolved_to` when asking what the router decided.

## Assertions: did the config take effect?

Every trial record carries an `assertions` list, and `cmd/analyze` recomputes
it from the log. Each check is `pass`, `fail`, `skip` (does not apply) or
`flag` (not wrong, but not a clean routing observation).

| Check | Fails or flags when |
| --- | --- |
| `plugin_id_matches_router` | the request paired a model slug with the other router's plugin id |
| `expected_status` | the status is not the cell's `expect_status` (200 unless set) |
| `allowed_models` | the router's choice matches no allow-list pattern |
| `excluded_models` | the choice, or a fallback that answered, matches an exclusion |
| `classified` | flag: a success with no `task_type` (classification unavailable) |
| `served_by_chosen_model` | flag: a fallback model answered instead of the router's choice |

Patterns use one wildcard, `*`, which may span the `/` between vendor and
model. Analysis uses only trials with no fail and no flag.

Three findings compare cells and so need a whole run:

- `observable_effect` flags a configured cell whose choices equal the
  unconfigured baseline for the same router, transport and prompt. A flag means
  the data cannot tell "honoured" from "ignored"; it is not proof of either.
- `cost_tier_direction` checks that cost does not fall as the tier rises, and
  that each band picks a different, dearer model than the band below.
- `cost_tier_precedence` compares a cell that sent both cost settings with the
  tier-only and dial-only cells for the same values.

A cell that is meant to fail declares it with `expect_status: 404`; the 404 is
then the passing result.

## Adding an experiment

Add an entry to `experiments.yaml`:

```yaml
  - id: tier-high            # lowercase letters, digits, . _ -
    description: What this cell is for.
    router: both             # auto | auto-beta | both
    transport: chat_completions
    prompt_id: math-sum      # an id from prompts.yaml
    repeats: 5
    plugin:
      cost_tier: high
```

`router: both` expands to one cell per track with identical settings; that is
how a head-to-head comparison is declared, and it requires the default
`stickiness: isolate`. Unknown or misspelled keys are an error, as is a block
that sets nothing. Run `-dry-run` after editing to see exactly what would be
sent.

`session_id` is sent in the request body only; the `x-session-id` header is
never used (the body wins when both are present, and the limit is 256
characters). Sticky sessions expire after 10 idle minutes. For a sticky cell
the harness prefixes the session id with the run id, so one run cannot inherit
a session an earlier run left behind.

A sticky or implicit cell can change task inside its session with
`prompt_sequence: [id, id, ...]` in place of `prompt_id`. Two sticky
experiments that set the same `session_id` share one session and run in file
order.

## Running

Run everything from this directory.

```bash
go test ./...                                        # offline, network mocked
go run ./cmd/lab -dry-run                            # render every trial, send nothing
go run ./cmd/lab -max-spend 0.05 -only smoke-low     # run named experiments
go run ./cmd/lab -max-spend 0.50                     # run the whole matrix
go run ./cmd/lab -config experiments-slow.yaml -max-spend 0.05   # the 11-minute decay test
go run ./cmd/analyze                                 # assertion report for the latest run
go run ./cmd/analyze -run <run_id>                   # ... or for a named run
```

A live run needs `-max-spend`. Before sending anything it prints an estimated
cost per cell and asks you to type `yes` when the total is above
`-confirm-above` (default $0.10). During the run:

- Spend is counted from the cost each response reports. Once it passes
  `-max-spend` the run halts and in-flight trials are cancelled, so the
  overshoot is at most three trials. A response with no reported cost is charged
  at the estimate, never at zero.
- `-max-trials` (default 300) refuses a plan that is too large, whole, rather
  than running part of it.
- Three sequences run at a time. Isolate trials are independent; trials that
  share a session run in order.
- 429 and 5xx responses and network failures are retried up to three attempts,
  with jittered backoff or the server's `Retry-After`. Other 4xx responses are
  never retried: a 404 from an impossible allow-list is a result.
- `defaults.max_price` in the YAML caps what any endpoint may charge. A cell
  whose chosen model has no endpoint under the cap fails with a 404; that is
  intended and is not retried without the cap.

Trials are appended to `logs/trials.jsonl` (mode 0600, ignored by git), one
JSON object per trial. Prompt and response text are left out unless you pass
`-log-bodies`; the API key is never logged, and `-debug-key-fingerprint` logs
only its last four characters.

Not built yet: `cmd/backfill`, and the rest of the `cmd/analyze` report.

## Why this has its own go.mod

This is a self-contained subproject inside a larger repository, like the other
Go projects next to it. Its `go.mod` and README are its own (ignore rules come
from the repository root `.gitignore`), and
`go` commands run from the repository root do not see it.
