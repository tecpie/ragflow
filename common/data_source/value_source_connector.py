"""Query RDBMS / REST API connectors for metadata enums and data preview."""

from collections.abc import Mapping
from typing import Any

from common.constants import FileSource
from common.data_source import build_connector_for_source

QUERYABLE_SOURCES = frozenset({FileSource.MYSQL, FileSource.POSTGRESQL, FileSource.REST_API})
DEFAULT_QUERY_LIMIT = 100


def is_queryable_source(source: str) -> bool:
    return source in QUERYABLE_SOURCES


def query_connector_data(connector_record: dict, *, limit: int = DEFAULT_QUERY_LIMIT) -> dict[str, Any]:
    source = connector_record["source"]
    if source in (FileSource.MYSQL, FileSource.POSTGRESQL):
        return query_rdbms_data(connector_record, limit=limit)
    if source == FileSource.REST_API:
        return query_rest_api_data(connector_record, limit=limit)
    raise ValueError(f"Unsupported query source type: {source}")


def fetch_enum_options(connector_record: dict, vs: dict | None = None) -> list[dict[str, str]]:
    """Return [{"value": str, "description": str}, ...]. `vs` is the field-level value_source dict."""
    vs = vs or {}
    source = connector_record["source"]
    if source in (FileSource.MYSQL, FileSource.POSTGRESQL):
        options = _fetch_enum_from_rdbms(connector_record, vs)
    elif source == FileSource.REST_API:
        options = _fetch_enum_from_rest_api(connector_record, vs)
    else:
        raise ValueError(f"Unsupported value source type: {source}")
    if not options:
        raise ValueError(
            f"Value source returned no options (connector_id={connector_record.get('id', '')})"
        )
    return options


def query_rdbms_data(connector_record: dict, *, limit: int) -> dict[str, Any]:
    source = connector_record["source"]
    config = connector_record["config"]
    connector = build_connector_for_source(source, config)
    connection = connector._get_connection()
    cursor = connection.cursor()
    try:
        cursor.execute(config["query"])
        column_names = [desc[0] for desc in (cursor.description or [])]
        rows: list[dict[str, Any]] = []
        for row in cursor.fetchall():
            if len(rows) >= limit:
                break
            row_dict = _row_to_dict(column_names, row)
            if row_dict is None:
                continue
            rows.append({col: _serialize_cell(row_dict.get(col)) for col in column_names})
        return {"columns": column_names, "rows": rows}
    finally:
        cursor.close()
        connector._close_connection()


def query_rest_api_data(connector_record: dict, *, limit: int) -> dict[str, Any]:
    source = connector_record["source"]
    config = connector_record.get("config") or {}
    connector = build_connector_for_source(source, config)
    items = _rest_api_first_page_items(connector)
    serialized: list[dict[str, Any]] = []
    for it in items:
        if len(serialized) >= limit:
            break
        if not isinstance(it, Mapping):
            continue
        serialized.append({str(k): _serialize_cell(v) for k, v in it.items()})
    return {"items": serialized}


def _resolve_result_column_name(column_names: list[str], requested: str) -> str | None:
    req = (requested or "").strip()
    if not req:
        return None
    if req in column_names:
        return req
    rl = req.lower()
    for cn in column_names:
        if cn.lower() == rl:
            return cn
    return None


def _row_to_dict(column_names: list[str], row: Any) -> dict[str, Any] | None:
    if isinstance(row, (list, tuple)):
        return dict(zip(column_names, row))
    if isinstance(row, dict):
        return row
    return None


def _serialize_cell(value: Any) -> Any:
    if value is None or isinstance(value, (str, int, float, bool)):
        return value
    if isinstance(value, Mapping):
        return {str(k): _serialize_cell(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [_serialize_cell(v) for v in value]
    return str(value)


def _fetch_enum_from_rdbms(connector_record: dict, vs: dict) -> list[dict[str, str]]:
    val_field = (vs.get("enum_value_field") or "").strip()
    if not val_field:
        raise ValueError(
            "value_source.enum_value_field is required for MySQL/PostgreSQL metadata value sources "
            "(use the result column name or alias, same as in RDBMSConnector._row_to_document)"
        )
    desc_field = (vs.get("enum_description_field") or "").strip()

    source = connector_record["source"]
    config = connector_record["config"]
    connector = build_connector_for_source(source, config)
    connection = connector._get_connection()
    cursor = connection.cursor()
    try:
        cursor.execute(config["query"])
        column_names = [desc[0] for desc in (cursor.description or [])]
        val_col = _resolve_result_column_name(column_names, val_field)
        if val_col is None:
            raise ValueError(
                f"value_source.enum_value_field {val_field!r} not found in query result columns {column_names!r}"
            )
        desc_col: str | None = None
        if desc_field:
            desc_col = _resolve_result_column_name(column_names, desc_field)
            if desc_col is None:
                raise ValueError(
                    f"value_source.enum_description_field {desc_field!r} not found in query result columns {column_names!r}"
                )
            if desc_col == val_col:
                raise ValueError("enum_value_field and enum_description_field must map to different columns")

        out: list[dict[str, str]] = []
        for row in cursor.fetchall():
            row_dict = _row_to_dict(column_names, row)
            if row_dict is None:
                continue
            raw_v = row_dict.get(val_col)
            if raw_v is None:
                continue
            val = str(raw_v)
            desc = ""
            if desc_col:
                raw_d = row_dict.get(desc_col)
                if raw_d is not None:
                    desc = str(raw_d)
            out.append({"value": val, "description": desc})
        return out
    finally:
        cursor.close()
        connector._close_connection()


def _fetch_enum_from_rest_api(connector_record: dict, vs: dict) -> list[dict[str, str]]:
    val_field = (vs.get("enum_value_field") or "").strip()
    if not val_field:
        raise ValueError(
            "value_source.enum_value_field is required for REST API metadata value sources"
        )
    desc_key = (vs.get("enum_description_field") or "").strip()

    source = connector_record["source"]
    config = connector_record.get("config") or {}
    connector = build_connector_for_source(source, config)
    items = _rest_api_first_page_items(connector)
    if not items:
        raise ValueError(
            "REST value source: no object items in response; configure items_path / pagination so "
            "_extract_items returns records, or check the first page body."
        )

    out: list[dict[str, str]] = []
    for it in items:
        if not isinstance(it, Mapping):
            continue
        v = it.get(val_field)
        if v is None:
            continue
        desc = ""
        if desc_key:
            dv = it.get(desc_key)
            if dv is not None:
                desc = str(dv)
        out.append({"value": str(v), "description": desc})
    return out


def _rest_api_first_page_items(connector: Any) -> list[Any]:
    from common.data_source.rest_api_connector import PaginationType

    params: dict[str, Any] = {}
    if connector.pagination_type == PaginationType.PAGE:
        page = int(connector.pagination_config.get("start_page", 1))
        per_page = connector._resolve_page_size()
        connector._apply_page_pagination(params, page, per_page)
    elif connector.pagination_type == PaginationType.OFFSET:
        per_page = connector._resolve_page_size()
        offset = int(connector.pagination_config.get("start_offset", 0))
        limit = int(connector.pagination_config.get("limit", per_page))
        if limit <= 0:
            limit = per_page
        connector._apply_offset_pagination(params, offset, limit)
    elif connector.pagination_type == PaginationType.CURSOR:
        cursor = connector.pagination_config.get("initial_cursor")
        if cursor is not None:
            connector._apply_cursor_pagination(params, cursor)

    data = connector._fetch_page(params=params)
    return connector._extract_items(data)
