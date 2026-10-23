#!/usr/bin/env python3
"""Explicit, bounded JSON and SSE verification against an inference gateway."""

import argparse
import json
import math
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


MAX_RESPONSE_BYTES = 1024 * 1024
MAX_FRAME_BYTES = 64 * 1024


class VerificationError(Exception):
    """A safe error message that contains no credentials or response content."""


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # An explicit gateway destination must never forward the key elsewhere.
        raise VerificationError("gateway redirect refused")


def endpoint_url(base_url, endpoint):
    parsed = urllib.parse.urlsplit(base_url)
    if (
        parsed.scheme not in ("http", "https")
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or parsed.path not in ("", "/", "/v1", "/v1/")
    ):
        raise VerificationError("base URL must be a gateway origin or its /v1 URL")
    if endpoint not in ("completions", "chat/completions"):
        raise VerificationError("unsupported OpenAI endpoint")
    return urllib.parse.urlunsplit((parsed.scheme, parsed.netloc, "/v1/" + endpoint, "", ""))


def decode_result(payload):
    try:
        value = json.loads(payload)
    except (ValueError, UnicodeDecodeError):
        raise VerificationError("gateway returned invalid JSON") from None
    if not isinstance(value, dict) or value.get("error") is not None:
        raise VerificationError("gateway returned an OpenAI application error")
    return value


def verify_json(response):
    if response.headers.get_content_type() != "application/json":
        raise VerificationError("normal response is not application/json")
    payload = response.read(MAX_RESPONSE_BYTES + 1)
    if len(payload) > MAX_RESPONSE_BYTES:
        raise VerificationError("normal response exceeds limit")
    value = decode_result(payload)
    choices = value.get("choices")
    if not isinstance(choices, list) or not choices or not all(isinstance(c, dict) for c in choices):
        raise VerificationError("normal response has no OpenAI choices")
    if not any(
        isinstance(c.get("text"), str)
        or (isinstance(c.get("message"), dict) and isinstance(c["message"].get("content"), str))
        for c in choices
    ):
        raise VerificationError("normal response has no completion content")
    return {"choices": len(choices), "bytes": len(payload)}


def verify_sse(response, deadline):
    if response.headers.get_content_type() != "text/event-stream":
        raise VerificationError("stream response is not text/event-stream")
    size = 0
    frame_size = 0
    data = []
    chunks = 0
    saw_choice = False
    while True:
        if time.monotonic() > deadline:
            raise VerificationError("stream exceeded total time limit")
        line = response.readline(MAX_FRAME_BYTES + 1)
        size += len(line)
        frame_size += len(line)
        if size > MAX_RESPONSE_BYTES or frame_size > MAX_FRAME_BYTES:
            raise VerificationError("stream response exceeds limit")
        if not line:
            raise VerificationError("stream ended before [DONE]")
        line = line.rstrip(b"\r\n")
        if line == b"":
            frame_size = 0
            if not data:
                continue
            payload = b"\n".join(data)
            data = []
            if payload == b"[DONE]":
                if not saw_choice:
                    raise VerificationError("stream completed without OpenAI choices")
                return {"chunks": chunks, "bytes": size, "done": True}
            value = decode_result(payload)
            choices = value.get("choices")
            if not isinstance(choices, list) or not all(isinstance(c, dict) for c in choices):
                raise VerificationError("stream chunk has invalid OpenAI choices")
            # Usage-only chunks legitimately contain choices: [].
            saw_choice = saw_choice or bool(choices)
            chunks += 1
        elif line.startswith(b"data:"):
            field = line[5:]
            data.append(field[1:] if field.startswith(b" ") else field)
        # SSE comments, event names, ids and retry hints do not contain output.


def verify_invocation(base_url, model, api_key, prompt="Reply with OK.", endpoint="completions", timeout=30):
    url = endpoint_url(base_url, endpoint)
    if not model.strip() or not api_key or any(c in api_key for c in "\r\n"):
        raise VerificationError("model and a valid gateway API key are required")
    if not math.isfinite(timeout) or not 0 < timeout <= 120:
        raise VerificationError("timeout must be greater than zero and at most 120 seconds")
    if not prompt or len(prompt.encode()) > 16384:
        raise VerificationError("prompt must be nonempty and at most 16 KiB")
    opener = urllib.request.build_opener(NoRedirects())
    results = {}
    for stream in (False, True):
        payload = {"model": model, "max_tokens": 16, "stream": stream}
        if endpoint == "chat/completions":
            payload["messages"] = [{"role": "user", "content": prompt}]
        else:
            payload["prompt"] = prompt
        request = urllib.request.Request(
            url,
            data=json.dumps(payload).encode(),
            headers={"Content-Type": "application/json", "x-api-key": api_key},
            method="POST",
        )
        started = time.monotonic()
        try:
            with opener.open(request, timeout=timeout) as response:
                result = verify_sse(response, started + timeout) if stream else verify_json(response)
        except VerificationError:
            raise
        except urllib.error.HTTPError as exc:
            raise VerificationError(f"gateway returned HTTP {exc.code}") from None
        except (urllib.error.URLError, OSError, ValueError):
            raise VerificationError("gateway transport failed or timed out") from None
        result["elapsed_ms"] = round((time.monotonic() - started) * 1000)
        results["stream" if stream else "normal"] = result
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True, help="explicit Higress public origin or /v1 URL")
    parser.add_argument("--model", required=True, help="the published served_model_name")
    parser.add_argument("--endpoint", choices=("completions", "chat/completions"), default="completions")
    parser.add_argument("--prompt", default="Reply with OK.")
    parser.add_argument("--timeout", type=float, default=30, help="timeout per request, at most 120 seconds")
    args = parser.parse_args()
    try:
        result = verify_invocation(args.base_url, args.model, os.environ.get("ANI_INFERENCE_API_KEY", ""), args.prompt, args.endpoint, args.timeout)
    except VerificationError as exc:
        print(json.dumps({"ok": False, "error": str(exc)}), file=sys.stderr)
        return 1
    print(json.dumps({"ok": True, **result}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
