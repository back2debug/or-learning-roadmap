# reasoning-explorer

A Go learning project that dissects how **reasoning models** work through the
[OpenRouter](https://openrouter.ai) chat completions API. It is deliberately
not a polished tool: everything is optimized for **inspectability** — raw HTTP
with the standard library only (`net/http`, `encoding/json`, `bufio`), no SDKs,
and every request/response logged verbatim before it is sent/parsed.

## Setup

```bash
export OPENROUTER_API_KEY='sk-or-...'
go run . phase1   # or phase2 ... phase6
```

Every run writes timestamped files to `./logs/`:

```
logs/2026-08-01T11-44-17_phase2_deepseek-deepseek-r1-0528_request.json
logs/2026-08-01T11-44-17_phase2_deepseek-deepseek-r1-0528_response.json
logs/2026-08-01T12-03-46_phase4_deepseek-deepseek-r1-0528_sse.txt   # raw SSE transcript
```

The request is logged **before** sending; the raw response body is logged
**before** parsing (so even error bodies and unexpected fields are preserved).

All phases (except phase 3/5's matrix) use the same prompt — *"Which number is
bigger, 9.11 or 9.9?"* — a classic coin-flip question for non-reasoning models,
so the only variable is the model/config.

## The phases and what they demonstrated

### Phase 1 — Baseline anatomy (non-reasoning model)
`openai/gpt-4o-mini`, plain request. Unmarshals into structs **and** a
`map[string]any` to expose fields the structs miss. OpenRouter additions over
vanilla OpenAI: `gen-...` id (queryable at `/api/v1/generation`), `provider`,
`native_finish_reason`, `usage.cost` (with `"usage": {"include": true}`),
`cost_details`. Note `message.reasoning: null` exists even here — the slot is
always present.

### Phase 2 — Same prompt, reasoning model
`deepseek/deepseek-r1-0528`. Reasoning appears in **two mirrored places**:
`message.reasoning` (plaintext) and `message.reasoning_details[]` (structured:
`type`, `format`, `text`/`summary`/`data`, optional `signature`).
Token accounting: `completion_tokens` **includes** reasoning
(`usage.completion_tokens_details.reasoning_tokens` breaks it out), and
**reasoning tokens are billed at the output rate**. 

### Phase 3 — The `reasoning` request parameter
Matrix over `{"effort": "low"|"high"}`, `{"max_tokens": 512}`,
`{"exclude": true}`, and no param, on R1:

- `effort` is a **ceiling, not a target** for budget-style providers. R1 has no
  native effort dial; low/high results were dominated by sampling variance
  (low actually reasoned 10× *more* than high in one sample).
- `max_tokens: 512` **was enforced**: reasoning stopped at 514 tokens, and
  `finish_reason` stayed `"stop"` (reasoning truncation ≠ `"length"`).
- `exclude: true` hid the reasoning text **but still billed 731 reasoning
  tokens** — it's a display option, not a cost option.

### Phase 4 — Streaming (hand-parsed SSE)
`"stream": true`, parsed with `bufio.Scanner`. The wire format:
`: OPENROUTER PROCESSING` keepalive comments, `data: {json}` chunks,
`data: [DONE]` sentinel (not JSON — special-case it). Chunks put fragments in
`choices[].delta`; reasoning deltas mirror each fragment in both
`delta.reasoning` and `delta.reasoning_details[]`. 

### Phase 5 — Cross-provider comparison (`effort: "medium"`, same prompt)

| model | reasoning visibility | `reasoning_details` shape | reason/compl tokens |
|---|---|---|---|
| deepseek/deepseek-r1-0528 | raw text | `reasoning.text`, `format=unknown` | 446/533 |
| anthropic/claude-haiku-4.5 | raw text | `reasoning.text`, `format=anthropic-claude-v1`, **signed** | 35/94 |
| openai/gpt-5-mini | **encrypted only** | `reasoning.encrypted`, `format=openai-responses-v1`, base64 blob | 128/139 |
| google/gemini-2.5-flash | raw text | `reasoning.text`, `format=google-gemini-v1` | 283/286 |
| qwen/qwen3-235b-thinking | raw text | `reasoning.text`, `format=unknown` | 439/446 |

### Phase 6 (stretch) — Multi-turn tool calling with thinking blocks
`anthropic/claude-haiku-4.5` + a local `lookup_number` tool. Turn 1 returns
`finish_reason: "tool_calls"` with a **signed** `reasoning.text` block
(`format=anthropic-claude-v1`) and two parallel tool calls (`toolu_bdrk_...`
ids — Bedrock leaking through). Turn 2 echoes the assistant message + `role:
"tool"` results back, in three variants:

- **preserved** `reasoning_details`: works; the re-sent thinking block is
  billed as *input* (+75 prompt tokens vs stripped).
- **stripped**: also accepted with the correct answer — on Claude 4.x via
  Bedrock, omitting prior thinking did not hard-fail (earlier model
  generations rejected this; the docs still require preservation).
- **tampered** (edited text, original signature): also accepted, and billed
  *identically* to the preserved run — evidence the edited text never reached
  signature verification on this route.
