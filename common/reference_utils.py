#
#  Copyright 2024 The InfiniFlow Authors. All Rights Reserved.
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
"""Shared helpers for retrieval reference (chunks + doc_aggs) attached to answers."""

import re
from typing import Any

from common.text_utils import normalize_arabic_digits

# Same as dialog_service.CITATION_MARKER_PATTERN: [ID:n] or [n], with Arabic-Indic digits normalized later.
_CITATION_MARKER_RE = re.compile(r"\[(?:ID:)?([0-9\u0660-\u0669\u06F0-\u06F9]+)\]")


def _strip_redacted_thinking(text: str) -> str:
    return re.sub(r"<think>.*?</think>", "", text or "", flags=re.DOTALL | re.IGNORECASE)


def _norm_doc_id(v: Any) -> str | None:
    if v is None:
        return None
    return str(v)


def _reference_agg_doc_id(doc: Any) -> Any:
    if not isinstance(doc, dict):
        return None
    if doc.get("doc_id") is not None:
        return doc.get("doc_id")
    return doc.get("document_id")


def _chunk_doc_id(ck: Any) -> str | None:
    if not isinstance(ck, dict):
        return None
    if ck.get("document_id") is not None:
        return _norm_doc_id(ck.get("document_id"))
    return _norm_doc_id(ck.get("doc_id"))


def _extract_cited_ids(answer: str) -> set[int] | None:
    normalized = normalize_arabic_digits(_strip_redacted_thinking(answer or "")) or ""
    if not _CITATION_MARKER_RE.search(normalized):
        return None
    cited_ids: set[int] = set()
    for match in _CITATION_MARKER_RE.finditer(normalized):
        try:
            cited_ids.add(int(match.group(1)))
        except ValueError:
            continue
    return cited_ids


def _filter_doc_aggs(doc_aggs: Any, used_doc_ids: set[str]) -> Any:
    if isinstance(doc_aggs, dict):
        out = {}
        for name, doc in doc_aggs.items():
            rid = _norm_doc_id(_reference_agg_doc_id(doc))
            if rid and rid in used_doc_ids:
                out[name] = doc
        return out
    if isinstance(doc_aggs, list):
        return [d for d in doc_aggs if _norm_doc_id(_reference_agg_doc_id(d)) in used_doc_ids]
    return doc_aggs


def _empty_filtered_reference(chunks: Any, doc_aggs: Any) -> dict:
    if isinstance(chunks, dict):
        return {"chunks": {}, "doc_aggs": _filter_doc_aggs(doc_aggs, set())}
    if isinstance(chunks, list):
        return {"chunks": [], "doc_aggs": _filter_doc_aggs(doc_aggs, set())}
    return {"chunks": chunks, "doc_aggs": doc_aggs}


def filter_reference_by_answer_citations(answer: str, reference: dict) -> dict:
    """
    Keep only retrieval chunks cited in the answer and doc_aggs for those documents.

    - Agent/canvas style: reference["chunks"] is a dict keyed by hash id (kb_prompt hash_id=True);
      citations [ID:n] or [n] must match those keys.
    - Chat style: reference["chunks"] is a list; n is the chunk list index ([ID:n] or [n]).

    If there is no citation marker in the visible answer, returns empty chunks/doc_aggs.
    """
    chunks = reference.get("chunks")
    doc_aggs = reference.get("doc_aggs") or {}
    if chunks is None:
        return reference
    if not chunks and not doc_aggs:
        return reference

    cited_ids = _extract_cited_ids(answer)
    if cited_ids is None:
        return _empty_filtered_reference(chunks, doc_aggs)

    if isinstance(chunks, dict):
        new_chunks: dict = {}
        for k, ck in chunks.items():
            try:
                kid = int(str(k))
            except (TypeError, ValueError):
                continue
            if kid in cited_ids:
                new_chunks[k] = ck
        used_doc_ids = set()
        for ck in new_chunks.values():
            did = _chunk_doc_id(ck)
            if did:
                used_doc_ids.add(did)
        return {"chunks": new_chunks, "doc_aggs": _filter_doc_aggs(doc_aggs, used_doc_ids)}

    if isinstance(chunks, list):
        list_cited_ids = {i for i in cited_ids if 0 <= i < len(chunks)}
        if not list_cited_ids:
            return _empty_filtered_reference(chunks, doc_aggs)
        new_list = [chunks[i] for i in sorted(list_cited_ids)]
        used_doc_ids = set()
        for ck in new_list:
            did = _chunk_doc_id(ck)
            if did:
                used_doc_ids.add(did)
        return {"chunks": new_list, "doc_aggs": _filter_doc_aggs(doc_aggs, used_doc_ids)}

    return reference
