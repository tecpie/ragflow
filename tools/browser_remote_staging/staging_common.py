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

import os
import re
import uuid
from pathlib import Path
from typing import Any

_SAFE_FILENAME_RE = re.compile(r"[^A-Za-z0-9._-]+")


def env_int(name: str, default: int) -> int:
    raw = str(os.getenv(name, "") or "").strip()
    if not raw:
        return default
    try:
        value = int(raw)
        return value if value > 0 else default
    except ValueError:
        return default


def default_staging_dir() -> Path:
    explicit = str(os.getenv("BROWSER_STAGING_DIR", "") or "").strip()
    if explicit:
        return Path(explicit).expanduser()
    if os.name == "nt":
        return Path(os.getenv("ProgramData", r"C:\ProgramData")) / "ragflow" / "browser-uploads"
    return Path("/data/browser-uploads")


STAGING_DIR = default_staging_dir()
STAGING_TOKEN = str(os.getenv("BROWSER_STAGING_TOKEN", "") or "").strip()
STAGING_MAX_BYTES = env_int("BROWSER_STAGING_MAX_BYTES", 100 * 1024 * 1024)


_SAFE_FILENAME_RE = re.compile(r"[^A-Za-z0-9._-]+")
# Preserve Unicode filenames; only remove path/control characters unsafe on Windows/macOS/Linux.
_UNSAFE_PATH_CHARS_RE = re.compile(r'[\\/:\x00-\x1f\x7f<>|?*"]')


def safe_filename(name: str) -> str:
    base = os.path.basename(str(name or "").strip())
    if not base:
        return f"upload_{uuid.uuid4().hex[:8]}.bin"
    cleaned = _UNSAFE_PATH_CHARS_RE.sub("_", base).strip().strip(".")
    return cleaned or f"upload_{uuid.uuid4().hex[:8]}.bin"


def safe_session_id(raw: str) -> str:
    session_id = str(raw or "").strip() or uuid.uuid4().hex
    session_id = _SAFE_FILENAME_RE.sub("_", session_id).strip("._")
    return session_id or uuid.uuid4().hex


def is_authorized(headers: dict[str, str]) -> bool:
    if not STAGING_TOKEN:
        return True
    auth = str(headers.get("Authorization", "") or headers.get("authorization", "")).strip()
    if auth == f"Bearer {STAGING_TOKEN}":
        return True
    token = str(headers.get("X-Staging-Token", "") or headers.get("x-staging-token", "")).strip()
    return token == STAGING_TOKEN


def staging_health_payload() -> dict[str, Any]:
    return {
        "status": "ok",
        "staging_dir": str(STAGING_DIR),
    }


def save_staging_upload(body: bytes, filename: str, session_id: str) -> dict[str, Any]:
    if len(body) <= 0:
        raise ValueError("empty body")
    if len(body) > STAGING_MAX_BYTES:
        raise ValueError(f"file exceeds max size {STAGING_MAX_BYTES}")

    safe_name = safe_filename(filename)
    safe_session = safe_session_id(session_id)
    target_dir = STAGING_DIR / safe_session
    target_dir.mkdir(parents=True, exist_ok=True)
    target_path = target_dir / safe_name
    index = 1
    while target_path.exists():
        target_path = target_dir / f"{target_path.stem}_{index}{target_path.suffix}"
        index += 1

    target_path.write_bytes(body)
    return {
        "path": str(target_path.resolve()),
        "name": target_path.name,
        "size": len(body),
        "session_id": safe_session,
    }
