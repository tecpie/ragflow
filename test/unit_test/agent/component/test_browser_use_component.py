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

import asyncio
import json
import sys
import types
from pathlib import Path
from types import SimpleNamespace

from api.db import FileType


def _install_cv2_stub_if_unavailable():
    try:
        import cv2  # noqa: F401
        return
    except Exception:
        pass
    stub = types.ModuleType("cv2")
    stub.INTER_LINEAR = 1
    stub.INTER_CUBIC = 2
    stub.BORDER_CONSTANT = 0
    stub.BORDER_REPLICATE = 1

    def _module_getattr(name):
        if name.isupper():
            return 0
        raise RuntimeError("cv2 runtime call is unavailable in this test environment")

    stub.__getattr__ = _module_getattr
    sys.modules["cv2"] = stub


_install_cv2_stub_if_unavailable()

from agent.component import browser as browser_use_module  # noqa: E402


class _FakeCanvas:
    def __init__(self, refs=None):
        self._refs = refs or {}

    def is_reff(self, token):
        key = token.strip("{} ")
        return key in self._refs or token in self._refs

    def get_variable_value(self, token):
        key = token.strip("{} ")
        if key in self._refs:
            return self._refs[key]
        return self._refs[token]

    def get_tenant_id(self):
        return "tenant-1"


def _build_component():
    component = browser_use_module.Browser.__new__(browser_use_module.Browser)
    component._canvas = _FakeCanvas()
    component._param = SimpleNamespace(upload_sources="")
    return component


def test_prepare_input_values_records_variable_inputs():
    component = browser_use_module.Browser.__new__(browser_use_module.Browser)
    component._canvas = _FakeCanvas(refs={"sys.query": "open example.com"})
    component._param = browser_use_module.BrowserParam()
    component._param.prompts = "{sys.query}"
    component._param.inputs = {}

    component._prepare_input_values()

    assert component.get_input_value("sys.query") == "open example.com"
    assert component.get_input_values()["sys.query"] == "open example.com"


def test_extract_ids_supports_mixed_literals_and_variables():
    component = browser_use_module.Browser.__new__(browser_use_module.Browser)
    component._canvas = _FakeCanvas(
        refs={
            "{begin@file_ids}": ["f2", "f3,f4"],
            "{begin@extra_file}": "f5",
        }
    )

    file_ids = component._extract_ids("f1,{begin@file_ids},{begin@extra_file},f1")

    assert file_ids == ["f1", "f2", "f3", "f4", "f5"]


def test_extract_ids_supports_json_array_and_csv_format():
    component = browser_use_module.Browser.__new__(browser_use_module.Browser)
    component._canvas = _FakeCanvas()

    json_ids = component._extract_ids('["1","2"]')
    csv_ids = component._extract_ids("1,2")

    assert json_ids == ["1", "2"]
    assert csv_ids == ["1", "2"]


def test_extract_ids_supports_variable_values_from_input_or_globals():
    component = browser_use_module.Browser.__new__(browser_use_module.Browser)
    component._canvas = _FakeCanvas(
        refs={
            "{begin@upload_ids}": '["10","20"]',
            "{sys@upload_id}": 30,
            "{begin@file_obj}": {"id": "40", "name": "demo.pdf"},
        }
    )

    file_ids = component._extract_ids("{begin@upload_ids},{sys@upload_id},{begin@file_obj}")

    assert file_ids == ["10", "20", "30", "40"]


def test_extract_ids_supports_url_key_in_variable_object():
    component = browser_use_module.Browser.__new__(browser_use_module.Browser)
    component._canvas = _FakeCanvas(
        refs={
            "{begin@upload_url_obj}": {"url": "https://example.com/demo.pdf"},
        }
    )

    refs = component._extract_ids("{begin@upload_url_obj}")

    assert refs == ["https://example.com/demo.pdf"]


def test_extract_ids_does_not_split_http_url_by_comma():
    component = browser_use_module.Browser.__new__(browser_use_module.Browser)
    component._canvas = _FakeCanvas()

    refs = component._extract_ids("https://example.com/download?name=a,b.txt")

    assert refs == ["https://example.com/download?name=a,b.txt"]


def test_resolve_upload_source_items_preserves_session_file_object():
    component = _build_component()
    component._canvas = _FakeCanvas(
        refs={
            "{begin@file_obj}": {
                "id": "abc123uuid456789012345678901234",
                "name": "项目建议书.pdf",
                "created_by": "user-1",
            },
        }
    )
    component._param.upload_sources = "{begin@file_obj}"

    items = component._resolve_upload_source_items()

    assert len(items) == 1
    assert items[0]["kind"] == "session_blob"
    assert items[0]["file_id"] == "abc123uuid456789012345678901234"
    assert items[0]["name"] == "项目建议书.pdf"
    assert items[0]["created_by"] == "user-1"


def test_resolve_upload_source_items_resolves_id_ref_with_parent_name():
    component = _build_component()
    file_obj = {
        "id": "abc123uuid456789012345678901234",
        "name": "技改计划.docx",
        "created_by": "user-1",
    }
    component._canvas = _FakeCanvas(
        refs={
            "{begin@files}": [file_obj],
            "{begin@files.0.id}": file_obj["id"],
            "{begin@files.0}": file_obj,
        }
    )
    component._param.upload_sources = "{begin@files.0.id}"

    items = component._resolve_upload_source_items()

    assert len(items) == 1
    assert items[0]["kind"] == "session_blob"
    assert items[0]["name"] == "技改计划.docx"


def test_prepare_upload_files_uses_session_blob_name(monkeypatch, tmp_path):
    component = _build_component()
    component._param.upload_sources = {
        "id": "abc123uuid456789012345678901234",
        "name": "项目建议书.pdf",
        "created_by": "user-1",
    }

    monkeypatch.setattr(
        browser_use_module.FileService,
        "get_blob",
        lambda user_id, location: b"pdf-bytes" if user_id == "user-1" else None,
    )

    prepared = component._prepare_upload_files(str(tmp_path))

    assert len(prepared) == 1
    assert prepared[0]["name"] == "项目建议书.pdf"
    assert prepared[0]["local_path"].endswith("项目建议书.pdf")
    assert Path(prepared[0]["local_path"]).read_bytes() == b"pdf-bytes"


def test_resolve_original_filename_uses_document_name_when_file_name_is_uuid(monkeypatch):
    from api.db.services.document_service import DocumentService
    from api.db.services.file2document_service import File2DocumentService

    component = _build_component()
    opaque_id = "abc123uuid456789012345678901234"
    file = SimpleNamespace(name=opaque_id, location=opaque_id, type="pdf")

    class _FakeF2D:
        document_id = "doc-1"

    monkeypatch.setattr(File2DocumentService, "get_by_file_id", lambda _file_id: [_FakeF2D()])
    monkeypatch.setattr(
        DocumentService,
        "get_by_id",
        lambda _doc_id: (True, SimpleNamespace(name="真实文档.pdf")),
    )

    assert component._resolve_original_filename(file, opaque_id) == "真实文档.pdf"


def test_prepare_upload_files_supports_http_url(monkeypatch, tmp_path):
    component = _build_component()
    component._param.upload_sources = "https://example.com/files/demo.txt"

    class _FakeResponse:
        def __init__(self):
            self.headers = {"Content-Disposition": 'attachment; filename="remote_demo.txt"'}
            self._data = b"hello from url"
            self._pos = 0

        def read(self, size=-1):
            if size <= 0:
                chunk = self._data[self._pos :]
                self._pos = len(self._data)
                return chunk
            chunk = self._data[self._pos : self._pos + size]
            self._pos += len(chunk)
            return chunk

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc_val, exc_tb):
            return False

    monkeypatch.setattr(browser_use_module, "urlopen", lambda *_args, **_kwargs: _FakeResponse())

    prepared = component._prepare_upload_files(str(tmp_path))

    assert len(prepared) == 1
    assert prepared[0]["file_id"] == ""
    assert prepared[0]["name"] == "remote_demo.txt"
    assert prepared[0]["source_url"] == "https://example.com/files/demo.txt"
    assert Path(prepared[0]["local_path"]).exists()
    assert Path(prepared[0]["local_path"]).read_bytes() == b"hello from url"


def test_save_downloads_persists_file_records(monkeypatch, tmp_path):
    component = _build_component()
    component._canvas = _FakeCanvas()

    download_file = tmp_path / "report.txt"
    download_file.write_text("ok", encoding="utf-8")

    monkeypatch.setattr(
        browser_use_module.FileService,
        "get_by_id",
        lambda _folder_id: (True, SimpleNamespace(type=FileType.FOLDER.value)),
    )
    monkeypatch.setattr(browser_use_module, "duplicate_name", lambda *_args, **_kwargs: "report.txt")

    stored = {}

    def _put(parent_id, location, blob):
        stored["parent_id"] = parent_id
        stored["location"] = location
        stored["blob"] = blob

    monkeypatch.setattr(browser_use_module.settings, "STORAGE_IMPL", SimpleNamespace(put=_put))
    monkeypatch.setattr(
        browser_use_module.FileService,
        "insert",
        lambda data: SimpleNamespace(
            id="file-1",
            name=data["name"],
            size=data["size"],
            parent_id=data["parent_id"],
        ),
    )

    result = component._save_downloads(str(tmp_path), "dir-1")

    assert len(result) == 1
    assert result[0]["file_id"] == "file-1"
    assert result[0]["parent_id"] == "dir-1"
    assert stored["parent_id"] == "dir-1"
    assert stored["location"] == "report.txt"
    assert stored["blob"] == b"ok"
    assert Path(download_file).exists()


def test_run_browser_use_async_supports_cdp_connection(monkeypatch, tmp_path):
    component = _build_component()
    component._param = SimpleNamespace(
        max_steps=3,
        headless=True,
        enable_default_extensions=False,
        chromium_sandbox=False,
        use_cdp=True,
        cdp_url="ws://127.0.0.1:9222/devtools/browser/mock-id",
    )
    component._build_browser_llm = lambda: object()

    captured = {}

    class _FakeBrowser:
        def __init__(self, **kwargs):
            captured["browser_kwargs"] = kwargs

        def close(self):
            captured["browser_closed"] = True

    class _FakeAgent:
        def __init__(self, **kwargs):
            captured["agent_kwargs"] = kwargs

        async def run(self, **kwargs):
            captured["run_kwargs"] = kwargs
            return SimpleNamespace(final_result=lambda: "ok")

    fake_browser_use = types.ModuleType("browser_use")
    fake_browser_use.Agent = _FakeAgent
    fake_browser_use.Browser = _FakeBrowser
    monkeypatch.setitem(sys.modules, "browser_use", fake_browser_use)

    history = asyncio.run(component._run_browser_use_async("open ragflow.io", str(tmp_path)))

    assert history.final_result() == "ok"
    assert captured["browser_kwargs"]["cdp_url"] == "ws://127.0.0.1:9222/devtools/browser/mock-id"
    assert captured["browser_kwargs"]["downloads_path"] == str(tmp_path)
    assert "executable_path" not in captured["browser_kwargs"]
    assert captured["run_kwargs"]["max_steps"] == 3


def test_strip_think_tags_from_llm_output():
    raw = '<' + 'think>\n\n</think>\n{"action": "done"}'
    assert browser_use_module.strip_think_tags_from_llm_output(raw) == '{"action": "done"}'


def test_resolve_browser_enable_thinking_defaults_to_false():
    component = browser_use_module.Browser.__new__(browser_use_module.Browser)
    component._canvas = _FakeCanvas()
    component._canvas.globals = {}
    component._param = browser_use_module.BrowserParam()
    assert component._resolve_browser_enable_thinking() is False


def test_resolve_browser_enable_thinking_uses_agent_global():
    component = browser_use_module.Browser.__new__(browser_use_module.Browser)
    component._canvas = _FakeCanvas()
    component._canvas.globals = {"sys.enable_thinking": True}
    component._param = browser_use_module.BrowserParam()
    assert component._resolve_browser_enable_thinking() is True


def test_normalize_browser_llm_output_extracts_json_from_prose():
    raw = (
        "The user wants me to:\n"
        "1. open the page\n"
        '{"action": [{"done": {"text": "ok", "success": true}}]}'
    )
    normalized = browser_use_module.normalize_browser_llm_output_for_json(raw)
    assert json.loads(normalized) == {"action": [{"done": {"text": "ok", "success": True}}]}


def test_normalize_browser_llm_output_strips_markdown_fence():
    raw = 'The user requested me to finish.\n```json\n{"action": []}\n```'
    normalized = browser_use_module.normalize_browser_llm_output_for_json(raw)
    assert json.loads(normalized) == {"action": []}


def test_patch_browser_llm_client_strips_think_tags_and_extra_body():
    component = browser_use_module.Browser.__new__(browser_use_module.Browser)
    captured = {}

    class _FakeMessage:
        def __init__(self, content):
            self.content = content

    class _FakeChoice:
        def __init__(self, content):
            self.message = _FakeMessage(content)

    class _FakeResponse:
        def __init__(self, content):
            self.choices = [_FakeChoice(content)]

    class _FakeCompletions:
        async def create(self, **kwargs):
            captured["kwargs"] = kwargs
            return _FakeResponse('<' + 'think></think>\n{"ok": true}')

    class _FakeChat:
        completions = _FakeCompletions()

    class _FakeClient:
        chat = _FakeChat()

    class _FakeLLM:
        def get_client(self):
            return _FakeClient()

    llm = component._patch_browser_llm_client(_FakeLLM(), {"enable_thinking": False})
    response = asyncio.run(llm.get_client().chat.completions.create(model="qwen3-14b"))
    response_again = asyncio.run(llm.get_client().chat.completions.create(model="qwen3-14b"))

    assert captured["kwargs"]["extra_body"] == {"enable_thinking": False}
    assert json.loads(response.choices[0].message.content) == {"ok": True}
    assert json.loads(response_again.choices[0].message.content) == {"ok": True}
