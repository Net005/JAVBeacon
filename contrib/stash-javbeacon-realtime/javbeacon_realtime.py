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


def _sidecar_json_path(scene_path):
    base, _ext = os.path.splitext(str(scene_path or ""))
    return base + ".en.srt.json"


def _read_subtitle_sidecar(scene_path):
    """Reads the .en.srt.json sidecar JAVBeacon-Subs writes next to a scene's
    video file, if any. A missing file, an unreadable file, or one that is
    not valid JSON are all treated the same as "no sidecar" by the caller:
    an older or manually placed subtitle JAVBeacon-Subs never version-
    stamped."""
    path = _sidecar_json_path(scene_path)
    try:
        with open(path, "r", encoding="utf-8") as handle:
            data = json.load(handle)
    except (OSError, json.JSONDecodeError):
        return None
    return data if isinstance(data, dict) else None


def _backends_endpoint(base_url):
    value = str(base_url or "").strip().rstrip("/")
    parsed = urllib.parse.urlparse(value)
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        raise RuntimeError("configure a valid JAVBeacon-Subs base URL in Settings > Plugins")
    if parsed.username or parsed.password:
        raise RuntimeError("JAVBeacon-Subs base URL must not contain credentials")
    if value.endswith("/api/v1/backends"):
        return value
    if value.endswith("/api/v1/jobs"):
        value = value[: -len("/api/v1/jobs")]
    return value + "/api/v1/backends"


def _current_subtitle_backends(settings, timeout):
    """Asks JAVBeacon-Subs which transcription/translation backend it
    currently runs new jobs with, so an existing sidecar's recorded backend
    can be compared against it. Returns None - never raises - when the
    request fails for any reason: an older JAVBeacon-Subs release built
    before this endpoint existed, a network hiccup, or an unexpected
    response shape. Callers treat a None result as "freshness unknown" and
    fall back to the plain confirmation prompt instead of blocking the
    subtitle request on an optional check."""
    token = str(settings.get("subs_api_token") or "").strip()
    if not token:
        return None
    try:
        endpoint = _backends_endpoint(settings.get("subs_base_url"))
    except RuntimeError:
        return None
    request = urllib.request.Request(
        endpoint,
        headers={"Authorization": "Bearer " + token},
        method="GET",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            result = _read_json_response(response)
    except (urllib.error.HTTPError, urllib.error.URLError, RuntimeError):
        return None
    if not isinstance(result, dict):
        return None
    transcription = str(result.get("transcription_backend") or "").strip()
    translation = str(result.get("translation_backend") or "").strip()
    if not transcription or not translation:
        return None
    return {"transcription_backend": transcription, "translation_backend": translation}


def subtitle_status(payload, args):
    """Compares a scene's existing .en.srt.json sidecar (if any) against the
    transcription/translation backend JAVBeacon-Subs currently uses for new
    jobs, so the scene button can ask a version-aware overwrite question
    instead of a blind confirmation. See the README for the three cases this
    produces (no sidecar, outdated sidecar, up-to-date sidecar)."""
    scene_id = str(args.get("scene_id") or "").strip()
    if not scene_id:
        raise RuntimeError("subtitle status check did not include a scene ID")
    scene_path, settings = _scene_and_subs_settings(payload, scene_id)
    timeout = max(1, int(_setting(settings, "subs_timeout_seconds", 30)))

    sidecar = _read_subtitle_sidecar(scene_path)
    if sidecar is None:
        return {
            "mode": "subtitle_status",
            "scene_id": scene_id,
            "sidecar_found": False,
            "up_to_date": False,
            "reason": "no_sidecar",
        }

    sidecar_backends = {
        "transcription_backend": str(sidecar.get("transcription_backend") or "").strip(),
        "translation_backend": str(sidecar.get("translation_backend") or "").strip(),
    }
    current = _current_subtitle_backends(settings, timeout)
    if current is None:
        return {
            "mode": "subtitle_status",
            "scene_id": scene_id,
            "sidecar_found": True,
            "up_to_date": None,
            "reason": "current_backend_unknown",
            "sidecar_backends": sidecar_backends,
        }

    up_to_date = (
        sidecar_backends["transcription_backend"] == current["transcription_backend"]
        and sidecar_backends["translation_backend"] == current["translation_backend"]
    )
    return {
        "mode": "subtitle_status",
        "scene_id": scene_id,
        "sidecar_found": True,
        "up_to_date": up_to_date,
        "reason": "up_to_date" if up_to_date else "outdated",
        "sidecar_backends": sidecar_backends,
        "current_backends": current,
    }


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
    if "overwrite" in args:
        if not isinstance(args["overwrite"], bool):
            raise RuntimeError("subtitle overwrite choice must be true or false")
        body["overwrite"] = args["overwrite"]
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
    hook_input = hook.get("input") if isinstance(hook.get("input"), dict) else {}
    scene_id = str(
        hook.get("id")
        or hook.get("scene_id")
        or hook_input.get("id")
        or args.get("scene_id")
        or ""
    ).strip()
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


def request_release_link(payload, args):
    scene_id = str(args.get("scene_id") or "").strip()
    if not scene_id:
        raise RuntimeError("release link request did not include a scene ID")
    settings = _plugin_settings(payload)
    base_url = str(settings.get("javbeacon_url") or "").strip().rstrip("/")
    browser_url = str(settings.get("javbeacon_browser_url") or base_url).strip().rstrip("/")
    secret = str(settings.get("webhook_secret") or "").strip()
    timeout = max(1, int(_setting(settings, "timeout_seconds", 10)))
    for name, value in (("JAVBeacon URL", base_url), ("JAVBeacon browser URL", browser_url)):
        parsed = urllib.parse.urlparse(value)
        if parsed.scheme not in ("http", "https") or not parsed.netloc or parsed.username or parsed.password:
            raise RuntimeError(f"configure a valid {name} in Settings > Plugins")
    if not secret:
        raise RuntimeError("configure the JAVBeacon webhook secret in Settings > Plugins")

    request = urllib.request.Request(
        base_url + "/api/hooks/stash/release-link",
        data=json.dumps({"scene_id": scene_id}).encode(),
        headers={"Authorization": "Bearer " + secret, "Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            result = _read_json_response(response)
    except urllib.error.HTTPError as error:
        detail = _http_error_detail(error)
        raise RuntimeError(f"JAVBeacon release lookup returned HTTP {error.code}: {detail}") from error
    except urllib.error.URLError as error:
        raise RuntimeError(f"could not reach JAVBeacon: {error.reason}") from error

    release_path = str(result.get("release_path") or "").strip()
    if not release_path.startswith("/release/"):
        raise RuntimeError("JAVBeacon returned an invalid release link")
    return {
        "mode": "release_link",
        "scene_id": scene_id,
        "release_id": result.get("release_id"),
        "video_id": result.get("video_id"),
        "url": browser_url + release_path,
    }


def main():
    payload = json.load(sys.stdin)
    args = payload.get("args") or {}
    mode = str(args.get("mode") or "hook").strip().lower()
    if mode == "subtitles":
        output = request_subtitles(payload, args)
    elif mode == "subtitle_status":
        output = subtitle_status(payload, args)
    elif mode == "release_link":
        output = request_release_link(payload, args)
    else:
        output = request_realtime_sync(payload, args)
    return {"output": output}


if __name__ == "__main__":
    try:
        print(json.dumps(main()))
    except Exception as error:
        print(json.dumps({"error": str(error)}))
        sys.exit(1)
