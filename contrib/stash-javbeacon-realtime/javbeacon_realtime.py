#!/usr/bin/env python3
"""Stash hook bridge for JAVBeacon's targeted realtime scene sync."""

import json
import sys
import urllib.error
import urllib.request


def main():
    payload = json.load(sys.stdin)
    args = payload.get("args") or {}
    hook = args.get("hookContext") or {}
    scene_id = str(hook.get("id") or "").strip()
    base_url = str(args.get("javbeacon_url") or "").strip().rstrip("/")
    secret = str(args.get("webhook_secret") or "").strip()
    timeout = max(1, int(args.get("timeout_seconds") or 10))
    if not scene_id or not base_url or not secret or secret.startswith("CHANGE_ME"):
        raise RuntimeError("configure javbeacon_url and webhook_secret in javbeacon-realtime.yml")

    body = json.dumps({"scene_id": scene_id, "event": hook.get("type") or "Scene.Update.Post"}).encode()
    request = urllib.request.Request(
        base_url + "/api/hooks/stash/scene",
        data=body,
        headers={"Authorization": "Bearer " + secret, "Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            result = json.loads(response.read() or b"{}")
    except urllib.error.HTTPError as error:
        detail = error.read().decode("utf-8", "replace")
        raise RuntimeError(f"JAVBeacon hook returned HTTP {error.code}: {detail}") from error
    return {"output": {"queued_scene_id": scene_id, "javbeacon": result}}


if __name__ == "__main__":
    try:
        print(json.dumps(main()))
    except Exception as error:
        print(json.dumps({"error": str(error)}))
        sys.exit(1)
