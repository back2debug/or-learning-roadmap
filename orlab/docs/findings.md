# Findings — live-docs reconciliation and live runs

Checked: **2026-08-27** against https://openrouter.ai/docs (llms.txt index,
errors-and-debugging, router-metadata, chat-completion reference, anthropic-messages
reference, responses overview/basic-usage). This API surface moves; re-verify before
trusting anything below.

## Confirmed as specified

- Base URL `https://openrouter.ai/api/v1`; three completion routes:
  `/chat/completions`, `/responses`, `/messages`.
- `debug: { echo_upstream_body: true }` exists and is **streaming-only**:
  "Only works with streaming mode." Non-streaming requests silently ignore it.
  On provider fallbacks, one debug chunk is emitted **per attempted provider**.
- Chat Completions: `session_id` max 256 chars; **no `output_config`** in the schema;
  `reasoning.effort` enum `max | xhigh | high | medium | low | minimal | none`;
  `reasoning.summary` enum `auto | concise | detailed`; `max_tokens` deprecated in
  favor of `max_completion_tokens` ("some providers enforce a minimum of 16");
  no top-level `usage` request field; `stream_options.include_usage` deprecated —
  "This field has no effect. Full usage details are always included.";
  `provider.require_parameters` wording confirmed verbatim ("providers will receive
  only the parameters they support, and ignore the rest");
  `metadata` max 16 pairs, 64-char keys, 512-char values; `stop` max 4 sequences;
  `response_format` is a union of `text | json_object | json_schema | grammar | python`.
- Responses API: stateless — requests with `store: true` or a non-null
  `previous_response_id` are **rejected with a 400**. Forwarded to the provider's
  Responses endpoint without conversion to Chat Completions. Uses `input`,
  `max_output_tokens`, typed SSE events (`response.created`,
  `response.output_item.added`, `response.content_part.delta`, …, `response.done`).
- Router metadata: header `X-OpenRouter-Metadata: enabled`
  (legacy alias `X-OpenRouter-Experimental-Metadata`), value case-insensitive.
  Wired into **all** completion routes including `/messages`, streaming and
  non-streaming. Omitted from 500s by design and from failures that occur before
  the router has routing state (auth, rate-limit, edge validation). Present on
  404 (no providers), 403 guardrail blocks (with `pipeline` array), 502/503/504/529.
- `GET /models` and `GET /generation?id=` as described.
- OpenRouter keys begin `sk-or-`.

## Drift — the live docs contradict the project brief

1. **`echo_upstream_body` is explicitly NOT supported on `/messages`.**
   The brief left this untested; the errors-and-debugging page now states it is
   supported on `/chat/completions` and `/responses` only. We still probe it
   empirically (the probe is cheap and the docs could lag), but the expected
   result is: ignored/absent on `/messages`. Delivery differs by dialect:
   - Chat Completions: debug arrives as the **first stream chunk** with an empty
     `choices` array.
   - Responses: arrives as a typed **`response.debug`** SSE event.

2. **`max_tokens` is NOT required on `/messages`.** The brief said it was
   (matching Anthropic's own API). OpenRouter's schema lists only `model` and
   `messages` as required. The `error_probe` entry "missing max_tokens on
   /messages" is therefore expected to *succeed*, which is itself a finding —
   keep the probe, reframe the expectation.

3. **`session_id` cap on `/messages` is 256, not 128.** The documented asymmetry
   the brief wanted to catch has evidently been fixed (or was never real). The
   200-char `extras_parity` probe stays — now the expected result is uniform
   acceptance, and a rejection anywhere is the finding.

4. **`thinking` on `/messages` has three modes**, not just enabled/disabled:
   `{type:"enabled", budget_tokens}` | `{type:"disabled"}` |
   `{type:"adaptive", display?: "summarized"|"omitted"}`.

5. **`output_config` on `/messages` is richer than sketched**:
   `effort` (`low|medium|high|xhigh|max`), `format`
   (`{type:"json_schema", schema}`), and `task_budget`
   (`{type:"tokens", total, remaining?}`). Still absent from the Chat schema —
   the "does Chat reject or ignore it" probe stands.

6. **Error taxonomy is larger than the brief's enum.** Documented
   `error.metadata.error_type` values now include: `context_length_exceeded`,
   `max_tokens_exceeded`, `token_limit_exceeded`, `string_too_long`,
   `authentication` (401), `permission_denied` (403), `payment_required` (402),
   `rate_limit_exceeded` (429), `provider_overloaded` (503),
   `provider_unavailable` (502), `invalid_request`, `invalid_prompt`,
   `not_found` (404), `content_policy_violation`, `refusal`,
   image-specific types, `server` (500), `timeout` (**504**, not 408),
   `unmapped` (500). `provider_code` carries the upstream provider's own code
   when available. On 500s the `message` is replaced with a generic string and
   `provider_code`/`openrouter_metadata` are omitted, **but `error_type`
   survives**. "Stable across all API formats" remains the claim to test on
   `/responses` and `/messages`.

7. **Streaming delivery of `openrouter_metadata`**: final chunk before
   `data: [DONE]` on Chat/Responses; part of the terminal **`message_stop`**
   event on Messages. **Cache hits never include `openrouter_metadata`** —
   a run that reuses a cached prompt will lose that channel; don't misread the
   absence as a router bug.

## Measured against the live API — 2026-08-28

First live run, `x-ai/grok-4.6` (provider xAI, 500K context, 15 declared
supported parameters).

1. **`/messages` routes to non-Anthropic models.** Grok 4.6 over
   `/api/v1/messages` returns 200 with a well-formed Anthropic-shaped
   response (`content` blocks, `stop_reason: end_turn`). The headline open
   question is answered: yes, OpenRouter translates the Anthropic Messages
   format for arbitrary models.

2. **`/messages` streams an OpenAI-style terminator that Anthropic never
   sends.** The stream ends:

   ```
   event: message_stop
   data: {"type":"message_stop","openrouter_metadata":{…}}

   event: data
   data: [DONE]
   ```

   Anthropic's own API terminates at `message_stop`. OpenRouter appends a
   `data: [DONE]` sentinel under an `event: data` label. **A strict
   Anthropic-compatible SSE client will fail here**, because `[DONE]` is not
   valid JSON and `data` is not a member of the Messages event enum. This
   harness hit exactly that failure on its first live request. Undocumented
   as far as the pages above go.

3. **`echo_upstream_body` really is absent on `/messages`**, confirming the
   docs: 0 upstream bodies captured there, 1 each on `/chat/completions` and
   `/responses`.

4. **Prompt-token accounting differs by dialect for identical input.** Same
   prompt, same model, same provider, same request moment:

   | dialect | prompt | completion | reasoning | cached |
   |---|---|---|---|---|
   | chat | 214 | 124 | 120 | 128 |
   | responses | 214 | 127 | 123 | 128 |
   | messages | **86** | 92 | *not reported* | 128 |

   Chat and Responses agree at 214; Messages reports 86 for the same bytes —
   a ~2.5× disagreement. Messages also omits reasoning tokens from its usage
   block entirely even though the model demonstrably reasoned (54 bytes of
   reasoning text came back). Anyone budgeting or billing off the inline
   usage block will get materially different numbers depending only on which
   dialect they speak.

5. **Router metadata is richer than documented** and includes an `endpoints`
   object naming the candidate providers and which was selected, plus a
   resolved dated model id: `available=2, selected=xAI`, model
   `x-ai/grok-4.6-20260810`.

6. **Catalog churn is real.** `mistralai/mistral-small` no longer exists;
   current variants are `mistral-small-2603`, `mistral-small-3.2-24b-instruct`,
   and others. Validate every slug at preflight — never hardcode.

## Full run — 2026-08-28, 4 models × 18 entries × 3 dialects (207 outcomes)

Models: `x-ai/grok-4.6`, `google/gemini-2.5-flash`, `deepseek/deepseek-chat`,
`mistralai/mistral-small-2603`. None are Anthropic or OpenAI.

### The `error_type` promise is false

The Chat schema calls `error.metadata.error_type` a "canonical OpenRouter
error type, **stable across all API formats**". Measured over 12 error
probes: **held for 0, broke for 9, absent from every dialect for 3.**

| dialect | where `error_type` was found |
|---|---|
| chat | absent ×11 |
| responses | absent ×10 |
| messages | `error.error_type` ×9 |

Two separate problems. First, **only `/messages` surfaces it at all** — chat
and responses never did, across every probe and every model. Second, where it
does appear it sits at **`error.error_type`, not the documented
`error.metadata.error_type`**, so even code that reads it will look in the
wrong place.

The envelopes are structurally different, not just differently populated:

```jsonc
// chat + responses — no error_type anywhere, and an account id leaks in
{"error":{"message":"orlab/does-not-exist is not a valid model ID","code":400},
 "user_id":"user_…"}

// messages — Anthropic-shaped, with error_type grafted on as a sibling
{"type":"error",
 "error":{"type":"invalid_request_error",
          "message":"orlab/does-not-exist is not a valid model ID",
          "error_type":"invalid_request"},
 "request_id":"gen-…"}
```

Note `user_id` in the chat/responses envelopes: an account identifier echoed
into every error body, and therefore into any log that stores error responses.

### Upstream drift: it depends on the model, not just the dialect

Comparing the echoed upstream bodies for chat vs responses on the same model
and prompt: **18 identical, 29 divergent.** The split is per-model:

| model | identical | divergent |
|---|---|---|
| google/gemini-2.5-flash | 10 | 2 |
| x-ai/grok-4.6 | 8 | 3 |
| deepseek/deepseek-chat | 0 | 12 |
| mistralai/mistral-small-2603 | 0 | 12 |

So the answer to "does OpenRouter send the same bytes upstream regardless of
dialect" is **sometimes** — and you cannot predict it from the dialect alone.

The divergences are not cosmetic. Beyond `$.messages[0].content` being
restructured, the upstream bodies pick up **provider-specific fields that the
client never sent**: `venice_parameters`, `safe_prompt`, `stream_options`,
and asymmetric `top_p` / `top_k` / `stop`. OpenRouter injects provider-shaped
parameters during translation, and *which* it injects differs between
dialects for the same logical request.

### Other confirmations

- **`/messages` works with every non-Anthropic model tested** (54/56;
  the misses are error probes).
- **`echo_upstream_body`**: present on 54/54 chat and 55/55 responses
  streamed successes, **0/56 on messages**. Docs confirmed exactly.
- **No buffered response ever carried an upstream body** — the streaming-only
  constraint holds.
- **`max_tokens` is genuinely optional on `/messages`**: the
  `err_missing_max_tokens` probe succeeded 4/4 on all three dialects.
- **A 300-char `session_id` is rejected 400 on all three dialects and all
  four models** — the cap is enforced uniformly, so the documented 128-vs-256
  asymmetry really is gone. A 200-char id was accepted everywhere except one
  responses case.
- **`provider.require_parameters: true` turns silent drops into hard 404s**:
  `param_fate_strict` returned 404 "No endpoints found that can handle the
  requested parameters" on chat for all 4 models, while the identical request
  without the flag succeeded 4/4. That is the documented silent-drop behavior
  made visible, exactly as the flag promises.

### Caveat on these numbers

They come from re-analyzing the recorded run offline via `-replay`. A request
that timed out mid-stream live still has its partial body in the cassette and
replays as a parse success, so replay slightly overstates success versus the
live pass (live showed 3/4 on `long_ctx` and `param_fate` where replay shows
4/4). Use the live transcript for "did the request work", the replay analysis
for "what did the bytes say".

## Still-open questions the harness must answer empirically

- Does `/messages` actually route to non-Anthropic models (Grok, Gemini,
  DeepSeek, Mistral, Qwen), and what does the echo/metadata evidence say about
  how the translation happens?
- Does the `error_type` "stable across all API formats" promise hold on
  `/responses` and `/messages`?
- Does `/responses` work with non-OpenAI models at all, given it is "forwarded
  to the provider's Responses endpoint without conversion"? If only OpenAI-ish
  providers implement a Responses endpoint, this dialect may fail for most of
  our default model list — record per-model.
- Do the three upstream bodies (via echo, where available) match for the same
  model?
- Chat's reaction to `output_config`: rejected, ignored, or accepted?

Sources: [errors & debugging](https://openrouter.ai/docs/api_reference/errors-and-debugging.md),
[router metadata](https://openrouter.ai/docs/guides/features/router-metadata.md),
[chat completion reference](https://openrouter.ai/docs/api-reference/chat-completion),
[messages reference](https://openrouter.ai/docs/api/api-reference/anthropic-messages/create-a-message.md),
[responses overview](https://openrouter.ai/docs/api_reference/responses/overview.md),
[responses basic usage](https://openrouter.ai/docs/api_reference/responses/basic-usage.md).
