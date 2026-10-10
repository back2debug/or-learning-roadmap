# Findings

What the harness has shown about OpenRouter's two Auto Router tracks,
`openrouter/auto` (stable) and `openrouter/auto-beta` (early access).

Everything here was measured on **2026-10-09**, between 15:38 and 17:07 US
Eastern, from one account. It is a snapshot: the routing pool rotates and the
beta track changes by design, so re-run before relying on any of it. The
tables these conclusions rest on are in the generated report at the bottom of
this file.

## The data

- Runs `7e624b1726f01f6d` (the main matrix, 198 trials) and `1c4192b6945f2e64`
  (the follow-up file, 100 trials). 287 of the 298 trials are clean.
- An earlier pass of the main matrix (`14f6394197f52044`, 192 trials) is in the
  log too. Every one of the 54 cells common to both passes chose the same model
  in both, about an hour apart.
- Total spend across all seven live runs: $0.17.
- Token budget: 64 completion tokens per trial. Selection, not answer quality,
  is what was measured.

## Answers to the nine questions

**1. Where do the tracks diverge?** Almost everywhere. With the same prompt and
the same settings, the two tracks chose different models in 36 of 41
head-to-head pairs. They agreed on the essay prompt, on `cost_tier: xhigh` for
the debugging prompt, and on the Google allow-list once flash models were
excluded. Beta leans on a different set of cheap models (`deepseek-v4-flash`,
`gpt-5.6-luna`) than stable (`deepseek-v4.1-flash`, `gpt-6-luna`), and at the
top band picks `claude-opus-5` where stable picks `claude-opus-5.5`.

**2. Do both tracks classify identically?** Yes. All five prompts got the same
`task_type` on both tracks, and the same one on every repeat. So the
divergence in question 1 is not explained by the classifier; it comes from
ranking or candidate selection after classification.

**3. Precedence.** `cost_tier` wins on both tracks. Tested in both directions
(`max` with dial 10, `low` with dial 0), each cell chose what its tier-only
cell chose and not what its dial-only cell chose.

**4. Effective default.** On both tracks, sending no cost setting chose the
same model as `cost_tier: low` and as dial 9. That fits both the docs ("roughly
the low band") and the API spec ("defaults to 9"). It does not distinguish
them, because on these prompts low and dial 9 select the same model.

**5. Band behaviour.** The band moves and excludes the cheap end. On the
debugging prompt each step from `low` to `max` chose a different model, and no
higher tier fell back to a cheaper tier's choice:

| Tier | Stable chose | Beta chose |
|---|---|---|
| low | deepseek-v4.1-flash | gpt-5.6-luna |
| medium | deepseek-v4-pro | deepseek-v4.1-flash |
| high | glm-5.3 | glm-5.2 |
| xhigh | kimi-k3 | kimi-k3 |
| max | claude-opus-5.5 | claude-opus-5 |

**6. The deprecated dial on its own.** It still works on both tracks, but it
is coarse and maps differently. Stable moves only at the ends: 0 gives the
`xhigh` model, 10 a cheaper model than the default, and 3 to 9 all match the
default. Beta has three steps: 0 to 5 give the `high` model, 7 to 9 the
default, and 10 a cheaper one.

**7. `plugins` over the Messages endpoint.** Honoured, on both tracks. The
allow-list nothing can match returned 404 on each, and all 12 mirrored cells
chose the same model over Messages as over Chat Completions. `provider` and
`session_id` are accepted there too, and `X-OpenRouter-Metadata` returns the
same routing block.

**8. Stickiness.** The two tracks behave differently, and this is the clearest
lead/lag candidate in the data.

- Beta carries a session's model. A session opened with `cost_tier: high` and
  continued with no setting kept the opening model in 4 of 5 sessions (8 of 9
  decisive turns), on the same provider, where fresh routing picks something
  else. The exception was the planning prompt, where the opening model
  (`claude-sonnet-5`) was dropped; that fits "reused only while still a top
  candidate".
- Stable did not carry in any of 5 sessions. Every later turn chose what fresh
  routing chooses.
- Changing the task mid-session produced a fresh choice on both tracks.
- A session established on stable and continued on beta: beta chose its own
  usual model, not stable's. This cannot show the router is part of a
  session's identity, since stable never showed carrying in the first place.
- Not measured: how the preference decays (the 11-minute test in
  `experiments-slow.yaml` has not been run), and whether an explicit
  `session_id` overrides the message fingerprint.

**9. Graceful degradation.** Not observed. Every successful response carried a
`task_type`, so the detector (flag a success with none) is untested against a
real case. A different degradation did occur and is detectable: see "the
answering model is not always the decision" below.

## Where the original brief was wrong or incomplete

- **`max_price` is applied at different stages on the two tracks.** Under a cap
  of $0.01 per million tokens with `cost_tier: max`, beta chose a $4/M model
  and failed with 404 `No endpoints found that satisfy the max price`, as the
  brief described. Stable instead chose zero-priced models and returned 200 or
  an upstream 502. Stable filters by price while choosing the model; beta
  filters afterwards.
- **A numeric default exists.** The API spec says `cost_quality_tradeoff` is
  0 to 10, higher is cheaper, and defaults to 9. The docs page gives no number.
- **The session facts are documented**, on the prompt-caching page: body
  `session_id` beats the `x-session-id` header, 256 characters maximum, expiry
  after 10 idle minutes.
- **The metadata shape differs between tracks.** Stable's pipeline stage is
  named `auto-router`; beta's is named `phaser` and adds `intent`,
  `routing_mode` and `config_version`.

## Things that would have produced wrong conclusions

- **A mismatched plugin id is silent.** `openrouter/auto` with the
  `auto-beta-router` id, and the reverse, both returned 200 and an ordinary
  model while the allow-list was ignored. The matched pair returned 404.
- **The answering model is not always the decision.** When the chosen model
  fails upstream, one of the router's fallback models answers, and the
  response's `model` field names the fallback. Four trials were affected, for
  example `claude-opus-5` chosen and `claude-opus-4.8` answering. The decision
  is `resolved_to` in the routing metadata; the harness records
  `model_fallback` and sets those trials aside.
- **Routing is deterministic.** Every one of 82 isolate cells chose one model
  across all its repeats. Repeating a task inside a session therefore says
  nothing about memory; only a session whose opening turn is routed differently
  from its later turns can.
- **The SDK overwrites the plugin id.** Setting the beta id on the SDK's stable
  plugin type sent the stable id. The id is decided by which constructor is
  called.
- **Observed cost is a noisy proxy.** The same model cost $0.000434 in one run
  and $0.000360 in the next, depending on the provider that served it. One
  adjacent tier step (stable, medium to high) is inconclusive for that reason.

## Checks on the harness itself

- **The price cap is not a confound at its default level.** The main matrix
  sends `max_price` of $25/$125 per million on every cell. With and without it,
  all 20 baseline pairs chose the same model.
- **No app labelling.** The backfilled generation records show `app_id` and
  `http_referer` null on all 497 generations.
- **No response caching.** `response_cache_source_id` is null on all 497, so
  the determinism above is the router's, not a cache replaying answers.
- **Inline cost is accurate.** `usage.cost` matched the backfilled `total_cost`
  on all 496 trials compared.

## Open questions

- Is the stickiness difference a behaviour on its way to stable, or a beta
  experiment? One day's data cannot say. Re-running in a few weeks would.
- Does stable carry a session's model under other conditions, for example with
  conversation history in the request?
- Why did stable choose a music model (`google/lyria-3-clip-preview`) for a
  maths prompt under the tight price cap?
- The tier and dial sweeps cover one prompt. Do the bands and dial steps fall
  in the same places for other task types?
- Does the Anthropic SDK's own request shape (`anthropic-version` header,
  `x-api-key` auth) change anything on the Messages endpoint? The harness sends
  neither.

<!-- Everything below this line is generated by cmd/analyze. Do not edit below it. -->

## Generated report

Runs: 7e624b1726f01f6d, 1c4192b6945f2e64. 298 live trials, 287 clean (succeeded, classified, answered by the model the router chose). Reported spend $0.1002.

Model names are the router's decision (`resolved_to`), not the model that answered, which differs when a fallback model serves the request. Cross-cell comparisons use clean isolate trials only.

### Track divergence: `auto` vs `auto-beta` on identical prompts and settings

The tracks chose different models in **36 of 41** head-to-head pairs. Each difference is a place where beta behaves differently from stable today; whether it is new behaviour on its way to stable or a beta-only experiment cannot be told from one snapshot.

| Experiment | Transport | Prompt | Setting | `auto` chose | `auto-beta` chose | Same |
|---|---|---|---|---|---|---|
| baseline-math-sum | chat\_completions | math-sum | no cost setting max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | deepseek/deepseek-v4-flash-20260731 | no |
| smoke-low | chat\_completions | math-sum | tier=low max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | deepseek/deepseek-v4-flash-20260731 | no |
| baseline-debug-nil-map | chat\_completions | debug-nil-map | no cost setting max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | openai/gpt-5.6-luna-20260709 | no |
| baseline-plan-migration | chat\_completions | plan-migration | no cost setting max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | deepseek/deepseek-v4-flash-20260731 | no |
| baseline-fact-lookup | chat\_completions | fact-lookup | no cost setting max\_price=25/125 | openai/gpt-6-luna-20260922 | deepseek/deepseek-v4-flash-20260731 | no |
| baseline-write-essay | chat\_completions | write-essay | no cost setting max\_price=25/125 | z-ai/glm-5.3-flash-20260826 | z-ai/glm-5.3-flash-20260826 | yes |
| tier-low | chat\_completions | debug-nil-map | tier=low max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | openai/gpt-5.6-luna-20260709 | no |
| tier-medium | chat\_completions | debug-nil-map | tier=medium max\_price=25/125 | deepseek/deepseek-v4-pro-20260813 | deepseek/deepseek-v4.1-flash-20260910 | no |
| tier-high | chat\_completions | debug-nil-map | tier=high max\_price=25/125 | z-ai/glm-5.3-20260816 | z-ai/glm-5.2-20260616 | no |
| tier-xhigh | chat\_completions | debug-nil-map | tier=xhigh max\_price=25/125 | moonshotai/kimi-k3-20260715 | moonshotai/kimi-k3-20260715 | yes |
| tier-max | chat\_completions | debug-nil-map | tier=max max\_price=25/125 | anthropic/claude-opus-5.5-20260921 | anthropic/claude-opus-5-20260723 | no |
| dial-0 | chat\_completions | debug-nil-map | dial=0 max\_price=25/125 | moonshotai/kimi-k3-20260715 | z-ai/glm-5.2-20260616 | no |
| dial-3 | chat\_completions | debug-nil-map | dial=3 max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | z-ai/glm-5.2-20260616 | no |
| dial-5 | chat\_completions | debug-nil-map | dial=5 max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | z-ai/glm-5.2-20260616 | no |
| dial-7 | chat\_completions | debug-nil-map | dial=7 max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | openai/gpt-5.6-luna-20260709 | no |
| dial-9 | chat\_completions | debug-nil-map | dial=9 max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | openai/gpt-5.6-luna-20260709 | no |
| dial-10 | chat\_completions | debug-nil-map | dial=10 max\_price=25/125 | openai/gpt-6-luna-20260922 | deepseek/deepseek-v4-flash-20260423 | no |
| precedence-tier-max-dial-10 | chat\_completions | debug-nil-map | tier=max dial=10 max\_price=25/125 | anthropic/claude-opus-5.5-20260921 | anthropic/claude-opus-5-20260723 | no |
| precedence-tier-low-dial-0 | chat\_completions | debug-nil-map | tier=low dial=0 max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | openai/gpt-5.6-luna-20260709 | no |
| restrict-google | chat\_completions | math-sum | allow=google/\* max\_price=25/125 | google/gemini-3.8-flash-20260902 | google/gemini-3.7-flash-20260813 | no |
| restrict-google-minus-flash | chat\_completions | math-sum | allow=google/\* exclude=\*flash\* max\_price=25/125 | google/gemini-3.1-pro-preview-20260219 | google/gemini-3.1-pro-preview-20260219 | yes |
| messages-smoke-low | messages | math-sum | tier=low max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | deepseek/deepseek-v4-flash-20260731 | no |
| messages-baseline-math-sum | messages | math-sum | no cost setting max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | deepseek/deepseek-v4-flash-20260731 | no |
| messages-baseline-debug-nil-map | messages | debug-nil-map | no cost setting max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | openai/gpt-5.6-luna-20260709 | no |
| messages-tier-low | messages | debug-nil-map | tier=low max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | openai/gpt-5.6-luna-20260709 | no |
| messages-tier-max | messages | debug-nil-map | tier=max max\_price=25/125 | anthropic/claude-opus-5.5-20260921 | anthropic/claude-opus-5-20260723 | no |
| messages-restrict-google | messages | math-sum | allow=google/\* max\_price=25/125 | google/gemini-3.8-flash-20260902 | google/gemini-3.7-flash-20260813 | no |
| nocap-math-sum | chat\_completions | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | deepseek/deepseek-v4-flash-20260731 | no |
| cap-math-sum | chat\_completions | math-sum | no cost setting max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | deepseek/deepseek-v4-flash-20260731 | no |
| nocap-debug-nil-map | chat\_completions | debug-nil-map | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | openai/gpt-5.6-luna-20260709 | no |
| cap-debug-nil-map | chat\_completions | debug-nil-map | no cost setting max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | openai/gpt-5.6-luna-20260709 | no |
| nocap-plan-migration | chat\_completions | plan-migration | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | deepseek/deepseek-v4-flash-20260731 | no |
| cap-plan-migration | chat\_completions | plan-migration | no cost setting max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | deepseek/deepseek-v4-flash-20260731 | no |
| nocap-fact-lookup | chat\_completions | fact-lookup | no cost setting | openai/gpt-6-luna-20260922 | deepseek/deepseek-v4-flash-20260731 | no |
| cap-fact-lookup | chat\_completions | fact-lookup | no cost setting max\_price=25/125 | openai/gpt-6-luna-20260922 | deepseek/deepseek-v4-flash-20260731 | no |
| nocap-write-essay | chat\_completions | write-essay | no cost setting | z-ai/glm-5.3-flash-20260826 | z-ai/glm-5.3-flash-20260826 | yes |
| cap-write-essay | chat\_completions | write-essay | no cost setting max\_price=25/125 | z-ai/glm-5.3-flash-20260826 | z-ai/glm-5.3-flash-20260826 | yes |
| high-math-sum | chat\_completions | math-sum | tier=high | anthropic/claude-opus-5.5-20260921 | z-ai/glm-5.2-20260616 | no |
| high-debug-nil-map | chat\_completions | debug-nil-map | tier=high | z-ai/glm-5.3-20260816 | z-ai/glm-5.2-20260616 | no |
| high-plan-migration | chat\_completions | plan-migration | tier=high | openai/gpt-6.1-sol-20260929 | anthropic/claude-sonnet-5-20260630 | no |
| high-fact-lookup | chat\_completions | fact-lookup | tier=high | anthropic/claude-sonnet-5.5-20260928 | z-ai/glm-5.2-20260616 | no |

### Task classification

| Prompt | Expected | `auto` classified as | `auto-beta` classified as | Stable across repeats | Tracks agree |
|---|---|---|---|---|---|
| math-sum | math | math | math | yes | yes |
| debug-nil-map | code:debugging | code:debugging | code:debugging | yes | yes |
| plan-migration | agent:multi\_step\_planning | agent:multi\_step\_planning | agent:multi\_step\_planning | yes | yes |
| fact-lookup | qa\_knowledge | qa\_knowledge | qa\_knowledge | yes | yes |
| write-essay | research\_report | content\_writing | content\_writing | yes | yes |

The tracks agreed on 5 of 5 prompts. "Expected" is the harness author's guess, recorded for comparison, not an assertion.

### Effective default: which settings match sending none

| Baseline cell | Prompt | No setting chose | Settings that chose the same | Settings that chose differently |
|---|---|---|---|---|
| baseline-math-sum/auto | math-sum | deepseek/deepseek-v4.1-flash-20260910 | tier=low | (none) |
| baseline-math-sum/auto-beta | math-sum | deepseek/deepseek-v4-flash-20260731 | tier=low | (none) |
| baseline-debug-nil-map/auto | debug-nil-map | deepseek/deepseek-v4.1-flash-20260910 | tier=low, dial=3, dial=5, dial=7, dial=9 | tier=medium, tier=high, tier=xhigh, tier=max, dial=0, dial=10 |
| baseline-debug-nil-map/auto-beta | debug-nil-map | openai/gpt-5.6-luna-20260709 | tier=low, dial=7, dial=9 | tier=medium, tier=high, tier=xhigh, tier=max, dial=0, dial=3, dial=5, dial=10 |
| messages-baseline-math-sum/auto | math-sum | deepseek/deepseek-v4.1-flash-20260910 | tier=low | (none) |
| messages-baseline-math-sum/auto-beta | math-sum | deepseek/deepseek-v4-flash-20260731 | tier=low | (none) |
| messages-baseline-debug-nil-map/auto | debug-nil-map | deepseek/deepseek-v4.1-flash-20260910 | tier=low | tier=max |
| messages-baseline-debug-nil-map/auto-beta | debug-nil-map | openai/gpt-5.6-luna-20260709 | tier=low | tier=max |
| nocap-math-sum/auto | math-sum | deepseek/deepseek-v4.1-flash-20260910 | (none) | tier=high |
| nocap-math-sum/auto-beta | math-sum | deepseek/deepseek-v4-flash-20260731 | (none) | tier=high |
| nocap-debug-nil-map/auto | debug-nil-map | deepseek/deepseek-v4.1-flash-20260910 | (none) | tier=high |
| nocap-debug-nil-map/auto-beta | debug-nil-map | openai/gpt-5.6-luna-20260709 | (none) | tier=high |
| nocap-plan-migration/auto | plan-migration | deepseek/deepseek-v4.1-flash-20260910 | (none) | tier=high |
| nocap-plan-migration/auto-beta | plan-migration | deepseek/deepseek-v4-flash-20260731 | (none) | tier=high |
| nocap-fact-lookup/auto | fact-lookup | openai/gpt-6-luna-20260922 | (none) | tier=high |
| nocap-fact-lookup/auto-beta | fact-lookup | deepseek/deepseek-v4-flash-20260731 | (none) | tier=high |

### Precedence: `cost_tier` vs `cost_quality_tradeoff`

Each cell sent both settings pulling in opposite directions, and is compared with the cells that sent only one.

| Cell | Result | Detail |
|---|---|---|
| precedence-tier-max-dial-10/auto | pass | cost\_tier wins: anthropic/claude-opus-5.5-20260921, as in tier-max/auto; dial-10/auto chose openai/gpt-6-luna-20260922 |
| precedence-tier-low-dial-0/auto | pass | cost\_tier wins: deepseek/deepseek-v4.1-flash-20260910, as in tier-low/auto; dial-0/auto chose moonshotai/kimi-k3-20260715 |
| precedence-tier-max-dial-10/auto-beta | pass | cost\_tier wins: anthropic/claude-opus-5-20260723, as in tier-max/auto-beta; dial-10/auto-beta chose deepseek/deepseek-v4-flash-20260423 |
| precedence-tier-low-dial-0/auto-beta | pass | cost\_tier wins: openai/gpt-5.6-luna-20260709, as in tier-low/auto-beta; dial-0/auto-beta chose z-ai/glm-5.2-20260616 |

### Cost tier sweep: does the band move?

Pass means the tier chose a different, dearer model than the tier below it: the band excluded the cheaper choice as well as bounding the dear end.

| Cell | Result | Detail |
|---|---|---|
| tier-medium/auto | pass | dearer and different, so the lower band's choice was excluded: $0.000421 (deepseek/deepseek-v4-pro-20260813), up from tier-low/auto at $0.000099 (deepseek/deepseek-v4.1-flash-20260910) |
| tier-high/auto | flag | different model, but its observed cost was not higher; at this token budget cost varies with the provider that served, so this step is inconclusive: $0.000360 (z-ai/glm-5.3-20260816), up from tier-medium/auto at $0.000421 (deepseek/deepseek-v4-pro-20260813) |
| tier-xhigh/auto | pass | dearer and different, so the lower band's choice was excluded: $0.001180 (moonshotai/kimi-k3-20260715), up from tier-high/auto at $0.000360 (z-ai/glm-5.3-20260816) |
| tier-max/auto | pass | dearer and different, so the lower band's choice was excluded: $0.001556 (anthropic/claude-opus-5.5-20260921), up from tier-xhigh/auto at $0.001180 (moonshotai/kimi-k3-20260715) |
| tier-medium/auto-beta | pass | dearer and different, so the lower band's choice was excluded: $0.000099 (deepseek/deepseek-v4.1-flash-20260910), up from tier-low/auto-beta at $0.000087 (openai/gpt-5.6-luna-20260709) |
| tier-high/auto-beta | pass | dearer and different, so the lower band's choice was excluded: $0.000716 (z-ai/glm-5.2-20260616), up from tier-medium/auto-beta at $0.000099 (deepseek/deepseek-v4.1-flash-20260910) |
| tier-xhigh/auto-beta | pass | dearer and different, so the lower band's choice was excluded: $0.001013 (moonshotai/kimi-k3-20260715), up from tier-high/auto-beta at $0.000716 (z-ai/glm-5.2-20260616) |
| tier-max/auto-beta | pass | dearer and different, so the lower band's choice was excluded: $0.001798 (anthropic/claude-opus-5-20260723), up from tier-xhigh/auto-beta at $0.001013 (moonshotai/kimi-k3-20260715) |
| messages-tier-max/auto | pass | dearer and different, so the lower band's choice was excluded: $0.001556 (anthropic/claude-opus-5.5-20260921), up from messages-tier-low/auto at $0.000099 (deepseek/deepseek-v4.1-flash-20260910) |
| messages-tier-max/auto-beta | pass | dearer and different, so the lower band's choice was excluded: $0.001785 (anthropic/claude-opus-5-20260723), up from messages-tier-low/auto-beta at $0.000087 (openai/gpt-5.6-luna-20260709) |

### Config with no observable effect

A flag means the cell chose exactly what the unconfigured baseline chose, so this cell alone cannot tell "honoured" from "ignored". It is not evidence the setting was ignored: a setting equal to the default looks the same.

| Cell | Result | Detail |
|---|---|---|
| smoke-low/auto | flag | same choice as the baseline (deepseek/deepseek-v4.1-flash-20260910); this cell cannot show the config did anything |
| smoke-low/auto-beta | flag | same choice as the baseline (deepseek/deepseek-v4-flash-20260731); this cell cannot show the config did anything |
| tier-low/auto | flag | same choice as the baseline (deepseek/deepseek-v4.1-flash-20260910); this cell cannot show the config did anything |
| dial-3/auto | flag | same choice as the baseline (deepseek/deepseek-v4.1-flash-20260910); this cell cannot show the config did anything |
| dial-5/auto | flag | same choice as the baseline (deepseek/deepseek-v4.1-flash-20260910); this cell cannot show the config did anything |
| dial-7/auto | flag | same choice as the baseline (deepseek/deepseek-v4.1-flash-20260910); this cell cannot show the config did anything |
| dial-9/auto | flag | same choice as the baseline (deepseek/deepseek-v4.1-flash-20260910); this cell cannot show the config did anything |
| precedence-tier-low-dial-0/auto | flag | same choice as the baseline (deepseek/deepseek-v4.1-flash-20260910); this cell cannot show the config did anything |
| tier-low/auto-beta | flag | same choice as the baseline (openai/gpt-5.6-luna-20260709); this cell cannot show the config did anything |
| dial-7/auto-beta | flag | same choice as the baseline (openai/gpt-5.6-luna-20260709); this cell cannot show the config did anything |
| dial-9/auto-beta | flag | same choice as the baseline (openai/gpt-5.6-luna-20260709); this cell cannot show the config did anything |
| precedence-tier-low-dial-0/auto-beta | flag | same choice as the baseline (openai/gpt-5.6-luna-20260709); this cell cannot show the config did anything |
| messages-smoke-low/auto | flag | same choice as the baseline (deepseek/deepseek-v4.1-flash-20260910); this cell cannot show the config did anything |
| messages-smoke-low/auto-beta | flag | same choice as the baseline (deepseek/deepseek-v4-flash-20260731); this cell cannot show the config did anything |
| messages-tier-low/auto | flag | same choice as the baseline (deepseek/deepseek-v4.1-flash-20260910); this cell cannot show the config did anything |
| messages-tier-low/auto-beta | flag | same choice as the baseline (openai/gpt-5.6-luna-20260709); this cell cannot show the config did anything |

### Control: does a `max_price` cap change the routing decision?

The cap changed the decision in 0 of 20 pairs. Every unconfigured cell with a cap is paired with the uncapped cell for the same router and prompt, so a prompt appears once per capped cell.

| Router | Prompt | Cell with no provider block | Chose | Cell with a cap | Chose | Same |
|---|---|---|---|---|---|---|
| auto | math-sum | nocap-math-sum/auto | deepseek/deepseek-v4.1-flash-20260910 | baseline-math-sum/auto | deepseek/deepseek-v4.1-flash-20260910 | yes |
| auto | math-sum | nocap-math-sum/auto | deepseek/deepseek-v4.1-flash-20260910 | cap-math-sum/auto | deepseek/deepseek-v4.1-flash-20260910 | yes |
| auto-beta | math-sum | nocap-math-sum/auto-beta | deepseek/deepseek-v4-flash-20260731 | baseline-math-sum/auto-beta | deepseek/deepseek-v4-flash-20260731 | yes |
| auto-beta | math-sum | nocap-math-sum/auto-beta | deepseek/deepseek-v4-flash-20260731 | cap-math-sum/auto-beta | deepseek/deepseek-v4-flash-20260731 | yes |
| auto | debug-nil-map | nocap-debug-nil-map/auto | deepseek/deepseek-v4.1-flash-20260910 | baseline-debug-nil-map/auto | deepseek/deepseek-v4.1-flash-20260910 | yes |
| auto | debug-nil-map | nocap-debug-nil-map/auto | deepseek/deepseek-v4.1-flash-20260910 | cap-debug-nil-map/auto | deepseek/deepseek-v4.1-flash-20260910 | yes |
| auto-beta | debug-nil-map | nocap-debug-nil-map/auto-beta | openai/gpt-5.6-luna-20260709 | baseline-debug-nil-map/auto-beta | openai/gpt-5.6-luna-20260709 | yes |
| auto-beta | debug-nil-map | nocap-debug-nil-map/auto-beta | openai/gpt-5.6-luna-20260709 | cap-debug-nil-map/auto-beta | openai/gpt-5.6-luna-20260709 | yes |
| auto | plan-migration | nocap-plan-migration/auto | deepseek/deepseek-v4.1-flash-20260910 | baseline-plan-migration/auto | deepseek/deepseek-v4.1-flash-20260910 | yes |
| auto | plan-migration | nocap-plan-migration/auto | deepseek/deepseek-v4.1-flash-20260910 | cap-plan-migration/auto | deepseek/deepseek-v4.1-flash-20260910 | yes |
| auto-beta | plan-migration | nocap-plan-migration/auto-beta | deepseek/deepseek-v4-flash-20260731 | baseline-plan-migration/auto-beta | deepseek/deepseek-v4-flash-20260731 | yes |
| auto-beta | plan-migration | nocap-plan-migration/auto-beta | deepseek/deepseek-v4-flash-20260731 | cap-plan-migration/auto-beta | deepseek/deepseek-v4-flash-20260731 | yes |
| auto | fact-lookup | nocap-fact-lookup/auto | openai/gpt-6-luna-20260922 | baseline-fact-lookup/auto | openai/gpt-6-luna-20260922 | yes |
| auto | fact-lookup | nocap-fact-lookup/auto | openai/gpt-6-luna-20260922 | cap-fact-lookup/auto | openai/gpt-6-luna-20260922 | yes |
| auto-beta | fact-lookup | nocap-fact-lookup/auto-beta | deepseek/deepseek-v4-flash-20260731 | baseline-fact-lookup/auto-beta | deepseek/deepseek-v4-flash-20260731 | yes |
| auto-beta | fact-lookup | nocap-fact-lookup/auto-beta | deepseek/deepseek-v4-flash-20260731 | cap-fact-lookup/auto-beta | deepseek/deepseek-v4-flash-20260731 | yes |
| auto | write-essay | nocap-write-essay/auto | z-ai/glm-5.3-flash-20260826 | baseline-write-essay/auto | z-ai/glm-5.3-flash-20260826 | yes |
| auto | write-essay | nocap-write-essay/auto | z-ai/glm-5.3-flash-20260826 | cap-write-essay/auto | z-ai/glm-5.3-flash-20260826 | yes |
| auto-beta | write-essay | nocap-write-essay/auto-beta | z-ai/glm-5.3-flash-20260826 | baseline-write-essay/auto-beta | z-ai/glm-5.3-flash-20260826 | yes |
| auto-beta | write-essay | nocap-write-essay/auto-beta | z-ai/glm-5.3-flash-20260826 | cap-write-essay/auto-beta | z-ai/glm-5.3-flash-20260826 | yes |

### Transport divergence: Chat Completions vs Messages

The two transports chose different models in 0 of 12 mirrored pairs.

| Router | Prompt | Setting | Chat Completions chose | Messages chose | Same |
|---|---|---|---|---|---|
| auto | math-sum | tier=low max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | deepseek/deepseek-v4.1-flash-20260910 | yes |
| auto-beta | math-sum | tier=low max\_price=25/125 | deepseek/deepseek-v4-flash-20260731 | deepseek/deepseek-v4-flash-20260731 | yes |
| auto | math-sum | no cost setting max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | deepseek/deepseek-v4.1-flash-20260910 | yes |
| auto-beta | math-sum | no cost setting max\_price=25/125 | deepseek/deepseek-v4-flash-20260731 | deepseek/deepseek-v4-flash-20260731 | yes |
| auto | debug-nil-map | no cost setting max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | deepseek/deepseek-v4.1-flash-20260910 | yes |
| auto-beta | debug-nil-map | no cost setting max\_price=25/125 | openai/gpt-5.6-luna-20260709 | openai/gpt-5.6-luna-20260709 | yes |
| auto | debug-nil-map | tier=low max\_price=25/125 | deepseek/deepseek-v4.1-flash-20260910 | deepseek/deepseek-v4.1-flash-20260910 | yes |
| auto-beta | debug-nil-map | tier=low max\_price=25/125 | openai/gpt-5.6-luna-20260709 | openai/gpt-5.6-luna-20260709 | yes |
| auto | debug-nil-map | tier=max max\_price=25/125 | anthropic/claude-opus-5.5-20260921 | anthropic/claude-opus-5.5-20260921 | yes |
| auto-beta | debug-nil-map | tier=max max\_price=25/125 | anthropic/claude-opus-5-20260723 | anthropic/claude-opus-5-20260723 | yes |
| auto | math-sum | allow=google/\* max\_price=25/125 | google/gemini-3.8-flash-20260902 | google/gemini-3.8-flash-20260902 | yes |
| auto-beta | math-sum | allow=google/\* max\_price=25/125 | google/gemini-3.7-flash-20260813 | google/gemini-3.7-flash-20260813 | yes |

### Stickiness: does a session carry its model?

Decisive turns only: those where the fresh choice differs from the previous turn's model, so keeping it and choosing again look different. A session that carries its model contributes one carried turn per later turn.

| Router | Same task: carried | Same task: routed fresh | Task changed: carried | Task changed: routed fresh |
|---|---|---|---|---|
| auto | 0 | 5 | 0 | 2 |
| auto-beta | 8 | 1 | 0 | 3 |

| Session | Turn | Router | Prompt | Setting | Chose | Provider | Verdict |
|---|---|---|---|---|---|---|---|
| sticky-same-task-auto | 1 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | opening turn |
| sticky-same-task-auto | 2 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | cannot tell (fresh choice equals previous model) |
| sticky-same-task-auto | 3 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | cannot tell (fresh choice equals previous model) |
| sticky-same-task-auto | 4 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | cannot tell (fresh choice equals previous model) |
| sticky-same-task-beta | 1 | auto-beta | math-sum | no cost setting | deepseek/deepseek-v4-flash-20260731 | Baidu | opening turn |
| sticky-same-task-beta | 2 | auto-beta | math-sum | no cost setting | deepseek/deepseek-v4-flash-20260731 | Baidu | cannot tell (fresh choice equals previous model) |
| sticky-same-task-beta | 3 | auto-beta | math-sum | no cost setting | deepseek/deepseek-v4-flash-20260731 | Baidu | cannot tell (fresh choice equals previous model) |
| sticky-same-task-beta | 4 | auto-beta | math-sum | no cost setting | deepseek/deepseek-v4-flash-20260731 | Baidu | cannot tell (fresh choice equals previous model) |
| sticky-task-switch-auto | 1 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | opening turn |
| sticky-task-switch-auto | 2 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | cannot tell (fresh choice equals previous model) |
| sticky-task-switch-auto | 3 | auto | write-essay | no cost setting | z-ai/glm-5.3-flash-20260826 | CoreWeave | FRESH (previous model dropped) |
| sticky-task-switch-auto | 4 | auto | debug-nil-map | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | FRESH (previous model dropped) |
| sticky-task-switch-auto | 5 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | cannot tell (fresh choice equals previous model) |
| router-switch | 1 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | opening turn |
| router-switch | 2 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | cannot tell (fresh choice equals previous model) |
| router-switch | 3 | auto-beta | math-sum | no cost setting | deepseek/deepseek-v4-flash-20260731 | Baidu | router changed |
| router-switch | 4 | auto-beta | math-sum | no cost setting | deepseek/deepseek-v4-flash-20260731 | Baidu | cannot tell (fresh choice equals previous model) |
| sticky-task-switch-beta | 1 | auto-beta | math-sum | no cost setting | deepseek/deepseek-v4-flash-20260731 | Baidu | opening turn |
| sticky-task-switch-beta | 2 | auto-beta | math-sum | no cost setting | deepseek/deepseek-v4-flash-20260731 | Baidu | cannot tell (fresh choice equals previous model) |
| sticky-task-switch-beta | 3 | auto-beta | write-essay | no cost setting | z-ai/glm-5.3-flash-20260826 | CoreWeave | FRESH (previous model dropped) |
| sticky-task-switch-beta | 4 | auto-beta | debug-nil-map | no cost setting | openai/gpt-5.6-luna-20260709 | Azure | FRESH (previous model dropped) |
| sticky-task-switch-beta | 5 | auto-beta | math-sum | no cost setting | deepseek/deepseek-v4-flash-20260731 | Baidu | FRESH (previous model dropped) |
| carry-auto | 1 | auto | debug-nil-map | tier=medium | deepseek/deepseek-v4-pro-20260813 | Sail Research | opening turn |
| carry-auto | 2 | auto | debug-nil-map | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | FRESH (previous model dropped) |
| carry-auto | 3 | auto | debug-nil-map | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | cannot tell (fresh choice equals previous model) |
| carry-beta | 1 | auto-beta | debug-nil-map | tier=high | z-ai/glm-5.2-20260616 | Decart | opening turn |
| carry-beta | 2 | auto-beta | debug-nil-map | no cost setting | z-ai/glm-5.2-20260616 | Decart | CARRIED (kept previous model; fresh would be openai/gpt-5.6-luna-20260709) |
| carry-beta | 3 | auto-beta | debug-nil-map | no cost setting | z-ai/glm-5.2-20260616 | Decart | CARRIED (kept previous model; fresh would be openai/gpt-5.6-luna-20260709) |
| messages-sticky-same-task-auto | 1 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | opening turn |
| messages-sticky-same-task-auto | 2 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | cannot tell (fresh choice equals previous model) |
| messages-sticky-same-task-auto | 3 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | cannot tell (fresh choice equals previous model) |
| messages-sticky-same-task-auto | 4 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | cannot tell (fresh choice equals previous model) |
| carry-beta-math-sum | 1 | auto-beta | math-sum | tier=high | z-ai/glm-5.2-20260616 | Decart | opening turn |
| carry-beta-math-sum | 2 | auto-beta | math-sum | no cost setting | z-ai/glm-5.2-20260616 | Decart | CARRIED (kept previous model; fresh would be deepseek/deepseek-v4-flash-20260731) |
| carry-beta-math-sum | 3 | auto-beta | math-sum | no cost setting | z-ai/glm-5.2-20260616 | Decart | CARRIED (kept previous model; fresh would be deepseek/deepseek-v4-flash-20260731) |
| carry-auto-math-sum | 1 | auto | math-sum | tier=high | anthropic/claude-opus-5.5-20260921 | Azure | opening turn |
| carry-auto-math-sum | 2 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | FRESH (previous model dropped) |
| carry-auto-math-sum | 3 | auto | math-sum | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | cannot tell (fresh choice equals previous model) |
| carry-beta-debug-nil-map | 1 | auto-beta | debug-nil-map | tier=high | z-ai/glm-5.2-20260616 | Decart | opening turn |
| carry-beta-debug-nil-map | 2 | auto-beta | debug-nil-map | no cost setting | z-ai/glm-5.2-20260616 | Decart | CARRIED (kept previous model; fresh would be openai/gpt-5.6-luna-20260709) |
| carry-beta-debug-nil-map | 3 | auto-beta | debug-nil-map | no cost setting | z-ai/glm-5.2-20260616 | Decart | CARRIED (kept previous model; fresh would be openai/gpt-5.6-luna-20260709) |
| carry-auto-debug-nil-map | 1 | auto | debug-nil-map | tier=high | z-ai/glm-5.3-20260816 | Mistral | opening turn |
| carry-auto-debug-nil-map | 2 | auto | debug-nil-map | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | FRESH (previous model dropped) |
| carry-auto-debug-nil-map | 3 | auto | debug-nil-map | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | cannot tell (fresh choice equals previous model) |
| carry-beta-plan-migration | 1 | auto-beta | plan-migration | tier=high | anthropic/claude-sonnet-5-20260630 | Google | opening turn |
| carry-beta-plan-migration | 2 | auto-beta | plan-migration | no cost setting | deepseek/deepseek-v4-flash-20260731 | Baidu | FRESH (previous model dropped) |
| carry-beta-plan-migration | 3 | auto-beta | plan-migration | no cost setting | deepseek/deepseek-v4-flash-20260731 | Baidu | cannot tell (fresh choice equals previous model) |
| carry-beta-fact-lookup | 1 | auto-beta | fact-lookup | tier=high | z-ai/glm-5.2-20260616 | Decart | opening turn |
| carry-beta-fact-lookup | 2 | auto-beta | fact-lookup | no cost setting | z-ai/glm-5.2-20260616 | Decart | CARRIED (kept previous model; fresh would be deepseek/deepseek-v4-flash-20260731) |
| carry-beta-fact-lookup | 3 | auto-beta | fact-lookup | no cost setting | z-ai/glm-5.2-20260616 | Decart | CARRIED (kept previous model; fresh would be deepseek/deepseek-v4-flash-20260731) |
| carry-auto-plan-migration | 1 | auto | plan-migration | tier=high | openai/gpt-6.1-sol-20260929 | Azure | opening turn |
| carry-auto-plan-migration | 2 | auto | plan-migration | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | FRESH (previous model dropped) |
| carry-auto-plan-migration | 3 | auto | plan-migration | no cost setting | deepseek/deepseek-v4.1-flash-20260910 | Together | cannot tell (fresh choice equals previous model) |
| carry-auto-fact-lookup | 1 | auto | fact-lookup | tier=high | anthropic/claude-sonnet-5.5-20260928 | Azure | opening turn |
| carry-auto-fact-lookup | 2 | auto | fact-lookup | no cost setting | openai/gpt-6-luna-20260922 | OpenAI | FRESH (previous model dropped) |
| carry-auto-fact-lookup | 3 | auto | fact-lookup | no cost setting | openai/gpt-6-luna-20260922 | OpenAI | cannot tell (fresh choice equals previous model) |

### Model selection frequency by router, task type and cost setting

| Task type | Setting | Router | Model chosen | Trials |
|---|---|---|---|---|
| agent:multi\_step\_planning | no cost setting | auto | deepseek/deepseek-v4.1-flash-20260910 | 9 |
| agent:multi\_step\_planning | no cost setting | auto-beta | deepseek/deepseek-v4-flash-20260731 | 9 |
| agent:multi\_step\_planning | tier=high | auto | openai/gpt-6.1-sol-20260929 | 2 |
| agent:multi\_step\_planning | tier=high | auto-beta | anthropic/claude-sonnet-5-20260630 | 2 |
| code:debugging | dial=0 | auto | moonshotai/kimi-k3-20260715 | 3 |
| code:debugging | dial=0 | auto-beta | z-ai/glm-5.2-20260616 | 3 |
| code:debugging | dial=10 | auto | openai/gpt-6-luna-20260922 | 3 |
| code:debugging | dial=10 | auto-beta | deepseek/deepseek-v4-flash-20260423 | 3 |
| code:debugging | dial=3 | auto | deepseek/deepseek-v4.1-flash-20260910 | 3 |
| code:debugging | dial=3 | auto-beta | z-ai/glm-5.2-20260616 | 3 |
| code:debugging | dial=5 | auto | deepseek/deepseek-v4.1-flash-20260910 | 3 |
| code:debugging | dial=5 | auto-beta | z-ai/glm-5.2-20260616 | 3 |
| code:debugging | dial=7 | auto | deepseek/deepseek-v4.1-flash-20260910 | 3 |
| code:debugging | dial=7 | auto-beta | openai/gpt-5.6-luna-20260709 | 3 |
| code:debugging | dial=9 | auto | deepseek/deepseek-v4.1-flash-20260910 | 3 |
| code:debugging | dial=9 | auto-beta | openai/gpt-5.6-luna-20260709 | 3 |
| code:debugging | no cost setting | auto | deepseek/deepseek-v4.1-flash-20260910 | 12 |
| code:debugging | no cost setting | auto-beta | openai/gpt-5.6-luna-20260709 | 12 |
| code:debugging | tier=high | auto | z-ai/glm-5.3-20260816 | 5 |
| code:debugging | tier=high | auto-beta | z-ai/glm-5.2-20260616 | 5 |
| code:debugging | tier=low | auto | deepseek/deepseek-v4.1-flash-20260910 | 6 |
| code:debugging | tier=low | auto-beta | openai/gpt-5.6-luna-20260709 | 6 |
| code:debugging | tier=low dial=0 | auto | deepseek/deepseek-v4.1-flash-20260910 | 3 |
| code:debugging | tier=low dial=0 | auto-beta | openai/gpt-5.6-luna-20260709 | 3 |
| code:debugging | tier=max | auto | anthropic/claude-opus-5.5-20260921 | 6 |
| code:debugging | tier=max | auto-beta | anthropic/claude-opus-5-20260723 | 4 |
| code:debugging | tier=max dial=10 | auto | anthropic/claude-opus-5.5-20260921 | 3 |
| code:debugging | tier=max dial=10 | auto-beta | anthropic/claude-opus-5-20260723 | 2 |
| code:debugging | tier=medium | auto | deepseek/deepseek-v4-pro-20260813 | 3 |
| code:debugging | tier=medium | auto-beta | deepseek/deepseek-v4.1-flash-20260910 | 3 |
| code:debugging | tier=xhigh | auto | moonshotai/kimi-k3-20260715 | 3 |
| code:debugging | tier=xhigh | auto-beta | moonshotai/kimi-k3-20260715 | 3 |
| content\_writing | no cost setting | auto | z-ai/glm-5.3-flash-20260826 | 9 |
| content\_writing | no cost setting | auto-beta | z-ai/glm-5.3-flash-20260826 | 9 |
| math | allow=google/\* | auto | google/gemini-3.8-flash-20260902 | 6 |
| math | allow=google/\* | auto-beta | google/gemini-3.7-flash-20260813 | 6 |
| math | allow=google/\* exclude=\*flash\* | auto | google/gemini-3.1-pro-preview-20260219 | 3 |
| math | allow=google/\* exclude=\*flash\* | auto-beta | google/gemini-3.1-pro-preview-20260219 | 3 |
| math | no cost setting | auto | deepseek/deepseek-v4.1-flash-20260910 | 12 |
| math | no cost setting | auto-beta | deepseek/deepseek-v4-flash-20260731 | 12 |
| math | tier=high | auto | anthropic/claude-opus-5.5-20260921 | 2 |
| math | tier=high | auto-beta | z-ai/glm-5.2-20260616 | 2 |
| math | tier=low | auto | deepseek/deepseek-v4.1-flash-20260910 | 2 |
| math | tier=low | auto-beta | deepseek/deepseek-v4-flash-20260731 | 2 |
| qa\_knowledge | no cost setting | auto | openai/gpt-6-luna-20260922 | 9 |
| qa\_knowledge | no cost setting | auto-beta | deepseek/deepseek-v4-flash-20260731 | 9 |
| qa\_knowledge | tier=high | auto | anthropic/claude-sonnet-5.5-20260928 | 2 |
| qa\_knowledge | tier=high | auto-beta | z-ai/glm-5.2-20260616 | 2 |

### Cost and latency per cell

Successful trials. "Billed" is the backfilled `total_cost` from `/api/v1/generation`, shown where the backfill has it; latency includes any upstream retries OpenRouter made.

| Cell | Trials | Mean cost (inline) | Min | Max | Mean billed (backfill) | Backfilled | Mean native completion tokens | Median latency ms | Max latency ms |
|---|---|---|---|---|---|---|---|---|---|
| baseline-math-sum/auto | 3 | $0.000092 | $0.000092 | $0.000092 | $0.000092 | 3/3 | 64 | 563 | 1052 |
| smoke-low/auto | 1 | $0.000092 | $0.000092 | $0.000092 | $0.000092 | 1/1 | 64 | 1067 | 1067 |
| smoke-low/auto-beta | 1 | $0.000130 | $0.000130 | $0.000130 | $0.000130 | 1/1 | 64 | 1439 | 1439 |
| baseline-math-sum/auto-beta | 3 | $0.000130 | $0.000130 | $0.000130 | $0.000130 | 3/3 | 64 | 1015 | 1156 |
| baseline-debug-nil-map/auto | 3 | $0.000099 | $0.000099 | $0.000099 | $0.000099 | 3/3 | 64 | 513 | 530 |
| baseline-debug-nil-map/auto-beta | 3 | $0.000087 | $0.000087 | $0.000087 | $0.000087 | 3/3 | 64 | 5358 | 6165 |
| baseline-plan-migration/auto | 3 | $0.000095 | $0.000095 | $0.000095 | $0.000095 | 3/3 | 64 | 461 | 488 |
| baseline-plan-migration/auto-beta | 3 | $0.000134 | $0.000134 | $0.000134 | $0.000134 | 3/3 | 64 | 852 | 980 |
| baseline-fact-lookup/auto | 3 | $0.000023 | $0.000000 | $0.000034 | $0.000023 | 3/3 | 42 | 2907 | 4061 |
| baseline-fact-lookup/auto-beta | 3 | $0.000129 | $0.000129 | $0.000129 | $0.000129 | 3/3 | 64 | 878 | 1153 |
| baseline-write-essay/auto | 3 | $0.000037 | $0.000037 | $0.000037 | $0.000037 | 3/3 | 64 | 672 | 785 |
| baseline-write-essay/auto-beta | 3 | $0.000037 | $0.000037 | $0.000037 | $0.000037 | 3/3 | 64 | 666 | 716 |
| tier-low/auto | 3 | $0.000099 | $0.000099 | $0.000099 | $0.000099 | 3/3 | 64 | 505 | 1439 |
| tier-low/auto-beta | 3 | $0.000087 | $0.000087 | $0.000087 | $0.000087 | 3/3 | 64 | 7149 | 8420 |
| tier-medium/auto | 3 | $0.000421 | $0.000421 | $0.000421 | $0.000421 | 3/3 | 64 | 792 | 2699 |
| tier-medium/auto-beta | 3 | $0.000099 | $0.000099 | $0.000099 | $0.000099 | 3/3 | 64 | 467 | 484 |
| tier-high/auto | 3 | $0.000360 | $0.000360 | $0.000360 | $0.000360 | 3/3 | 64 | 746 | 806 |
| tier-high/auto-beta | 3 | $0.000716 | $0.000716 | $0.000716 | $0.000716 | 3/3 | 64 | 485 | 504 |
| tier-xhigh/auto | 3 | $0.001180 | $0.001013 | $0.001349 | $0.001180 | 3/3 | 64 | 1024 | 11105 |
| tier-xhigh/auto-beta | 3 | $0.001013 | $0.001013 | $0.001013 | $0.001013 | 3/3 | 64 | 823 | 942 |
| tier-max/auto | 3 | $0.001556 | $0.001556 | $0.001556 | $0.001556 | 3/3 | 64 | 2553 | 2755 |
| tier-max/auto-beta | 3 | $0.001843 | $0.001660 | $0.001935 | $0.001843 | 3/3 | 60 | 2649 | 2882 |
| dial-0/auto | 3 | $0.001013 | $0.001013 | $0.001013 | $0.001013 | 3/3 | 64 | 1041 | 2340 |
| dial-0/auto-beta | 3 | $0.000716 | $0.000716 | $0.000716 | $0.000716 | 3/3 | 64 | 419 | 432 |
| dial-3/auto | 3 | $0.000099 | $0.000099 | $0.000099 | $0.000099 | 3/3 | 64 | 662 | 735 |
| dial-3/auto-beta | 3 | $0.000716 | $0.000716 | $0.000716 | $0.000716 | 3/3 | 64 | 367 | 410 |
| dial-5/auto | 3 | $0.000099 | $0.000099 | $0.000099 | $0.000099 | 3/3 | 64 | 669 | 742 |
| dial-5/auto-beta | 3 | $0.000716 | $0.000716 | $0.000716 | $0.000716 | 3/3 | 64 | 450 | 465 |
| dial-7/auto | 3 | $0.000099 | $0.000099 | $0.000099 | $0.000099 | 3/3 | 64 | 520 | 630 |
| dial-7/auto-beta | 3 | $0.000087 | $0.000087 | $0.000087 | $0.000087 | 3/3 | 64 | 2934 | 5478 |
| dial-9/auto | 3 | $0.000099 | $0.000099 | $0.000099 | $0.000099 | 3/3 | 64 | 437 | 451 |
| dial-9/auto-beta | 3 | $0.000087 | $0.000087 | $0.000087 | $0.000087 | 3/3 | 64 | 5231 | 5938 |
| dial-10/auto | 3 | $0.000037 | $0.000037 | $0.000037 | $0.000037 | 3/3 | 64 | 3950 | 4433 |
| dial-10/auto-beta | 3 | $0.000016 | $0.000016 | $0.000016 | $0.000016 | 3/3 | 64 | 1297 | 1658 |
| precedence-tier-max-dial-10/auto | 3 | $0.001556 | $0.001556 | $0.001556 | $0.001556 | 3/3 | 64 | 3062 | 3363 |
| precedence-tier-max-dial-10/auto-beta | 3 | $0.001810 | $0.001560 | $0.001935 | $0.001810 | 3/3 | 59 | 2319 | 2876 |
| precedence-tier-low-dial-0/auto | 3 | $0.000099 | $0.000099 | $0.000099 | $0.000099 | 3/3 | 64 | 441 | 466 |
| precedence-tier-low-dial-0/auto-beta | 3 | $0.000087 | $0.000087 | $0.000087 | $0.000087 | 3/3 | 64 | 5862 | 8316 |
| restrict-google/auto | 3 | $0.000242 | $0.000242 | $0.000242 | $0.000242 | 3/3 | 60 | 2625 | 3106 |
| restrict-google/auto-beta | 3 | $0.000242 | $0.000242 | $0.000242 | $0.000242 | 3/3 | 60 | 1897 | 2131 |
| restrict-google-minus-flash/auto | 3 | $0.000766 | $0.000766 | $0.000766 | $0.000766 | 3/3 | 60 | 1945 | 2078 |
| restrict-google-minus-flash/auto-beta | 3 | $0.000770 | $0.000766 | $0.000778 | $0.000770 | 3/3 | 60 | 1965 | 2550 |
| sticky-same-task-auto/auto | 4 | $0.000092 | $0.000092 | $0.000092 | $0.000092 | 4/4 | 64 | 449 | 489 |
| sticky-same-task-beta/auto-beta | 4 | $0.000130 | $0.000130 | $0.000130 | $0.000130 | 4/4 | 64 | 1076 | 1164 |
| sticky-task-switch-auto/auto | 5 | $0.000082 | $0.000037 | $0.000099 | $0.000082 | 5/5 | 64 | 443 | 593 |
| sticky-router-switch-1/auto | 2 | $0.000092 | $0.000092 | $0.000092 | $0.000092 | 2/2 | 64 | 423 | 423 |
| sticky-task-switch-beta/auto-beta | 5 | $0.000103 | $0.000037 | $0.000130 | $0.000103 | 5/5 | 64 | 1021 | 2719 |
| sticky-router-switch-2/auto-beta | 2 | $0.000130 | $0.000130 | $0.000130 | $0.000130 | 2/2 | 64 | 967 | 967 |
| sticky-carry-auto-1/auto | 1 | $0.000217 | $0.000217 | $0.000217 | $0.000217 | 1/1 | 50 | 1071 | 1071 |
| sticky-carry-auto-2/auto | 2 | $0.000099 | $0.000099 | $0.000099 | $0.000099 | 2/2 | 64 | 447 | 447 |
| sticky-carry-beta-1/auto-beta | 1 | $0.000716 | $0.000716 | $0.000716 | $0.000716 | 1/1 | 64 | 458 | 458 |
| sticky-carry-beta-2/auto-beta | 2 | $0.000716 | $0.000716 | $0.000716 | $0.000716 | 2/2 | 64 | 385 | 385 |
| messages-smoke-low/auto | 1 | $0.000092 | $0.000092 | $0.000092 | $0.000092 | 1/1 | 64 | 437 | 437 |
| messages-smoke-low/auto-beta | 1 | $0.000130 | $0.000130 | $0.000130 | $0.000130 | 1/1 | 64 | 906 | 906 |
| messages-baseline-math-sum/auto | 3 | $0.000092 | $0.000092 | $0.000092 | $0.000092 | 3/3 | 64 | 437 | 533 |
| implicit-repeat/auto | 4 | $0.000026 | $0.000000 | $0.000034 | $0.000026 | 4/4 | 48 | 2431 | 3456 |
| messages-baseline-math-sum/auto-beta | 3 | $0.000130 | $0.000130 | $0.000130 | $0.000130 | 3/3 | 64 | 1351 | 1378 |
| messages-baseline-debug-nil-map/auto | 3 | $0.000099 | $0.000099 | $0.000099 | $0.000099 | 3/3 | 64 | 453 | 482 |
| messages-baseline-debug-nil-map/auto-beta | 3 | $0.000087 | $0.000087 | $0.000087 | $0.000087 | 3/3 | 64 | 5452 | 8556 |
| messages-tier-low/auto | 3 | $0.000099 | $0.000099 | $0.000099 | $0.000099 | 3/3 | 64 | 439 | 441 |
| messages-tier-low/auto-beta | 3 | $0.000087 | $0.000087 | $0.000087 | $0.000087 | 3/3 | 64 | 6678 | 6892 |
| messages-tier-max/auto | 3 | $0.001556 | $0.001556 | $0.001556 | $0.001556 | 3/3 | 64 | 2541 | 3443 |
| messages-tier-max/auto-beta | 3 | $0.001835 | $0.001635 | $0.001935 | $0.001835 | 3/3 | 60 | 2587 | 2881 |
| messages-restrict-google/auto | 3 | $0.000244 | $0.000242 | $0.000246 | $0.000244 | 3/3 | 60 | 1851 | 2071 |
| messages-restrict-google/auto-beta | 3 | $0.000242 | $0.000242 | $0.000242 | $0.000242 | 3/3 | 60 | 2542 | 2711 |
| messages-sticky-same-task-auto/auto | 4 | $0.000092 | $0.000092 | $0.000092 | $0.000092 | 4/4 | 64 | 399 | 432 |
| messages-max-price-floor/auto | 1 | $0.000000 | $0.000000 | $0.000000 | $0.000000 | 1/1 | 64 | 5055 | 5055 |
| nocap-math-sum/auto | 3 | $0.000092 | $0.000092 | $0.000092 | $0.000092 | 3/3 | 64 | 573 | 623 |
| nocap-math-sum/auto-beta | 3 | $0.000130 | $0.000130 | $0.000130 | $0.000130 | 3/3 | 64 | 1096 | 1172 |
| cap-math-sum/auto | 3 | $0.000092 | $0.000092 | $0.000092 | $0.000092 | 3/3 | 64 | 413 | 481 |
| cap-math-sum/auto-beta | 3 | $0.000130 | $0.000130 | $0.000130 | $0.000130 | 3/3 | 64 | 1188 | 1207 |
| nocap-debug-nil-map/auto | 3 | $0.000099 | $0.000099 | $0.000099 | $0.000099 | 3/3 | 64 | 438 | 522 |
| nocap-debug-nil-map/auto-beta | 3 | $0.000087 | $0.000087 | $0.000087 | $0.000087 | 3/3 | 64 | 4908 | 5242 |
| cap-debug-nil-map/auto | 3 | $0.000099 | $0.000099 | $0.000099 | $0.000099 | 3/3 | 64 | 438 | 572 |
| cap-debug-nil-map/auto-beta | 3 | $0.000087 | $0.000087 | $0.000087 | $0.000087 | 3/3 | 64 | 6557 | 9025 |
| nocap-plan-migration/auto | 3 | $0.000095 | $0.000095 | $0.000095 | $0.000095 | 3/3 | 64 | 334 | 398 |
| nocap-plan-migration/auto-beta | 3 | $0.000134 | $0.000134 | $0.000134 | $0.000134 | 3/3 | 64 | 956 | 1000 |
| cap-plan-migration/auto | 3 | $0.000095 | $0.000095 | $0.000095 | $0.000095 | 3/3 | 64 | 348 | 375 |
| cap-plan-migration/auto-beta | 3 | $0.000134 | $0.000134 | $0.000134 | $0.000134 | 3/3 | 64 | 873 | 1013 |
| nocap-fact-lookup/auto | 3 | $0.000012 | $0.000000 | $0.000034 | $0.000012 | 3/3 | 21 | 1630 | 3170 |
| nocap-fact-lookup/auto-beta | 3 | $0.000129 | $0.000129 | $0.000129 | $0.000129 | 3/3 | 64 | 1001 | 1006 |
| cap-fact-lookup/auto | 3 | $0.000034 | $0.000034 | $0.000034 | $0.000034 | 3/3 | 64 | 3096 | 3241 |
| cap-fact-lookup/auto-beta | 3 | $0.000129 | $0.000129 | $0.000129 | $0.000129 | 3/3 | 64 | 1027 | 1202 |
| nocap-write-essay/auto | 3 | $0.000043 | $0.000037 | $0.000046 | $0.000043 | 3/3 | 64 | 835 | 845 |
| nocap-write-essay/auto-beta | 3 | $0.000040 | $0.000037 | $0.000046 | $0.000040 | 3/3 | 64 | 739 | 888 |
| cap-write-essay/auto | 3 | $0.000043 | $0.000037 | $0.000046 | $0.000043 | 3/3 | 64 | 1137 | 1158 |
| cap-write-essay/auto-beta | 3 | $0.000040 | $0.000037 | $0.000046 | $0.000040 | 3/3 | 64 | 606 | 1196 |
| high-math-sum/auto-beta | 2 | $0.000656 | $0.000656 | $0.000656 | $0.000656 | 2/2 | 64 | 2302 | 2302 |
| high-math-sum/auto | 2 | $0.001412 | $0.001412 | $0.001412 | $0.001412 | 2/2 | 64 | 1645 | 1645 |
| carry-beta-math-sum-1/auto-beta | 1 | $0.000656 | $0.000656 | $0.000656 | $0.000656 | 1/1 | 64 | 282 | 282 |
| carry-beta-math-sum-2/auto-beta | 2 | $0.000656 | $0.000656 | $0.000656 | $0.000656 | 2/2 | 64 | 274 | 274 |
| carry-auto-math-sum-1/auto | 1 | $0.001412 | $0.001412 | $0.001412 | $0.001412 | 1/1 | 64 | 1428 | 1428 |
| high-debug-nil-map/auto | 2 | $0.000451 | $0.000360 | $0.000542 | $0.000451 | 2/2 | 64 | 655 | 655 |
| carry-auto-math-sum-2/auto | 2 | $0.000092 | $0.000092 | $0.000092 | $0.000092 | 2/2 | 64 | 374 | 374 |
| high-debug-nil-map/auto-beta | 2 | $0.000716 | $0.000716 | $0.000716 | $0.000716 | 2/2 | 64 | 310 | 310 |
| carry-beta-debug-nil-map-1/auto-beta | 1 | $0.000716 | $0.000716 | $0.000716 | $0.000716 | 1/1 | 64 | 289 | 289 |
| carry-auto-debug-nil-map-1/auto | 1 | $0.000360 | $0.000360 | $0.000360 | $0.000360 | 1/1 | 64 | 589 | 589 |
| carry-beta-debug-nil-map-2/auto-beta | 2 | $0.000716 | $0.000716 | $0.000716 | $0.000716 | 2/2 | 64 | 286 | 286 |
| carry-auto-debug-nil-map-2/auto | 2 | $0.000099 | $0.000099 | $0.000099 | $0.000099 | 2/2 | 64 | 360 | 360 |
| high-plan-migration/auto-beta | 2 | $0.000752 | $0.000752 | $0.000752 | $0.000752 | 2/2 | 64 | 2627 | 2627 |
| high-plan-migration/auto | 2 | $0.000712 | $0.000712 | $0.000712 | $0.000712 | 2/2 | 64 | 8247 | 8247 |
| carry-beta-plan-migration-1/auto-beta | 1 | $0.000752 | $0.000752 | $0.000752 | $0.000752 | 1/1 | 64 | 2478 | 2478 |
| high-fact-lookup/auto | 2 | $0.000706 | $0.000706 | $0.000706 | $0.000706 | 2/2 | 64 | 1235 | 1235 |
| carry-beta-plan-migration-2/auto-beta | 2 | $0.000134 | $0.000134 | $0.000134 | $0.000134 | 2/2 | 64 | 883 | 883 |
| high-fact-lookup/auto-beta | 2 | $0.000654 | $0.000654 | $0.000654 | $0.000654 | 2/2 | 64 | 292 | 292 |
| carry-beta-fact-lookup-1/auto-beta | 1 | $0.000654 | $0.000654 | $0.000654 | $0.000654 | 1/1 | 64 | 283 | 283 |
| carry-beta-fact-lookup-2/auto-beta | 2 | $0.000654 | $0.000654 | $0.000654 | $0.000654 | 2/2 | 64 | 1865 | 1865 |
| carry-auto-plan-migration-1/auto | 1 | $0.000712 | $0.000712 | $0.000712 | $0.000712 | 1/1 | 64 | 7466 | 7466 |
| carry-auto-fact-lookup-1/auto | 1 | $0.000706 | $0.000706 | $0.000706 | $0.000706 | 1/1 | 64 | 1172 | 1172 |
| carry-auto-plan-migration-2/auto | 2 | $0.000095 | $0.000095 | $0.000095 | $0.000095 | 2/2 | 64 | 425 | 425 |
| carry-auto-fact-lookup-2/auto | 2 | $0.000034 | $0.000034 | $0.000034 | $0.000034 | 2/2 | 64 | 3295 | 3295 |

### Per-trial assertions

| Check | Pass | Fail | Flag | Skip |
|---|---|---|---|---|
| plugin\_id\_matches\_router | 146 | 0 | 0 | 152 |
| expected\_status | 296 | 2 | 0 | 0 |
| allowed\_models | 18 | 0 | 0 | 280 |
| excluded\_models | 6 | 0 | 0 | 292 |
| classified | 291 | 0 | 0 | 7 |
| served\_by\_chosen\_model | 287 | 0 | 4 | 7 |

### Trials that failed an assertion or were set aside

A failed or flagged trial is excluded from every comparison above.

| Run | Cell | Rep | Check | Status | Detail |
|---|---|---|---|---|---|
| 7e624b1726f01f6d | tier-max/auto-beta | 0.0 | served\_by\_chosen\_model | flag | router chose anthropic/claude-opus-5-20260723, anthropic/claude-opus-4.8 answered; use resolved\_to |
| 7e624b1726f01f6d | precedence-tier-max-dial-10/auto-beta | 0.0 | served\_by\_chosen\_model | flag | router chose anthropic/claude-opus-5-20260723, anthropic/claude-opus-4.8 answered; use resolved\_to |
| 7e624b1726f01f6d | max-price-floor/auto | 0.0 | expected\_status | fail | status 502, expected 404 |
| 7e624b1726f01f6d | messages-tier-max/auto-beta | 2.0 | served\_by\_chosen\_model | flag | router chose anthropic/claude-opus-5-20260723, anthropic/claude-opus-4.8 answered; use resolved\_to |
| 7e624b1726f01f6d | messages-max-price-floor/auto | 0.0 | expected\_status | fail | status 200, expected 404 |
| 7e624b1726f01f6d | messages-max-price-floor/auto | 0.0 | served\_by\_chosen\_model | flag | router chose google/lyria-3-clip-preview-20260330, inclusionai/ling-3.1-flash answered; use resolved\_to |
