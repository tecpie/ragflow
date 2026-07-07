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
    staging_base_url_from_cdp,
)


def test_resolve_remote_staging_config_prefers_param_over_env(monkeypatch):
    monkeypatch.setenv("RAGFLOW_BROWSER_REMOTE_STAGING_URL", "http://env-host:8765")
    monkeypatch.setenv("RAGFLOW_BROWSER_REMOTE_STAGING_TOKEN", "env-token")

    config = resolve_remote_staging_config("http://node-host:8765", "node-token")

    assert config is not None
    assert config.base_url == "http://node-host:8765"
    assert config.token == "node-token"


def test_resolve_remote_staging_config_uses_cdp_url_before_env(monkeypatch):
    monkeypatch.setenv("RAGFLOW_BROWSER_REMOTE_STAGING_URL", "http://172.16.0.118:19080")

    config = resolve_remote_staging_config("", "", cdp_url_fallback="http://172.20.10.2:19080")

    assert config is not None
    assert config.base_url == "http://172.20.10.2:19080"


def test_staging_base_url_from_cdp_supports_ws_gateway():
    assert staging_base_url_from_cdp("ws://172.20.10.2:19080/devtools/browser/abc") == "http://172.20.10.2:19080"
    assert staging_base_url_from_cdp("http://172.20.10.2:19080") == "http://172.20.10.2:19080"


def test_normalize_upload_filename_strips_uuid_prefix():
    from agent.component.browser_remote_staging import normalize_upload_filename

    opaque = "a53d5dc6761211f1918e69d7ac1cc180"
    assert (
        normalize_upload_filename(f"{opaque}_项目建议书.docx")
        == "项目建议书.docx"
    )
    assert normalize_upload_filename("项目建议书.docx") == "项目建议书.docx"


def test_safe_filename_preserves_unicode_display_name():
    from agent.component.browser_remote_staging import _safe_filename

    assert _safe_filename("项目建议书.docx") == "项目建议书.docx"
    assert _safe_filename("docx.pdf") == "docx.pdf"
    assert _safe_filename("../evil/docx.pdf") == "docx.pdf"


def test_remote_staging_client_uploads_with_original_filename(monkeypatch, tmp_path):
    local_file = tmp_path / "temp.bin"
    local_file.write_bytes(b"pdf-content")
    captured = {}

    class _FakeResponse:
        def read(self):
            return json.dumps(
                {
                    "path": r"C:\ProgramData\ragflow\browser-uploads\项目建议书.docx",
                    "name": "项目建议书.docx",
                    "size": 11,
                    "session_id": "s1",
                }
            ).encode("utf-8")

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc_val, exc_tb):
            return False

    def _fake_urlopen(req, timeout=0):
        captured["url"] = req.full_url
        return _FakeResponse()

    monkeypatch.setattr(staging_module, "urlopen", _fake_urlopen)
    client = RemoteStagingClient(RemoteStagingConfig(base_url="http://chrome-host:8765"))
    result = client.upload_file(str(local_file), session_id="s1", filename="项目建议书.docx")

    assert "filename=%E9%A1%B9%E7%9B%AE%E5%BB%BA%E8%AE%AE%E4%B9%A6.docx" in captured["url"]
    assert result.name == "项目建议书.docx"


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
                "path": "/data/browser-uploads/demo.pdf",
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

    assert result.path == "/data/browser-uploads/demo.pdf"
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
                    "local_path": "/data/browser-uploads/report.txt",
                    "remote_path": "/data/browser-uploads/report.txt",
                    "staging_session_id": "s1",
                }
            ]

    monkeypatch.setattr(
        browser_use_module,
        "RemoteStagingClient",
        lambda _config: _FakeClient(),
    )

    staged = component._stage_upload_files_for_remote_browser(prepared)

    assert staged[0]["remote_path"] == "/data/browser-uploads/report.txt"
    assert staged[0]["local_path"] == "/data/browser-uploads/report.txt"
