# Jev decisions — run analysis

What three runs of `jev_decisions.go` against `typesafe/jev-1.13` showed. The request and response
shapes are documented in the header comment of the Go file and are not repeated here.

Three runs so far: Sep 18, Oct 8 and Oct 9 2026. All sent byte-identical requests and resolved to
the same dated model, `typesafe/jev-1.13-20260917` — including request 3, which uses the
`~typesafe/jev-latest` alias.

---

## Answers

| Request | Question | Type | Sep 18 | Oct 8 | Oct 9 |
|---|---|---|---|---|---|
| 1 | `is_urgent` | noul | 0.96 | 0.95 | 0.96 |
| 2 | `queue` | choice | billing (p=1.00) | billing (p=1.00) | billing (p=1.00) |
| 3 | `area` | choice | payments (p=1.00) | payments (p=1.00) | payments (p=1.00) |
| 3 | `needs_second_reviewer` | noul | 0.88 | 0.88 | 0.88 |
| 3 | `risk` | score | 2.93 (conf 0.93) | 2.92 (conf 0.92) | 2.91 (conf 0.92) |

`risk` rung probabilities: Sep 18 `2: 0.08, 3: 0.91, 4: 0.01`; Oct 8 and Oct 9 `2: 0.09, 3: 0.90, 4: 0.01`.

- **No decision ever changed.** Every choice, and every noul on either side of 0.5, was the same in
  all three runs.
- **Graded values are not exactly reproducible.** `is_urgent` and `risk` moved by 0.01–0.02 between
  runs on identical input, and the model exposes no sampling parameters to pin them. A threshold
  placed within a few hundredths of an observed value will flip between runs.
- **`score` is the probability-weighted mean of the rungs.** Sep 18: 2×0.08 + 3×0.91 + 4×0.01 = 2.93.
  Oct 8 and Oct 9: 2×0.09 + 3×0.90 + 4×0.01 = 2.92, reported as 2.92 and 2.91 (the probabilities are
  rounded to two places). So 2.92 reads as "rung 3, with a little weight on rung 2".
- **`confidence` on a score is not the top rung's probability.** It was 0.92–0.93 against a top
  probability of 0.90–0.91. What it does measure is not established by these runs.
- **These inputs are all easy.** Both choice questions came back at p=1.00 every time, so the runs
  say nothing about how the probabilities behave on an ambiguous case.

## Latency

| Request | Sep 18 | Oct 8 | Oct 9 |
|---|---|---|---|
| 1 — one noul, string state | 350 ms | 333 ms | 336 ms |
| 2 — one choice, object state | 186 ms | 164 ms | 273 ms |
| 3 — three questions, alias | 1229 ms | 910 ms | 167 ms |

- Requests 1 and 2 stay between 164 and 350 ms. Request 1 is the first call of each run, so its
  time includes setting up the connection.
- Request 3 took about a second in two runs and 167 ms in the third. It differs from the others in
  two ways at once (three questions, and the alias instead of a pinned slug), so these runs cannot
  say which one costs the time, or whether it is noise.

## Tokens and cost

| Request | Input | Output | Cost |
|---|---|---|---|
| 1 | 367 | 23 | $0.000015414 |
| 2 | 474 | 46 | $0.000019908 |
| 3 | 652 | 82 | $0.000027384 |
| **Run** | **1493** | **151** | **$0.000063** |

- Token counts and cost were identical in all three runs.
- **Output is free, confirmed.** Each cost equals input tokens × $0.042/M exactly
  (367 × 0.042 / 1,000,000 = 0.000015414); output tokens add nothing.
- **Input probably carries a fixed overhead.** Request 1 is billed 367 input tokens for roughly a
  hundred tokens of visible JSON (an estimate, not a measurement), which suggests a hidden template
  of a few hundred tokens per call. If so, batching questions into one call saves more than the
  repeated `state` alone.

## Not tested yet

- An ambiguous input, to see probabilities that are not 0 or 1.
- The same three questions sent separately versus batched, to measure the saving rather than infer it.
- Request 3 with a pinned slug, and a single question with the alias, to separate the two possible
  causes of the slow calls.
- Many repeats of one request, to get the real spread of a noul or score value.

## Gotchas found along the way

- `GET /api/v1/models` filters to `output_modalities=text` by default, and Jev is not in that list.
  Use `?output_modalities=decisions` (or `all`).
- `/chat/completions` rejects the model with `"jev is not a valid model ID"`. Decisions go to
  `POST /api/alpha/decisions`, an alpha path with no stability guarantee.
- Use the inference key (`OPENROUTER_API_KEY`), not the management key.
