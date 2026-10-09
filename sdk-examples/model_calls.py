"""
OpenRouter Multimodal Models - Call Each Modality

This module calls a model of each modality (text, image, speech, video,
embeddings) through the matching SDK endpoint, plus the interactive menu and
command-line entry point that tie the listing and calling together.

The "audio" modality can be listed but not called from here.

Model discovery/categorization lives in models_catalog.py, from which the
shared helpers and listing functions are imported.
"""

import base64
import math
import os
import sys
import time

from openrouter import OpenRouter
from openrouter.errors import OpenRouterError

from common import (
    MAX_TEXT_LENGTH,
    REQUEST_TIMEOUT_MS,
    get_api_key,
    safe_print_error,
    validate_job_id,
    validate_model_name,
    validate_text,
)
from models_catalog import (
    get_supported_voices,
    list_all_models_modality,
    list_models_by_modality,
)

# Speech synthesis streams the audio back and can take well over the 30s the
# other endpoints get, so it runs on its own, longer timeout.
SPEECH_TIMEOUT_MS = 120_000

# Video jobs run for minutes, so polling gets its own cadence and ceiling, and
# downloading the finished file gets a longer timeout than a normal request.
VIDEO_POLL_INTERVAL_S = 10
VIDEO_POLL_TIMEOUT_S = 900
VIDEO_DOWNLOAD_TIMEOUT_MS = 300_000

# Statuses the job never moves out of - anything else means "keep polling".
VIDEO_TERMINAL_STATUSES = ("completed", "failed", "cancelled", "expired")

# Two ways of saying the same thing plus one unrelated sentence: enough for the
# similarity scores to speak for themselves without any arguments.
DEFAULT_EMBEDDING_TEXTS = [
    "AI is awesome",
    "Machine learning is great",
    "I need to buy milk on the way home",
]

# One request embeds every text given, so cap how many to keep an accidental
# shell glob from turning into a large bill.
MAX_EMBEDDING_TEXTS = 100

# Generated media is written straight to disk, so both the chunk size we read
# and the ceiling we refuse to grow past are ours to choose, not the server's.
DOWNLOAD_CHUNK_BYTES = 1024 * 1024
MAX_VIDEO_BYTES = 512 * 1024 * 1024

# Media types the image endpoint is expected to return. The extension of the
# file we write is derived from the response, so it comes from a fixed set
# rather than from whatever string the API sends.
SAFE_IMAGE_EXTENSIONS = ("png", "jpeg", "jpg", "webp", "gif")


def _check_model(model_id: str) -> bool:
    """
    Reject a malformed model ID before it is sent anywhere
    """
    if validate_model_name(model_id):
        return True

    print(f"❌ Invalid model ID: {model_id!r}")
    print("   Expected provider/model, e.g. openai/gpt-4o-mini")
    return False


def _check_text(content: str, label: str = "text") -> bool:
    """
    Reject an empty or oversized text field before it is sent anywhere
    """
    if validate_text(content):
        return True

    print(f"❌ Invalid {label}: must be 1-{MAX_TEXT_LENGTH:,} characters")
    return False


def call_text_model(model_id: str, prompt: str = "Say hello in one sentence"):
    """
    Call a text/chat model
    """
    print("\n" + "="*80)
    print(f"CALLING TEXT MODEL: {model_id}")
    print("="*80)

    if not _check_model(model_id) or not _check_text(prompt, "prompt"):
        return

    api_key = get_api_key()
    if not api_key:
        return

    try:
        with OpenRouter(api_key=api_key) as client:
            print(f"\nPrompt: {prompt}")
            print(f"Model: {model_id}\n")

            response = client.chat.send(
                model=model_id,
                messages=[{"role": "user", "content": prompt}],
                max_tokens=200,
                timeout_ms=REQUEST_TIMEOUT_MS,
            )

            if not response.choices or not response.choices[0].message:
                print("❌ No response from model")
                return

            print("✅ Response:")
            print(f"   {response.choices[0].message.content}")

            if response.usage:
                print("\nUsage:")
                print(f"   Input tokens: {response.usage.prompt_tokens}")
                print(f"   Output tokens: {response.usage.completion_tokens}")

    except OpenRouterError as e:
        safe_print_error("Request Error", e)
    except Exception as e:
        safe_print_error("Error", e)


def call_image_model(model_id: str,
                     prompt: str = "A sunset over mountains",
                     output_path: str = "generated_image"):
    """
    Call an image generation model and save the result
    """
    print("\n" + "="*80)
    print(f"CALLING IMAGE MODEL: {model_id}")
    print("="*80)

    if not _check_model(model_id) or not _check_text(prompt, "prompt"):
        return

    api_key = get_api_key()
    if not api_key:
        return

    try:
        with OpenRouter(api_key=api_key) as client:
            print(f"\nPrompt: {prompt}")
            print(f"Model: {model_id}\n")

            response = client.images.generate(
                model=model_id,
                prompt=prompt,
                timeout_ms=REQUEST_TIMEOUT_MS,
            )

            if not response.data:
                print("❌ No image returned")
                return

            print(f"✅ Generated {len(response.data)} image(s):")

            for i, image in enumerate(response.data, 1):
                # media_type looks like "image/png", but it comes from the API,
                # so only a known subtype is allowed to shape the filename -
                # anything else (or anything with a path separator in it) would
                # otherwise steer the write somewhere unintended.
                subtype = (image.media_type or "").split("/")[-1].lower()
                extension = subtype if subtype in SAFE_IMAGE_EXTENSIONS else "png"
                path = f"{output_path}_{i}.{extension}"

                with open(path, "wb") as f:
                    f.write(base64.b64decode(image.b64_json))

                print(f"   {i}. Saved to {path}")

    except OpenRouterError as e:
        safe_print_error("Request Error", e)
    except Exception as e:
        safe_print_error("Error", e)


def _download_video(client, job_id: str, count: int, output_path: str):
    """
    Download the finished clips of a completed job

    A job can produce more than one clip, addressed by index; each is streamed
    back as MP4, so read() pulls the whole body before writing it out.
    """
    for i in range(count):
        # One clip keeps the plain name, several get numbered.
        path = f"{output_path}.mp4" if count == 1 else f"{output_path}_{i + 1}.mp4"

        response = client.video_generation.get_video_content(
            job_id=job_id,
            index=i,
            timeout_ms=VIDEO_DOWNLOAD_TIMEOUT_MS,
        )

        # The body is streamed, so write it out chunk by chunk: a video never
        # has to fit in memory, and the transfer is stopped at a size we chose
        # rather than run until the disk fills.
        written = 0
        try:
            with open(path, "wb") as f:
                for chunk in response.iter_bytes(chunk_size=DOWNLOAD_CHUNK_BYTES):
                    written += len(chunk)
                    if written > MAX_VIDEO_BYTES:
                        raise ValueError(f"clip exceeds the {MAX_VIDEO_BYTES:,} byte limit")
                    f.write(chunk)
        except ValueError as e:
            # Don't leave a truncated file that looks like a real download.
            os.remove(path)
            print(f"   {i + 1}. ❌ Aborted: {e}")
            continue
        finally:
            response.close()

        print(f"   {i + 1}. Saved {written:,} bytes to {path}")


def _poll_video_job(client,
                    job_id: str,
                    output_path: str,
                    poll_interval: int = VIDEO_POLL_INTERVAL_S,
                    poll_timeout: int = VIDEO_POLL_TIMEOUT_S):
    """
    Poll a video job to completion and download the result

    Returns the last status seen, or None if the job outlived poll_timeout.
    """
    print(f"\n⏳ Polling every {poll_interval}s (giving up after {poll_timeout}s)")
    print("   Ctrl-C stops waiting - the job keeps running server-side\n")

    started = time.monotonic()
    last_status = None

    while True:
        response = client.video_generation.get_generation(
            job_id=job_id,
            timeout_ms=REQUEST_TIMEOUT_MS,
        )
        elapsed = int(time.monotonic() - started)

        # Only announce transitions, so a long job doesn't scroll the terminal.
        if response.status != last_status:
            print(f"   [{elapsed:>4}s] {response.status}")
            last_status = response.status

        if response.status in VIDEO_TERMINAL_STATUSES:
            break

        if elapsed + poll_interval > poll_timeout:
            print(f"\n⏱️  Still {response.status} after {elapsed}s - giving up on waiting.")
            print(f"   The job is unaffected; resume with:")
            print(f"   python model_calls.py poll-video {job_id}")
            return None

        time.sleep(poll_interval)

    if response.status != "completed":
        print(f"\n❌ Job {response.status}")
        if response.error:
            print(f"   {response.error}")
        return response.status

    # unsigned_urls is one entry per generated clip; a completed job that
    # doesn't list any still has a clip at index 0 to download.
    count = len(response.unsigned_urls or []) or 1

    print(f"\n✅ Completed in {int(time.monotonic() - started)}s - downloading {count} clip(s):")
    _download_video(client, job_id, count, output_path)

    if response.usage and response.usage.cost is not None:
        print(f"\nUsage:")
        print(f"   Cost: ${response.usage.cost}")

    return response.status


def call_video_model(model_id: str,
                     prompt: str = "A cat playing with a ball",
                     output_path: str = "generated_video",
                     wait: bool = True):
    """
    Start a video generation job and wait for the video

    Video generation is asynchronous: the API returns a job handle, which is
    polled with get_generation() until the status turns terminal, and the
    finished clips are then downloaded with get_video_content().

    Args:
        model_id: full "provider/model" ID of a video model
        prompt: what the video should show
        output_path: path stem for the MP4(s); ".mp4" is appended
        wait: poll to completion and download; when False, just print the
            job handle so it can be picked up later with poll_video_job()
    """
    print("\n" + "="*80)
    print(f"CALLING VIDEO MODEL: {model_id}")
    print("="*80)

    if not _check_model(model_id) or not _check_text(prompt, "prompt"):
        return

    api_key = get_api_key()
    if not api_key:
        return

    # Bound before the request so the Ctrl-C handler can tell whether there is
    # a job to resume.
    job_id = None

    try:
        with OpenRouter(api_key=api_key) as client:
            print(f"\nPrompt: {prompt}")
            print(f"Model: {model_id}\n")

            response = client.video_generation.generate(
                model=model_id,
                prompt=prompt,
                timeout_ms=REQUEST_TIMEOUT_MS,
            )

            job_id = response.id

            print("✅ Video generation job submitted:")
            print(f"   Job ID: {job_id}")
            print(f"   Status: {response.status}")

            if response.polling_url:
                print(f"   Polling URL: {response.polling_url}")

            if response.error:
                print(f"   ⚠️  Error: {response.error}")
                return

            if not wait:
                print(f"\n📌 Poll for the result with:")
                print(f"   python model_calls.py poll-video {job_id}")
                return

            _poll_video_job(client, job_id, output_path)

    except KeyboardInterrupt:
        if job_id:
            print(f"\n\n⏹️  Stopped waiting. The job keeps running - resume with:")
            print(f"   python model_calls.py poll-video {job_id}")
        else:
            print("\n\n⏹️  Cancelled.")
    except OpenRouterError as e:
        safe_print_error("Request Error", e)
    except Exception as e:
        safe_print_error("Error", e)


def poll_video_job(job_id: str, output_path: str = "generated_video"):
    """
    Resume polling a video job that was started earlier

    Args:
        job_id: the ID printed when the job was submitted
        output_path: path stem for the MP4(s); ".mp4" is appended
    """
    print("\n" + "="*80)
    print(f"POLLING VIDEO JOB: {job_id}")
    print("="*80)

    # The job ID lands in the request path unescaped, so check it here - this
    # is the one value a user types in by hand rather than copies from a
    # response object.
    if not validate_job_id(job_id):
        print(f"❌ Invalid job ID: {job_id!r}")
        print("   Expected the ID printed when the job was submitted")
        return

    api_key = get_api_key()
    if not api_key:
        return

    try:
        with OpenRouter(api_key=api_key) as client:
            _poll_video_job(client, job_id, output_path)

    except KeyboardInterrupt:
        print(f"\n\n⏹️  Stopped waiting. The job keeps running - resume with:")
        print(f"   python model_calls.py poll-video {job_id}")
    except OpenRouterError as e:
        safe_print_error("Request Error", e)
    except Exception as e:
        safe_print_error("Error", e)


def call_speech_model(model_id: str,
                      text: str = "Hello, this is a test.",
                      voice: str = None,
                      output_path: str = "speech.mp3"):
    """
    Call a speech model (text-to-speech) and save the audio

    Args:
        model_id: full "provider/model" ID of a speech model
        text: the text to speak
        voice: a voice the model supports; when omitted, its first one is used
        output_path: where to write the MP3
    """
    print("\n" + "="*80)
    print(f"CALLING SPEECH MODEL: {model_id}")
    print("="*80)

    if not _check_model(model_id) or not _check_text(text, "text"):
        return

    api_key = get_api_key()
    if not api_key:
        return

    # Every speech model publishes its own voice names and rejects the rest, so
    # there is no safe hardcoded default - ask the catalog for a real one.
    if not voice:
        voices = get_supported_voices(model_id)
        if not voices:
            print(f"❌ Could not determine a voice for {model_id}")
            print("   Pass one explicitly, or check the model page for its voices")
            return
        voice = voices[0]

    try:
        with OpenRouter(api_key=api_key) as client:
            print(f"\nText: {text}")
            print(f"Model: {model_id}")
            print(f"Voice: {voice}\n")

            # create_speech returns the raw httpx response, streamed - read()
            # pulls the whole body before the connection is released.
            # response_format defaults to raw PCM, so ask for MP3 to match the
            # file we write.
            response = client.tts.create_speech(
                model=model_id,
                input=text,
                voice=voice,
                response_format="mp3",
                timeout_ms=SPEECH_TIMEOUT_MS,
            )
            audio = response.read()

            with open(output_path, "wb") as f:
                f.write(audio)

            print(f"✅ Saved {len(audio)} bytes to {output_path}")

    except OpenRouterError as e:
        safe_print_error("Request Error", e)
    except Exception as e:
        safe_print_error("Error", e)


def _cosine_similarity(a, b) -> float:
    """
    Cosine of the angle between two vectors

    1.0 means they point the same way (near-identical meaning), 0.0 means they
    are unrelated. Embeddings often arrive normalized, in which case this is
    just the dot product, but dividing by the magnitudes costs nothing and
    works for the models that don't normalize.
    """
    dot = sum(x * y for x, y in zip(a, b))
    magnitude_a = math.sqrt(sum(x * x for x in a))
    magnitude_b = math.sqrt(sum(y * y for y in b))

    if not magnitude_a or not magnitude_b:
        return 0.0

    return dot / (magnitude_a * magnitude_b)


def _truncate(text: str, width: int = 34) -> str:
    """
    Shorten a text so pairs line up in the similarity table
    """
    return text if len(text) <= width else text[:width - 1] + "…"


def print_similarity_report(texts, vectors):
    """
    Print every pair of texts ranked by how close their embeddings are

    This is the part that shows what an embedding actually is: the numbers in
    one vector mean nothing on their own, but the distance between two of them
    tracks how similar the texts are in meaning - which is the property search
    and RAG are built on.
    """
    pairs = []
    for i in range(len(texts)):
        for j in range(i + 1, len(texts)):
            pairs.append((_cosine_similarity(vectors[i], vectors[j]), texts[i], texts[j]))

    # Most similar first, so the pair the model considers closest leads - the
    # same ranking a semantic search would produce.
    pairs.sort(key=lambda pair: pair[0], reverse=True)

    print("\n🔗 Pairwise cosine similarity (1.00 = same meaning, 0.00 = unrelated):")
    for score, left, right in pairs:
        print(f"   {score:.2f}  \"{_truncate(left)}\"  ↔  \"{_truncate(right)}\"")


def call_embedding_model(model_id: str, texts=None):
    """
    Call an embedding model to get vector representations

    Args:
        model_id: full "provider/model" ID of an embedding model
        texts: a string, or a list of strings to compare against each other.
            Two or more texts also get a similarity report; the default set is
            two paraphrases and one unrelated sentence, which makes the scores
            interpretable at a glance.
    """
    print("\n" + "="*80)
    print(f"CALLING EMBEDDING MODEL: {model_id}")
    print("="*80)

    if texts is None:
        texts = DEFAULT_EMBEDDING_TEXTS
    elif isinstance(texts, str):
        texts = [texts]

    if not texts:
        print("❌ Nothing to embed")
        return

    if not _check_model(model_id):
        return

    if len(texts) > MAX_EMBEDDING_TEXTS:
        print(f"❌ Too many texts: {len(texts)} (limit {MAX_EMBEDDING_TEXTS})")
        return

    for i, text in enumerate(texts, 1):
        if not _check_text(text, f"text {i}"):
            return

    api_key = get_api_key()
    if not api_key:
        return

    try:
        with OpenRouter(api_key=api_key) as client:
            print(f"\nModel: {model_id}")
            print(f"Texts ({len(texts)}):")
            for i, text in enumerate(texts, 1):
                print(f"   {i}. {text}")
            print()

            # The endpoint takes a list, so every text is embedded in one
            # request - one round trip, and the vectors are directly comparable.
            response = client.embeddings.generate(
                model=model_id,
                input=texts,
                timeout_ms=REQUEST_TIMEOUT_MS,
            )

            if not response.data:
                print("❌ No embedding returned")
                return

            # index maps each vector back to its input; don't assume the API
            # returns them in order.
            vectors = [None] * len(texts)
            for item in response.data:
                if 0 <= item.index < len(vectors):
                    vectors[item.index] = item.embedding

            for i, vector in enumerate(vectors):
                if vector is None:
                    print(f"⚠️  No embedding returned for text {i + 1}")
                    continue
                preview = ", ".join(f"{v:.4f}" for v in vector[:5])
                print(f"✅ Embedding {i}: {len(vector)} dimensions")
                print(f"   [{preview}, ...]")

            if len(texts) > 1 and all(v is not None for v in vectors):
                print_similarity_report(texts, vectors)

            if response.usage:
                print(f"\nUsage:")
                print(f"   Input tokens: {response.usage.prompt_tokens}")

    except OpenRouterError as e:
        safe_print_error("Request Error", e)
    except Exception as e:
        safe_print_error("Error", e)


def main():
    """
    Main interactive menu
    """
    if not get_api_key():
        print("Set it before running: export OPENROUTER_API_KEY='your-key-here'")
        sys.exit(1)

    print("\n" + "🚀 " * 20)
    print("OpenRouter Multimodal Models - List & Call Each Type".center(80))
    print("🚀 " * 20)

    while True:
        print("\n" + "="*80)
        print("MENU")
        print("="*80)
        print("""
📋 LIST MODELS BY MODALITY
  1. List all models organized by type
  2. List TEXT models only
  3. List IMAGE models only
  4. List SPEECH models only
  5. List AUDIO models only (list-only - called via chat)
  6. List VIDEO models only
  7. List EMBEDDING models only

🎯 CALL MODELS INDIVIDUALLY
  8. Call a TEXT model
  9. Call an IMAGE model
  10. Call a SPEECH model
  11. Call a VIDEO model
  12. Call an EMBEDDING model
  13. Resume polling a VIDEO job

0. Exit
""")

        choice = input("Select an option (0-13): ").strip()

        if choice == "1":
            list_all_models_modality()

        elif choice == "2":
            list_models_by_modality("text")

        elif choice == "3":
            list_models_by_modality("image")

        elif choice == "4":
            list_models_by_modality("speech")

        elif choice == "5":
            list_models_by_modality("audio")

        elif choice == "6":
            list_models_by_modality("video")

        elif choice == "7":
            list_models_by_modality("embeddings")

        elif choice == "8":
            model_id = input("Enter text model ID (e.g., openai/gpt-4o-mini): ").strip()
            if model_id:
                prompt = input("Enter prompt (or press Enter for default): ").strip()
                if not prompt:
                    prompt = "Say hello in one sentence"
                call_text_model(model_id, prompt)

        elif choice == "9":
            model_id = input("Enter image model ID (e.g., openai/gpt-image-1): ").strip()
            if model_id:
                prompt = input("Enter image prompt (or press Enter for default): ").strip()
                if not prompt:
                    prompt = "A sunset over mountains"
                call_image_model(model_id, prompt)

        elif choice == "10":
            model_id = input("Enter speech model ID (e.g., hexgrad/kokoro-82m): ").strip()
            if model_id:
                text = input("Enter text to convert to speech (or press Enter for default): ").strip()
                if not text:
                    text = "Hello, this is a test."
                voice = input("Enter voice (or press Enter for the model's first): ").strip()
                call_speech_model(model_id, text, voice or None)

        elif choice == "11":
            model_id = input("Enter video model ID (e.g., google/veo-3): ").strip()
            if model_id:
                prompt = input("Enter video prompt (or press Enter for default): ").strip()
                if not prompt:
                    prompt = "A cat playing with a ball"
                wait = input("Wait for the video? [Y/n]: ").strip().lower() != "n"
                call_video_model(model_id, prompt, wait=wait)

        elif choice == "12":
            model_id = input("Enter embedding model ID (e.g., openai/text-embedding-3-small): ").strip()
            if model_id:
                # Two or more texts get compared to each other, so keep asking
                # until an empty line; nothing at all falls back to the demo set.
                print("Enter texts to embed, one per line (blank line to finish,")
                print("or press Enter straight away for the default comparison):")
                texts = []
                while True:
                    text = input(f"   {len(texts) + 1}. ").strip()
                    if not text:
                        break
                    texts.append(text)
                call_embedding_model(model_id, texts or None)

        elif choice == "13":
            job_id = input("Enter the video job ID: ").strip()
            if job_id:
                poll_video_job(job_id)

        elif choice == "0":
            print("\n👋 Goodbye!")
            break

        else:
            print("❌ Invalid option. Please select 0-13.")


def run_command_line_examples():
    """
    Run example calls from command line arguments

    Usage:
      # List operations
      python model_calls.py list-all
      python model_calls.py list-text
      python model_calls.py list-image
      python model_calls.py list-speech
      python model_calls.py list-audio
      python model_calls.py list-video
      python model_calls.py list-embeddings

      # Call operations (no call-audio - see the module docstring)
      python model_calls.py call-text "model-id" "prompt"
      python model_calls.py call-image "model-id" "prompt"
      python model_calls.py call-speech "model-id" "text" ["voice"]
      python model_calls.py call-video "model-id" "prompt" [--no-wait]
      python model_calls.py poll-video "job-id"
      python model_calls.py call-embedding "model-id" ["text" ...]
    """
    if len(sys.argv) < 2:
        main()
        return

    command = sys.argv[1]

    # List operations
    if command == "list-all":
        list_all_models_modality()

    elif command == "list-text":
        list_models_by_modality("text")

    elif command == "list-image":
        list_models_by_modality("image")

    elif command == "list-speech":
        list_models_by_modality("speech")

    elif command == "list-audio":
        list_models_by_modality("audio")

    elif command == "list-video":
        list_models_by_modality("video")

    elif command == "list-embeddings":
        list_models_by_modality("embeddings")

    # Call operations
    elif command == "call-text":
        if len(sys.argv) < 3:
            print("Usage: python model_calls.py call-text <model_id> [prompt]")
            return
        model_id = sys.argv[2]
        prompt = sys.argv[3] if len(sys.argv) > 3 else "Say hello in one sentence"
        call_text_model(model_id, prompt)

    elif command == "call-image":
        if len(sys.argv) < 3:
            print("Usage: python model_calls.py call-image <model_id> [prompt]")
            return
        model_id = sys.argv[2]
        prompt = sys.argv[3] if len(sys.argv) > 3 else "A sunset over mountains"
        call_image_model(model_id, prompt)

    elif command == "call-speech":
        if len(sys.argv) < 3:
            print("Usage: python model_calls.py call-speech <model_id> [text] [voice]")
            return
        model_id = sys.argv[2]
        text = sys.argv[3] if len(sys.argv) > 3 else "Hello, this is a test."
        voice = sys.argv[4] if len(sys.argv) > 4 else None
        call_speech_model(model_id, text, voice)

    elif command == "call-video":
        # --no-wait submits the job and prints its ID instead of polling.
        args = [a for a in sys.argv[2:] if a != "--no-wait"]
        wait = "--no-wait" not in sys.argv
        if not args:
            print("Usage: python model_calls.py call-video <model_id> [prompt] [--no-wait]")
            return
        model_id = args[0]
        prompt = args[1] if len(args) > 1 else "A cat playing with a ball"
        call_video_model(model_id, prompt, wait=wait)

    elif command == "poll-video":
        if len(sys.argv) < 3:
            print("Usage: python model_calls.py poll-video <job_id>")
            return
        poll_video_job(sys.argv[2])

    elif command == "call-embedding":
        if len(sys.argv) < 3:
            print("Usage: python model_calls.py call-embedding <model_id> [text ...]")
            return
        model_id = sys.argv[2]
        # Every remaining argument is a text; two or more get compared, and
        # none at all falls back to the default comparison set.
        call_embedding_model(model_id, sys.argv[3:] or None)

    else:
        print("❌ Unknown command. Available commands:")
        print("\n📋 LIST COMMANDS:")
        print("  list-all")
        print("  list-text")
        print("  list-image")
        print("  list-speech")
        print("  list-audio")
        print("  list-video")
        print("  list-embeddings")
        print("\n🎯 CALL COMMANDS:")
        print("  call-text <model_id> [prompt]")
        print("  call-image <model_id> [prompt]")
        print("  call-speech <model_id> [text] [voice]")
        print("  call-video <model_id> [prompt] [--no-wait]")
        print("  poll-video <job_id>")
        print("  call-embedding <model_id> [text ...]")
        print("\n(audio is list-only: those models return audio through the")
        print(" chat endpoint, not an endpoint of their own)")


if __name__ == "__main__":
    run_command_line_examples()
