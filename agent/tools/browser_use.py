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
import importlib
import inspect
import json
import logging
from abc import ABC
from typing import Any, Callable

from agent.tools.base import ToolBase, ToolMeta, ToolParamBase
from api.db.joint_services.tenant_model_service import (
    get_model_config_by_type_and_name,
    get_tenant_default_model_by_type,
)
from common.constants import LLMType


class BrowserUseParam(ToolParamBase):
    def __init__(self):
        self.meta: ToolMeta = {
            "name": "browser_use",
            "description": (
                "Use browser automation to perform multi-step web tasks such as opening "
                "pages, extracting information, and interacting with elements."
            ),
            "parameters": {
                "task": {
                    "type": "string",
                    "description": "A clear instruction describing what browser-use should do.",
                    "default": "{sys.query}",
                    "required": True,
                },
                "start_url": {
                    "type": "string",
                    "description": "Optional URL to open before executing the task.",
                    "default": "",
                    "required": False,
                },
                "max_steps": {
                    "type": "integer",
                    "description": "Maximum number of interaction steps.",
                    "default": 8,
                    "required": False,
                },
                "timeout_sec": {
                    "type": "integer",
                    "description": "Execution timeout in seconds.",
                    "default": 180,
                    "required": False,
                },
                "headless": {
                    "type": "boolean",
                    "description": "Run browser in headless mode.",
                    "default": True,
                    "required": False,
                },
            },
        }
        super().__init__()
        self.task = "{sys.query}"
        self.start_url = ""
        self.max_steps = 8
        self.timeout_sec = 180
        self.headless = True
        self.llm_id = ""
        self.outputs = {
            "result": {"value": "", "type": "string"},
            "json": {"value": {}, "type": "Object"},
        }

    def check(self):
        self.check_positive_integer(self.max_steps, "Maximum steps")
        self.check_positive_integer(self.timeout_sec, "Timeout")
        self.check_boolean(self.headless, "Headless")


class BrowserUse(ToolBase, ABC):
    component_name = "BrowserUse"

    async def _invoke_async(self, **kwargs):
        payload = self._prepare_payload(kwargs)

        if self.check_if_canceled("BrowserUse processing"):
            return ""

        result = await self._run(payload)
        final_text, structured = self._normalize_output(result)

        self.set_output("result", final_text)
        self.set_output("json", structured)
        return final_text

    def _prepare_payload(self, kwargs: dict[str, Any]) -> dict[str, Any]:
        return {
            "task": kwargs.get("task", self._param.task),
            "start_url": kwargs.get("start_url", self._param.start_url),
            "max_steps": int(kwargs.get("max_steps", self._param.max_steps)),
            "timeout_sec": int(kwargs.get("timeout_sec", self._param.timeout_sec)),
            "headless": bool(kwargs.get("headless", self._param.headless)),
        }

    async def _run(self, payload: dict[str, Any]) -> Any:
        return await self._run_with_python_api(payload)

    async def _run_with_python_api(self, payload: dict[str, Any]) -> Any:
        try:
            module = importlib.import_module("browser_use")
        except ImportError as e:
            raise RuntimeError(
                "browser-use is unavailable. Install dependency first. "
                "For source deployments run: uv sync --python 3.12. "
                "For Docker deployments rebuild the image after pulling latest code."
            ) from e

        agent_cls = getattr(module, "Agent", None)
        if agent_cls is None:
            raise RuntimeError("Cannot find browser_use.Agent. Please use BROWSER_USE_RUNNER integration.")

        init_sig = inspect.signature(agent_cls.__init__)
        init_kwargs: dict[str, Any] = {}
        init_params = init_sig.parameters
        if "task" in init_params:
            init_kwargs["task"] = payload["task"]
        for key in ("start_url", "starting_url", "url"):
            if key in init_params and payload["start_url"]:
                init_kwargs[key] = payload["start_url"]
                break
        for key in ("max_steps", "max_actions"):
            if key in init_params:
                init_kwargs[key] = payload["max_steps"]
                break

        llm_obj = self._build_local_llm()
        if llm_obj is not None and "llm" in init_params:
            init_kwargs["llm"] = llm_obj

        llm_param = init_params.get("llm")
        if llm_param is not None and llm_param.default is inspect._empty and "llm" not in init_kwargs:
            raise RuntimeError(
                "browser_use.Agent requires an llm parameter in this version. "
                "Please select a chat model in BrowserUse config (llm_id), "
                "or configure a tenant default chat model."
            )

        agent = agent_cls(**init_kwargs)
        run_fn = getattr(agent, "run", None)
        if not callable(run_fn):
            raise RuntimeError("browser_use.Agent.run is unavailable.")

        run_sig = inspect.signature(run_fn)
        run_kwargs: dict[str, Any] = {}
        if "max_steps" in run_sig.parameters:
            run_kwargs["max_steps"] = payload["max_steps"]
        if "task" in run_sig.parameters and "task" not in init_kwargs:
            run_kwargs["task"] = payload["task"]
        if "start_url" in run_sig.parameters and payload["start_url"]:
            run_kwargs["start_url"] = payload["start_url"]
        if "headless" in run_sig.parameters:
            run_kwargs["headless"] = payload["headless"]

        result = run_fn(**run_kwargs)
        if inspect.isawaitable(result):
            return await asyncio.wait_for(result, timeout=payload["timeout_sec"])
        return await asyncio.wait_for(asyncio.to_thread(lambda: result), timeout=payload["timeout_sec"])

    def _build_local_llm(self) -> Any | None:
        model, base_url, api_key = self._resolve_llm_connection()
        temperature = 0.2

        # If user does not provide llm_id and tenant default is unavailable,
        # keep browser-use default behavior (which may require cloud API key).
        if not model and not base_url:
            return None

        candidates = [
            ("browser_use.llm.openai.chat", "ChatOpenAI"),
            ("langchain_openai", "ChatOpenAI"),
        ]
        kwargs = {
            "model": model,
            "model_name": model,
            "base_url": base_url,
            "api_key": api_key,
            "temperature": temperature,
        }

        for module_name, class_name in candidates:
            try:
                mod = importlib.import_module(module_name)
                cls = getattr(mod, class_name, None)
                if cls is None:
                    continue
                return self._instantiate_with_supported_kwargs(cls, kwargs)
            except Exception as e:
                logging.warning("Failed to initialize %s.%s: %s", module_name, class_name, e)

        raise RuntimeError(
            "Failed to initialize local LLM for browser-use. "
            "Please install langchain-openai."
        )

    def _resolve_llm_connection(self) -> tuple[str, str, str]:
        # Priority:
        # 1) BrowserUse tool param llm_id from UI configuration.
        # 2) Tenant default chat model.
        llm_id = getattr(self._param, "llm_id", "") or ""
        tenant_id = self._canvas.get_tenant_id()

        if llm_id:
            try:
                model_config = get_model_config_by_type_and_name(tenant_id, LLMType.CHAT, llm_id)
                model = model_config.get("llm_name", "") or llm_id
                base_url = model_config.get("api_base", "") or ""
                api_key = (model_config.get("api_key", "") or "EMPTY").strip() or "EMPTY"
                return model, base_url, api_key
            except Exception as e:
                logging.warning("BrowserUse failed to resolve llm_id=%s: %s", llm_id, e)

        # Fallback to tenant default chat model when llm_id is not set.
        if not llm_id:
            try:
                model_config = get_tenant_default_model_by_type(tenant_id, LLMType.CHAT)
                model = model_config.get("llm_name", "") or ""
                base_url = model_config.get("api_base", "") or ""
                api_key = (model_config.get("api_key", "") or "EMPTY").strip() or "EMPTY"
                if model or base_url:
                    return model, base_url, api_key
            except Exception as e:
                logging.warning("BrowserUse failed to load tenant default chat model: %s", e)
        return "", "", "EMPTY"

    @staticmethod
    def _instantiate_with_supported_kwargs(cls: Any, raw_kwargs: dict[str, Any]) -> Any:
        sig = inspect.signature(cls.__init__)
        supported = set(sig.parameters.keys())
        kwargs = {}
        for key, value in raw_kwargs.items():
            if key not in supported:
                continue
            if key in ("model", "model_name") and not value:
                continue
            if key == "base_url" and not value:
                continue
            kwargs[key] = value
        return cls(**kwargs)

    @staticmethod
    def _normalize_output(result: Any) -> tuple[str, dict[str, Any]]:
        if isinstance(result, str):
            return result, {"result": result}
        if isinstance(result, dict):
            text = result.get("result") or result.get("final_result") or result.get("stdout") or json.dumps(result, ensure_ascii=False)
            return str(text), result

        text = str(result)
        return text, {"result": text}

    def thoughts(self) -> str:
        return "Using browser-use to complete a web automation task."
