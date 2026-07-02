#
#  Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
#
#  Licensed under the Apache License, Version 2.0 (the "License");
#  you may not use this file except in compliance with the License.
#  You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
#  Unless required by applicable law or agreed to in writing, software
#  distributed under the License is distributed on an "AS IS" BASIS,
#  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#  See the License for the specific language governing permissions and
#  limitations under the License.
#

import json
from types import SimpleNamespace

from agent.component import browser as browser_use_module
from agent.component import browser_remote_staging as staging_module
from agent.component.browser_remote_staging import (
    RemoteStagingClient,
    RemoteStagingConfig,
    resolve_remote_staging_config,
)


def test_resolve_remote_staging_config_prefers_param_over_env(monkeypatch):
    monkeypatch.setenv("RAGFLOW_BROWSER_REMOTE_STAGING_URL", "http://env-host:8765")
    monkeypatch.setenv("RAGFLOW_BROWSER_REMOTE_STAGING_TOKEN", "env-token")

    config = resolve_remote_staging_config("http://node-host:8765", "node-token")

    assert config is not None
    assert config.base_url == "http://node-host:8765"
    assert config.token == "node-token"


def test_remote_staging_client_uploads_file(monkeypatch, tmp_path):
    local_file = tmp_path / "demo.pdf"
    local_file.write_bytes(b"pdf-content")
    captured = {}

    class _FakeResponse:
        def __init__(self, payload: bytes):
            self._payload = payload

        def read(self):
            return self._payload

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc_val, exc_tb):
            return False

    def _fake_urlopen(req, timeout=0):
        captured["url"] = req.full_url
        captured["method"] = req.get_method()
        captured["headers"] = dict(req.header_items())
        captured["body"] = req.data
        payload = json.dumps(
            {
                "path": "/data/browser-uploads/session/demo.pdf",
                "name": "demo.pdf",
                "size": len(req.data or b""),
                "session_id": req.headers["X-staging-session"],
            }
        ).encode("utf-8")
        return _FakeResponse(payload)

    monkeypatch.setattr(staging_module, "urlopen", _fake_urlopen)

    client = RemoteStagingClient(
        RemoteStagingConfig(base_url="http://chrome-host:8765", token="secret", timeout=30)
    )
    result = client.upload_file(str(local_file), session_id="session-1")

    assert result.path == "/data/browser-uploads/session/demo.pdf"
    assert result.name == "demo.pdf"
    assert result.size == len(b"pdf-content")
    assert captured["method"] == "POST"
    assert captured["body"] == b"pdf-content"
    assert "Authorization" in captured["headers"]


def test_stage_upload_files_for_remote_browser(monkeypatch, tmp_path):
    component = browser_use_module.Browser.__new__(browser_use_module.Browser)
    component._param = SimpleNamespace(
        use_cdp=True,
        cdp_url="http://chrome-host:9222",
        remote_staging_url="http://chrome-host:8765",
        remote_staging_token="secret",
    )

    local_file = tmp_path / "report.txt"
    local_file.write_text("hello", encoding="utf-8")
    prepared = [{"file_id": "f1", "name": "report.txt", "local_path": str(local_file), "size": 5}]

    class _FakeClient:
        def health_check(self):
            return True

        def stage_prepared_files(self, files):
            assert files == prepared
            return [
                {
                    **files[0],
                    "local_path": "/data/browser-uploads/s1/report.txt",
                    "remote_path": "/data/browser-uploads/s1/report.txt",
                    "staging_session_id": "s1",
                }
            ]

    monkeypatch.setattr(
        browser_use_module,
        "RemoteStagingClient",
        lambda _config: _FakeClient(),
    )

    staged = component._stage_upload_files_for_remote_browser(prepared)

    assert staged[0]["remote_path"] == "/data/browser-uploads/s1/report.txt"
    assert staged[0]["local_path"] == "/data/browser-uploads/s1/report.txt"
