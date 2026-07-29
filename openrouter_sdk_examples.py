"""
OpenRouter Python SDK Learning Script
Examples of calling OpenRouter API endpoints using the official SDK

Configuration, key handling, error redaction, and input validation come from
common.py, so this file demonstrates SDK usage rather than re-implementing the
guards around it.
"""

import asyncio
import os
import sys

from openrouter import OpenRouter
from openrouter.utils import BackoffStrategy, RetryConfig

from common import (
    REQUEST_TIMEOUT_MS,
    validate_model_name,
    validate_text,
)
from common import safe_print_error as _safe_print_error

# Configuration
MAX_CONCURRENT_REQUESTS = 5  # Prevent DoS/overwhelming the API

# Retry with exponential backoff on transient failures. The SDK budgets by
# elapsed time rather than by attempt count.
RETRY_CONFIG = RetryConfig(
    strategy="backoff",
    backoff=BackoffStrategy(
        initial_interval=500,
        max_interval=8_000,
        exponent=1.5,
        max_elapsed_time=30_000,
    ),
    retry_connection_errors=True,
)

# Model IDs change as providers retire versions - check the current catalog
# with `python model_calls.py list-text` if a call 404s.
DEFAULT_MODEL = "openai/gpt-4o-mini"
COMPARISON_MODELS = [
    "openai/gpt-4o-mini",
    "meta-llama/llama-3.3-70b-instruct",
    # Pick instruct models here, not reasoning-tuned ones: the comparison caps
    # each reply at 100 tokens, which a thinking model spends before it writes
    # any content (google/gemini-3.6-flash and the newer Kimis both come back
    # empty or truncated).
    "google/gemini-3.5-flash-lite",
]


def safe_print_error(error_type: str, error) -> None:
    """
    Print an error, indented to sit under the section headings these examples
    print. The redaction itself lives in common.safe_print_error.
    """
    _safe_print_error(error_type, error, indent="   ")


def example_basic_chat():
    """
    Basic chat completion - send a message and get a response
    """
    print("\n" + "="*60)
    print("EXAMPLE: Basic Chat Completion")
    print("="*60)
    
    model = DEFAULT_MODEL
    user_message = "Say hello in one sentence"
    
    # Input validation
    if not validate_model_name(model):
        safe_print_error("Validation", "Invalid model name format")
        return
    
    if not validate_text(user_message):
        safe_print_error("Validation", "Invalid message content")
        return
    
    try:
        with OpenRouter(api_key=os.getenv("OPENROUTER_API_KEY")) as client:
            print("\nRequest:")
            print(f"  model: {model}")
            print(f"  message: {user_message}")
            
            response = client.chat.send(
                model=model,
                messages=[
                    {"role": "user", "content": user_message}
                ]
            )
            
            # Validate response structure before accessing
            if not response.choices or not response.choices[0].message:
                safe_print_error("Response", "Invalid response structure")
                return
            
            print("\n✅ Response:")
            print(f"   {response.choices[0].message.content}")
            print(f"\nUsage:")
            print(f"   Input tokens: {response.usage.prompt_tokens}")
            print(f"   Output tokens: {response.usage.completion_tokens}")
    
    except (ValueError, TypeError) as e:
        safe_print_error("Input Error", str(e))
    except Exception as e:
        safe_print_error("Request Error", str(e))


def example_with_system_prompt():
    """
    Chat with a system prompt to set context/behavior
    """
    print("\n" + "="*60)
    print("EXAMPLE: Chat with System Prompt")
    print("="*60)
    
    model = DEFAULT_MODEL
    system_msg = "You are a helpful assistant that speaks like a pirate"
    user_msg = "How are you doing today?"
    
    # Input validation
    if not validate_model_name(model):
        safe_print_error("Validation", "Invalid model name format")
        return
    
    if not validate_text(system_msg) or not validate_text(user_msg):
        safe_print_error("Validation", "Invalid message content")
        return
    
    try:
        with OpenRouter(api_key=os.getenv("OPENROUTER_API_KEY")) as client:
            print("\nRequest:")
            print(f"  model: {model}")
            print(f"  system: {system_msg}")
            print(f"  message: {user_msg}")
            
            response = client.chat.send(
                model=model,
                messages=[
                    {"role": "system", "content": system_msg},
                    {"role": "user", "content": user_msg}
                ]
            )
            
            if response.choices and response.choices[0].message:
                print("\n✅ Response:")
                print(f"   {response.choices[0].message.content}")
            else:
                safe_print_error("Response", "Invalid response structure")
    
    except (ValueError, TypeError) as e:
        safe_print_error("Input Error", str(e))
    except Exception as e:
        safe_print_error("Request Error", str(e))


def example_multi_turn_conversation():
    """
    Multi-turn conversation - maintaining context across messages
    """
    print("\n" + "="*60)
    print("EXAMPLE: Multi-Turn Conversation")
    print("="*60)
    
    model = DEFAULT_MODEL
    messages = [
        {"role": "user", "content": "My name is Alex"},
        {"role": "assistant", "content": "Nice to meet you, Alex! How can I help you today?"},
        {"role": "user", "content": "What's my name?"}
    ]
    
    # Input validation
    if not validate_model_name(model):
        safe_print_error("Validation", "Invalid model name format")
        return
    
    # Validate all messages
    for msg in messages:
        if not isinstance(msg, dict) or "role" not in msg or "content" not in msg:
            safe_print_error("Validation", "Invalid message structure")
            return
        if not validate_text(msg["content"]):
            safe_print_error("Validation", "Invalid message content")
            return
    
    try:
        with OpenRouter(api_key=os.getenv("OPENROUTER_API_KEY")) as client:
            print("\nRequest:")
            print(f"  model: {model}")
            print("  messages: (3-message conversation)")
            for msg in messages:
                print(f"    - {msg['role']}: {msg['content']}")
            
            response = client.chat.send(
                model=model,
                messages=messages
            )
            
            if response.choices and response.choices[0].message:
                print("\n✅ Response:")
                print(f"   {response.choices[0].message.content}")
            else:
                safe_print_error("Response", "Invalid response structure")
    
    except (ValueError, TypeError) as e:
        safe_print_error("Input Error", str(e))
    except Exception as e:
        safe_print_error("Request Error", str(e))


def example_with_parameters():
    """
    Chat with additional parameters (temperature, top_p, max_tokens)
    """
    print("\n" + "="*60)
    print("EXAMPLE: Chat with Parameters")
    print("="*60)
    
    model = DEFAULT_MODEL
    user_msg = "Write a short creative poem about coding"
    temperature = 0.7
    top_p = 0.9
    max_tokens = 150
    
    # Input validation
    if not validate_model_name(model):
        safe_print_error("Validation", "Invalid model name format")
        return
    
    if not validate_text(user_msg):
        safe_print_error("Validation", "Invalid message content")
        return
    
    # Validate parameter ranges
    if not (0 <= temperature <= 2):
        safe_print_error("Validation", "Temperature must be between 0 and 2")
        return
    
    if not (0 <= top_p <= 1):
        safe_print_error("Validation", "top_p must be between 0 and 1")
        return
    
    if not (1 <= max_tokens <= 4096):
        safe_print_error("Validation", "max_tokens must be between 1 and 4096")
        return
    
    try:
        with OpenRouter(api_key=os.getenv("OPENROUTER_API_KEY")) as client:
            print("\nRequest:")
            print(f"  model: {model}")
            print(f"  temperature: {temperature}  (more creative)")
            print(f"  top_p: {top_p}")
            print(f"  max_tokens: {max_tokens}")
            print(f"  message: {user_msg}")
            
            response = client.chat.send(
                model=model,
                messages=[
                    {"role": "user", "content": user_msg}
                ],
                temperature=temperature,
                top_p=top_p,
                max_tokens=max_tokens
            )
            
            if response.choices and response.choices[0].message:
                print("\n✅ Response:")
                print(f"   {response.choices[0].message.content}")
            else:
                safe_print_error("Response", "Invalid response structure")
    
    except (ValueError, TypeError) as e:
        safe_print_error("Input Error", str(e))
    except Exception as e:
        safe_print_error("Request Error", str(e))


def example_streaming():
    """
    Streaming response - get tokens as they're generated
    """
    print("\n" + "="*60)
    print("EXAMPLE: Streaming Response")
    print("="*60)
    
    model = DEFAULT_MODEL
    user_message = "Count from 1 to 5, one number per line"
    
    # Input validation
    if not validate_model_name(model):
        safe_print_error("Validation", "Invalid model name format")
        return
    
    if not validate_text(user_message):
        safe_print_error("Validation", "Invalid message content")
        return
    
    try:
        with OpenRouter(api_key=os.getenv("OPENROUTER_API_KEY")) as client:
            print("\nRequest:")
            print(f"  model: {model}")
            print("  stream: True")
            print(f"  message: {user_message}")
            
            print("\n✅ Streaming response:")
            print("   ", end="")
            
            total_chars = 0
            max_output = 5000  # Prevent DoS via infinite streaming
            
            stream = client.chat.send(
                model=model,
                messages=[
                    {"role": "user", "content": user_message}
                ],
                stream=True
            )
            
            for event in stream:
                if event.choices and event.choices[0].delta.content:
                    content = event.choices[0].delta.content
                    
                    # Safety check: prevent unbounded output
                    total_chars += len(content)
                    if total_chars > max_output:
                        print("\n   [Output limit reached]")
                        break
                    
                    print(content, end="", flush=True)
            
            print("\n   [Stream complete]")
    
    except (ValueError, TypeError) as e:
        safe_print_error("Input Error", str(e))
    except Exception as e:
        safe_print_error("Streaming Error", str(e))


def example_different_models():
    """
    Try different models to see speed/quality tradeoffs
    """
    print("\n" + "="*60)
    print("EXAMPLE: Different Models Comparison")
    print("="*60)
    
    prompt = "What is machine learning in one sentence?"

    # Input validation
    if not validate_text(prompt):
        safe_print_error("Validation", "Invalid prompt")
        return

    # Drop invalid names up front, so the request loop below only sees models
    # that passed validation
    models = []
    for model in COMPARISON_MODELS:
        if validate_model_name(model):
            models.append(model)
        else:
            safe_print_error("Validation", f"Invalid model name: {model}")

    if not models:
        safe_print_error("Validation", "No valid models to compare")
        return

    try:
        with OpenRouter(api_key=os.getenv("OPENROUTER_API_KEY")) as client:
            for model in models:
                print(f"\n📌 Model: {model}")
                print(f"   Prompt: {prompt}")
                
                try:
                    response = client.chat.send(
                        model=model,
                        messages=[{"role": "user", "content": prompt}],
                        max_tokens=100
                    )
                    
                    # Validate response before accessing
                    if response.choices and response.choices[0].message:
                        print(f"   Response: {response.choices[0].message.content}")
                        print(f"   Tokens: {response.usage.prompt_tokens} input, {response.usage.completion_tokens} output")
                    else:
                        safe_print_error("Response", "Invalid response structure")
                
                except (ValueError, TypeError) as e:
                    safe_print_error("Input Error", str(e))
                except Exception as e:
                    safe_print_error("Request Error", str(e))
    
    except Exception as e:
        safe_print_error("Comparison Error", str(e))


async def example_async():
    """
    Async example - make multiple requests concurrently with safety limits
    """
    print("\n" + "="*60)
    print("EXAMPLE: Async Requests")
    print("="*60)
    
    model = DEFAULT_MODEL
    prompts = [
        "Say 'hello' in one word",
        "Say 'goodbye' in one word",
        "Say 'thank you' in one word"
    ]
    
    # Input validation
    if not validate_model_name(model):
        safe_print_error("Validation", "Invalid model name format")
        return
    
    for prompt in prompts:
        if not validate_text(prompt):
            safe_print_error("Validation", "Invalid message content")
            return
    
    try:
        async with OpenRouter(api_key=os.getenv("OPENROUTER_API_KEY")) as client:
            print(f"\nRequest: {len(prompts)} concurrent chat requests (limit: {MAX_CONCURRENT_REQUESTS})")
            
            # Validate we don't exceed concurrency limits
            if len(prompts) > MAX_CONCURRENT_REQUESTS:
                safe_print_error("Rate Limit", f"Too many concurrent requests. Max: {MAX_CONCURRENT_REQUESTS}")
                return
            
            requests = [
                client.chat.send_async(
                    model=model,
                    messages=[{"role": "user", "content": prompt}],
                    timeout_ms=REQUEST_TIMEOUT_MS,
                    retries=RETRY_CONFIG,
                )
                for prompt in prompts
            ]
            
            # Use gather with return_exceptions to catch individual failures
            responses = await asyncio.gather(*requests, return_exceptions=True)
            
            print("\n✅ Responses:")
            for i, response in enumerate(responses, 1):
                if isinstance(response, Exception):
                    safe_print_error(f"Request {i}", str(response))
                elif hasattr(response, 'choices') and response.choices:
                    print(f"   {i}. {response.choices[0].message.content}")
                else:
                    safe_print_error(f"Request {i}", "Invalid response structure")
    
    except (ValueError, TypeError) as e:
        safe_print_error("Input Error", str(e))
    except Exception as e:
        safe_print_error("Async Error", str(e))


# CLI name -> (example function, is it a coroutine). Keeping the mapping in one
# place means the usage text below can't drift from what actually runs.
EXAMPLES = {
    "basic-chat": (example_basic_chat, False),
    "system-prompt": (example_with_system_prompt, False),
    "multi-turn": (example_multi_turn_conversation, False),
    "parameters": (example_with_parameters, False),
    "streaming": (example_streaming, False),
    "compare-models": (example_different_models, False),
    "async": (example_async, True),
}


def run_example(name: str) -> None:
    """
    Run a single example by its CLI name
    """
    func, is_async = EXAMPLES[name]

    if not is_async:
        func()
        return

    # Coroutines need an event loop; the example prints its own heading.
    try:
        asyncio.run(func())
    except Exception as e:
        safe_print_error("Async Error", e)


def main():
    """
    Run all examples, or the single one named on the command line
    """
    api_key = os.getenv("OPENROUTER_API_KEY")
    
    if not api_key:
        print("⚠️  OPENROUTER_API_KEY not set!")
        print("Set it before running: export OPENROUTER_API_KEY='your-key-here'")
        sys.exit(1)
    
    # Validate API key format (basic check)
    # OpenRouter keys typically start with sk-
    if not api_key.startswith("sk-"):
        print("⚠️  WARNING: API key doesn't match expected format (should start with 'sk-')")
        print("   This may still work, but verify the key is correct.")
    
    if len(api_key) < 20:
        print("⚠️  WARNING: API key seems too short. Verify it's correct.")
        sys.exit(1)

    # One example named on the command line runs on its own; no arguments runs
    # the lot, which is several billed calls.
    if len(sys.argv) > 1:
        name = sys.argv[1]
        if name not in EXAMPLES:
            print(f"❌ Unknown example: {name}")
            print("\nAvailable examples:")
            for available in EXAMPLES:
                print(f"  {available}")
            print("\n(no argument runs all of them)")
            sys.exit(1)
        run_example(name)
        return

    print("\n" + "🚀 " * 15)
    print("OpenRouter Python SDK Examples".center(70))
    print("🚀 " * 15)
    
    try:
        for name in EXAMPLES:
            run_example(name)

        print("\n" + "="*60)
        print("✨ All examples completed!")
        print("="*60)
        print("\nNext steps:")
        print("  1. Check openrouter.ai/docs/api/reference for all parameters")
        print("  2. Try different models - openai/*, meta-llama/*, mistralai/*, etc.")
        print("  3. Use provider routing with provider={'order': ['price']}")
        print("  4. Test error handling for rate limits and invalid models")
        print("  5. Explore embeddings API with client.embeddings.generate()")
    
    except KeyboardInterrupt:
        print("\n\n⏹️  Interrupted by user")
    except Exception as e:
        print(f"\n❌ Error: {e}")


if __name__ == "__main__":
    main()
