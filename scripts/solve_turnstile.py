#!/usr/bin/env python3
"""Optional helper: obtain a Cloudflare Turnstile token via CapSolver / 2Captcha.

NewAPI protects POST /api/user/checkin with middleware that reads:
  ?turnstile=<token>

Site keys are domain-bound. This script does NOT solve Turnstile locally;
it calls a third-party solver API.

Usage:
  set CAPSOLVER_API_KEY=...
  python scripts/solve_turnstile.py {sitekey} {url}

  # or with env URL overrides:
  set TURNSTILE_SITEKEY=0x4...
  set TURNSTILE_PAGE_URL=https://cngov.cc.cd/
  python scripts/solve_turnstile.py

With newapi-checkin:
  newapi-checkin -config config.yaml -only "cngov" ^
    -turnstile-cmd "python scripts/solve_turnstile.py {sitekey} {url}"

Supported providers (first available key wins):
  CAPSOLVER_API_KEY  -> https://api.capsolver.com
  TWOCAPTCHA_API_KEY / TWO_CAPTCHA_API_KEY -> https://api.2captcha.com

Prints the token on the first stdout line. Exit non-zero on failure.
"""

from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request


def http_json(url: str, payload: dict, timeout: float = 60.0) -> dict:
    """POST a provider request and decode its JSON response."""
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=data,
        headers={"Content-Type": "application/json", "User-Agent": "newapi-checkin-turnstile/1.0"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode("utf-8"))


def solve_capsolver(api_key: str, sitekey: str, page_url: str) -> str:
    """Create and poll a CapSolver proxyless Turnstile task."""
    created = http_json(
        "https://api.capsolver.com/createTask",
        {
            "clientKey": api_key,
            "task": {
                "type": "AntiTurnstileTaskProxyLess",
                "websiteURL": page_url,
                "websiteKey": sitekey,
            },
        },
    )
    if created.get("errorId"):
        raise RuntimeError(created.get("errorDescription") or created)
    task_id = created.get("taskId")
    if not task_id:
        raise RuntimeError(f"capsolver createTask missing taskId: {created}")

    for _ in range(60):
        time.sleep(2)
        polled = http_json(
            "https://api.capsolver.com/getTaskResult",
            {"clientKey": api_key, "taskId": task_id},
        )
        if polled.get("errorId"):
            raise RuntimeError(polled.get("errorDescription") or polled)
        if polled.get("status") == "ready":
            token = (polled.get("solution") or {}).get("token") or ""
            if not token:
                raise RuntimeError(f"capsolver empty token: {polled}")
            return token
    raise RuntimeError("capsolver timeout waiting for token")


def solve_2captcha(api_key: str, sitekey: str, page_url: str) -> str:
    """Create and poll a 2Captcha proxyless Turnstile task."""
    created = http_json(
        "https://api.2captcha.com/createTask",
        {
            "clientKey": api_key,
            "task": {
                "type": "TurnstileTaskProxyless",
                "websiteURL": page_url,
                "websiteKey": sitekey,
            },
        },
    )
    if created.get("errorId"):
        raise RuntimeError(created.get("errorDescription") or created)
    task_id = created.get("taskId")
    if not task_id:
        raise RuntimeError(f"2captcha createTask missing taskId: {created}")

    for _ in range(60):
        time.sleep(3)
        polled = http_json(
            "https://api.2captcha.com/getTaskResult",
            {"clientKey": api_key, "taskId": task_id},
        )
        if polled.get("errorId"):
            raise RuntimeError(polled.get("errorDescription") or polled)
        if polled.get("status") == "ready":
            token = (polled.get("solution") or {}).get("token") or ""
            if not token:
                raise RuntimeError(f"2captcha empty token: {polled}")
            return token
    raise RuntimeError("2captcha timeout waiting for token")


def main() -> int:
    """Resolve inputs/provider, obtain a token, and expose it on stdout."""
    sitekey = os.environ.get("TURNSTILE_SITEKEY", "").strip()
    page_url = os.environ.get("TURNSTILE_PAGE_URL", "").strip()
    if len(sys.argv) >= 3:
        sitekey = sys.argv[1].strip() or sitekey
        page_url = sys.argv[2].strip() or page_url
    elif len(sys.argv) == 2:
        # single arg may be sitekey when URL comes from env
        sitekey = sys.argv[1].strip() or sitekey

    if not sitekey or not page_url:
        print(
            "usage: solve_turnstile.py <sitekey> <page_url>\n"
            "env: CAPSOLVER_API_KEY or TWOCAPTCHA_API_KEY",
            file=sys.stderr,
        )
        return 2

    capsolver = os.environ.get("CAPSOLVER_API_KEY", "").strip()
    twocaptcha = (
        os.environ.get("TWOCAPTCHA_API_KEY", "").strip()
        or os.environ.get("TWO_CAPTCHA_API_KEY", "").strip()
    )

    try:
        if capsolver:
            token = solve_capsolver(capsolver, sitekey, page_url)
        elif twocaptcha:
            token = solve_2captcha(twocaptcha, sitekey, page_url)
        else:
            print(
                "set CAPSOLVER_API_KEY or TWOCAPTCHA_API_KEY "
                "(Turnstile cannot be solved offline with the site key alone)",
                file=sys.stderr,
            )
            return 2
    except (urllib.error.URLError, TimeoutError, RuntimeError, json.JSONDecodeError) as exc:
        print(f"solve failed: {exc}", file=sys.stderr)
        return 1

    print(token)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
