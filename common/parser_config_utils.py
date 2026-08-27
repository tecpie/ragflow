#
#  Copyright 2025 The InfiniFlow Authors. All Rights Reserved.
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

import re
from typing import Any

_MODEL_ID_RE = re.compile(r"^[0-9a-f]{32}$", re.I)


def is_tenant_model_id(value: Any) -> bool:
    return isinstance(value, str) and bool(_MODEL_ID_RE.match(value.strip()))


# Parser-specific option keys. ``_has_mineru_options`` uses these to detect
# whether the operator clearly intended the MinerU parser (issue #17114).
MINERU_OPTION_KEYS: tuple[str, ...] = (
    "mineru_parse_method",
    "mineru_formula_enable",
    "mineru_table_enable",
    "mineru_lang",
)


def has_mineru_options(parser_config: Any) -> bool:
    """Return True if parser_config carries any MinerU-specific option.

    Used by the PDF dispatch in :mod:`rag.app.naive` to recover from a
    misconfigured ``layout_recognize`` value (a stale TenantModel id rather
    than the ``"MinerU"`` keyword) — see issue #17114.
    """
    if not isinstance(parser_config, dict):
        return False
    return any(k in parser_config for k in MINERU_OPTION_KEYS)


def normalize_layout_recognizer(layout_recognizer_raw: Any) -> tuple[Any, str | None]:
    parser_model_name: str | None = None
    layout_recognizer = layout_recognizer_raw

    if isinstance(layout_recognizer_raw, str):
        lowered = layout_recognizer_raw.lower()
        if lowered.endswith("@mineru"):
            parser_model_name = layout_recognizer_raw
            layout_recognizer = "MinerU"
        elif lowered.endswith("@paddleocr"):
            parser_model_name = layout_recognizer_raw
            layout_recognizer = "PaddleOCR"
        elif lowered.endswith("@opendataloader"):
            parser_model_name = layout_recognizer_raw
            layout_recognizer = "OpenDataLoader"
        elif lowered.endswith("@somark"):
            # Keep the full 3-segment form ``<llm_name>@<instance_name>@<provider>``
            # produced by the new Tenant LLM Provider UI (#14595); downstream
            # ``get_model_config_from_provider_instance`` -> ``split_model_name``
            # expects all three segments to locate the provider/instance row.
            parser_model_name = layout_recognizer_raw
            layout_recognizer = "SoMark"
        elif lowered.endswith("@mistral ocr"):
            # Separate OCR-only factory (never the multi-type "Mistral" factory),
            # so this suffix cannot collide with pixtral vision models.
            parser_model_name = layout_recognizer_raw
            layout_recognizer = "Mistral OCR"

    return layout_recognizer, parser_model_name


def resolve_layout_recognizer(
    tenant_id: str | None,
    layout_recognizer_raw: Any,
) -> tuple[Any, str | None]:
    """Resolve layout_recognize to (parser_kind, model_ref).

    When tenant_id is set, hex tenant_model.id values are resolved via DB.
    Otherwise only static names and ``model@instance@provider`` suffixes
    are normalized.
    """
    if tenant_id:
        from api.db.joint_services.tenant_model_service import (
            resolve_layout_recognizer as _resolve_from_db,
        )

        return _resolve_from_db(tenant_id, layout_recognizer_raw)
    return normalize_layout_recognizer(layout_recognizer_raw)
