#!/usr/bin/env python3
"""Stash hook bridge for JAVBeacon's targeted realtime scene sync."""

import json
import sys
import time
import urllib.error
import urllib.request
import uuid


def debug(message, **fields):
    details = " ".join(f"{key}={value}" for key, value in fields.items() if value not in (None, ""))
    print(f"[JAVBeacon Realtime] {message}{' ' + details if details else ''}", file=sys.stderr, flush=True)


def main():
    payload = json.load(sys.stdin)
    args = payload.get("args") or {}
    hook = args.get("hookContext") or {}
    scene_id = str(hook.get("id") or "").strip()
    base_url = str(args.get("javbeacon_url") or "").strip().rstrip("/")
    secret = str(args.get("webhook_secret") or "").strip()
    timeout = max(1, int(args.get("timeout_seconds") or 10))
    mode = str(args.get("mode") or "hook").strip().lower()
    request_id = uuid.uuid4().hex[:12]
    if not base_url or not secret or secret.startswith("CHANGE_ME"):
        raise RuntimeError("configure javbeacon_url and webhook_secret in javbeacon-realtime.yml")
    if mode != "test" and not scene_id:
        raise RuntimeError("Stash hook did not include a scene ID")

    endpoint = "/api/hooks/stash/test" if mode == "test" else "/api/hooks/stash/scene"
    event = "Connection.Test" if mode == "test" else hook.get("type") or "Scene.Update.Post"
    body = json.dumps({"scene_id": scene_id, "event": event, "request_id": request_id}).encode()
    request = urllib.request.Request(
        base_url + endpoint,
        data=body,
        headers={"Authorization": "Bearer " + secret, "Content-Type": "application/json"},
        method="POST",
    )
    started = time.monotonic()
    debug("sending request", mode=mode, event=event, scene_id=scene_id, request_id=request_id, url=base_url + endpoint)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            result = json.loads(response.read() or b"{}")
    except urllib.error.HTTPError as error:
        detail = error.read().decode("utf-8", "replace")
        debug("request failed", request_id=request_id, status=error.code, elapsed_ms=round((time.monotonic() - started) * 1000), detail=detail)
        raise RuntimeError(f"JAVBeacon hook returned HTTP {error.code}: {detail}") from error
    except urllib.error.URLError as error:
        debug("connection failed", request_id=request_id, elapsed_ms=round((time.monotonic() - started) * 1000), error=error.reason)
        raise RuntimeError(f"could not reach JAVBeacon: {error.reason}") from error
    debug("request completed", request_id=request_id, elapsed_ms=round((time.monotonic() - started) * 1000), result=result.get("state", "accepted"))
    return {"output": {"mode": mode, "request_id": request_id, "queued_scene_id": scene_id or None, "javbeacon": result}}


if __name__ == "__main__":
    try:
        print(json.dumps(main()))
    except Exception as error:
        print(json.dumps({"error": str(error)}))
        sys.exit(1)
