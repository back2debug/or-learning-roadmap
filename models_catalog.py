"""
OpenRouter Models - Fetch & Categorize by Modality

This module handles discovering what's available on OpenRouter:
1. Fetches models from OpenRouter via the official Python SDK
2. Categorizes them by modality (text, image, speech, audio, video, embeddings)
3. Lists them, grouped or filtered by a single modality

Configuration, key handling, error redaction, and input validation come from
common.py, which model_calls.py and openrouter_sdk_examples.py share.
"""

from openrouter import OpenRouter
from openrouter.errors import OpenRouterError

from common import (
    REQUEST_TIMEOUT_MS,
    get_api_key,
    safe_print_error,
    validate_model_name,
)

# Configuration
DEFAULT_PAGE_SIZE = 100      # models requested per API call
MAX_PAGE_SIZE = 1000         # API cap on the `limit` param
MAX_PAGES = 100              # safety valve so we can never loop forever

# "speech" is text-to-speech output (the /audio/speech endpoint). It is distinct
# from OpenRouter's "audio" modality, which is chat models that emit audio inline
# alongside text (openai/gpt-audio, google/lyria-3) and are called via chat.send -
# so audio models are listed here but have no call-* counterpart.
#
# The /models endpoint filters on every one of these server-side, so
# list_models_by_modality never has to page the whole catalog to find them.
MODALITIES = ("text", "image", "speech", "audio", "video", "embeddings")


def fetch_models(output_modalities: str = "all",
                 page_size: int = DEFAULT_PAGE_SIZE,
                 max_models: int = None):
    """
    Fetch models through the SDK, one page at a time.

    client.models.list() returns a paginated handle: .result holds the current
    page and .next() fetches the following one (or None at the end), so we walk
    the catalog in pages instead of asking for everything in a single request.

    Args:
        output_modalities: server-side modality filter - a comma-separated list
            of the MODALITIES names, or "all". Note the API defaults to
            "text" when this is omitted, which is why we pass it explicitly.
        page_size: models to request per page (capped at 1000 by the API)
        max_models: stop once this many models are collected (None = fetch all)

    Returns:
        List of Model objects, or None if nothing could be fetched
    """
    api_key = get_api_key()
    if not api_key:
        return None

    page_size = max(1, min(page_size, MAX_PAGE_SIZE))
    models = []

    try:
        with OpenRouter(api_key=api_key) as client:
            page = client.models.list(
                output_modalities=output_modalities,
                offset=0,
                limit=page_size,
                timeout_ms=REQUEST_TIMEOUT_MS,
            )

            for _ in range(MAX_PAGES):
                if page is None:
                    break

                models.extend(page.result.data or [])

                if max_models is not None and len(models) >= max_models:
                    return models[:max_models]

                page = page.next()
            else:
                print(f"⚠️  Stopped after {MAX_PAGES} pages ({len(models)} models)")

        return models or None

    except OpenRouterError as e:
        safe_print_error("Error fetching models", e)
        if models:
            print(f"   Returning the {len(models)} models fetched so far")
            return models
        return None
    except Exception as e:
        safe_print_error("Error", e)
        return None


def get_supported_voices(model_id: str):
    """
    Look up the voices a speech model accepts.

    Voice names are model-specific ("af_bella" for Kokoro,
    "en-US-Harper:MAI-Voice-2" for MAI-Voice), and a wrong one is rejected, so
    the speech caller uses this to pick a valid default.

    Args:
        model_id: full "provider/model" ID

    Returns:
        List of voice names, or None if the model can't be looked up
    """
    api_key = get_api_key()
    # author and slug become path segments of the request URL unescaped, so an
    # ID that isn't a clean provider/model pair never reaches the SDK.
    if not api_key or not validate_model_name(model_id):
        return None

    author, _, slug = model_id.partition("/")

    try:
        with OpenRouter(api_key=api_key) as client:
            # models.get() returns a wrapper - the Model itself is on .data
            response = client.models.get(
                author=author,
                slug=slug,
                timeout_ms=REQUEST_TIMEOUT_MS,
            )

        model = getattr(response, "data", None)
        return getattr(model, "supported_voices", None) or None

    except OpenRouterError as e:
        safe_print_error("Error looking up voices", e)
        return None
    except Exception as e:
        safe_print_error("Error", e)
        return None


def categorize_models_by_modality(models):
    """
    Organize models by their output modalities (see MODALITIES)
    """
    categorized = {modality: [] for modality in MODALITIES}

    for model in models:
        architecture = getattr(model, "architecture", None)
        output_modalities = getattr(architecture, "output_modalities", None) or []

        for modality in output_modalities:
            if modality in categorized:
                categorized[modality].append(model)

    return categorized


def format_price(model) -> str:
    """
    Render a model's prompt price, flagging free models
    """
    prompt_price = getattr(model.pricing, "prompt", None) if model.pricing else None

    if prompt_price is None:
        return "N/A"
    if str(prompt_price) == "0":
        return "[FREE]"
    return f"${prompt_price}"


def list_all_models_modality():
    """
    List all available models grouped by modality
    """
    print("\n" + "="*80)
    print("OPENROUTER MODELS - ORGANIZED BY MODALITY")
    print("="*80)

    models = fetch_models()
    if not models:
        return

    categorized = categorize_models_by_modality(models)

    print(f"\n📊 Total models: {len(models)}")
    for modality, model_list in categorized.items():
        if model_list:
            print(f"   • {modality.upper()}: {len(model_list)} models")

    # Display by category
    for modality in MODALITIES:
        model_list = categorized[modality]

        if not model_list:
            print(f"\n📌 {modality.upper()}: No models found")
            continue

        print("\n" + "="*80)
        print(f"📌 {modality.upper()} MODELS ({len(model_list)} available)")
        print("="*80)

        for model in model_list[:10]:  # Show first 10
            price = format_price(model)
            marker = "✨" if price == "[FREE]" else "•"
            print(f"  {marker} {model.id:<45} {model.name:<30} {price}")

        if len(model_list) > 10:
            print(f"  ... and {len(model_list) - 10} more {modality} models")

    return categorized


def list_models_by_modality(modality: str):
    """
    List all models that support a specific modality

    Args:
        modality: any of MODALITIES - "text", "image", "speech", "audio",
            "video", or "embeddings"
    """
    print("\n" + "="*80)
    print(f"MODELS SUPPORTING {modality.upper()}")
    print("="*80)

    if modality not in MODALITIES:
        print(f"❌ Unknown modality: {modality}")
        print(f"   Available: {', '.join(MODALITIES)}")
        return

    # Let the API do the filtering, so we only page through the models we
    # actually intend to show.
    model_list = fetch_models(output_modalities=modality)

    if not model_list:
        print(f"❌ No models found supporting {modality}")
        return

    print(f"\n✅ Found {len(model_list)} {modality} models:\n")

    for i, model in enumerate(model_list, 1):
        price = format_price(model)
        price_str = price if price == "[FREE]" else f"{price}/token"
        context = model.context_length if model.context_length is not None else "N/A"

        print(f"{i:3d}. {model.id:<50} {price_str:<20} ({context} context)")
        print(f"     {model.name}\n")

    return model_list
