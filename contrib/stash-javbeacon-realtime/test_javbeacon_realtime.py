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


if __name__ == "__main__":
    unittest.main()
