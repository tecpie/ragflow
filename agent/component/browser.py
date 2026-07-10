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
import hashlib
import inspect
import json
import logging
import os
import re
import shutil
import tempfile
from abc import ABC
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import unquote, urlparse
from urllib.request import Request, urlopen

import json_repair

from agent.component.base import ComponentBase
from agent.component.browser_cdp_blob_staging import (
    CdpBlobStagingError,
    resolve_remote_upload_mode,
    stage_prepared_files_via_cdp_blob,
)
from agent.component.browser_cdp_file_upload import install_cdp_upload_event_dispatch_patch
from agent.component.browser_remote_staging import (
    RemoteStagingClient,
    RemoteStagingError,
    resolve_remote_staging_config,
)
from agent.component.llm import LLMParam
from api.db import FileType
from api.db.joint_services.tenant_model_service import resolve_model_config, resolve_model_type
from api.db.services import duplicate_name
from api.db.services.file_service import FileService
from api.utils.file_utils import filename_type
from common import settings
from common.connection_utils import timeout
from common.misc_utils import get_uuid
from common.model_thinking_utils import apply_enable_thinking_policy
from rag.llm import FACTORY_DEFAULT_BASE_URL

_THINK_BLOCK_RE = re.compile(
    r"<(?:think|redacted_thinking|redacted_reasoning)>[\s\S]*?</(?:think|redacted_thinking|redacted_reasoning)>\s*",
    re.IGNORECASE,
)
_MARKDOWN_FENCE_RE = re.compile(r"```(?:json)?\s*", re.IGNORECASE)


def strip_think_tags_from_llm_output(text: str) -> str:
    if not text:
        return text
    return _THINK_BLOCK_RE.sub("", text).lstrip("\n\r\t ")


def _strip_markdown_json_fence(text: str) -> str:
    cleaned = _MARKDOWN_FENCE_RE.sub("", text or "")
    return re.sub(r"\s*```\s*$", "", cleaned).strip()


def normalize_browser_llm_output_for_json(text: str) -> str:
    """Normalize browser-use LLM output so AgentOutput JSON parsing can succeed."""
    if not text:
        return text

    cleaned = _strip_markdown_json_fence(strip_think_tags_from_llm_output(text))
    if not cleaned:
        return cleaned

    candidates = [cleaned]
    for opener in ("{", "["):
        idx = cleaned.find(opener)
        if idx >= 0:
            candidates.append(cleaned[idx:])

    for candidate in candidates:
        fragment = _strip_markdown_json_fence(candidate)
        if not fragment:
            continue
        try:
            parsed = json_repair.loads(fragment)
            if isinstance(parsed, (dict, list)):
                return json.dumps(parsed, ensure_ascii=False)
        except Exception:
            continue

    return cleaned


class BrowserParam(LLMParam):
    """
    Parameters for Browser node.
    """

    def __init__(self):
        super().__init__()
        self.prompts = "{sys.query}"
        self.max_steps = 30
        self.headless = True
        self.use_cdp = False
        self.cdp_url = ""
        self.use_vision = False
        self.enable_default_extensions = False
        self.chromium_sandbox = False
        # Reuse browser profile across runs of the same agent node by default.
        self.persist_session = True
        self.enable_thinking = False
        self.upload_sources = []
        self.remote_staging_url = ""
        self.remote_staging_token = ""
        self.remote_upload_mode = "auto"
        self.outputs = {
            "content": {"type": "string", "value": ""},
            "downloaded_files": {"type": "Array<Object>", "value": []},
        }

    def check(self):
        self.check_empty(self.llm_id, "[Browser] LLM")
        self.check_positive_integer(self.max_steps, "[Browser] Max steps")
        self.check_boolean(self.headless, "[Browser] Headless")
        self.check_boolean(self.use_cdp, "[Browser] Use CDP")
        if self.use_cdp:
            self.check_empty(str(self.cdp_url or "").strip(), "[Browser] CDP URL")
        self.check_boolean(self.use_vision, "[Browser] Use vision")
        self.check_boolean(self.enable_default_extensions, "[Browser] Enable default extensions")
        self.check_boolean(self.chromium_sandbox, "[Browser] Chromium sandbox")
        self.check_boolean(self.persist_session, "[Browser] Persist session")
        self.check_boolean(self.enable_thinking, "[Browser] Enable thinking")
        self.check_empty(self.prompts, "[Browser] Prompts")
        return True

    def get_input_form(self) -> dict[str, dict]:
        return {
            "prompts": {"type": "text", "name": "Prompts"},
            "upload_sources": {"type": "line", "name": "Upload sources"},
            "remote_staging_url": {"type": "line", "name": "Remote staging URL"},
            "remote_staging_token": {"type": "line", "name": "Remote staging token"},
        }


class Browser(ComponentBase, ABC):
    component_name = "Browser"

    def _prepare_input_values(self):
        for key, meta in self.get_input_elements().items():
            val = meta.get("value")
            if val is None:
                val = ""
            elif not isinstance(val, str):
                val = json.dumps(val, ensure_ascii=False)
            self.set_input_value(key, val)

    def get_input_elements(self) -> dict[str, dict]:
        text_parts = [
            str(self._param.prompts or ""),
            json.dumps(self._param.upload_sources, ensure_ascii=False),
        ]
        return self.get_input_elements_from_text("\n".join(text_parts))

    def _resolve_param_value(self, value: Any) -> Any:
        if isinstance(value, str):
            direct_ref = value.strip()
            if direct_ref.startswith("{") and direct_ref.endswith("}") and self._canvas.is_reff(direct_ref):
                return self._canvas.get_variable_value(direct_ref)
            return value
        return value

    def _extract_ids(self, value: Any) -> list[str]:
        ids: list[str] = []
        value = self._resolve_param_value(value)

        def collect(item: Any):
            if item is None:
                return
            if isinstance(item, str):
                token = item.strip()
                if not token:
                    return
                if token.startswith("{") and token.endswith("}") and self._canvas.is_reff(token):
                    collect(self._canvas.get_variable_value(token))
                    return
                if token.startswith("[") and token.endswith("]"):
                    try:
                        parsed = json.loads(token)
                        collect(parsed)
                        return
                    except Exception:
                        pass
                if self._is_http_url(token):
                    ids.append(token)
                    return
                if "," in token:
                    for part in token.split(","):
                        collect(part)
                    return
                ids.append(token)
                return
            if isinstance(item, dict):
                for k in ("file_id", "id", "url", "value"):
                    if k in item:
                        collect(item[k])
                        return
                for v in item.values():
                    collect(v)
                return
            if isinstance(item, (list, tuple, set)):
                for v in item:
                    collect(v)
                return
            token = str(item).strip()
            if token:
                ids.append(token)

        collect(value)
        deduped: list[str] = []
        visited = set()
        for item in ids:
            if item in visited:
                continue
            visited.add(item)
            deduped.append(item)
        return deduped

    @staticmethod
    def _is_http_url(value: str) -> bool:
        token = str(value or "").strip()
        if not token:
            return False
        parsed = urlparse(token)
        return parsed.scheme in {"http", "https"} and bool(parsed.netloc)

    @staticmethod
    def _extract_url_filename(url: str, headers: Any) -> str:
        content_disposition = str(getattr(headers, "get", lambda *_args, **_kwargs: "")("Content-Disposition", "") or "")
        if content_disposition:
            # Prefer RFC 5987 encoded filename*=UTF-8''... when present.
            m = re.search(r"filename\*\s*=\s*(?:UTF-8''|utf-8'')?([^;]+)", content_disposition)
            if m:
                name = unquote(m.group(1).strip().strip('"'))
                if name:
                    return os.path.basename(name)
            m = re.search(r'filename\s*=\s*"([^"]+)"', content_disposition)
            if m:
                name = m.group(1).strip()
                if name:
                    return os.path.basename(name)
            m = re.search(r"filename\s*=\s*([^;]+)", content_disposition)
            if m:
                name = m.group(1).strip().strip('"')
                if name:
                    return os.path.basename(name)

        parsed = urlparse(url)
        raw_name = os.path.basename(parsed.path or "")
        name = unquote(raw_name).strip()
        if name:
            return name
        return f"url_file_{get_uuid()[:8]}.bin"

    @staticmethod
    def _resolve_upload_url_max_bytes() -> int:
        raw = str(os.getenv("RAGFLOW_BROWSER_UPLOAD_URL_MAX_BYTES", "") or "").strip()
        default_max_bytes = 100 * 1024 * 1024
        if not raw:
            return default_max_bytes
        try:
            parsed = int(raw)
            return parsed if parsed > 0 else default_max_bytes
        except (TypeError, ValueError):
            return default_max_bytes

    @staticmethod
    def _restore_env_var(key: str, value: str | None):
        if value is None:
            os.environ.pop(key, None)
            return
        os.environ[key] = value

    def _prepare_upload_url_file(self, url: str, upload_dir: str) -> dict[str, Any] | None:
        max_bytes = self._resolve_upload_url_max_bytes()
        local_path = ""
        local_name = ""
        total_size = 0
        try:
            req = Request(url, headers={"User-Agent": "RAGFlow-Browser-Node/1.0"})
            with urlopen(req, timeout=30) as response:
                local_name = self._extract_url_filename(url, response.headers)

                local_path = os.path.join(upload_dir, local_name)
                index = 1
                while os.path.exists(local_path):
                    stem, ext = os.path.splitext(local_name)
                    local_path = os.path.join(upload_dir, f"{stem}_{index}{ext}")
                    index += 1

                with open(local_path, "wb") as f:
                    while True:
                        chunk = response.read(1024 * 1024)
                        if not chunk:
                            break
                        total_size += len(chunk)
                        if total_size > max_bytes:
                            raise ValueError(f"upload url file exceeds max size limit: {max_bytes}")
                        f.write(chunk)
        except (HTTPError, URLError, OSError, TimeoutError, ValueError) as e:
            if local_path and os.path.exists(local_path):
                try:
                    os.remove(local_path)
                except OSError:
                    pass
            logging.warning("Browser failed to fetch upload url. url=%s, error=%s", url, e)
            return None

        if total_size <= 0:
            if local_path and os.path.exists(local_path):
                try:
                    os.remove(local_path)
                except OSError:
                    pass
            logging.warning("Browser upload url returned empty content: %s", url)
            return None

        return {
            "file_id": "",
            "name": local_name,
            "size": total_size,
            "local_path": local_path,
            "source_url": url,
        }

    def _resolve_text(self, raw_text: Any) -> str:
        text = str(self._resolve_param_value(raw_text) or "")
        vars_map = self.get_input_elements_from_text(text)
        kv = {}
        for key, meta in vars_map.items():
            val = meta.get("value", "")
            if isinstance(val, str):
                kv[key] = val
            else:
                kv[key] = json.dumps(val, ensure_ascii=False)
        return self.string_format(text, kv)

    @staticmethod
    def _as_model_config_dict(cfg_obj: Any) -> dict[str, Any]:
        if cfg_obj is None:
            return {}
        if isinstance(cfg_obj, dict):
            return cfg_obj
        if hasattr(cfg_obj, "to_dict") and callable(cfg_obj.to_dict):
            try:
                result = cfg_obj.to_dict()
                return result if isinstance(result, dict) else {}
            except (AttributeError, TypeError, ValueError):
                return {}
        result = {}
        for key in ("model", "model_name", "llm_name", "llm_factory", "api_key", "base_url", "api_base", "temperature"):
            val = getattr(cfg_obj, key, None)
            if val not in (None, ""):
                result[key] = val
        return result

    @staticmethod
    def _error_chain(exc: Exception) -> str:
        parts = []
        cur = exc
        depth = 0
        while cur is not None and depth < 6:
            parts.append(f"{type(cur).__name__}: {cur}")
            cur = cur.__cause__ or cur.__context__
            depth += 1
        return " <- ".join(parts)

    @staticmethod
    def _resolve_browser_executable() -> str:
        explicit_candidates = [
            os.getenv("BROWSER_USE_EXECUTABLE_PATH", "").strip(),
            os.getenv("BROWSER_USE_BROWSER_BINARY_PATH", "").strip(),
            os.getenv("BROWSER_USE_CHROME_BINARY_PATH", "").strip(),
        ]
        for explicit in explicit_candidates:
            if explicit and os.path.isfile(explicit) and os.access(explicit, os.X_OK):
                return explicit
        candidates = [
            "/opt/chrome/chrome",
            "/usr/local/bin/chrome",
            "/usr/local/bin/google-chrome",
            "/usr/bin/google-chrome",
            "/usr/bin/google-chrome-stable",
            "/usr/bin/chromium",
            "/usr/bin/chromium-browser",
        ]
        for path in candidates:
            if os.path.isfile(path) and os.access(path, os.X_OK):
                return path
        for cmd in ("chrome", "google-chrome", "google-chrome-stable", "chromium", "chromium-browser"):
            path = shutil.which(cmd)
            if path and os.path.isfile(path) and os.access(path, os.X_OK):
                return path
        return ""

    @staticmethod
    def _normalize_model_name(model: Any) -> str:
        name = str(model or "").strip()
        if not name:
            return ""
        if name.startswith("bu-") or name.startswith("browser-use/"):
            return name
        if "@" in name:
            # RAGFlow model aliases may include provider suffix, e.g. qwen3.5-flash@Tongyi-Qianwen.
            # browser-use OpenAI-compatible adapters need the pure model name.
            name = name.split("@", 1)[0].strip()
        return name

    @staticmethod
    def _safe_path_segment(value: Any) -> str:
        token = str(value or "").strip()
        if not token:
            return "unknown"
        token = re.sub(r"[^A-Za-z0-9._-]+", "_", token)
        return token.strip("._-") or "unknown"

    def _resolve_persistent_profile_dir(self) -> str:
        root = os.path.join(tempfile.gettempdir(), "ragflow_browser_use_profiles")
        tenant = self._safe_path_segment(self._canvas.get_tenant_id())
        raw_canvas_id = getattr(self._canvas, "_id", "")
        if not raw_canvas_id:
            graph_text = json.dumps(
                self._canvas.dsl.get("graph", {}),
                sort_keys=True,
                ensure_ascii=False,
            )
            raw_canvas_id = (
                f"dsl_{hashlib.sha1(graph_text.encode('utf-8')).hexdigest()[:12]}"
            )
        canvas_id = self._safe_path_segment(raw_canvas_id)
        node_id = self._safe_path_segment(self._id)
        return os.path.join(root, tenant, canvas_id, node_id)

    def _should_persist_session(self) -> bool:
        return bool(self._param.persist_session)

    def _infer_provider_name(self, cfg: dict[str, Any]) -> str:
        provider = str(cfg.get("llm_factory") or "").strip()
        if provider:
            return provider
        llm_id = str(self._param.llm_id or "")
        if "@" in llm_id:
            return llm_id.split("@", 1)[1].strip()
        return ""

    def _resolve_openai_compatible_base_url(self, cfg: dict[str, Any]) -> str:
        explicit = str(cfg.get("base_url") or cfg.get("api_base") or "").strip()
        if explicit:
            return explicit

        provider = self._infer_provider_name(cfg)
        fallback = str(FACTORY_DEFAULT_BASE_URL.get(provider, "")).strip()
        return fallback if fallback else ""

    def _resolve_browser_enable_thinking(self) -> bool:
        param_val = getattr(self._param, "enable_thinking", None)
        if param_val is not None:
            return bool(param_val)
        global_val = self._canvas.globals.get("sys.enable_thinking")
        if global_val is not None:
            return bool(global_val)
        return False

    def _build_browser_thinking_extra_body(self, cfg: dict[str, Any], model_name: str) -> dict[str, Any]:
        provider = self._infer_provider_name(cfg)
        _, thinking_kwargs = apply_enable_thinking_policy(
            model_name,
            provider,
            {"reasoning": self._resolve_browser_enable_thinking()},
        )
        extra_body = thinking_kwargs.get("extra_body")
        return dict(extra_body) if isinstance(extra_body, dict) else {}

    def _patch_browser_llm_client(self, llm, extra_body: dict[str, Any] | None = None):
        original_get_client = llm.get_client
        thinking_extra = dict(extra_body or {})

        def patched_get_client():
            client = original_get_client()
            if getattr(client, "_ragflow_browser_patched", False):
                return client
            original_create = client.chat.completions.create

            async def create_with_patch(**kwargs):
                if thinking_extra:
                    kwargs["extra_body"] = {**thinking_extra, **(kwargs.get("extra_body") or {})}
                response = await original_create(**kwargs)
                for choice in response.choices or []:
                    message = getattr(choice, "message", None)
                    content = getattr(message, "content", None) if message else None
                    if content:
                        message.content = normalize_browser_llm_output_for_json(content)
                return response

            client.chat.completions.create = create_with_patch
            client._ragflow_browser_patched = True
            return client

        llm.get_client = patched_get_client
        return llm

    def _build_browser_llm(self):
        from browser_use.llm import ChatBrowserUse, ChatOpenAI

        chat_model_config = resolve_model_config(
            self._canvas.get_tenant_id(),
            resolve_model_type(self._canvas.get_tenant_id(), self._param.llm_id),
            self._param.llm_id,
        )
        cfg = self._as_model_config_dict(chat_model_config)
        model_name = self._normalize_model_name(cfg.get("model_name") or cfg.get("model") or self._param.llm_id)
        if not model_name:
            raise ValueError(f"Invalid model config for Browser llm_id={self._param.llm_id}")
        base_url = self._resolve_openai_compatible_base_url(cfg)
        thinking_extra_body = self._build_browser_thinking_extra_body(cfg, model_name)

        # ChatBrowserUse only supports bu-* models. For tenant models, use OpenAI-compatible adapter.
        if model_name.startswith("bu-") or model_name.startswith("browser-use/"):
            llm_kwargs = {
                "model": model_name,
                "api_key": cfg.get("api_key"),
                "base_url": base_url,
                "temperature": self._param.temperature,
                "max_retries": self._param.max_retries,
            }
            llm_kwargs = {k: v for k, v in llm_kwargs.items() if v not in (None, "")}
            return self._patch_browser_llm_client(ChatBrowserUse(**llm_kwargs), thinking_extra_body)

        # browser-use Agent defaults to json_schema response_format and may use tool_choice via
        # ChatDeepSeek. Many providers (e.g. DeepSeek thinking models) reject both. Use ChatOpenAI
        # with schema-in-prompt and without forced structured output on the first run.
        llm_kwargs = {
            "model": model_name,
            "api_key": cfg.get("api_key"),
            "base_url": base_url,
            "temperature": self._param.temperature,
            "max_retries": self._param.max_retries,
            "add_schema_to_system_prompt": True,
            "dont_force_structured_output": True,
        }
        llm_kwargs = {k: v for k, v in llm_kwargs.items() if v not in (None, "")}
        return self._patch_browser_llm_client(ChatOpenAI(**llm_kwargs), thinking_extra_body)

    async def _run_browser_use_async(
        self,
        task_text: str,
        download_dir: str,
        available_file_paths: list[str] | None = None,
        profile_dir: str | None = None,
        *,
        upload_system_extension: str = "",
    ):
        from browser_use import Agent as BrowserUseAgent, Browser as BrowserUseBrowser

        llm = self._build_browser_llm()
        # NOTE:
        # _invoke() uses asyncio.run(), which creates a fresh event loop per task run.
        # Reusing a Browser object created by a previous loop can deadlock/timestamp out
        # in browser-use watchdog handlers on subsequent runs.
        # We keep persistent user_data_dir for session continuity, but we do not keep
        # browser instances alive across runs.
        available_file_paths = available_file_paths or []
        if available_file_paths:
            install_cdp_upload_event_dispatch_patch()
        agent_kwargs: dict[str, Any] = {
            "task": task_text,
            "llm": llm,
            "available_file_paths": available_file_paths,
            "use_vision": bool(getattr(self._param, "use_vision", False)),
        }
        if upload_system_extension:
            agent_kwargs["extend_system_message"] = upload_system_extension
        browser_obj = None
        previous_disable_extensions = os.environ.get("BROWSER_USE_DISABLE_EXTENSIONS")
        previous_browser_binary_path = os.environ.get("BROWSER_USE_BROWSER_BINARY_PATH")
        cdp_url = str(getattr(self._param, "cdp_url", "") or "").strip()
        use_cdp = bool(getattr(self._param, "use_cdp", False) and cdp_url)

        try:
            browser_kwargs: dict[str, Any] = {}
            browser_init_params = set()
            browser_accepts_any_kwargs = False
            try:
                browser_signature = inspect.signature(BrowserUseBrowser)
                browser_init_params = set(browser_signature.parameters.keys())
                browser_accepts_any_kwargs = any(
                    p.kind == inspect.Parameter.VAR_KEYWORD for p in browser_signature.parameters.values()
                )
            except (TypeError, ValueError):
                browser_init_params = set()
                browser_accepts_any_kwargs = False

            if use_cdp:
                cdp_param = ""
                for candidate in ("cdp_url", "connect_url", "browser_ws_endpoint", "ws_endpoint", "browser_cdp_url"):
                    if candidate in browser_init_params:
                        cdp_param = candidate
                        break
                if not cdp_param and browser_accepts_any_kwargs:
                    cdp_param = "cdp_url"
                if cdp_param:
                    browser_kwargs[cdp_param] = cdp_url
                    if browser_accepts_any_kwargs or not browser_init_params or "downloads_path" in browser_init_params:
                        browser_kwargs["downloads_path"] = download_dir
                    logging.info("Browser will connect via CDP. url=%s", cdp_url)
                else:
                    logging.warning(
                        "Browser CDP is enabled but browser-use Browser has no recognized CDP argument. "
                        "Fallback to local browser launch."
                    )
                    use_cdp = False

            if not use_cdp:
                enable_default_extensions = bool(self._param.enable_default_extensions)
                if not enable_default_extensions:
                    os.environ["BROWSER_USE_DISABLE_EXTENSIONS"] = "1"
                else:
                    os.environ.pop("BROWSER_USE_DISABLE_EXTENSIONS", None)

                browser_kwargs = {
                    "headless": self._param.headless,
                    "downloads_path": download_dir,
                    # Docker often runs as root without user namespaces; disable sandbox by default.
                    "chromium_sandbox": bool(self._param.chromium_sandbox),
                    # Disable runtime extension download by default for intranet/offline environments.
                    # Enable only when explicitly required and extensions are pre-cached.
                    "enable_default_extensions": enable_default_extensions,
                }
                executable_path = self._resolve_browser_executable()
                if executable_path:
                    browser_kwargs["executable_path"] = executable_path
                    # Keep browser-use watchdog fallback in sync with our resolved path.
                    os.environ["BROWSER_USE_BROWSER_BINARY_PATH"] = executable_path
                else:
                    logging.warning(
                        "Browser no local browser executable found. "
                        "Set BROWSER_USE_EXECUTABLE_PATH or preinstall chromium in image to avoid runtime playwright install."
                    )
                if profile_dir:
                    browser_kwargs["user_data_dir"] = profile_dir
                    # browser-use expects profile_directory to be a profile name
                    # such as "Default" / "Profile 1", not an absolute path.
                    browser_kwargs["profile_directory"] = "Default"

            browser_obj = BrowserUseBrowser(**browser_kwargs)
            agent_kwargs["browser"] = browser_obj
        except (OSError, RuntimeError, TypeError, ValueError) as e:
            logging.warning("Browser browser context customization skipped: %s", e)

        agent = BrowserUseAgent(**agent_kwargs)

        history = None
        run_fn = getattr(agent, "run", None)
        if run_fn is None:
            raise RuntimeError("browser-use Agent does not provide run().")

        run_kwargs = {"max_steps": self._param.max_steps}
        try:
            if inspect.iscoroutinefunction(run_fn):
                history = await run_fn(**run_kwargs)
            else:
                history = await asyncio.to_thread(run_fn, **run_kwargs)
        except Exception as e:
            logging.error("Browser agent.run failed. error_chain=%s", self._error_chain(e))
            logging.exception("Browser agent.run traceback")
            raise
        finally:
            if browser_obj:
                close_fn = getattr(browser_obj, "close", None)
                if close_fn:
                    try:
                        if inspect.iscoroutinefunction(close_fn):
                            await close_fn()
                        else:
                            await asyncio.to_thread(close_fn)
                    except Exception as close_err:
                        logging.warning("Browser failed to close browser object cleanly: %s", close_err)
            self._restore_env_var("BROWSER_USE_DISABLE_EXTENSIONS", previous_disable_extensions)
            self._restore_env_var("BROWSER_USE_BROWSER_BINARY_PATH", previous_browser_binary_path)

        return history

    @staticmethod
    def _looks_like_opaque_id(value: str) -> bool:
        token = str(value or "").strip()
        if not token:
            return False
        if re.fullmatch(r"[0-9a-f]{32}", token, flags=re.I):
            return True
        return bool(re.fullmatch(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}", token, flags=re.I))

    @staticmethod
    def _normalize_upload_display_name(name: str) -> str:
        from agent.component.browser_remote_staging import normalize_upload_filename

        return normalize_upload_filename(name)

    def _resolve_original_filename(self, file: Any, file_id: str) -> str:
        from api.db.services.document_service import DocumentService
        from api.db.services.file2document_service import File2DocumentService

        candidates: list[str] = []
        name = str(getattr(file, "name", "") or "").strip()
        location = str(getattr(file, "location", "") or "").strip()
        if name and not self._looks_like_opaque_id(name):
            candidates.append(name)
        if location:
            base = os.path.basename(location.replace("\\", "/"))
            if base and not self._looks_like_opaque_id(base):
                candidates.append(base)
        for f2d in File2DocumentService.get_by_file_id(file_id) or []:
            doc_id = str(getattr(f2d, "document_id", "") or "").strip()
            if not doc_id:
                continue
            exists, doc = DocumentService.get_by_id(doc_id)
            if exists:
                doc_name = str(getattr(doc, "name", "") or "").strip()
                if doc_name and not self._looks_like_opaque_id(doc_name):
                    candidates.append(doc_name)
        if name:
            ext = str(getattr(file, "type", "") or "").strip().lower().lstrip(".")
            if ext and ext not in {"folder", "virtual", "other"} and "." not in name:
                candidates.append(f"{name}.{ext}")
            candidates.append(name)
        for candidate in candidates:
            cleaned = os.path.basename(str(candidate).replace("\\", "/")).strip()
            if cleaned:
                return self._normalize_upload_display_name(cleaned)
        return self._normalize_upload_display_name(f"{file_id}.bin")

    def _resolve_upload_source_items(self) -> list[dict[str, Any]]:
        raw = getattr(self._param, "upload_sources", "")
        input_value = self.get_input_value("upload_sources")
        if input_value not in (None, ""):
            raw = input_value
        value = self._resolve_param_value(raw)
        items: list[dict[str, Any]] = []

        seen_keys: set[str] = set()

        def append_item(item: dict[str, Any]):
            key = json.dumps(item, sort_keys=True, ensure_ascii=False)
            if key in seen_keys:
                return
            seen_keys.add(key)
            items.append(item)

        def collect(item: Any):
            if item is None:
                return
            if isinstance(item, str):
                token = item.strip()
                if not token:
                    return
                if token.startswith("{") and token.endswith("}") and self._canvas.is_reff(token):
                    resolved = self._canvas.get_variable_value(token)
                    inner = token.strip("{} ").strip()
                    if isinstance(resolved, str):
                        resolved_id = resolved.strip()
                        if self._looks_like_opaque_id(resolved_id) and inner.endswith(".id"):
                            parent_ref = "{" + inner[:-3] + "}"
                            display_name = ""
                            created_by = str(self._canvas.get_tenant_id() or "").strip()
                            if self._canvas.is_reff(parent_ref):
                                parent = self._canvas.get_variable_value(parent_ref)
                                if isinstance(parent, dict):
                                    display_name = self._normalize_upload_display_name(
                                        str(parent.get("name") or parent.get("filename") or "").strip()
                                    )
                                    created_by = str(
                                        parent.get("created_by") or parent.get("tenant_id") or created_by
                                    ).strip()
                            if display_name:
                                append_item(
                                    {
                                        "kind": "session_blob",
                                        "file_id": resolved_id,
                                        "name": display_name,
                                        "created_by": created_by,
                                    }
                                )
                            else:
                                append_item({"kind": "file_id", "file_id": resolved_id})
                            return
                    collect(resolved)
                    return
                if token.startswith("[") and token.endswith("]"):
                    try:
                        parsed = json.loads(token)
                        collect(parsed)
                        return
                    except Exception:
                        pass
                if self._is_http_url(token):
                    append_item({"kind": "url", "url": token})
                    return
                if "," in token:
                    for part in token.split(","):
                        collect(part)
                    return
                append_item({"kind": "file_id", "file_id": token})
                return
            if isinstance(item, dict):
                file_id = str(item.get("file_id") or item.get("id") or "").strip()
                display_name = self._normalize_upload_display_name(
                    str(item.get("name") or item.get("filename") or "").strip()
                )
                created_by = str(
                    item.get("created_by") or item.get("tenant_id") or self._canvas.get_tenant_id() or ""
                ).strip()
                if file_id and display_name:
                    append_item(
                        {
                            "kind": "session_blob",
                            "file_id": file_id,
                            "name": display_name,
                            "created_by": created_by,
                        }
                    )
                    return
                if file_id:
                    append_item({"kind": "file_id", "file_id": file_id, "name": display_name})
                    return
                url = str(item.get("url") or item.get("value") or "").strip()
                if self._is_http_url(url):
                    append_item({"kind": "url", "url": url})
                    return
                for k in ("file_id", "id", "url", "value"):
                    if k in item:
                        collect(item[k])
                        return
                for v in item.values():
                    collect(v)
                return
            if isinstance(item, (list, tuple, set)):
                for v in item:
                    collect(v)
                return
            token = str(item).strip()
            if token:
                collect(token)

        collect(value)
        return items

    def _resolve_upload_source_refs(self) -> list[str]:
        refs: list[str] = []
        for item in self._resolve_upload_source_items():
            if item.get("kind") == "url":
                refs.append(str(item.get("url") or ""))
            else:
                refs.append(str(item.get("file_id") or ""))
        return [ref for ref in refs if ref]

    def _build_upload_task_appendix(self, uploaded_files: list[dict[str, Any]], *, remote_host: bool) -> str:
        if not uploaded_files:
            return ""
        path_label = "remote_path" if remote_host else "local_path"
        lines = [
            f"- file_id={item.get('file_id', '')}, name={item.get('name', '')}, {path_label}={item.get('local_path', '')}"
            for item in uploaded_files
            if item.get("local_path")
        ]
        if not lines:
            return ""
        location_hint = (
            "Preloaded upload files (from Browser node upload_sources, staged via HTTP to remote Chrome host). "
            "Use upload_file with the exact remote_path values (CDP DOM.setFileInputFiles):\n"
            if remote_host
            else "Preloaded upload files (from Browser node upload_sources). "
            "Use upload_file with these exact local paths (CDP DOM.setFileInputFiles):\n"
        )
        return "\n\n" + location_hint + "\n".join(lines) + self._build_upload_execution_hints(remote_host=remote_host)

    @staticmethod
    def _build_upload_system_extension(uploaded_files: list[dict[str, Any]], *, remote_host: bool) -> str:
        if not uploaded_files:
            return ""
        names = ", ".join(str(item.get("name") or item.get("file_id") or "file") for item in uploaded_files)
        host_note = (
            "Files were pushed to the remote Chrome host via HTTP staging before this task started."
            if remote_host
            else "Files were prepared locally before this task started."
        )
        return (
            "\n\n[RAGFlow Browser upload policy]\n"
            f"- Upload sources (configured on this Browser node): {names}\n"
            f"- {host_note}\n"
            "- Follow the user task to navigate to the correct page and table row.\n"
            + (
                "- CRITICAL (remote CDP): NEVER click 上传 / 选择文件 / 浏览 / 一键上传 / any button that opens the OS file picker.\n"
                "  That native Windows dialog CANNOT be controlled by CDP and will block the task.\n"
                "- Find the hidden input[type=file] in the target row (selector map index) and call upload_file(index, remote_path) DIRECTLY without opening the file dialog.\n"
                if remote_host
                else "- Prefer upload_file on input[type=file] without opening the OS file picker when possible.\n"
            )
            + "- upload_file uses CDP DOM.setFileInputFiles; RAGFlow dispatches composed input/change events automatically.\n"
            "- After upload_file, click only the confirm/submit control for that upload slot if the UI requires a separate confirm step.\n"
            "- BEFORE uploading: use search_page (or DOM context from the user task) to locate the correct target slot/row/field. "
            "If the user task's success criteria already appear satisfied (e.g. expected filename, attachment listed, success toast, or any completion signal described in the task), call done immediately — do NOT upload again.\n"
            "- After ONE upload_file + optional confirm + short wait: re-check using the success criteria from the USER task. "
            "If satisfied OR upload_file returned success, call done. Do NOT loop indefinitely."
        )

    def _build_agent_task_text(
        self,
        user_prompt: str,
        uploaded_files: list[dict[str, Any]],
        *,
        remote_host: bool,
    ) -> str:
        return str(user_prompt or "") + self._build_upload_task_appendix(uploaded_files, remote_host=remote_host)

    def _prepare_upload_files(self, upload_dir: str) -> list[dict[str, Any]]:
        prepared = []
        for item in self._resolve_upload_source_items():
            kind = str(item.get("kind") or "").strip()
            if kind == "url":
                prepared_url_file = self._prepare_upload_url_file(str(item.get("url") or ""), upload_dir)
                if prepared_url_file:
                    prepared.append(prepared_url_file)
                continue

            if kind == "session_blob":
                file_id = str(item.get("file_id") or "").strip()
                original_name = self._normalize_upload_display_name(str(item.get("name") or "").strip())
                created_by = str(item.get("created_by") or self._canvas.get_tenant_id() or "").strip()
                if not file_id or not original_name:
                    logging.warning("Browser upload session file missing id/name: %s", item)
                    continue
                try:
                    blob = FileService.get_blob(created_by, file_id)
                    if not blob:
                        logging.warning("Browser upload session blob not found: file_id=%s", file_id)
                        continue
                    local_name = os.path.basename(original_name.replace("\\", "/"))
                    local_path = os.path.join(upload_dir, local_name)
                    index = 1
                    while os.path.exists(local_path):
                        stem, ext = os.path.splitext(local_name)
                        local_path = os.path.join(upload_dir, f"{stem}_{index}{ext}")
                        index += 1
                    with open(local_path, "wb") as f:
                        f.write(blob)
                except OSError as e:
                    logging.warning("Browser failed to prepare session upload file. file_id=%s, error=%s", file_id, e)
                    continue
                except Exception as e:
                    logging.warning("Browser failed to fetch session upload blob. file_id=%s, error=%s", file_id, e)
                    continue
                prepared.append(
                    {
                        "file_id": file_id,
                        "name": original_name,
                        "size": len(blob),
                        "local_path": local_path,
                    }
                )
                continue

            file_id = str(item.get("file_id") or "").strip()
            if not file_id:
                continue
            if self._is_http_url(file_id):
                prepared_url_file = self._prepare_upload_url_file(file_id, upload_dir)
                if prepared_url_file:
                    prepared.append(prepared_url_file)
                continue

            exists, file = FileService.get_by_id(file_id)
            if not exists:
                created_by = str(item.get("created_by") or self._canvas.get_tenant_id() or "").strip()
                hint_name = self._normalize_upload_display_name(str(item.get("name") or "").strip())
                try:
                    blob = FileService.get_blob(created_by, file_id) if created_by else None
                except Exception as e:
                    logging.warning("Browser failed to fetch session upload blob. file_id=%s, error=%s", file_id, e)
                    continue
                if not blob:
                    logging.warning("Browser upload file_id not found: %s", file_id)
                    continue
                original_name = hint_name if hint_name and not self._looks_like_opaque_id(hint_name) else f"{file_id}.bin"
                local_name = os.path.basename(original_name.replace("\\", "/"))
                local_path = os.path.join(upload_dir, local_name)
                index = 1
                while os.path.exists(local_path):
                    stem, ext = os.path.splitext(local_name)
                    local_path = os.path.join(upload_dir, f"{stem}_{index}{ext}")
                    index += 1
                try:
                    with open(local_path, "wb") as f:
                        f.write(blob)
                except OSError as e:
                    logging.warning("Browser failed to prepare session upload file. file_id=%s, error=%s", file_id, e)
                    continue
                prepared.append(
                    {
                        "file_id": file_id,
                        "name": original_name,
                        "size": len(blob),
                        "local_path": local_path,
                    }
                )
                continue
            try:
                blob = settings.STORAGE_IMPL.get(file.parent_id, file.location)
                if not blob:
                    logging.warning("Browser upload blob not found: %s", file_id)
                    continue
                hint_name = self._normalize_upload_display_name(str(item.get("name") or "").strip())
                original_name = hint_name if hint_name and not self._looks_like_opaque_id(hint_name) else self._resolve_original_filename(file, file_id)
                local_name = os.path.basename(original_name.replace("\\", "/"))
                local_path = os.path.join(upload_dir, local_name)
                index = 1
                while os.path.exists(local_path):
                    stem, ext = os.path.splitext(local_name)
                    local_path = os.path.join(upload_dir, f"{stem}_{index}{ext}")
                    index += 1
                with open(local_path, "wb") as f:
                    f.write(blob)
            except OSError as e:
                logging.warning("Browser failed to prepare upload file. file_id=%s, error=%s", file_id, e)
                continue
            except Exception as e:
                logging.warning("Browser failed to fetch upload blob. file_id=%s, error=%s", file_id, e)
                continue
            prepared.append(
                {
                    "file_id": file.id,
                    "name": original_name,
                    "size": file.size,
                    "local_path": local_path,
                }
            )
        return prepared

    def _resolve_remote_staging_config(self):
        return resolve_remote_staging_config(
            str(getattr(self._param, "remote_staging_url", "") or "").strip(),
            str(getattr(self._param, "remote_staging_token", "") or "").strip(),
            cdp_url_fallback=str(getattr(self._param, "cdp_url", "") or "").strip(),
        )

    def _uses_remote_cdp(self) -> bool:
        cdp_url = str(getattr(self._param, "cdp_url", "") or "").strip()
        return bool(getattr(self._param, "use_cdp", False) and cdp_url)

    def _resolve_remote_upload_mode(self) -> str:
        return resolve_remote_upload_mode(str(getattr(self._param, "remote_upload_mode", "") or ""))

    def _resolve_effective_remote_upload_mode(self) -> str:
        mode = self._resolve_remote_upload_mode()
        if mode != "auto":
            return mode
        if self._resolve_remote_staging_config() is not None:
            return "staging"
        return "blob_cdp"

    def _should_stage_on_remote_host(self, uploaded_files: list[dict[str, Any]]) -> bool:
        return bool(uploaded_files) and self._uses_remote_cdp()

    def _should_use_remote_staging(self) -> bool:
        return self._uses_remote_cdp() and self._resolve_effective_remote_upload_mode() == "staging"

    def _stage_upload_files_for_remote_browser(self, prepared_files: list[dict[str, Any]]) -> list[dict[str, Any]]:
        if not prepared_files:
            return []

        staging_config = self._resolve_remote_staging_config()
        if staging_config is None:
            raise RemoteStagingError(
                "Remote CDP browser requires remote staging for file uploads. "
                "Configure Browser node remote_staging_url or env RAGFLOW_BROWSER_REMOTE_STAGING_URL, "
                "and deploy tools/browser_remote_staging/server.py on the Chrome host."
            )

        client = RemoteStagingClient(staging_config)
        healthy, health_reason = client.health_check_detail()
        if not healthy:
            raise RemoteStagingError(
                f"Remote staging server is unreachable or unhealthy: {staging_config.base_url}/health "
                f"({health_reason}). "
                "On the Windows Chrome host, start ragflow-browser-gateway.exe (or gateway.py) and ensure "
                f"port {urlparse(staging_config.base_url).port or 19080} is listening and allowed through the firewall."
            )
        return client.stage_prepared_files(prepared_files)

    async def _stage_upload_files_on_remote_host(self, prepared_files: list[dict[str, Any]]) -> list[dict[str, Any]]:
        if not prepared_files:
            return []

        mode = self._resolve_effective_remote_upload_mode()
        if mode == "staging":
            return await asyncio.to_thread(self._stage_upload_files_for_remote_browser, prepared_files)

        cdp_url = str(getattr(self._param, "cdp_url", "") or "").strip()
        try:
            return await stage_prepared_files_via_cdp_blob(cdp_url, prepared_files)
        except CdpBlobStagingError:
            raise
        except Exception as e:
            raise CdpBlobStagingError(f"CDP blob staging failed: {e}") from e

    async def _run_invoke_async(
        self,
        task_text: str,
        download_dir: str,
        upload_local_paths: list[str],
        profile_dir: str | None,
        *,
        upload_system_extension: str = "",
    ):
        return await self._run_browser_use_async(
            task_text,
            download_dir,
            upload_local_paths,
            profile_dir,
            upload_system_extension=upload_system_extension,
        )

    def _save_downloads(self, download_dir: str, parent_id: str) -> list[dict[str, Any]]:
        downloaded_files: list[dict[str, Any]] = []
        exists, folder = FileService.get_by_id(parent_id)
        if not exists or folder.type != FileType.FOLDER.value:
            raise ValueError(f"RAGFlow target folder does not exist or is not a folder: {parent_id}")
        tenant_id = self._canvas.get_tenant_id()
        storage_put = settings.STORAGE_IMPL.put
        storage_rm = getattr(settings.STORAGE_IMPL, "rm", None)
        insert_file = FileService.insert

        for path in Path(download_dir).rglob("*"):
            if not path.is_file():
                continue
            try:
                if path.stat().st_size <= 0:
                    continue
                blob = path.read_bytes()
            except OSError as e:
                logging.warning("Browser failed to read downloaded file. path=%s, error=%s", path, e)
                continue
            if not blob:
                continue
            display_name = ""
            blob_stored = False
            try:
                display_name = duplicate_name(FileService.query, name=path.name, parent_id=parent_id)
                storage_put(parent_id, display_name, blob)
                blob_stored = True
                file_data = {
                    "id": get_uuid(),
                    "parent_id": parent_id,
                    "tenant_id": tenant_id,
                    "created_by": tenant_id,
                    "type": filename_type(display_name),
                    "name": display_name,
                    "location": display_name,
                    "size": len(blob),
                }
                inserted = insert_file(file_data)
                downloaded_files.append(
                    {
                        "file_id": inserted.id,
                        "name": inserted.name,
                        "size": inserted.size,
                        "parent_id": inserted.parent_id,
                    }
                )
            except Exception as e:
                if blob_stored and callable(storage_rm):
                    try:
                        storage_rm(parent_id, display_name)
                    except Exception as rollback_err:
                        logging.warning(
                            "Browser rollback stored download failed. path=%s, parent_id=%s, display_name=%s, error=%s",
                            path,
                            parent_id,
                            display_name,
                            rollback_err,
                        )
                logging.error(
                    "Browser failed to save download. path=%s, tenant_id=%s, parent_id=%s, display_name=%s, error=%s",
                    path,
                    tenant_id,
                    parent_id,
                    display_name,
                    e,
                )
                continue
        return downloaded_files

    @staticmethod
    def _extract_history_text(history: Any) -> str:
        if history is None:
            return ""

        def pick_final_result(value: Any) -> str:
            if value is None:
                return ""
            if isinstance(value, str):
                return value.strip()
            if isinstance(value, (int, float, bool)):
                return str(value)
            return ""

        # Only trust browser-use's explicit final_result API/property.
        final_result_fn = getattr(history, "final_result", None)
        if callable(final_result_fn):
            try:
                final_result_value = final_result_fn()
                return pick_final_result(final_result_value)
            except Exception:
                return ""
        return pick_final_result(final_result_fn)

    @staticmethod
    def _build_upload_execution_hints(*, remote_host: bool = False) -> str:
        remote_rules = (
            "3. REMOTE CDP: Do NOT click buttons that open the native OS file picker (e.g. Upload / Browse / Choose file / 上传 / 选择文件 / 浏览) — they WILL FAIL on remote Chrome.\n"
            "4. Locate input[type=file] for the target slot described in the user task (often hidden in Shadow DOM) and call upload_file(index, remote_path) directly.\n"
            "5. If multiple file inputs exist, pick the one that matches the user task context (same row/section/label), not unrelated inputs.\n"
            "6. RAGFlow automatically dispatches composed input/change events after upload_file (CDP).\n"
            "7. Only AFTER upload_file, click that slot's confirm/submit control if the UI requires it, then wait briefly.\n"
        )
        local_rules = (
            "3. In the target slot/row/section from the user task, prefer upload_file on input[type=file] without opening the OS file picker.\n"
            "4. Call upload_file on the file input index in that same context only.\n"
            "5. RAGFlow automatically dispatches composed input/change events after upload_file (CDP).\n"
            "6. Then click that slot's confirm control if needed and wait briefly.\n"
        )
        tail = (
            "8. BEFORE upload_file: locate the target using labels/context from the USER task (search_page helps). "
            "If success criteria from the user task already hold, call done immediately — do NOT upload again.\n"
            "9. Perform upload_file (+ confirm if needed) at most TWICE for the same target. "
            "After the second attempt, call done(success=true) if upload_file succeeded, even if the UI is hard to read.\n"
            "10. Element indices change every snapshot — re-locate input[type=file] each time; never reuse stale indices from memory.\n"
            "11. Use the USER task to define what counts as success; do not invent fixed status words not mentioned in the task.\n"
            if remote_host
            else
            "7. BEFORE upload_file: confirm the target slot from the user task. "
            "If success criteria already hold, call done immediately.\n"
            "8. Perform upload_file at most TWICE for the same target, then call done if upload_file succeeded.\n"
            "9. Element indices change every snapshot — re-locate the file input each time.\n"
            "10. Use the USER task for success criteria; do not retry more than twice."
        )
        return (
            "\n\nFile upload execution rules (critical for Shadow DOM / custom upload widgets):\n"
            "1. navigate URLs must be exact http(s) URLs only. Never append instructions to URLs.\n"
            "2. Follow navigation and verification steps exactly as described in the USER task (labels, codes, filters, etc.).\n"
            + (remote_rules if remote_host else local_rules)
            + tail
        )

    @timeout(int(os.environ.get("COMPONENT_EXEC_TIMEOUT", 20 * 60)))
    def _invoke(self, **kwargs):
        profile_dir = None
        persist_session = self._should_persist_session()
        try:
            self._prepare_input_values()
            user_prompt = self._resolve_text(kwargs.get("prompts", self._param.prompts))
            with tempfile.TemporaryDirectory(prefix="browser_use_upload_") as upload_dir, tempfile.TemporaryDirectory(
                prefix="browser_use_download_"
            ) as download_dir:
                uploaded_files = self._prepare_upload_files(upload_dir)
                stage_on_remote = self._should_stage_on_remote_host(uploaded_files)
                remote_upload_mode = self._resolve_effective_remote_upload_mode() if stage_on_remote else ""

                upload_local_paths = [item.get("local_path", "") for item in uploaded_files if item.get("local_path")]
                if stage_on_remote:
                    logging.info(
                        "Browser remote upload mode=%s, cdp_url=%s, upload_sources=%s, files=%s",
                        remote_upload_mode,
                        getattr(self._param, "cdp_url", ""),
                        self._resolve_upload_source_refs(),
                        len(uploaded_files),
                    )

                async def _execute_browser_task():
                    nonlocal uploaded_files, upload_local_paths
                    if stage_on_remote and uploaded_files:
                        uploaded_files = await self._stage_upload_files_on_remote_host(uploaded_files)
                        upload_local_paths = [
                            item.get("local_path", "") for item in uploaded_files if item.get("local_path")
                        ]
                    task_text = self._build_agent_task_text(
                        user_prompt,
                        uploaded_files,
                        remote_host=stage_on_remote,
                    )
                    upload_system_extension = self._build_upload_system_extension(
                        uploaded_files,
                        remote_host=stage_on_remote,
                    )
                    return await self._run_invoke_async(
                        task_text,
                        download_dir,
                        upload_local_paths,
                        profile_dir,
                        upload_system_extension=upload_system_extension,
                    )

                if persist_session:
                    profile_dir = self._resolve_persistent_profile_dir()
                    os.makedirs(profile_dir, exist_ok=True)
                else:
                    try:
                        profile_dir = tempfile.mkdtemp(prefix="browser_use_profile_")
                    except OSError:
                        profile_dir = None
                history = asyncio.run(_execute_browser_task())
                target_dir_id = FileService.get_root_folder(self._canvas.get_tenant_id())["id"]
                downloaded_files = self._save_downloads(download_dir, target_dir_id)

                self.set_output("content", self._extract_history_text(history))
                self.set_output("downloaded_files", downloaded_files)
                return self.output()
        except Exception as e:
            logging.exception("Browser invoke failed")
            self.set_output("_ERROR", str(e))
            return self.output()
        finally:
            if profile_dir and not persist_session:
                shutil.rmtree(profile_dir, ignore_errors=True)

    def thoughts(self) -> str:
        return "Planning and executing browser actions..."
