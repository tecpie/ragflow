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
"""Prompt helpers for DeepDOC table/signature Vision enhancement."""

from jinja2.sandbox import SandboxedEnvironment

from rag.prompts.template import load_prompt

_PROMPT_ENV = SandboxedEnvironment(autoescape=False, trim_blocks=True, lstrip_blocks=True)
_TABLE_PROMPT = load_prompt("vision_llm_table_describe_prompt")
_SIGNATURE_PROMPT = load_prompt("vision_llm_signature_describe_prompt")


def vision_llm_table_describe_prompt(language: str = "English") -> str:
    return _PROMPT_ENV.from_string(_TABLE_PROMPT).render(language=language)


def vision_llm_signature_describe_prompt(language: str = "English") -> str:
    return _PROMPT_ENV.from_string(_SIGNATURE_PROMPT).render(language=language)
