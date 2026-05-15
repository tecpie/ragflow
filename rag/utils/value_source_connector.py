"""Value source connector factory for fetching metadata enum values from existing connectors."""

from collections.abc import Mapping
from typing import Any

from common.constants import FileSource
from common.data_source.rdbms_connector import RDBMSConnector


class ValueSourceConnector:
    """Fetch enum options from a saved connector row (value + optional description per option)."""

    @staticmethod
    def fetch_enum_options(connector_record: dict, vs: dict | None = None) -> list[dict[str, str]]:
        """Return [{"value": str, "description": str}, ...]. `vs` is the field-level value_source dict."""
        vs = vs or {}
        source = connector_record["source"]
        handler = value_source_factory.get(source)
        if handler is None:
            raise ValueError(f"Unsupported value source type: {source}")
        options = handler(connector_record, vs)
        if not options:
            raise ValueError(
                f"Value source returned no options (connector_id={connector_record.get('id', '')})"
            )
        return options


def _resolve_result_column_name(column_names: list[str], requested: str) -> str | None:
    """Match user-configured name to a column from cursor.description (case-insensitive fallback)."""
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


def _fetch_from_rdbms(connector_record: dict, vs: dict) -> list[dict[str, str]]:
    val_field = (vs.get("enum_value_field") or "").strip()
    if not val_field:
        raise ValueError(
            "value_source.enum_value_field is required for MySQL/PostgreSQL metadata value sources "
            "(use the result column name or alias, same as in RDBMSConnector._row_to_document)"
        )
    desc_field = (vs.get("enum_description_field") or "").strip()

    config = connector_record["config"]
    creds = config["credentials"]
    source = connector_record["source"]
    connector = RDBMSConnector(
        db_type=source,
        host=config["host"],
        port=int(config["port"]),
        database=config["database"],
        query=config["query"],
        content_columns=config["content_columns"],
        metadata_columns=config["metadata_columns"],
        id_column=config["id_column"] or None,
        timestamp_column=config["timestamp_column"] or None,
    )
    connector.load_credentials({"username": creds["username"], "password": creds["password"]})
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
            row_dict = dict(zip(column_names, row)) if isinstance(row, (list, tuple)) else row
            if not isinstance(row_dict, dict):
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


def _fetch_from_rest_api(connector_record: dict, vs: dict) -> list[dict[str, str]]:
    from common.data_source.rest_api_connector import PaginationType, RestAPIConnector

    val_field = (vs.get("enum_value_field") or "").strip()
    if not val_field:
        raise ValueError(
            "value_source.enum_value_field is required for REST API metadata value sources"
        )
    desc_key = (vs.get("enum_description_field") or "").strip()

    raw = connector_record.get("config") or {}
    cfg = RestAPIConnector.parse_storage_config(raw)
    connector = RestAPIConnector.from_parsed_config(cfg, max_pages=1)
    connector.load_credentials(raw.get("credentials") or {})

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
    items = connector._extract_items(data)
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


value_source_factory = {
    FileSource.MYSQL: _fetch_from_rdbms,
    FileSource.POSTGRESQL: _fetch_from_rdbms,
    FileSource.REST_API: _fetch_from_rest_api,
}
