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

import json
import logging
import os
import re
from dataclasses import dataclass
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urljoin, urlparse
from urllib.request import Request, urlopen

from common.misc_utils import get_uuid

_SAFE_FILENAME_RE = re.compile(r"[^A-Za-z0-9._-]+")


@dataclass(frozen=True)
class RemoteStagingConfig:
    base_url: str
    token: str = ""
    timeout: float = 120.0
    max_bytes: int = 100 * 1024 * 1024


@dataclass(frozen=True)
class RemoteStagingUploadResult:
    path: str
    name: str
    size: int
    session_id: str


class RemoteStagingError(RuntimeError):
    pass


def resolve_remote_staging_config(
    param_url: str = "",
    param_token: str = "",
) -> RemoteStagingConfig | None:
    base_url = str(param_url or os.getenv("RAGFLOW_BROWSER_REMOTE_STAGING_URL", "")).strip().rstrip("/")
    if not base_url:
        return None
    token = str(param_token or os.getenv("RAGFLOW_BROWSER_REMOTE_STAGING_TOKEN", "")).strip()
    timeout_raw = str(os.getenv("RAGFLOW_BROWSER_REMOTE_STAGING_TIMEOUT", "") or "").strip()
    max_bytes_raw = str(os.getenv("RAGFLOW_BROWSER_REMOTE_STAGING_MAX_BYTES", "") or "").strip()
    timeout = 120.0
    max_bytes = 100 * 1024 * 1024
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
    return RemoteStagingConfig(base_url=base_url, token=token, timeout=timeout, max_bytes=max_bytes)


def _safe_filename(name: str) -> str:
    base = os.path.basename(str(name or "").strip())
    if not base:
        return f"upload_{get_uuid()[:8]}.bin"
    cleaned = _SAFE_FILENAME_RE.sub("_", base).strip("._")
    return cleaned or f"upload_{get_uuid()[:8]}.bin"


class RemoteStagingClient:
    def __init__(self, config: RemoteStagingConfig):
        self._config = config

    @property
    def config(self) -> RemoteStagingConfig:
        return self._config

    def _build_headers(self, session_id: str, extra: dict[str, str] | None = None) -> dict[str, str]:
        headers = {
            "User-Agent": "RAGFlow-Browser-Remote-Staging/1.0",
            "X-Staging-Session": session_id,
        }
        if self._config.token:
            headers["Authorization"] = f"Bearer {self._config.token}"
        if extra:
            headers.update(extra)
        return headers

    def _request_json(self, method: str, url: str, headers: dict[str, str], body: bytes | None = None) -> dict[str, Any]:
        req = Request(url, data=body, headers=headers, method=method)
        try:
            with urlopen(req, timeout=self._config.timeout) as response:
                payload = response.read().decode("utf-8")
        except HTTPError as e:
            detail = ""
            try:
                detail = e.read().decode("utf-8", errors="replace")
            except Exception:
                pass
            raise RemoteStagingError(f"remote staging HTTP {e.code}: {detail or e.reason}") from e
        except URLError as e:
            raise RemoteStagingError(f"remote staging request failed: {e}") from e
        except TimeoutError as e:
            raise RemoteStagingError(f"remote staging request timed out after {self._config.timeout}s") from e

        if not payload:
            return {}
        try:
            parsed = json.loads(payload)
        except json.JSONDecodeError as e:
            raise RemoteStagingError(f"remote staging returned invalid JSON: {payload[:200]}") from e
        if not isinstance(parsed, dict):
            raise RemoteStagingError("remote staging response must be a JSON object")
        return parsed

    def health_check(self) -> bool:
        url = urljoin(self._config.base_url + "/", "health")
        try:
            payload = self._request_json("GET", url, self._build_headers(session_id="health-check"))
        except RemoteStagingError:
            return False
        return str(payload.get("status", "")).lower() == "ok"

    def upload_file(self, local_path: str, session_id: str | None = None) -> RemoteStagingUploadResult:
        if not os.path.isfile(local_path):
            raise RemoteStagingError(f"local upload file does not exist: {local_path}")

        file_size = os.path.getsize(local_path)
        if file_size <= 0:
            raise RemoteStagingError(f"local upload file is empty: {local_path}")
        if file_size > self._config.max_bytes:
            raise RemoteStagingError(
                f"local upload file exceeds remote staging max size ({self._config.max_bytes} bytes): {local_path}"
            )

        session = str(session_id or get_uuid()).strip() or get_uuid()
        filename = _safe_filename(os.path.basename(local_path))
        url = urljoin(
            self._config.base_url + "/",
            f"staging/upload?filename={quote(filename)}",
        )
        with open(local_path, "rb") as f:
            body = f.read()

        payload = self._request_json(
            "POST",
            url,
            self._build_headers(
                session_id=session,
                extra={"Content-Type": "application/octet-stream"},
            ),
            body=body,
        )
        remote_path = str(payload.get("path") or "").strip()
        if not remote_path:
            raise RemoteStagingError(f"remote staging upload missing path field: {payload}")
        return RemoteStagingUploadResult(
            path=remote_path,
            name=str(payload.get("name") or filename),
            size=int(payload.get("size") or file_size),
            session_id=str(payload.get("session_id") or session),
        )

    def stage_prepared_files(self, prepared_files: list[dict[str, Any]]) -> list[dict[str, Any]]:
        if not prepared_files:
            return []

        session_id = get_uuid()
        staged: list[dict[str, Any]] = []
        for item in prepared_files:
            local_path = str(item.get("local_path") or "").strip()
            if not local_path:
                logging.warning("Browser remote staging skipped item without local_path: %s", item)
                continue
            result = self.upload_file(local_path, session_id=session_id)
            staged.append(
                {
                    **item,
                    "local_path": result.path,
                    "remote_path": result.path,
                    "staging_session_id": result.session_id,
                }
            )
            logging.info(
                "Browser staged upload file for remote CDP browser. name=%s, remote_path=%s, session_id=%s",
                result.name,
                result.path,
                result.session_id,
            )
        return staged


def validate_remote_staging_url(url: str) -> bool:
    token = str(url or "").strip()
    if not token:
        return False
    parsed = urlparse(token)
    return parsed.scheme in {"http", "https"} and bool(parsed.netloc)
