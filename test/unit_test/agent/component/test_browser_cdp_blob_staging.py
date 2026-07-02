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

from types import SimpleNamespace

from agent.component import browser as browser_use_module
from agent.component.browser_cdp_blob_staging import (
    _build_trigger_download_js,
    _normalize_http_cdp_base,
    resolve_cdp_blob_staging_config,
    resolve_remote_upload_mode,
)


def test_resolve_remote_upload_mode_aliases():
    assert resolve_remote_upload_mode("auto") == "auto"
    assert resolve_remote_upload_mode("staging") == "staging"
    assert resolve_remote_upload_mode("blob_cdp") == "blob_cdp"
    assert resolve_remote_upload_mode("blob") == "blob_cdp"


def test_normalize_http_cdp_base_from_ws_url():
    assert (
        _normalize_http_cdp_base("ws://127.0.0.1:19080/devtools/browser/abc")
        == "http://127.0.0.1:19080"
    )


def test_resolve_cdp_blob_staging_config_defaults(monkeypatch):
    monkeypatch.delenv("RAGFLOW_BROWSER_CDP_BLOB_DOWNLOAD_DIR", raising=False)
    config = resolve_cdp_blob_staging_config("http://127.0.0.1:19080")
    assert config.cdp_url == "http://127.0.0.1:19080"
    assert config.chunk_chars == 40_000
    assert config.download_dir == r"C:\ProgramData\ragflow\browser-uploads"


def test_build_trigger_download_js_escapes_filename():
    import json

    filename = 'demo "1".pdf'
    js = _build_trigger_download_js(filename, "application/pdf")
    assert f"a.download = {json.dumps(filename)};" in js
    assert "application/pdf" in js


def test_effective_remote_upload_mode_auto_prefers_staging(monkeypatch):
    component = browser_use_module.Browser.__new__(browser_use_module.Browser)
    component._param = SimpleNamespace(
        use_cdp=True,
        cdp_url="http://127.0.0.1:19080",
        remote_staging_url="http://127.0.0.1:19080",
        remote_staging_token="",
        remote_upload_mode="auto",
    )
    monkeypatch.delenv("RAGFLOW_BROWSER_REMOTE_STAGING_URL", raising=False)
    assert component._resolve_effective_remote_upload_mode() == "staging"


def test_effective_remote_upload_mode_auto_falls_back_to_blob(monkeypatch):
    component = browser_use_module.Browser.__new__(browser_use_module.Browser)
    component._param = SimpleNamespace(
        use_cdp=True,
        cdp_url="http://127.0.0.1:19080",
        remote_staging_url="",
        remote_staging_token="",
        remote_upload_mode="auto",
    )
    monkeypatch.delenv("RAGFLOW_BROWSER_REMOTE_STAGING_URL", raising=False)
    assert component._resolve_effective_remote_upload_mode() == "blob_cdp"
