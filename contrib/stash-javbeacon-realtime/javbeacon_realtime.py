#!/usr/bin/env python3
"""Stash bridge for JAVBeacon realtime sync and JAVBeacon-Subs jobs."""

import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


PLUGIN_ID = "javbeacon-realtime"


def debug(message, **fields):
    details = " ".join(f"{key}={value}" for key, value in fields.items() if value not in (None, ""))
    print(f"[JAVBeacon] {message}{' ' + details if details else ''}", file=sys.stderr, flush=True)


def _read_json_response(response):
    raw = response.read()
    if not raw:
        return {}
    try:
        return json.loads(raw)
    except json.JSONDecodeError as error:
        raise RuntimeError("server returned a non-JSON response") from error


def _http_error_detail(error):
    detail = error.read().decode("utf-8", "replace").strip()
    return detail[:2000] or error.reason


def _stash_graphql(payload, query, variables):
    connection = payload.get("server_connection") or {}
    scheme = str(connection.get("Scheme") or "http")
    host = str(connection.get("Host") or "127.0.0.1")
    if host in ("0.0.0.0", "::"):
        host = "127.0.0.1"
    if ":" in host and not host.startswith("["):
        host = f"[{host}]"
    port = int(connection.get("Port") or 9999)
    endpoint = f"{scheme}://{host}:{port}/graphql"
    headers = {"Content-Type": "application/json"}
    cookie = connection.get("SessionCookie") or {}
    cookie_name = cookie.get("Name")
    cookie_value = cookie.get("Value")
    if cookie_name and cookie_value:
        headers["Cookie"] = f"{cookie_name}={cookie_value}"

    request = urllib.request.Request(
        endpoint,
        data=json.dumps({"query": query, "variables": variables}).encode(),
        headers=headers,
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            result = _read_json_response(response)
    except urllib.error.HTTPError as error:
        raise RuntimeError(f"Stash GraphQL returned HTTP {error.code}: {_http_error_detail(error)}") from error
    except urllib.error.URLError as error:
        raise RuntimeError(f"could not query Stash: {error.reason}") from error

    errors = result.get("errors") or []
    if errors:
        messages = "; ".join(str(item.get("message") or item) for item in errors)
        raise RuntimeError(f"Stash GraphQL error: {messages}")
    return result.get("data") or {}


def _scene_and_subs_settings(payload, scene_id):
    query = """
      query JAVBeaconSubtitleScene($id: ID!) {
        findScene(id: $id) { files { path } }
        configuration { plugins(include: [\"javbeacon-realtime\"]) }
      }
    """
    data = _stash_graphql(payload, query, {"id": scene_id})
    scene = data.get("findScene")
    if not scene:
        raise RuntimeError(f"Stash scene {scene_id} was not found")
    paths = [str(item.get("path") or "").strip() for item in scene.get("files") or []]
    paths = [path for path in paths if path]
    if not paths:
        raise RuntimeError("the Stash scene has no file path")

    plugin_configs = (data.get("configuration") or {}).get("plugins") or {}
    settings = plugin_configs.get(PLUGIN_ID) or {}
    return paths[0], settings


def _plugin_settings(payload):
    query = """
      query JAVBeaconPluginSettings {
        configuration { plugins(include: [\"javbeacon-realtime\"]) }
      }
    """
    data = _stash_graphql(payload, query, {})
    plugin_configs = (data.get("configuration") or {}).get("plugins") or {}
    return plugin_configs.get(PLUGIN_ID) or {}


def _setting(settings, name, default):
    value = settings.get(name)
    return default if value is None or value == "" else value


def _bool_setting(settings, name, default):
    value = _setting(settings, name, default)
    if isinstance(value, str):
        normalized = value.strip().lower()
        if normalized in ("true", "1", "yes", "on"):
            return True
        if normalized in ("false", "0", "no", "off"):
            return False
        raise RuntimeError(f"{name} must be true or false")
    return bool(value)


def _scene_path_matches(scene_path, settings):
    raw_filters = str(settings.get("subs_scene_path_filters") or "")
    filters = [
        value.strip().casefold()
        for value in re.split(r"[\n,;]+", raw_filters)
        if value.strip()
    ]
    if not filters:
        return True
    normalized_path = str(scene_path or "").casefold()
    return any(value in normalized_path for value in filters)


def _jobs_endpoint(base_url):
    value = str(base_url or "").strip().rstrip("/")
    parsed = urllib.parse.urlparse(value)
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        raise RuntimeError("configure a valid JAVBeacon-Subs base URL in Settings > Plugins")
    if parsed.username or parsed.password:
        raise RuntimeError("JAVBeacon-Subs base URL must not contain credentials")
    if value.endswith("/api/v1/jobs"):
        return value
    return value + "/api/v1/jobs"


def _subtitle_body(scene_path, settings):
    options_raw = str(_setting(settings, "subs_job_options", "{}")).strip() or "{}"
    try:
        options = json.loads(options_raw)
    except json.JSONDecodeError as error:
        raise RuntimeError(f"Job options must be valid JSON: {error.msg}") from error
    if not isinstance(options, dict):
        raise RuntimeError("Job options must be a JSON object")

    body = {
        "recursive": _bool_setting(settings, "subs_recursive", False),
        "overwrite": _bool_setting(settings, "subs_overwrite", False),
        "auto_detect_release": _bool_setting(settings, "subs_auto_detect_release", True),
        "release_within_days": int(_setting(settings, "subs_release_within_days", 0)),
        "debug_mode": _bool_setting(settings, "subs_debug_mode", True),
        "keep_japanese": _bool_setting(settings, "subs_keep_japanese", True),
        "write_ass": _bool_setting(settings, "subs_write_ass", False),
    }
    body.update(options)
    body["inputs"] = [scene_path]
    return body


def request_subtitles(payload, args):
    scene_id = str(args.get("scene_id") or "").strip()
    if not scene_id:
        raise RuntimeError("subtitle request did not include a scene ID")
    scene_path, settings = _scene_and_subs_settings(payload, scene_id)
    if not _scene_path_matches(scene_path, settings):
        raise RuntimeError("scene path does not match the configured subtitle path filters")
    endpoint = _jobs_endpoint(settings.get("subs_base_url"))
    token = str(settings.get("subs_api_token") or "").strip()
    if not token:
        raise RuntimeError("configure the JAVBeacon-Subs API token in Settings > Plugins")
    timeout = max(1, int(_setting(settings, "subs_timeout_seconds", 30)))
    body = _subtitle_body(scene_path, settings)
    request_id = uuid.uuid4().hex[:12]
    request = urllib.request.Request(
        endpoint,
        data=json.dumps(body).encode(),
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
        method="POST",
    )

    started = time.monotonic()
    debug("sending subtitle request", scene_id=scene_id, request_id=request_id, url=endpoint)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            result = _read_json_response(response)
    except urllib.error.HTTPError as error:
        detail = _http_error_detail(error)
        debug("subtitle request failed", request_id=request_id, status=error.code, detail=detail)
        raise RuntimeError(f"JAVBeacon-Subs returned HTTP {error.code}: {detail}") from error
    except urllib.error.URLError as error:
        debug("subtitle connection failed", request_id=request_id, error=error.reason)
        raise RuntimeError(f"could not reach JAVBeacon-Subs: {error.reason}") from error

    debug(
        "subtitle request queued",
        scene_id=scene_id,
        request_id=request_id,
        elapsed_ms=round((time.monotonic() - started) * 1000),
    )
    return {
        "mode": "subtitles",
        "request_id": request_id,
        "scene_id": scene_id,
        "filename": os.path.basename(scene_path),
        "javbeacon_subs": result,
    }


def request_realtime_sync(payload, args):
    hook = args.get("hookContext") or {}
    scene_id = str(hook.get("id") or "").strip()
    settings = _plugin_settings(payload)
    base_url = str(settings.get("javbeacon_url") or "").strip().rstrip("/")
    secret = str(settings.get("webhook_secret") or "").strip()
    timeout = max(1, int(_setting(settings, "timeout_seconds", 10)))
    mode = str(args.get("mode") or "hook").strip().lower()
    request_id = uuid.uuid4().hex[:12]
    if not base_url or not secret:
        raise RuntimeError("configure the JAVBeacon URL and webhook secret in Settings > Plugins")
    if mode != "test" and not scene_id:
        raise RuntimeError("Stash hook did not include a scene ID")

    endpoint = "/api/hooks/stash/test" if mode == "test" else "/api/hooks/stash/scene"
    event = "Connection.Test" if mode == "test" else hook.get("type") or "Scene.Update.Post"
    body_fields = {"event": event, "request_id": request_id}
    if mode != "test":
        body_fields["scene_id"] = scene_id
    request = urllib.request.Request(
        base_url + endpoint,
        data=json.dumps(body_fields).encode(),
        headers={"Authorization": "Bearer " + secret, "Content-Type": "application/json"},
        method="POST",
    )
    started = time.monotonic()
    debug("sending realtime request", mode=mode, event=event, scene_id=scene_id, request_id=request_id, url=base_url + endpoint)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            result = _read_json_response(response)
    except urllib.error.HTTPError as error:
        detail = _http_error_detail(error)
        debug("realtime request failed", request_id=request_id, status=error.code, detail=detail)
        raise RuntimeError(f"JAVBeacon hook returned HTTP {error.code}: {detail}") from error
    except urllib.error.URLError as error:
        debug("realtime connection failed", request_id=request_id, error=error.reason)
        raise RuntimeError(f"could not reach JAVBeacon: {error.reason}") from error
    debug("realtime request completed", request_id=request_id, elapsed_ms=round((time.monotonic() - started) * 1000), result=result.get("state", "accepted"))
    return {"mode": mode, "request_id": request_id, "queued_scene_id": scene_id or None, "javbeacon": result}


def main():
    payload = json.load(sys.stdin)
    args = payload.get("args") or {}
    mode = str(args.get("mode") or "hook").strip().lower()
    if mode == "subtitles":
        output = request_subtitles(payload, args)
    else:
        output = request_realtime_sync(payload, args)
    return {"output": output}


if __name__ == "__main__":
    try:
        print(json.dumps(main()))
    except Exception as error:
        print(json.dumps({"error": str(error)}))
        sys.exit(1)
