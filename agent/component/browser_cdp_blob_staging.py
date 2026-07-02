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

from __future__ import annotations

import asyncio
import base64
import contextlib
import json
import logging
import mimetypes
import os
import re
from dataclasses import dataclass, field
from typing import Any
from urllib.parse import urlparse

import aiohttp

from agent.component.browser_remote_staging import RemoteStagingError, _safe_filename
from common.misc_utils import get_uuid

_CHUNK_VAR = "__ragflow_upload_chunks"


_DEFAULT_REMOTE_DOWNLOAD_DIR = r"C:\ProgramData\ragflow\browser-uploads"


@dataclass(frozen=True)
class CdpBlobStagingConfig:
    cdp_url: str
    download_dir: str
    timeout: float = 120.0
    max_bytes: int = 100 * 1024 * 1024
    chunk_chars: int = 40_000
    require_download_event: bool = False


@dataclass
class _DownloadTracker:
    expected_bytes: int = 0
    completed_path: str = ""
    canceled: bool = False
    done: asyncio.Event = field(default_factory=asyncio.Event)


class CdpBlobStagingError(RemoteStagingError):
    pass


def resolve_remote_upload_mode(param_mode: str = "") -> str:
    raw = str(param_mode or os.getenv("RAGFLOW_BROWSER_REMOTE_UPLOAD_MODE", "auto") or "auto").strip().lower()
    if raw in {"auto", "staging", "blob_cdp", "blob", "cdp_blob"}:
        return "blob_cdp" if raw in {"blob_cdp", "blob", "cdp_blob"} else raw
    return "auto"


def resolve_cdp_blob_staging_config(cdp_url: str) -> CdpBlobStagingConfig:
    token = str(cdp_url or "").strip().rstrip("/")
    if not token:
        raise CdpBlobStagingError("CDP URL is required for blob staging")

    download_dir = str(os.getenv("RAGFLOW_BROWSER_CDP_BLOB_DOWNLOAD_DIR", "") or "").strip()
    if not download_dir:
        # Blob staging targets remote Chrome (typically Windows). Do not infer from RAGFlow container OS.
        download_dir = _DEFAULT_REMOTE_DOWNLOAD_DIR

    timeout = 120.0
    max_bytes = 100 * 1024 * 1024
    chunk_chars = 40_000
    require_download_event = True
    timeout_raw = str(os.getenv("RAGFLOW_BROWSER_CDP_BLOB_TIMEOUT", "") or "").strip()
    max_bytes_raw = str(os.getenv("RAGFLOW_BROWSER_CDP_BLOB_MAX_BYTES", "") or os.getenv("RAGFLOW_BROWSER_REMOTE_STAGING_MAX_BYTES", "") or "").strip()
    chunk_raw = str(os.getenv("RAGFLOW_BROWSER_CDP_BLOB_CHUNK_CHARS", "") or "").strip()
    require_event_raw = str(os.getenv("RAGFLOW_BROWSER_CDP_BLOB_REQUIRE_DOWNLOAD_EVENT", "") or "").strip().lower()
    if timeout_raw:
        try:
            timeout = max(5.0, float(timeout_raw))
        except (TypeError, ValueError):
            pass
    if max_bytes_raw:
        try:
            parsed = int(max_bytes_raw)
            if parsed > 0:
                max_bytes = parsed
        except (TypeError, ValueError):
            pass
    if chunk_raw:
        try:
            parsed = int(chunk_raw)
            if parsed > 0:
                chunk_chars = parsed
        except (TypeError, ValueError):
            pass
    if require_event_raw in {"0", "false", "no", "off"}:
        require_download_event = False

    return CdpBlobStagingConfig(
        cdp_url=token,
        download_dir=_normalize_remote_download_dir(download_dir),
        timeout=timeout,
        max_bytes=max_bytes,
        chunk_chars=chunk_chars,
        require_download_event=require_download_event,
    )


def _normalize_http_cdp_base(cdp_url: str) -> str:
    parsed = urlparse(str(cdp_url or "").strip())
    if parsed.scheme in {"http", "https"}:
        return str(cdp_url).strip().rstrip("/")
    if parsed.scheme in {"ws", "wss"}:
        # Convert ws://host:9222/devtools/browser/<id> -> http://host:9222
        port = parsed.port or (443 if parsed.scheme == "wss" else 80)
        scheme = "https" if parsed.scheme == "wss" else "http"
        return f"{scheme}://{parsed.hostname}:{port}"
    raise CdpBlobStagingError(f"unsupported CDP URL for blob staging: {cdp_url}")


def _normalize_remote_download_dir(path: str) -> str:
    token = str(path or "").strip().strip('"').strip("'")
    if not token:
        return _DEFAULT_REMOTE_DOWNLOAD_DIR
    token = token.replace("/", "\\")
    if re.match(r"^[A-Za-z]:\\", token):
        return token.rstrip("\\")
    return token.rstrip("\\")


def _join_remote_win_path(base: str, *parts: str) -> str:
    segments = [_normalize_remote_download_dir(base)]
    for part in parts:
        token = str(part or "").strip().strip("\\/")
        if token:
            segments.append(token)
    return "\\".join(segments)


def _cdp_path_for_chrome(path: str) -> str:
    token = _normalize_remote_download_dir(path)
    if re.match(r"^[A-Za-z]:\\", token):
        return token
    return token.replace("/", "\\")


async def _fetch_json(session: aiohttp.ClientSession, url: str, timeout: float) -> Any:
    async with session.get(url, timeout=aiohttp.ClientTimeout(total=timeout)) as response:
        response.raise_for_status()
        return await response.json()


async def _resolve_browser_ws_url(http_base: str, timeout: float) -> str:
    async with aiohttp.ClientSession() as session:
        version = await _fetch_json(session, f"{http_base}/json/version", timeout)
        ws_url = str(version.get("webSocketDebuggerUrl") or "").strip()
        if not ws_url:
            raise CdpBlobStagingError(f"CDP /json/version missing webSocketDebuggerUrl: {http_base}")
        return ws_url


async def _pick_page_target_id(session: aiohttp.ClientSession, http_base: str, timeout: float) -> str:
    targets = await _fetch_json(session, f"{http_base}/json/list", timeout)
    if not isinstance(targets, list):
        raise CdpBlobStagingError(f"unexpected /json/list response: {targets!r}")

    page_targets = [item for item in targets if isinstance(item, dict) and item.get("type") == "page"]
    if not page_targets:
        raise CdpBlobStagingError("no page targets available for blob staging; open a tab in remote Chrome first")

    def score(item: dict[str, Any]) -> tuple[int, str]:
        url = str(item.get("url") or "")
        if url.startswith("http://") or url.startswith("https://"):
            return (0, url)
        if url.startswith("about:blank"):
            return (1, url)
        return (2, url)

    page_targets.sort(key=score)
    target_id = str(page_targets[0].get("id") or "").strip()
    if not target_id:
        raise CdpBlobStagingError("failed to resolve a page target id from /json/list")
    return target_id


class _CdpWebSocketSession:
    def __init__(self, ws: aiohttp.ClientWebSocketResponse, timeout: float):
        self._ws = ws
        self._timeout = timeout
        self._next_id = 0
        self._pending: dict[int, asyncio.Future] = {}
        self._event_handlers: list[Any] = []
        self._reader_task: asyncio.Task | None = None

    async def start(self):
        self._reader_task = asyncio.create_task(self._reader_loop())

    async def close(self):
        if self._reader_task:
            self._reader_task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await self._reader_task
        await self._ws.close()

    def add_event_handler(self, handler):
        self._event_handlers.append(handler)

    async def send(self, method: str, params: dict[str, Any] | None = None, session_id: str | None = None) -> dict[str, Any]:
        self._next_id += 1
        msg_id = self._next_id
        payload: dict[str, Any] = {"id": msg_id, "method": method, "params": params or {}}
        if session_id:
            payload["sessionId"] = session_id
        loop = asyncio.get_running_loop()
        future = loop.create_future()
        self._pending[msg_id] = future
        await self._ws.send_str(json.dumps(payload))
        try:
            result = await asyncio.wait_for(future, timeout=self._timeout)
        except asyncio.TimeoutError as e:
            self._pending.pop(msg_id, None)
            raise CdpBlobStagingError(f"CDP command timed out: {method}") from e
        return result if isinstance(result, dict) else {}

    async def _reader_loop(self):
        while True:
            msg = await self._ws.receive()
            if msg.type in {aiohttp.WSMsgType.CLOSE, aiohttp.WSMsgType.CLOSED, aiohttp.WSMsgType.ERROR}:
                break
            if msg.type != aiohttp.WSMsgType.TEXT:
                continue
            try:
                payload = json.loads(msg.data)
            except json.JSONDecodeError:
                continue
            if not isinstance(payload, dict):
                continue
            if "id" in payload:
                future = self._pending.pop(int(payload["id"]), None)
                if future and not future.done():
                    if "error" in payload:
                        future.set_exception(CdpBlobStagingError(json.dumps(payload["error"], ensure_ascii=False)))
                    else:
                        future.set_result(payload.get("result") or {})
                continue
            if "method" in payload:
                for handler in self._event_handlers:
                    try:
                        handler(payload)
                    except Exception as exc:
                        logging.warning("Browser CDP blob staging event handler failed: %s", exc)


def _build_trigger_download_js(filename: str, mime_type: str) -> str:
    safe_name = json.dumps(filename)
    safe_mime = json.dumps(mime_type or "application/octet-stream")
    return f"""
(() => {{
  const chunks = window.{_CHUNK_VAR} || [];
  const binary = atob(chunks.join(''));
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  const blob = new Blob([bytes], {{ type: {safe_mime} }});
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = {safe_name};
  a.style.display = 'none';
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
  delete window.{_CHUNK_VAR};
  return 'ok';
}})()
"""


async def _create_blank_staging_target(cdp: _CdpWebSocketSession) -> str:
    created = await cdp.send("Target.createTarget", {"url": "about:blank"})
    target_id = str(created.get("targetId") or "").strip()
    if not target_id:
        raise CdpBlobStagingError("Target.createTarget(about:blank) returned no targetId")
    return target_id


async def _close_staging_target(cdp: _CdpWebSocketSession, target_id: str) -> None:
    if not target_id:
        return
    with contextlib.suppress(Exception):
        await cdp.send("Target.closeTarget", {"targetId": target_id})


class CdpBlobStagingClient:
    def __init__(self, config: CdpBlobStagingConfig):
        self._config = config

    async def stage_prepared_files(self, prepared_files: list[dict[str, Any]]) -> list[dict[str, Any]]:
        if not prepared_files:
            return []

        http_base = _normalize_http_cdp_base(self._config.cdp_url)
        browser_ws_url = await _resolve_browser_ws_url(http_base, self._config.timeout)
        session_id = get_uuid().replace("-", "")
        # Use flat download dir (must exist on Windows). Chrome often won't create nested subdirs.
        remote_dir = _cdp_path_for_chrome(self._config.download_dir)

        staged: list[dict[str, Any]] = []
        timeout = aiohttp.ClientTimeout(total=self._config.timeout)
        async with aiohttp.ClientSession(timeout=timeout) as http_session:
            async with http_session.ws_connect(browser_ws_url, heartbeat=20) as ws:
                cdp = _CdpWebSocketSession(ws, self._config.timeout)
                await cdp.start()
                staging_target_id = ""
                try:
                    staging_target_id = await _create_blank_staging_target(cdp)
                    attach = await cdp.send(
                        "Target.attachToTarget",
                        {"targetId": staging_target_id, "flatten": True},
                    )
                    page_session_id = str(attach.get("sessionId") or "").strip()
                    if not page_session_id:
                        raise CdpBlobStagingError("Target.attachToTarget returned no sessionId")

                    await cdp.send("Runtime.enable", session_id=page_session_id)
                    await cdp.send("Page.enable", session_id=page_session_id)
                    download_behavior = {
                        "behavior": "allow",
                        "downloadPath": remote_dir,
                        "eventsEnabled": True,
                    }
                    # Browser domain commands run on the browser connection, not the page session.
                    browser_behavior = await cdp.send(
                        "Browser.setDownloadBehavior",
                        download_behavior,
                    )
                    logging.info(
                        "Browser CDP blob staging on about:blank. remote_dir=%s, result=%s",
                        remote_dir,
                        browser_behavior,
                    )
                    # Page-level fallback for Chrome builds that ignore Browser.setDownloadBehavior here.
                    with contextlib.suppress(Exception):
                        await cdp.send(
                            "Page.setDownloadBehavior",
                            download_behavior,
                            session_id=page_session_id,
                        )

                    for item in prepared_files:
                        staged.append(
                            await self._stage_single_file(
                                cdp,
                                page_session_id,
                                remote_dir,
                                item,
                                staging_prefix=session_id,
                            )
                        )
                finally:
                    await _close_staging_target(cdp, staging_target_id)
                    await cdp.close()
        return staged

    async def _stage_single_file(
        self,
        cdp: _CdpWebSocketSession,
        page_session_id: str,
        remote_dir: str,
        item: dict[str, Any],
        *,
        staging_prefix: str = "",
    ) -> dict[str, Any]:
        local_path = str(item.get("local_path") or "").strip()
        if not local_path or not os.path.isfile(local_path):
            raise CdpBlobStagingError(f"local upload file does not exist: {local_path}")

        file_size = os.path.getsize(local_path)
        if file_size <= 0:
            raise CdpBlobStagingError(f"local upload file is empty: {local_path}")
        if file_size > self._config.max_bytes:
            raise CdpBlobStagingError(
                f"local upload file exceeds blob staging max size ({self._config.max_bytes} bytes): {local_path}"
            )

        filename = _safe_filename(item.get("name") or os.path.basename(local_path))
        mime_type = mimetypes.guess_type(filename)[0] or "application/octet-stream"
        with open(local_path, "rb") as f:
            raw = f.read()

        tracker = _DownloadTracker(expected_bytes=file_size)
        expected_remote_path = _cdp_path_for_chrome(_join_remote_win_path(remote_dir, filename))

        def on_event(payload: dict[str, Any]):
            method = str(payload.get("method") or "")
            params = payload.get("params") or {}
            if method in {"Page.downloadWillBegin", "Browser.downloadWillBegin"}:
                suggested = str(params.get("suggestedFilename") or params.get("fileName") or filename)
                tracker.completed_path = _cdp_path_for_chrome(_join_remote_win_path(remote_dir, suggested))
                logging.info("Browser CDP blob downloadWillBegin. filename=%s, path=%s", suggested, tracker.completed_path)
            elif method in {"Page.downloadProgress", "Browser.downloadProgress"}:
                state = str(params.get("state") or "")
                logging.info("Browser CDP blob downloadProgress. state=%s, params=%s", state, params)
                if state == "completed":
                    if not tracker.completed_path:
                        tracker.completed_path = expected_remote_path
                    tracker.done.set()
                elif state == "canceled":
                    tracker.canceled = True
                    tracker.done.set()

        cdp.add_event_handler(on_event)

        await cdp.send(
            "Runtime.evaluate",
            {"expression": f"window.{_CHUNK_VAR} = [];", "awaitPromise": False},
            session_id=page_session_id,
        )

        encoded = base64.b64encode(raw).decode("ascii")
        for index in range(0, len(encoded), self._config.chunk_chars):
            chunk = encoded[index : index + self._config.chunk_chars]
            await cdp.send(
                "Runtime.evaluate",
                {
                    "expression": f"window.{_CHUNK_VAR}.push({json.dumps(chunk)});",
                    "awaitPromise": False,
                },
                session_id=page_session_id,
            )

        trigger_js = _build_trigger_download_js(filename, mime_type)
        result = await cdp.send(
            "Runtime.evaluate",
            {"expression": trigger_js, "awaitPromise": False, "returnByValue": True},
            session_id=page_session_id,
        )
        if result.get("exceptionDetails"):
            raise CdpBlobStagingError(f"blob download trigger failed: {result['exceptionDetails']}")

        trigger_value = ((result.get("result") or {}).get("value") if isinstance(result.get("result"), dict) else None)
        if trigger_value not in (None, "ok"):
            raise CdpBlobStagingError(f"blob download trigger returned unexpected value: {trigger_value!r}")

        wait_seconds = min(max(5.0, self._config.timeout / 4), 30.0)
        try:
            await asyncio.wait_for(tracker.done.wait(), timeout=wait_seconds)
        except asyncio.TimeoutError:
            raise CdpBlobStagingError(
                f"Blob+CDP staging failed: no download completion for {filename}. "
                f"The file was NOT written to {remote_dir}. "
                f"Ensure this Windows folder exists and is writable, then retry. "
                f"If it still fails, switch Browser remote upload mode to HTTP staging and run "
                f"tools/browser_remote_staging/gateway.exe on the Chrome host (recommended)."
            ) from None

        if tracker.canceled:
            raise CdpBlobStagingError(f"CDP download canceled for {filename}")

        remote_path = tracker.completed_path or expected_remote_path
        logging.info(
            "Browser staged upload file via CDP blob. name=%s, remote_path=%s, size=%s",
            filename,
            remote_path,
            file_size,
        )
        return {
            **item,
            "local_path": remote_path,
            "remote_path": remote_path,
            "staging_session_id": staging_prefix or os.path.basename(remote_dir.replace("\\", "/")),
            "staging_mode": "blob_cdp",
        }


async def stage_prepared_files_via_cdp_blob(cdp_url: str, prepared_files: list[dict[str, Any]]) -> list[dict[str, Any]]:
    config = resolve_cdp_blob_staging_config(cdp_url)
    client = CdpBlobStagingClient(config)
    return await client.stage_prepared_files(prepared_files)


def stage_prepared_files_via_cdp_blob_sync(cdp_url: str, prepared_files: list[dict[str, Any]]) -> list[dict[str, Any]]:
    return asyncio.run(stage_prepared_files_via_cdp_blob(cdp_url, prepared_files))
