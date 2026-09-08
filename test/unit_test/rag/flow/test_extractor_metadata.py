from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from rag.flow.extractor.extractor import Extractor


def test_persist_document_metadata_writes_immediately():
    extractor = Extractor.__new__(Extractor)
    extractor._param = MagicMock(field_name="metadata")
    extractor._canvas = MagicMock(_doc_id="doc_test")

    with (
        patch("rag.flow.extractor.extractor.DocMetadataService.get_document_metadata", return_value={"_version": 1}),
        patch("rag.flow.extractor.extractor.DocMetadataService.update_document_metadata", return_value=True) as update_document_metadata,
    ):
        extractor._persist_document_metadata(
            (
                '{"keywords":["火电厂","污染防治"],'
                '"documentDate":"2017","docAbstract":"火电厂污染防治可行技术指南。"}'
            )
        )

    update_document_metadata.assert_called_once_with(
        "doc_test",
        {
            "keywords": ["火电厂", "污染防治"],
            "documentDate": "2017",
            "docAbstract": "火电厂污染防治可行技术指南。",
            "_version": 1,
        },
    )


@pytest.mark.asyncio
async def test_metadata_extraction_combines_parser_chunks_and_calls_llm_once():
    extractor = Extractor.__new__(Extractor)
    extractor._param = MagicMock(field_name="metadata")
    extractor.callback = MagicMock()
    extractor.set_output = MagicMock()
    extractor.get_input_elements = MagicMock(
        return_value={
            "parser_json": {
                "value": [
                    {"text": "第一段"},
                    {"text": "第二段"},
                ]
            }
        }
    )
    extractor._sys_prompt_and_msg = MagicMock(return_value=([{"role": "user", "content": "全文"}], "system"))
    extractor._generate_async = AsyncMock(return_value='{"keywords":["火电厂"]}')
    extractor._persist_document_metadata = MagicMock()

    chunks = [{"text": "第一段"}, {"text": "第二段"}]
    await extractor._invoke(output_format="json", json=chunks)

    extractor._generate_async.assert_awaited_once()
    extractor._sys_prompt_and_msg.assert_called_once_with([], {"parser_json": "第一段\n\n第二段"})
    extractor._persist_document_metadata.assert_called_once_with('{"keywords":["火电厂"]}')
    extractor.set_output.assert_any_call("chunks", chunks)
