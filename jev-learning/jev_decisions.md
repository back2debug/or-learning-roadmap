# TypeSafe: Jev — structured decisions on OpenRouter

Reference notes for `jev_decisions.go`. Written 2026-09-18 against `typesafe/jev-1.13-20260917`.

---

## The model

OpenRouter's full description (the `/models` catalog truncates it):

> Jev is a structured decision model from TypeSafe, and the first of its **System One** models.
> System One models make fast, structured decisions for software, returning a typed choice rather
> than free-form text. It is suited for routing, classification, and other decision points inside
> an application where a fast, predictable answer matters more than generated prose.

The name is the thesis: "System One" is Kahneman's fast/intuitive thinking, as opposed to slow
deliberative reasoning. It is built for the decision points *inside* an app — the `if` statements
you cannot write by hand — not for chat.

| | |
|---|---|
| Slugs | `typesafe/jev-1.13`, alias `~typesafe/jev-latest` |
| Modality | `text->decisions` |
| Context | 32,000 (max completion 28,800) |
| Pricing | $0.042/M input, **$0/M output** |
| Observed latency | 179–350 ms |
| `supported_parameters` | `[]` — empty |

Two rows are worth dwelling on. **Output is free** because the output is not prose — it is a couple
of dozen tokens of structure, so billing is on input only. And **`supported_parameters` is empty**:
no `temperature`, no `max_tokens`, no `tools`, no `response_format`. There are no knobs. The typed
question *is* the knob.

### Finding it in the catalog

`GET /api/v1/models` defaults to `output_modalities=text` and returns 445 models — **Jev is not
among them**, and it appears nowhere in `llms.txt` or `llms-full.txt` either. Use:

```bash
curl -H "Authorization: Bearer $OPENROUTER_API_KEY" \
  "https://openrouter.ai/api/v1/models?output_modalities=all"        # 602 models
curl -H "Authorization: Bearer $OPENROUTER_API_KEY" \
  "https://openrouter.ai/api/v1/models?output_modalities=decisions"  # just the Jev family
```

This is a genuine trap: a model can look entirely absent — 404 from `/models/<slug>/endpoints`,
`"jev is not a valid model ID"` from chat completions, zero docs hits — purely because of that
default filter.

### Documentation status

- The **API reference is updated**: `POST /api/alpha/decisions` with full `DecisionsRequest` /
  `DecisionsResponse` schemas, plus Go/Python/TypeScript SDK pages under `Alpha.Decisions`.
- There is **no model-specific docs page** for Jev or TypeSafe. The docs index mentions neither by
  name. `openrouter.ai/typesafe/jev-1.13` renders as a catalog page, but there is no Jev guide.

---

## The endpoint

Because the output modality is `decisions` rather than `text`, Jev is **not** reachable through
`/chat/completions` — that path returns `"jev is not a valid model ID"`. It goes to the Decisions
router:

```
POST https://openrouter.ai/api/alpha/decisions
```

Note `/api/alpha/`, not `/api/v1/`. This is an alpha surface; the shape can move.

### Request envelope

```jsonc
{
  "model":      "typesafe/jev-1.13",        // required
  "state":      <string | object | array>,  // required — the thing being judged
  "questions":  { "<your-name>": <question> },
  "session_id": "...",   // optional, groups requests in the Logs view, max 256 chars
  "user":       "..."    // optional, end-user attribution, max 256 chars
}
```

The central design idea: **`state` and `questions` are separate.** Describe the situation once,
then ask N independent questions about it. Answers return in a map keyed by *your* names — send
`"is_urgent"`, get back `"is_urgent"`. No prompt parsing, no JSON-in-a-string to unwrap, no
retry-because-it-wrote-prose-again.

### Response envelope

```jsonc
{
  "id":       "gen-dec-...",
  "model":    "typesafe/jev-1.13-20260917",  // resolved, dated version
  "provider": "TypeSafe",
  "answers":  { "<your-name>": <answer> },
  "usage":    { "input_tokens": 0, "output_tokens": 0, "cost": 0.0 }
}
```

---

## The three question types

| Type | You supply | You get back |
|---|---|---|
| `noul` | `instructions`, optional `criteria.true` / `criteria.false` | `noul`: float 0–1 |
| `choice` | `instructions`, `criteria` map of option → guidance | `choice` (a key), `confidence`, `probabilities` |
| `score` | `instructions`, `criteria` **ordered array** | `score` (continuous), `confidence`, `legend`, `probabilities` |

`instructions` is required on all three. Anywhere guidance or state is expected, the API accepts a
plain string, a JSON object, or a JSON array.

### `noul` — graded boolean

The name is TypeSafe's own term. The schema describes it as a boolean evaluation, but the value
returned is a **float**, not `true`/`false`:

```json
{"type": "noul", "noul": 0.96}
```

So you choose the threshold, and you can route the middle band to a human. `criteria` is optional
here but sharpens the result — supply both `true` and `false` if you supply either.

### `choice` — pick one key

```json
{"type":"choice","choice":"billing","confidence":1,
 "probabilities":{"billing":1,"technical":0,"account":0,"abuse":0}}
```

Because the options are map *keys*, the returned `choice` is a string you can switch on directly.
It cannot come back as a synonym or a sentence.

### `score` — position on an ordered ladder

`criteria` is an **ordered array**, index 0 = low end. The returned score is **continuous**:

```json
{"type":"score","score":2.92,"confidence":0.92,
 "legend":{"0":"Trivial…","1":"Low…","2":"Moderate…","3":"High…","4":"Critical…"},
 "probabilities":{"0":0,"1":0,"2":0.09,"3":0.9,"4":0.01}}
```

`2.92` means "essentially rung 3, with a little pull toward 2". Formatting it `%.0f` displays a
flat `3` and throws away precisely the interesting part. `legend` echoes your rungs back keyed by
index, so a score can be labelled without keeping the original array around.

Across all three types the pattern is the same: **a committed answer plus a distribution.** Act on
the answer; use the distribution to decide when *not* to.

---

## The three requests

Each adds a dimension, so the file reads as a progression.

### 1 — `noul`, plain-string state

The minimum viable call. One question, prose state, one number back. Establishes auth, endpoint,
and round trip.

```json
{
  "model": "typesafe/jev-1.13",
  "state": "The checkout page has returned HTTP 500 for every user since the 14:20 deploy. …",
  "questions": {
    "is_urgent": {
      "type": "noul",
      "instructions": "Does this incident require paging the on-call engineer right now?",
      "criteria": {
        "true":  "Revenue or a core user flow is broken in production for many users.",
        "false": "Cosmetic, low-traffic, already mitigated, or affecting only a handful of users."
      }
    }
  }
}
```

→ `is_urgent: 0.96` · 367/23 tokens · $0.000015

### 2 — `choice`, object state

Two changes at once. The question becomes a routing decision over four queues, and `state` becomes
a JSON object (`subject`, `body`, `customer_plan`, `attachments`, `previous_tickets`) rather than
one blob of prose — so the model sees fields as fields. Also adds `session_id` and `user`.

→ `queue: "billing"` at p=1.00 · 474/46 tokens · $0.000020

### 3 — all three types, one round trip

A PR-triage object state with three questions in a single call: `risk` (score),
`needs_second_reviewer` (noul), `area` (choice).

This is the one that demonstrates the economics: three decisions for 652 input tokens, because
`state` is sent once and amortized across all three questions. Asking separately would mean three
copies of the same context. It also uses the `~typesafe/jev-latest` alias, which resolved to
`jev-1.13-20260917`.

→ `risk: 2.93` (conf 0.93) · `needs_second_reviewer: 0.88` · `area: "payments"` (p=1.00)
· 652/82 tokens · $0.000027

**Whole run: $0.000063.**

---

## The Go types

### Request side

`Guidance = any`, because the API accepts a string, object, or array anywhere guidance or state
appears.

```go
type Question interface{ isQuestion() }

type NoulQuestion struct {
    Type         string        `json:"type"`          // "noul"
    Instructions Guidance      `json:"instructions"`
    Criteria     *NoulCriteria `json:"criteria,omitempty"`
}
type ChoiceQuestion struct { /* Criteria map[string]Guidance */ }
type ScoreQuestion  struct { /* Criteria []Guidance — order matters */ }
```

The sealed `Question` interface (unexported `isQuestion()`) means only these three types can land
in the `questions` map — a stray struct will not compile. The constructors `Noul(…)`, `Choice(…)`,
`Score(…)` exist so you cannot forget to set `Type: "noul"`, the one field the wire format needs
and the compiler cannot check.

### Response side — a discriminated union

Answers arrive as `map[string]json.RawMessage`: deferred decoding, because the shape is unknowable
until `type` is read.

```go
func Decode(raw json.RawMessage) (any, error) {
    var probe struct{ Type string `json:"type"` }
    if err := json.Unmarshal(raw, &probe); err != nil {
        return nil, err
    }
    switch probe.Type {
    case "noul":   var a NoulAnswer;   return a, json.Unmarshal(raw, &a)
    case "choice": var a ChoiceAnswer; return a, json.Unmarshal(raw, &a)
    case "score":  var a ScoreAnswer;  return a, json.Unmarshal(raw, &a)
    default:       return nil, fmt.Errorf("unknown answer type %q", probe.Type)
    }
}
```

Read `type` first, then unmarshal into the concrete struct. The alternative — one wide struct with
every field and a lot of zero values — would let you read `.Score` off a `noul` answer and silently
get `0`. This way the type switch at the call site is exhaustive and a wrong field access does not
compile.

```go
type NoulAnswer struct {
    Type string  `json:"type"`
    Noul float64 `json:"noul"`   // 0..1 — how true, not whether true
}
type ChoiceAnswer struct {
    Type          string             `json:"type"`
    Choice        string             `json:"choice"`
    Confidence    float64            `json:"confidence"`
    Probabilities map[string]float64 `json:"probabilities"`
}
type ScoreAnswer struct {
    Type          string             `json:"type"`
    Score         float64            `json:"score"`
    Confidence    float64            `json:"confidence"`
    Legend        map[string]any     `json:"legend"`
    Probabilities map[string]float64 `json:"probabilities"`
}
```

---

## Practical notes

- **Pin the version in production.** `~typesafe/jev-latest` will drift; requests 1 and 2 pin
  `typesafe/jev-1.13` deliberately.
- **Alpha endpoint.** `/api/alpha/decisions` carries no stability guarantee.
- **Batch questions against one `state`.** Input tokens dominate the cost and `state` is sent once.
- **Use `session_id`** to group multi-question or multi-turn workflows in the OpenRouter Logs view.
- **Keep the probabilities.** The confidence distribution is the natural place to draw a
  human-escalation threshold, and the easiest thing to discard by accident.
- **Use the inference key** (`OPENROUTER_API_KEY`), not the management key (`OR_MGMT_KEY`) —
  management keys are for the keys/credits/analytics endpoints, not for inference.

## Files

| File | |
|---|---|
| `jev_decisions.go` | Typed client and the three requests; stdlib only, `go vet` clean |
| `jev_decisions_20260918_103713.log` | Full request/response bodies, timestamped |
| `jev_decisions.md` | This document |

```bash
go run jev_decisions.go   # needs OPENROUTER_API_KEY in the environment
```
