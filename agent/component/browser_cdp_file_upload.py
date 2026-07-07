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
from typing import Any


class CdpFileUploadError(RuntimeError):
    pass


def build_dispatch_input_events_js(backend_node_id: int) -> str:
    """Trigger input/change on a file input after DOM.setFileInputFiles (Shadow DOM safe)."""
    return f"""
(() => {{
  const findByBackend = (root, id) => {{
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT);
    let node = walker.currentNode;
    while (node) {{
      if (node.backendNodeId === id) return node;
      node = walker.nextNode();
    }}
    return null;
  }};
  let input = null;
  for (const root of [document, ...Array.from(document.querySelectorAll('*')).flatMap(el => {{
    try {{ return el.shadowRoot ? [el.shadowRoot] : []; }} catch (e) {{ return []; }}
  }})]) {{
    input = root.querySelector('input[type=file]') || findByBackend(root, {int(backend_node_id)});
    if (input) break;
  }}
  if (!input) {{
    const all = Array.from(document.querySelectorAll('input[type=file]'));
    input = all.length === 1 ? all[0] : null;
  }}
  if (!input) return 'no file input found';
  input.dispatchEvent(new Event('input', {{ bubbles: true, composed: true }}));
  input.dispatchEvent(new Event('change', {{ bubbles: true, composed: true }}));
  return 'ok';
}})()
"""


async def set_file_input_files_via_cdp(
    cdp_client: Any,
    session_id: str,
    backend_node_id: int,
    file_paths: list[str],
    *,
    dispatch_events: bool = True,
) -> None:
    """Upload via CDP DOM.setFileInputFiles, optionally dispatch composed change events."""
    paths = [str(p).strip() for p in file_paths if str(p or "").strip()]
    if not paths:
        raise CdpFileUploadError("no file paths provided for CDP upload")
    if not backend_node_id:
        raise CdpFileUploadError("backend_node_id is required for CDP upload")

    await cdp_client.send.DOM.setFileInputFiles(
        params={"files": paths, "backendNodeId": int(backend_node_id)},
        session_id=session_id,
    )

    if not dispatch_events:
        return

    await dispatch_file_input_events_via_cdp(cdp_client, session_id, backend_node_id)


async def dispatch_file_input_events_via_cdp(cdp_client: Any, session_id: str, backend_node_id: int) -> None:
    js = build_dispatch_input_events_js(backend_node_id)
    result = await cdp_client.send.Runtime.evaluate(
        params={"expression": js, "awaitPromise": False, "returnByValue": True},
        session_id=session_id,
    )
    if isinstance(result, dict) and result.get("exceptionDetails"):
        raise CdpFileUploadError(
            f"failed to dispatch input/change after CDP upload: {json.dumps(result['exceptionDetails'], ensure_ascii=False)}"
        )


def install_cdp_upload_event_dispatch_patch() -> None:
    """After browser-use DOM.setFileInputFiles, dispatch composed input/change events."""
    try:
        from browser_use.browser.watchdogs import default_action_watchdog as watchdog_module
    except ImportError:
        return

    watchdog_cls = watchdog_module.DefaultActionWatchdog
    if getattr(watchdog_cls, "_ragflow_upload_event_patch", False):
        return

    original = watchdog_cls.on_UploadFileEvent

    async def on_UploadFileEvent(self, event):
        await original(self, event)
        element_node = getattr(event, "node", None)
        backend_node_id = int(getattr(element_node, "backend_node_id", 0) or 0)
        if not backend_node_id:
            return
        try:
            cdp_client = self.browser_session.cdp_client
            session_id = await self._get_session_id_for_element(element_node)
            await dispatch_file_input_events_via_cdp(cdp_client, session_id, backend_node_id)
        except Exception as exc:
            import logging

            logging.warning("Browser CDP upload event dispatch failed: %s", exc)

    on_UploadFileEvent.__name__ = "on_UploadFileEvent"
    watchdog_cls.on_UploadFileEvent = on_UploadFileEvent
    watchdog_cls._ragflow_upload_event_patch = True
