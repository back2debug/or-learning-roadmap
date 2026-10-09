# jev-learning

A single-file Go program (standard library only) that sends three learning
requests to **TypeSafe: Jev**, a structured decision model on OpenRouter. Jev
does not use `/chat/completions` It answers typed questions through the alpha
Decisions endpoint, `POST /api/alpha/decisions`.

## Run

```bash
export OPENROUTER_API_KEY='sk-or-...'
go run jev_decisions.go
```

Each run makes three real API calls and writes the full requests and responses
to a timestamped `jev_decisions_<date>_<time>.log` in this directory.

## What it sends

| Request | Question types | What it shows |
| --- | --- | --- |
| 1 | `noul` | The smallest useful call: one graded boolean over a plain-string state |
| 2 | `choice` | Routing a support ticket to one key from a criteria map, with a JSON object as state |
| 3 | `score`, `noul`, `choice` | Several questions in one round trip, using the `~typesafe/jev-latest` alias |

## Run analysis

[jev_decisions.md](jev_decisions.md) compares the runs so far: how stable the
answers are, latency, and tokens and cost. The request and response shapes are
in the header comment of `jev_decisions.go`.
