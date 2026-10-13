#!/usr/bin/env python3
#
#  Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
#
#  Single-port gateway for Windows / restricted firewall environments.
#  Exposes BOTH:
#    - Chrome CDP (HTTP + WebSocket proxy to local Chrome)
#    - RAGFlow remote file staging (/health, /staging/upload)
#
#  Example (Windows PowerShell):
#    $env:BROWSER_GATEWAY_PORT="8443"
#    $env:BROWSER_STAGING_TOKEN="change-me"
#    $env:BROWSER_CDP_UPSTREAM="http://127.0.0.1:9222"
#    python tools/browser_remote_staging/gateway.py
#
#  RAGFlow Browser node:
#    CDP URL            = http://windows-host:8443
#    Remote staging URL = http://windows-host:8443

from __future__ import annotations

import asyncio
import contextlib
import json
import os
from typing import Any
from urllib.parse import urljoin, urlparse, urlunparse

from aiohttp import ClientSession, WSMsgType, web

from staging_common import (
    STAGING_DIR,
    STAGING_TOKEN,
    is_authorized,
    save_staging_upload,
    staging_health_payload,
)

GATEWAY_HOST = str(os.getenv("BROWSER_GATEWAY_HOST", "0.0.0.0") or "0.0.0.0").strip()
GATEWAY_PORT = int(os.getenv("BROWSER_GATEWAY_PORT", "8443") or 8443)
CDP_UPSTREAM = str(os.getenv("BROWSER_CDP_UPSTREAM", "http://127.0.0.1:9222") or "http://127.0.0.1:9222").strip().rstrip("/")
PUBLIC_ORIGIN = str(os.getenv("BROWSER_GATEWAY_PUBLIC_ORIGIN", "") or "").strip().rstrip("/")

_HOP_BY_HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailers",
    "transfer-encoding",
    "upgrade",
}


def _resolve_public_origin(request: web.Request) -> str:
    if PUBLIC_ORIGIN:
        return PUBLIC_ORIGIN
    host = str(request.headers.get("Host", "") or "").strip()
    if not host:
        return f"http://127.0.0.1:{GATEWAY_PORT}"
    scheme = "https" if request.headers.get("X-Forwarded-Proto", "").lower() == "https" else "http"
    return f"{scheme}://{host}"


def _rewrite_remote_url(url: str, public_origin: str, upstream_origin: str) -> str:
    token = str(url or "").strip()
    if not token:
        return token

    parsed = urlparse(token)
    public = urlparse(public_origin)

    if parsed.scheme in {"ws", "wss", "http", "https"}:
        if parsed.scheme in {"ws", "wss"}:
            scheme = "wss" if public.scheme == "https" else "ws"
        else:
            scheme = public.scheme
        netloc = public.netloc or parsed.netloc
        return urlunparse((scheme, netloc, parsed.path, parsed.params, parsed.query, parsed.fragment))

    if token.startswith("/"):
        scheme = "wss" if public.scheme == "https" else "ws"
        return urlunparse((scheme, public.netloc, token, "", "", ""))

    return token.replace(upstream_origin, public_origin).replace(
        upstream_origin.replace("http://", "ws://", 1).replace("https://", "wss://", 1),
        public_origin.replace("http://", "ws://", 1).replace("https://", "wss://", 1),
    )


def _rewrite_cdp_json_payload(text: str, public_origin: str, upstream_origin: str) -> str:
    try:
        data = json.loads(text)
    except json.JSONDecodeError:
        return text

    def patch_item(item: dict[str, Any]):
        for key in ("webSocketDebuggerUrl", "devtoolsFrontendUrl"):
            if key in item and isinstance(item[key], str):
                item[key] = _rewrite_remote_url(item[key], public_origin, upstream_origin)

    if isinstance(data, dict):
        patch_item(data)
    elif isinstance(data, list):
        for entry in data:
            if isinstance(entry, dict):
                patch_item(entry)

    return json.dumps(data, ensure_ascii=False)


async def handle_health(_request: web.Request) -> web.Response:
    return web.json_response(staging_health_payload())


async def handle_staging_upload(request: web.Request) -> web.Response:
    headers = {k: v for k, v in request.headers.items()}
    if not is_authorized(headers):
        return web.json_response({"error": "unauthorized"}, status=401)

    filename = request.query.get("filename", "")
    session_id = headers.get("X-Staging-Session") or headers.get("x-staging-session") or ""
    body = await request.read()
    try:
        payload = save_staging_upload(body, filename, session_id)
    except ValueError as e:
        status = 413 if "max size" in str(e) else 400
        return web.json_response({"error": str(e)}, status=status)
    return web.json_response(payload)


async def proxy_http(request: web.Request) -> web.StreamResponse:
    upstream_url = urljoin(CDP_UPSTREAM + "/", request.rel_url.path.lstrip("/"))
    if request.rel_url.query_string:
        upstream_url = f"{upstream_url}?{request.rel_url.query_string}"

    headers = {
        k: v
        for k, v in request.headers.items()
        if k.lower() not in _HOP_BY_HOP_HEADERS and k.lower() != "host"
    }
    body = await request.read() if request.can_read_body else None

    async with ClientSession(auto_decompress=False) as session:
        async with session.request(request.method, upstream_url, headers=headers, data=body) as upstream:
            raw = await upstream.read()
            content_type = upstream.headers.get("Content-Type", "")
            public_origin = _resolve_public_origin(request)
            if "application/json" in content_type.lower():
                text = raw.decode("utf-8", errors="replace")
                raw = _rewrite_cdp_json_payload(text, public_origin, CDP_UPSTREAM).encode("utf-8")

            response = web.Response(body=raw, status=upstream.status)
            for key, value in upstream.headers.items():
                lowered = key.lower()
                if lowered in _HOP_BY_HOP_HEADERS or lowered == "content-length":
                    continue
                response.headers[key] = value
            response.headers["Content-Length"] = str(len(raw))
            return response


async def proxy_websocket(request: web.Request) -> web.WebSocketResponse:
    upstream_ws_origin = CDP_UPSTREAM.replace("https://", "wss://").replace("http://", "ws://")
    upstream_url = urljoin(upstream_ws_origin + "/", request.rel_url.path.lstrip("/"))
    if request.rel_url.query_string:
        upstream_url = f"{upstream_url}?{request.rel_url.query_string}"

    client_ws = web.WebSocketResponse(autoping=True, heartbeat=30)
    await client_ws.prepare(request)

    async with ClientSession() as session, session.ws_connect(upstream_url, autoping=True, heartbeat=30) as upstream_ws:

        async def client_to_upstream():
            async for msg in client_ws:
                if msg.type == WSMsgType.TEXT:
                    await upstream_ws.send_str(msg.data)
                elif msg.type == WSMsgType.BINARY:
                    await upstream_ws.send_bytes(msg.data)
                elif msg.type in {WSMsgType.CLOSE, WSMsgType.ERROR}:
                    break

        async def upstream_to_client():
            async for msg in upstream_ws:
                if msg.type == WSMsgType.TEXT:
                    await client_ws.send_str(msg.data)
                elif msg.type == WSMsgType.BINARY:
                    await client_ws.send_bytes(msg.data)
                elif msg.type in {WSMsgType.CLOSE, WSMsgType.ERROR}:
                    break

        forwarders = [
            asyncio.create_task(client_to_upstream()),
            asyncio.create_task(upstream_to_client()),
        ]
        done, pending = await asyncio.wait(forwarders, return_when=asyncio.FIRST_COMPLETED)
        for task in pending:
            task.cancel()
        for task in done:
            with contextlib.suppress(Exception):
                await task

    return client_ws


async def dispatch_request(request: web.Request) -> web.StreamResponse:
    if request.headers.get("Upgrade", "").lower() == "websocket" and request.rel_url.path.startswith("/devtools/"):
        return await proxy_websocket(request)
    return await proxy_http(request)


def create_app() -> web.Application:
    app = web.Application()
    app.router.add_get("/health", handle_health)
    app.router.add_post("/staging/upload", handle_staging_upload)
    app.router.add_route("*", "/{tail:.*}", dispatch_request)
    return app


def main():
    STAGING_DIR.mkdir(parents=True, exist_ok=True)
    print(
        f"RAGFlow browser gateway listening on http://{GATEWAY_HOST}:{GATEWAY_PORT}\n"
        f"  cdp_upstream={CDP_UPSTREAM}\n"
        f"  staging_dir={STAGING_DIR}\n"
        f"  auth={'enabled' if STAGING_TOKEN else 'disabled'}\n"
        f"  public_origin={PUBLIC_ORIGIN or '(auto from Host header)'}\n"
        f"\n"
        f"Configure RAGFlow Browser node with the SAME URL for both CDP and remote staging:\n"
        f"  http://<windows-host>:{GATEWAY_PORT}",
        flush=True,
    )
    web.run_app(create_app(), host=GATEWAY_HOST, port=GATEWAY_PORT)


if __name__ == "__main__":
    main()
