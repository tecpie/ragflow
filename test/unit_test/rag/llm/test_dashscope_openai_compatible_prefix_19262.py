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

"""Tests for issue #19262: the ``LiteLLMBase`` prefix decision must use
the ``openai/`` prefix (not ``dashscope/``) when the target base URL is the
OpenAI-compatible DashScope endpoint (``*/compatible-mode/v1``).

The Tongyi-Qianwen / Dashscope factory default base URL is the
OpenAI-compatible endpoint, so LiteLLM must route via the OpenAI-compatible
client (``openai/<model>`` + ``api_base``). A bare model name fails for
models outside LiteLLM's registry (e.g. DashScope-hosted
``deepseek-v4-flash``). The native DashScope SDK path (``dashscope/...`` to
``*/api/v1``) keeps the dashscope/ prefix.
"""

import pytest

from rag.llm import LITELLM_PROVIDER_PREFIX, SupportedLiteLLMProvider
from rag.llm.chat_model import LiteLLMBase


def _init(provider: SupportedLiteLLMProvider, model_name: str, base_url: str) -> LiteLLMBase:
    return LiteLLMBase("sk-test", model_name, base_url, provider=provider)


class TestDashscopeOpenaiCompatiblePrefixDecision:
    """#19262: openai/ prefix for OpenAI-compatible DashScope endpoint."""

    def test_factory_default_tongyi_qianwen_uses_openai_prefix(self):
        m = _init(
            SupportedLiteLLMProvider.Tongyi_Qianwen,
            "qwen3-8b",
            "https://dashscope.aliyuncs.com/compatible-mode/v1",
        )
        assert m.prefix == "openai/"
        assert m.model_name == "openai/qwen3-8b"

    def test_factory_default_dashscope_uses_openai_prefix(self):
        m = _init(
            SupportedLiteLLMProvider.Dashscope,
            "qwen3-8b",
            "https://dashscope.aliyuncs.com/compatible-mode/v1",
        )
        assert m.prefix == "openai/"
        assert m.model_name == "openai/qwen3-8b"

    def test_unknown_dashscope_model_still_has_provider_prefix(self):
        """Regression: bare deepseek-v4-flash fails LiteLLM provider inference."""
        m = _init(
            SupportedLiteLLMProvider.Tongyi_Qianwen,
            "deepseek-v4-flash",
            "https://dashscope.aliyuncs.com/compatible-mode/v1",
        )
        assert m.model_name == "openai/deepseek-v4-flash"

    def test_international_endpoint_uses_openai_prefix(self):
        m = _init(
            SupportedLiteLLMProvider.Tongyi_Qianwen,
            "qwen3-8b",
            "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
        )
        assert m.prefix == "openai/"

    def test_international_endpoint_with_trailing_slash(self):
        m = _init(
            SupportedLiteLLMProvider.Tongyi_Qianwen,
            "qwen3-8b",
            "https://dashscope-intl.aliyuncs.com/compatible-mode/v1/",
        )
        assert m.prefix == "openai/"

    def test_native_endpoint_keeps_dashscope_prefix(self):
        m = _init(
            SupportedLiteLLMProvider.Dashscope,
            "qwen3-8b",
            "https://dashscope.aliyuncs.com/api/v1",
        )
        assert m.prefix == "dashscope/"
        assert m.model_name == "dashscope/qwen3-8b"

    def test_unrelated_url_keeps_dashscope_prefix(self):
        m = _init(
            SupportedLiteLLMProvider.Dashscope,
            "qwen3-8b",
            "https://api.openai.com/v1",
        )
        assert m.prefix == "dashscope/"


class TestDashscopeFamilyProviderGuard:
    @pytest.mark.parametrize(
        ("provider", "model_name"),
        [
            (SupportedLiteLLMProvider.Moonshot, "moonshot-v1-8k"),
            (SupportedLiteLLMProvider.DeepSeek, "deepseek-chat"),
            (SupportedLiteLLMProvider.Anthropic, "claude-3-7-sonnet"),
        ],
    )
    def test_compatible_mode_url_keeps_non_dashscope_prefix(self, provider, model_name):
        m = _init(provider, model_name, "https://mock.example.com/v1/compatible-mode/v1")
        assert m.prefix == LITELLM_PROVIDER_PREFIX.get(provider, "")


class TestTargetsOpenaiCompatibleEndpoint:
    def test_empty_url(self):
        assert LiteLLMBase._targets_openai_compatible_endpoint("") is True
        assert LiteLLMBase._targets_openai_compatible_endpoint(None) is True

    def test_strips_trailing_slash(self):
        assert LiteLLMBase._targets_openai_compatible_endpoint("https://dashscope.aliyuncs.com/compatible-mode/v1/") is True

    def test_unrelated_path_is_false(self):
        assert LiteLLMBase._targets_openai_compatible_endpoint("https://dashscope.aliyuncs.com/api/v1") is False

    def test_substring_is_false(self):
        assert LiteLLMBase._targets_openai_compatible_endpoint("https://example.test/custom-compatible-mode/v1") is False
