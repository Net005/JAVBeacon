import json
import unittest
from unittest import mock

import javbeacon_realtime as plugin


class FakeResponse:
    def __init__(self, body):
        self.body = json.dumps(body).encode()

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return False

    def read(self):
        return self.body


class SubtitleRequestTests(unittest.TestCase):
    @mock.patch.object(plugin.urllib.request, "urlopen")
    def test_scene_path_and_settings_are_resolved_through_stash(self, urlopen):
        urlopen.return_value = FakeResponse(
            {
                "data": {
                    "findScene": {
                        "files": [{"path": "/collections/jav/NSPS-642.mp4"}]
                    },
                    "configuration": {
                        "plugins": {
                            "javbeacon-realtime": {
                                "subs_base_url": "https://subs.example.com"
                            }
                        }
                    },
                }
            }
        )
        payload = {
            "server_connection": {
                "Scheme": "http",
                "Host": "127.0.0.1",
                "Port": 9999,
                "SessionCookie": {"Name": "session", "Value": "cookie-value"},
            }
        }

        scene_path, settings = plugin._scene_and_subs_settings(payload, "39381")

        request = urlopen.call_args.args[0]
        request_body = json.loads(request.data)
        self.assertEqual(request.full_url, "http://127.0.0.1:9999/graphql")
        self.assertEqual(request.get_header("Cookie"), "session=cookie-value")
        self.assertEqual(request_body["variables"], {"id": "39381"})
        self.assertEqual(scene_path, "/collections/jav/NSPS-642.mp4")
        self.assertEqual(settings["subs_base_url"], "https://subs.example.com")

    def test_default_payload_matches_javbeacon_subs_request(self):
        body = plugin._subtitle_body("/collections/jav/NSPS-642.mp4", {})

        self.assertEqual(
            body,
            {
                "inputs": ["/collections/jav/NSPS-642.mp4"],
                "recursive": False,
                "overwrite": False,
                "auto_detect_release": True,
                "release_within_days": 0,
                "debug_mode": True,
                "keep_japanese": True,
                "write_ass": False,
            },
        )

    def test_individual_settings_and_json_options_are_applied(self):
        settings = {
            "subs_recursive": True,
            "subs_overwrite": True,
            "subs_keep_japanese": False,
            "subs_job_options": json.dumps(
                {
                    "inputs": ["/wrong/file.mp4"],
                    "language": "en",
                    "auto_detect_release": False,
                }
            )
        }

        body = plugin._subtitle_body("/media/right.mp4", settings)

        self.assertEqual(body["inputs"], ["/media/right.mp4"])
        self.assertTrue(body["recursive"])
        self.assertTrue(body["overwrite"])
        self.assertFalse(body["keep_japanese"])
        self.assertFalse(body["auto_detect_release"])
        self.assertEqual(body["language"], "en")

    def test_jobs_endpoint_accepts_base_or_full_endpoint(self):
        self.assertEqual(
            plugin._jobs_endpoint("https://subs.example.com/"),
            "https://subs.example.com/api/v1/jobs",
        )
        self.assertEqual(
            plugin._jobs_endpoint("https://subs.example.com/api/v1/jobs"),
            "https://subs.example.com/api/v1/jobs",
        )

    def test_backends_endpoint_accepts_base_jobs_or_full_endpoint(self):
        self.assertEqual(
            plugin._backends_endpoint("https://subs.example.com/"),
            "https://subs.example.com/api/v1/backends",
        )
        self.assertEqual(
            plugin._backends_endpoint("https://subs.example.com/api/v1/jobs"),
            "https://subs.example.com/api/v1/backends",
        )
        self.assertEqual(
            plugin._backends_endpoint("https://subs.example.com/api/v1/backends"),
            "https://subs.example.com/api/v1/backends",
        )

    def test_sidecar_json_path_replaces_video_extension(self):
        self.assertEqual(
            plugin._sidecar_json_path("/collections/jav/NSPS-642.mp4"),
            "/collections/jav/NSPS-642.en.srt.json",
        )

    @mock.patch.object(plugin, "_current_subtitle_backends")
    @mock.patch.object(plugin, "_read_subtitle_sidecar")
    @mock.patch.object(plugin, "_scene_and_subs_settings")
    def test_subtitle_status_treats_missing_sidecar_as_outdated(
        self, scene_settings, read_sidecar, current_backends
    ):
        scene_settings.return_value = ("/collections/jav/NSPS-642.mp4", {})
        read_sidecar.return_value = None

        result = plugin.subtitle_status({}, {"scene_id": "39381"})

        self.assertEqual(
            result,
            {
                "mode": "subtitle_status",
                "scene_id": "39381",
                "sidecar_found": False,
                "up_to_date": False,
                "reason": "no_sidecar",
            },
        )
        current_backends.assert_not_called()

    @mock.patch.object(plugin, "_current_subtitle_backends")
    @mock.patch.object(plugin, "_read_subtitle_sidecar")
    @mock.patch.object(plugin, "_scene_and_subs_settings")
    def test_subtitle_status_reports_outdated_when_backends_differ(
        self, scene_settings, read_sidecar, current_backends
    ):
        scene_settings.return_value = ("/collections/jav/NSPS-642.mp4", {})
        read_sidecar.return_value = {
            "transcription_backend": "whisper-large-v2",
            "translation_backend": "gpt-4o-mini",
        }
        current_backends.return_value = {
            "transcription_backend": "Qwen/Qwen3-ASR-1.7B",
            "translation_backend": "gpt-5.6-luna",
        }

        result = plugin.subtitle_status({}, {"scene_id": "39381"})

        self.assertFalse(result["up_to_date"])
        self.assertEqual(result["reason"], "outdated")
        self.assertTrue(result["sidecar_found"])
        self.assertEqual(
            result["current_backends"],
            {
                "transcription_backend": "Qwen/Qwen3-ASR-1.7B",
                "translation_backend": "gpt-5.6-luna",
            },
        )

    @mock.patch.object(plugin, "_current_subtitle_backends")
    @mock.patch.object(plugin, "_read_subtitle_sidecar")
    @mock.patch.object(plugin, "_scene_and_subs_settings")
    def test_subtitle_status_reports_up_to_date_when_backends_match(
        self, scene_settings, read_sidecar, current_backends
    ):
        scene_settings.return_value = ("/collections/jav/NSPS-642.mp4", {})
        read_sidecar.return_value = {
            "transcription_backend": "Qwen/Qwen3-ASR-1.7B",
            "translation_backend": "gpt-5.6-luna",
        }
        current_backends.return_value = {
            "transcription_backend": "Qwen/Qwen3-ASR-1.7B",
            "translation_backend": "gpt-5.6-luna",
        }

        result = plugin.subtitle_status({}, {"scene_id": "39381"})

        self.assertTrue(result["up_to_date"])
        self.assertEqual(result["reason"], "up_to_date")

    @mock.patch.object(plugin, "_current_subtitle_backends")
    @mock.patch.object(plugin, "_read_subtitle_sidecar")
    @mock.patch.object(plugin, "_scene_and_subs_settings")
    def test_subtitle_status_ignores_revision_pin_when_model_matches(
        self, scene_settings, read_sidecar, current_backends
    ):
        # Same model, different HuggingFace revision hash - not a real
        # backend change, so this must still count as up to date.
        scene_settings.return_value = ("/collections/jav/NSPS-642.mp4", {})
        read_sidecar.return_value = {
            "transcription_backend": "Qwen/Qwen3-ASR-1.7B@old-commit-hash",
            "translation_backend": "gpt-6-luna",
        }
        current_backends.return_value = {
            "transcription_backend": "Qwen/Qwen3-ASR-1.7B@7278e1e70fe206f11671096ffdd38061171dd6e5",
            "translation_backend": "gpt-6-luna",
        }

        result = plugin.subtitle_status({}, {"scene_id": "39381"})

        self.assertTrue(result["up_to_date"])
        self.assertEqual(result["reason"], "up_to_date")

    @mock.patch.object(plugin, "_current_subtitle_backends")
    @mock.patch.object(plugin, "_read_subtitle_sidecar")
    @mock.patch.object(plugin, "_scene_and_subs_settings")
    def test_subtitle_status_when_current_backend_cannot_be_determined(
        self, scene_settings, read_sidecar, current_backends
    ):
        scene_settings.return_value = ("/collections/jav/NSPS-642.mp4", {})
        read_sidecar.return_value = {
            "transcription_backend": "whisper-large-v2",
            "translation_backend": "gpt-4o-mini",
        }
        current_backends.return_value = None

        result = plugin.subtitle_status({}, {"scene_id": "39381"})

        self.assertIsNone(result["up_to_date"])
        self.assertEqual(result["reason"], "current_backend_unknown")

    @mock.patch.object(plugin.urllib.request, "urlopen")
    def test_current_subtitle_backends_returns_none_on_http_error(self, urlopen):
        urlopen.side_effect = plugin.urllib.error.HTTPError(
            "https://subs.example.com/api/v1/backends", 404, "Not Found", {}, None
        )

        result = plugin._current_subtitle_backends(
            {"subs_base_url": "https://subs.example.com", "subs_api_token": "secret-token"},
            10,
        )

        self.assertIsNone(result)

    @mock.patch.object(plugin.urllib.request, "urlopen")
    def test_current_subtitle_backends_parses_a_valid_response(self, urlopen):
        urlopen.return_value = FakeResponse(
            {
                "transcription_backend": "Qwen/Qwen3-ASR-1.7B",
                "translation_backend": "gpt-5.6-luna",
            }
        )

        result = plugin._current_subtitle_backends(
            {"subs_base_url": "https://subs.example.com", "subs_api_token": "secret-token"},
            10,
        )

        request = urlopen.call_args.args[0]
        self.assertEqual(request.full_url, "https://subs.example.com/api/v1/backends")
        self.assertEqual(request.get_header("Authorization"), "Bearer secret-token")
        self.assertEqual(
            result,
            {
                "transcription_backend": "Qwen/Qwen3-ASR-1.7B",
                "translation_backend": "gpt-5.6-luna",
            },
        )

    def test_scene_path_filters_are_partial_and_case_insensitive(self):
        settings = {
            "subs_scene_path_filters": "/OTHER/PATH; /collections/JAV\n/archive"
        }

        self.assertTrue(
            plugin._scene_path_matches("/Collections/jav/NSPS-642.mp4", settings)
        )
        self.assertFalse(plugin._scene_path_matches("/media/NSPS-642.mp4", settings))
        self.assertTrue(plugin._scene_path_matches("/media/NSPS-642.mp4", {}))

    @mock.patch.object(plugin, "_scene_and_subs_settings")
    @mock.patch.object(plugin.urllib.request, "urlopen")
    def test_request_rejects_scene_outside_path_filters(self, urlopen, scene_settings):
        scene_settings.return_value = (
            "/media/NSPS-642.mp4",
            {
                "subs_scene_path_filters": "/collections/jav/",
                "subs_base_url": "https://subs.example.com",
                "subs_api_token": "secret-token",
            },
        )

        with self.assertRaisesRegex(RuntimeError, "does not match"):
            plugin.request_subtitles({}, {"scene_id": "39381"})
        urlopen.assert_not_called()

    @mock.patch.object(plugin, "_scene_and_subs_settings")
    @mock.patch.object(plugin.urllib.request, "urlopen")
    def test_request_uses_bearer_token_and_scene_path(self, urlopen, scene_settings):
        scene_settings.return_value = (
            "/collections/jav/NSPS-642.mp4",
            {
                "subs_base_url": "https://subs.example.com",
                "subs_api_token": "secret-token",
                "subs_timeout_seconds": 12,
            },
        )
        urlopen.return_value = FakeResponse({"id": "job-123"})

        result = plugin.request_subtitles({}, {"scene_id": "39381"})

        request = urlopen.call_args.args[0]
        body = json.loads(request.data)
        self.assertEqual(request.full_url, "https://subs.example.com/api/v1/jobs")
        self.assertEqual(request.get_header("Authorization"), "Bearer secret-token")
        self.assertEqual(request.get_method(), "POST")
        self.assertEqual(body["inputs"], ["/collections/jav/NSPS-642.mp4"])
        self.assertEqual(urlopen.call_args.kwargs["timeout"], 12)
        self.assertEqual(result["filename"], "NSPS-642.mp4")
        self.assertEqual(result["javbeacon_subs"], {"id": "job-123"})

    @mock.patch.object(plugin, "_scene_and_subs_settings")
    @mock.patch.object(plugin.urllib.request, "urlopen")
    def test_confirmed_overwrite_takes_precedence_over_job_defaults(self, urlopen, scene_settings):
        scene_settings.return_value = (
            "/collections/jav/NSPS-642.mp4",
            {
                "subs_base_url": "https://subs.example.com",
                "subs_api_token": "secret-token",
                "subs_job_options": '{"overwrite": false}',
            },
        )
        urlopen.return_value = FakeResponse({"id": "job-123"})

        plugin.request_subtitles({}, {"scene_id": "39381", "overwrite": True})
        self.assertTrue(json.loads(urlopen.call_args.args[0].data)["overwrite"])

        plugin.request_subtitles({}, {"scene_id": "39381", "overwrite": False})
        self.assertFalse(json.loads(urlopen.call_args.args[0].data)["overwrite"])

        with self.assertRaisesRegex(RuntimeError, "overwrite choice"):
            plugin.request_subtitles({}, {"scene_id": "39381", "overwrite": "true"})
        self.assertEqual(urlopen.call_count, 2)

    @mock.patch.object(plugin, "_plugin_settings")
    @mock.patch.object(plugin.urllib.request, "urlopen")
    def test_realtime_sync_uses_plugin_ui_settings(self, urlopen, plugin_settings):
        plugin_settings.return_value = {
            "javbeacon_url": "http://javbeacon:8080/",
            "webhook_secret": "hook-secret",
            "timeout_seconds": 14,
        }
        urlopen.return_value = FakeResponse({"state": "queued"})

        result = plugin.request_realtime_sync(
            {"server_connection": {}},
            {
                "mode": "hook",
                "hookContext": {"id": "39381", "type": "Scene.Update.Post"},
            },
        )

        request = urlopen.call_args.args[0]
        body = json.loads(request.data)
        self.assertEqual(
            request.full_url, "http://javbeacon:8080/api/hooks/stash/scene"
        )
        self.assertEqual(request.get_header("Authorization"), "Bearer hook-secret")
        self.assertEqual(urlopen.call_args.kwargs["timeout"], 14)
        self.assertEqual(
            body,
            {
                "event": "Scene.Update.Post",
                "request_id": result["request_id"],
                "scene_id": "39381",
            },
        )

    @mock.patch.object(plugin, "_plugin_settings")
    @mock.patch.object(plugin.urllib.request, "urlopen")
    def test_realtime_sync_accepts_nested_hook_scene_id(self, urlopen, plugin_settings):
        plugin_settings.return_value = {
            "javbeacon_url": "http://javbeacon:8080",
            "webhook_secret": "hook-secret",
        }
        urlopen.return_value = FakeResponse({"state": "queued"})

        plugin.request_realtime_sync(
            {},
            {
                "mode": "hook",
                "hookContext": {
                    "input": {"id": "39381"},
                    "type": "Scene.Create.Post",
                },
            },
        )

        request = urlopen.call_args.args[0]
        self.assertEqual(json.loads(request.data)["scene_id"], "39381")

    @mock.patch.object(plugin, "_plugin_settings", return_value={})
    def test_realtime_sync_requires_plugin_ui_settings(self, _plugin_settings):
        with self.assertRaisesRegex(RuntimeError, "Settings > Plugins"):
            plugin.request_realtime_sync(
                {},
                {"mode": "hook", "hookContext": {"id": "39381"}},
            )

    @mock.patch.object(plugin, "_plugin_settings")
    @mock.patch.object(plugin.urllib.request, "urlopen")
    def test_release_link_uses_browser_url_and_authenticated_lookup(self, urlopen, plugin_settings):
        plugin_settings.return_value = {
            "javbeacon_url": "http://javbeacon:8080/",
            "javbeacon_browser_url": "https://jav.example.com/",
            "webhook_secret": "hook-secret",
            "timeout_seconds": 9,
        }
        urlopen.return_value = FakeResponse({
            "release_id": 398721,
            "release_path": "/release/398721",
            "video_id": "NSPS-605",
        })

        result = plugin.request_release_link({}, {"scene_id": "39382"})

        request = urlopen.call_args.args[0]
        self.assertEqual(request.full_url, "http://javbeacon:8080/api/hooks/stash/release-link")
        self.assertEqual(request.get_header("Authorization"), "Bearer hook-secret")
        self.assertEqual(json.loads(request.data), {"scene_id": "39382"})
        self.assertEqual(urlopen.call_args.kwargs["timeout"], 9)
        self.assertEqual(result["url"], "https://jav.example.com/release/398721")
        self.assertEqual(result["video_id"], "NSPS-605")


if __name__ == "__main__":
    unittest.main()
