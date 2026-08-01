# OpenRouter Scripts

Learning scripts for the [OpenRouter](https://openrouter.ai) Python SDK — a set of
annotated examples covering chat completions, streaming, multi-turn conversations,
parameter tuning, model comparison, and async requests.

## Requirements

- Python 3.8+
- An OpenRouter API key ([openrouter.ai/keys](https://openrouter.ai/keys))

## Setup

```bash
python -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
```

Provide your API key via the environment — it is never read from a file or
hardcoded:

```bash
export OPENROUTER_API_KEY='sk-or-...'
```

Prefer a `.env` file for local work (it is gitignored):

```bash
echo "OPENROUTER_API_KEY=sk-or-..." > .env
```

...and load it with [`python-dotenv`](https://pypi.org/project/python-dotenv/),
which is listed as an optional dev dependency in `requirements.txt`.

Every script stops with a warning if `OPENROUTER_API_KEY` is unset;
`openrouter_sdk_examples.py` additionally warns when the key doesn't look like
an OpenRouter key.

### Browsing the model catalog

`model_calls.py` pages through OpenRouter's 400+ models across six
modalities and can call one of each. Model discovery and categorization live in
`models_catalog.py`, and the shared guards in `common.py`; `model_calls.py`
imports both:

| Modality | Purpose | Output |
| --- | --- | --- |
| **text** | Chat, Q&A, reasoning, coding | Printed completion |
| **image** | Image generation | File written to the working directory |
| **speech** | Text-to-speech | MP3 written to the working directory |
| **audio** | Chat with audio replies | List-only — see below |
| **video** | Video generation | MP4 written to the working directory |
| **embedding** | Vectorisation for search / RAG | Printed vectors + similarity scores |

With no arguments it opens an interactive menu; otherwise it takes a subcommand.
Each modality has a `list-<modality>` form (plus `list-all` for everything
grouped), and all but `audio` have a `call-<modality>`:

```bash
python model_calls.py list-all                    # every modality, grouped
python model_calls.py list-image                  # one modality
python model_calls.py call-text openai/gpt-4o-mini "Say hello"
python model_calls.py call-image openai/gpt-image-1 "A sunset over mountains"
python model_calls.py call-speech hexgrad/kokoro-82m "Welcome to OpenRouter."
python model_calls.py call-video google/veo-3.1 "A cat playing with a ball"
python model_calls.py call-embedding openai/text-embedding-3-small
```

Pass the exact `provider/model` ID from the listing — a display name like
`GPT-4` will not match. Listing is read-only and cheap; the `call-*` subcommands
bill your account.

`call-speech` takes an optional fourth argument, the voice. Voice names are
model-specific — `af_bella` on Kokoro, `en-US-Harper:MAI-Voice-2` on MAI-Voice —
and a name the model doesn't publish is rejected, so when it's omitted the
script looks the model up and uses the first voice it advertises:

```bash
python model_calls.py call-speech hexgrad/kokoro-82m "Is this thing on?" af_bella
```

`call-embedding` takes any number of texts and embeds them in a single request.
A vector on its own says nothing, so with two or more texts the script also
prints the cosine similarity of every pair, ranked — which is the property that
makes embeddings useful for semantic search and RAG. Called with no texts it
uses a built-in set of two paraphrases and one unrelated sentence:

```bash
$ python model_calls.py call-embedding openai/text-embedding-3-small
...
🔗 Pairwise cosine similarity (1.00 = same meaning, 0.00 = unrelated):
   0.51  "AI is awesome"  ↔  "Machine learning is great"
   0.11  "Machine learning is great"  ↔  "I need to buy milk on the way…"
   0.08  "AI is awesome"  ↔  "I need to buy milk on the way home"
```

Scores are model-specific — an absolute 0.51 means little on its own, what
matters is that related texts score well above unrelated ones. Pass your own
texts as separate arguments to compare them; a single text just prints its
vector:

```bash
python model_calls.py call-embedding openai/text-embedding-3-small "a dog" "a puppy" "a spreadsheet"
```

`call-video` is asynchronous under the hood: the API answers with a job handle,
and the script then polls it every 10s (up to 15 minutes) until the status turns
terminal, downloading the finished clip(s) to `generated_video.mp4`. Ctrl-C only
stops the waiting — the job carries on server-side, and `poll-video` picks it
back up by job ID. `--no-wait` skips the polling entirely and just prints the ID:

```bash
python model_calls.py call-video google/veo-3.1 "A cat playing with a ball" --no-wait
python model_calls.py poll-video <job_id>
```

**speech vs. audio:** `speech` is the text-to-speech modality, served by the
`/audio/speech` endpoint that `call-speech` uses. OpenRouter also has a separate
`audio` modality — chat models such as `openai/gpt-audio` that return audio
inline with their text reply through the chat endpoint. Those are not speech
models and won't work with `call-speech`, so `audio` is list-only here:

```bash
python model_calls.py list-audio                  # no call-audio counterpart
```

## Additional Examples

Running `openrouter_sdk_examples.py` with no arguments runs all seven in
sequence — real API calls against several models, so it consumes credit. Name a
single example to run just that one:

```bash
python openrouter_sdk_examples.py                 # all seven
python openrouter_sdk_examples.py basic-chat
python openrouter_sdk_examples.py system-prompt
python openrouter_sdk_examples.py multi-turn
python openrouter_sdk_examples.py parameters
python openrouter_sdk_examples.py streaming
python openrouter_sdk_examples.py compare-models
python openrouter_sdk_examples.py async
```

| Command | Function | What it shows |
| --- | --- | --- |
| `basic-chat` | `example_basic_chat` | Minimal chat completion + token usage |
| `system-prompt` | `example_with_system_prompt` | Steering behaviour with a system message |
| `multi-turn` | `example_multi_turn_conversation` | Carrying context across turns |
| `parameters` | `example_with_parameters` | `temperature`, `top_p`, `max_tokens` |
| `streaming` | `example_streaming` | Consuming a token stream with an output cap |
| `compare-models` | `example_different_models` | Same prompt across several models |
| `async` | `example_async` | Concurrent requests via `asyncio.gather` |

`compare-models` and `async` each issue several requests; the rest are one call
apiece. An unknown name prints the list above rather than running anything.

## reasoning-explorer/

A separate Go sub-project (stdlib only, no SDKs) that dissects reasoning models
through the raw OpenRouter HTTP API: the unified `reasoning` request parameter,
the `reasoning`/`reasoning_details` response fields, token accounting
(reasoning bills as output), hand-parsed SSE streaming, a cross-provider
comparison (DeepSeek/Anthropic/OpenAI/Gemini/Qwen), and multi-turn tool calling
with preserved thinking blocks. Every request and raw response is logged to
`reasoning-explorer/logs/`. See its own README:

```bash
cd reasoning-explorer && go run . phase1   # phases 1–6
```

## Configuration

Shared constants live in `common.py` and apply to every script:

| Constant | Default | Purpose |
| --- | --- | --- |
| `REQUEST_TIMEOUT_MS` | `30_000` | Per-request timeout, in milliseconds |
| `MAX_TEXT_LENGTH` | `10_000` | Longest text accepted in one request field |

`openrouter_sdk_examples.py`:

| Constant | Default | Purpose |
| --- | --- | --- |
| `RETRY_CONFIG` | 30s budget | Exponential backoff on transient failures |
| `MAX_CONCURRENT_REQUESTS` | `5` | Ceiling on in-flight async requests |
| `DEFAULT_MODEL` | `openai/gpt-4o-mini` | Model used by the single-model examples |
| `COMPARISON_MODELS` | 3 models | Models compared in `compare-models` |

`model_calls.py`:

| Constant | Default | Purpose |
| --- | --- | --- |
| `SPEECH_TIMEOUT_MS` | `120_000` | Longer budget for streamed speech synthesis |
| `VIDEO_POLL_INTERVAL_S` | `10` | Seconds between video job status checks |
| `VIDEO_POLL_TIMEOUT_S` | `900` | How long to wait before giving up on a job |
| `VIDEO_DOWNLOAD_TIMEOUT_MS` | `300_000` | Budget for downloading a finished clip |
| `MAX_VIDEO_BYTES` | 512 MB | Size at which a download is aborted |
| `MAX_EMBEDDING_TEXTS` | `100` | Texts accepted in one embedding request |

`models_catalog.py` holds the paging limits (`DEFAULT_PAGE_SIZE`,
`MAX_PAGE_SIZE`, `MAX_PAGES`).

## Security notes

Key handling, redaction, and validation live in `common.py` — one copy, shared
by all three scripts:

- API keys come from the environment only. `openrouter_sdk_examples.py`
  additionally warns on a key that doesn't look like one at startup.
- `safe_print_error` redacts anything resembling a key or bearer token from
  error output and truncates it to 200 characters.
- Model IDs and video job IDs are matched against strict patterns before use —
  they reach the request URL unescaped, so a `../` in one would retarget the
  call. Text fields are length- and type-checked before being sent.

Each script then bounds its own operations: streaming output and concurrency in
`openrouter_sdk_examples.py`, pagination in `models_catalog.py`, and polling,
download size, and batch size in `model_calls.py` — which also picks generated
file extensions from a whitelist rather than from the API's response.

### Known limitations

Honest gaps, not oversights:

- **Output paths are not sandboxed.** `call-image`/`call-video` write wherever
  you point them and overwrite silently. You're the operator; that's your call.
- **No retry/backoff in `model_calls.py`.** Failures surface immediately rather
  than being retried. `openrouter_sdk_examples.py` shows the backoff pattern.
- **Cost is not capped.** Every `call-*` subcommand bills your account, and
  nothing here enforces a spending ceiling.

## Resources

- [OWASP Top 10](https://owasp.org/www-project-top-ten/)
- [OWASP API Security Top 10](https://owasp.org/www-project-api-security/)
- [OpenRouter Documentation](https://openrouter.ai/docs)