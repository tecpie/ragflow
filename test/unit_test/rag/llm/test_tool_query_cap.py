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

import json
from types import SimpleNamespace

import pytest

from rag.llm.chat_model import Base, _MAX_TOOL_QUERY_CHARS, _prepare_tool_args, _tool_args_json

pytestmark = pytest.mark.p1


def test_prepare_tool_args_truncates_runaway_query():
    raw = json.dumps({"query": "封面公章" + "标注" * 400}, ensure_ascii=False)
    args = _prepare_tool_args("search_my_dateset_0", raw)
    assert len(args["query"]) == _MAX_TOOL_QUERY_CHARS
    assert args["query"].startswith("封面公章")


def test_prepare_tool_args_keeps_short_query():
    args = _prepare_tool_args("search_my_dateset_0", '{"query": "封面 公章 签字页"}')
    assert args["query"] == "封面 公章 签字页"


def test_prepare_tool_args_does_not_cap_context():
    ctx = "x" * 3000
    args = _prepare_tool_args("agent", json.dumps({"context": ctx, "query": "ok"}, ensure_ascii=False))
    assert args["context"] == ctx
    assert args["query"] == "ok"


def test_append_history_batch_uses_capped_args_not_raw_tool_call():
    raw_query = "封面" + "标注" * 400
    capped = _prepare_tool_args("search_my_dateset_0", json.dumps({"query": raw_query}, ensure_ascii=False))
    tc = SimpleNamespace(
        index=0,
        id="call_1",
        function=SimpleNamespace(name="search_my_dateset_0", arguments=json.dumps({"query": raw_query}, ensure_ascii=False)),
    )
    hist = []
    Base._append_history_batch(None, hist, [(tc, "search_my_dateset_0", capped, "ok", None)])
    stored = json.loads(hist[0]["tool_calls"][0]["function"]["arguments"])
    assert stored["query"] == capped["query"]
    assert len(stored["query"]) == _MAX_TOOL_QUERY_CHARS
    assert _tool_args_json(capped).startswith('{"query":')
