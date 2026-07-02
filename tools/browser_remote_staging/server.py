#!/usr/bin/env python3
#
#  Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
#
#  Lightweight staging server for RAGFlow Browser CDP uploads.
#  Run this on the remote Chrome host so RAGFlow (Docker) can push files
#  to filesystem paths that Chrome can read via CDP DOM.setFileInputFiles.
#
#  If only one external port is available (common on Windows), use gateway.py instead.

from __future__ import annotations

import json
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from urllib.parse import parse_qs, urlparse

from staging_common import (
    STAGING_DIR,
    STAGING_TOKEN,
    STAGING_MAX_BYTES,
    is_authorized,
    save_staging_upload,
    staging_health_payload,
)

STAGING_PORT = int(os.getenv("BROWSER_STAGING_PORT", "8765") or 8765)
STAGING_HOST = str(os.getenv("BROWSER_STAGING_HOST", "0.0.0.0") or "0.0.0.0").strip()


def _json_response(handler: BaseHTTPRequestHandler, status: int, payload: dict[str, Any]):
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json; charset=utf-8")
    handler.send_header("Content-Length", str(len(body)))
    handler.end_headers()
    handler.wfile.write(body)


class StagingHandler(BaseHTTPRequestHandler):
    server_version = "RAGFlowBrowserStaging/1.0"

    def log_message(self, format: str, *args):  # noqa: A003
        sys.stderr.write("%s - - [%s] %s\n" % (self.address_string(), self.log_date_time_string(), format % args))

    def do_GET(self):  # noqa: N802
        parsed = urlparse(self.path)
        if parsed.path == "/health":
            _json_response(self, 200, staging_health_payload())
            return
        _json_response(self, 404, {"error": "not found"})

    def do_POST(self):  # noqa: N802
        headers = {k: v for k, v in self.headers.items()}
        if not is_authorized(headers):
            _json_response(self, 401, {"error": "unauthorized"})
            return

        parsed = urlparse(self.path)
        if parsed.path != "/staging/upload":
            _json_response(self, 404, {"error": "not found"})
            return

        query = parse_qs(parsed.query)
        filename = (query.get("filename") or [""])[0]
        session_id = str(self.headers.get("X-Staging-Session", "") or "").strip()
        content_length = int(self.headers.get("Content-Length", "0") or 0)
        body = self.rfile.read(content_length) if content_length > 0 else b""
        try:
            payload = save_staging_upload(body, filename, session_id)
        except ValueError as e:
            status = 413 if "max size" in str(e) else 400
            _json_response(self, status, {"error": str(e)})
            return
        _json_response(self, 200, payload)


def main():
    STAGING_DIR.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer((STAGING_HOST, STAGING_PORT), StagingHandler)
    print(
        f"RAGFlow browser remote staging server listening on http://{STAGING_HOST}:{STAGING_PORT}\n"
        f"  staging_dir={STAGING_DIR}\n"
        f"  auth={'enabled' if STAGING_TOKEN else 'disabled'}\n"
        f"  max_bytes={STAGING_MAX_BYTES}",
        flush=True,
    )
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nStopping staging server...", flush=True)
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
