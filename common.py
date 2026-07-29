"""
Shared helpers for the OpenRouter scripts

Configuration, API-key handling, error redaction, and input validation - the
pieces every script here needs and none of them should own a private copy of.
The redaction pattern and the validators are security-relevant, so they live in
exactly one place: a fix applied here reaches every caller.

Nothing in this module talks to the network; it is pure local logic, which also
makes it the easy part to test.
"""

import os
import re

# Per-request timeout the SDK expects, in milliseconds. Individual scripts
# override this for operations that are legitimately slower (speech synthesis,
# video downloads) rather than raising it globally.
REQUEST_TIMEOUT_MS = 30_000

# Longest text we'll send in one request field, to keep a stray paste or a
# runaway loop from turning into a huge (and expensive) payload.
MAX_TEXT_LENGTH = 10_000

# OpenRouter keys look like sk-or-v1-..., so the pattern has to survive
# hyphens - matching only [a-zA-Z0-9] stops at "sk-or" and prints the rest.
SECRET_PATTERN = re.compile(r'(sk-[A-Za-z0-9_\-]{8,}|Bearer\s+\S+)')

# A model ID is provider/model with an optional :variant tail - OpenRouter uses
# :free, :nitro, :floor, :extended.
MODEL_ID_PATTERN = re.compile(r'^[a-zA-Z0-9\-_.]+/[a-zA-Z0-9\-_.]+(:[a-zA-Z0-9\-_.]+)?$')

# A job ID is an opaque handle: no separators, nothing that could reshape a URL.
JOB_ID_PATTERN = re.compile(r'^[A-Za-z0-9\-_.:]{1,200}$')


def get_api_key():
    """
    Read the API key from the environment, or explain why we can't continue
    """
    api_key = os.getenv("OPENROUTER_API_KEY")

    if not api_key:
        print("⚠️  OPENROUTER_API_KEY not set!")
        return None

    return api_key


def safe_print_error(error_type: str, error, indent: str = "") -> None:
    """
    Print an error without leaking the API key

    Args:
        error_type: short label for what failed
        error: the exception or message; anything printable
        indent: leading whitespace, for callers printing inside a section
    """
    sanitized = SECRET_PATTERN.sub('[REDACTED]', str(error))
    print(f"{indent}❌ {error_type}: {sanitized[:200]}")  # Truncate to 200 chars


def validate_model_name(model: str) -> bool:
    """
    Check a model ID is a plain "provider/model[:variant]" before it is used.

    This matters beyond tidiness: the SDK substitutes path parameters into the
    request URL *without* percent-encoding them (openrouter/utils/url.py), and
    models.get() puts the author and slug straight into the path. A model ID
    containing "../" would therefore point the request at a different endpoint,
    so the separators are rejected here rather than encoded later.
    """
    if not isinstance(model, str):
        return False

    return MODEL_ID_PATTERN.match(model) is not None


def validate_job_id(job_id: str) -> bool:
    """
    Check a video job ID before it is interpolated into the request path.

    Same unencoded-path-parameter reason as validate_model_name: a job ID is
    typed in by hand from a previous run, and "/" or "?" in it would rewrite
    the URL rather than being sent as part of the ID.
    """
    if not isinstance(job_id, str):
        return False

    return JOB_ID_PATTERN.match(job_id) is not None


def validate_text(content: str, max_length: int = MAX_TEXT_LENGTH) -> bool:
    """
    Check a text field is a string of a sane length.

    Guards against type confusion and against sending a payload big enough to
    be slow, costly, or rejected by the API.
    """
    if not isinstance(content, str):
        return False

    return 0 < len(content) <= max_length
