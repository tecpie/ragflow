//
// Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package connector

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"ragflow/internal/common"
)

// DefaultQueryLimit caps preview rows returned by QueryConnectorData,
// matching Python common/data_source/value_source_connector.py.
const DefaultQueryLimit = 100

// ConnectorRecord is the minimal connector row FetchEnumOptions / QueryConnectorData need.
type ConnectorRecord struct {
	ID     string
	Source string
	Config map[string]any
}

// ConnectorLoader resolves a connector id to its source + config.
type ConnectorLoader func(ctx context.Context, id string) (ConnectorRecord, error)

// IsQueryableSource reports whether source supports data/enum preview queries.
func IsQueryableSource(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "mysql", "postgresql", "rest_api":
		return true
	default:
		return false
	}
}

// PrefetchMetadataValueSources resolves value_source connectors once and fills
// EnumOptions / Enum on each field, mirroring Python
// _prefetch_metadata_value_sources. Fields without a connector_id are skipped.
// load may be nil when no field carries a value_source.
func PrefetchMetadataValueSources(ctx context.Context, fields []common.MetadataFieldDef, load ConnectorLoader) error {
	for i := range fields {
		vs := fields[i].ValueSource
		if vs == nil || strings.TrimSpace(vs.ConnectorID) == "" {
			continue
		}
		if load == nil {
			return fmt.Errorf("value source connector loader is required")
		}
		rec, err := load(ctx, vs.ConnectorID)
		if err != nil {
			return fmt.Errorf("value source connector not found: %s (field %q): %w", vs.ConnectorID, fields[i].Key, err)
		}
		opts, err := FetchEnumOptions(ctx, rec.Source, rec.Config, *vs)
		if err != nil {
			return fmt.Errorf("value source for field %q: %w", fields[i].Key, err)
		}
		fields[i].EnumOptions = opts
		enums := make([]string, 0, len(opts))
		for _, opt := range opts {
			enums = append(enums, opt.Value)
		}
		fields[i].Enum = enums
	}
	return nil
}

// FetchEnumOptions queries a MySQL / PostgreSQL / REST connector and returns
// [{value, description}, ...], mirroring Python fetch_enum_options.
func FetchEnumOptions(ctx context.Context, source string, config map[string]any, vs common.MetadataValueSource) ([]common.MetadataEnumOption, error) {
	source = strings.ToLower(strings.TrimSpace(source))
	var opts []common.MetadataEnumOption
	var err error
	switch source {
	case "mysql", "postgresql":
		opts, err = fetchEnumFromRDBMS(ctx, source, config, vs)
	case "rest_api":
		opts, err = fetchEnumFromREST(ctx, config, vs)
	default:
		return nil, fmt.Errorf("unsupported value source type: %s", source)
	}
	if err != nil {
		return nil, err
	}
	if len(opts) == 0 {
		return nil, fmt.Errorf("value source returned no options")
	}
	return opts, nil
}

// QueryConnectorData returns a preview of connector rows/items, mirroring
// Python query_connector_data.
func QueryConnectorData(ctx context.Context, source string, config map[string]any, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = DefaultQueryLimit
	}
	source = strings.ToLower(strings.TrimSpace(source))
	switch source {
	case "mysql", "postgresql":
		return queryRDBMSData(ctx, source, config, limit)
	case "rest_api":
		return queryRESTData(ctx, config, limit)
	default:
		return nil, fmt.Errorf("unsupported query source type: %s", source)
	}
}

func fetchEnumFromRDBMS(ctx context.Context, source string, config map[string]any, vs common.MetadataValueSource) ([]common.MetadataEnumOption, error) {
	valField := strings.TrimSpace(vs.EnumValueField)
	if valField == "" {
		return nil, fmt.Errorf("value_source.enum_value_field is required for MySQL/PostgreSQL metadata value sources")
	}
	descField := strings.TrimSpace(vs.EnumDescriptionField)

	columns, rows, err := queryRDBMSRows(ctx, source, config, 0)
	if err != nil {
		return nil, err
	}
	valCol := resolveResultColumnName(columns, valField)
	if valCol == "" {
		return nil, fmt.Errorf("value_source.enum_value_field %q not found in query result columns %v", valField, columns)
	}
	descCol := ""
	if descField != "" {
		descCol = resolveResultColumnName(columns, descField)
		if descCol == "" {
			return nil, fmt.Errorf("value_source.enum_description_field %q not found in query result columns %v", descField, columns)
		}
		if descCol == valCol {
			return nil, fmt.Errorf("enum_value_field and enum_description_field must map to different columns")
		}
	}

	out := make([]common.MetadataEnumOption, 0, len(rows))
	for _, row := range rows {
		rawV, ok := row[valCol]
		if !ok || rawV == nil {
			continue
		}
		opt := common.MetadataEnumOption{Value: fmt.Sprint(rawV)}
		if descCol != "" {
			if rawD, ok := row[descCol]; ok && rawD != nil {
				opt.Description = fmt.Sprint(rawD)
			}
		}
		out = append(out, opt)
	}
	return out, nil
}

func fetchEnumFromREST(ctx context.Context, config map[string]any, vs common.MetadataValueSource) ([]common.MetadataEnumOption, error) {
	valField := strings.TrimSpace(vs.EnumValueField)
	if valField == "" {
		return nil, fmt.Errorf("value_source.enum_value_field is required for REST API metadata value sources")
	}
	descKey := strings.TrimSpace(vs.EnumDescriptionField)

	items, err := restAPIFirstPageItems(ctx, config)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("REST value source: no object items in response; configure items_path / pagination so the first page returns records")
	}

	out := make([]common.MetadataEnumOption, 0, len(items))
	for _, it := range items {
		rawV, ok := it[valField]
		if !ok || rawV == nil {
			continue
		}
		opt := common.MetadataEnumOption{Value: fmt.Sprint(rawV)}
		if descKey != "" {
			if rawD, ok := it[descKey]; ok && rawD != nil {
				opt.Description = fmt.Sprint(rawD)
			}
		}
		out = append(out, opt)
	}
	return out, nil
}

func queryRDBMSData(ctx context.Context, source string, config map[string]any, limit int) (map[string]any, error) {
	columns, rows, err := queryRDBMSRows(ctx, source, config, limit)
	if err != nil {
		return nil, err
	}
	serialized := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		item := make(map[string]any, len(columns))
		for _, col := range columns {
			item[col] = serializeCell(row[col])
		}
		serialized = append(serialized, item)
	}
	return map[string]any{"columns": columns, "rows": serialized}, nil
}

func queryRESTData(ctx context.Context, config map[string]any, limit int) (map[string]any, error) {
	items, err := restAPIFirstPageItems(ctx, config)
	if err != nil {
		return nil, err
	}
	serialized := make([]map[string]any, 0, min(limit, len(items)))
	for _, it := range items {
		if len(serialized) >= limit {
			break
		}
		item := make(map[string]any, len(it))
		for k, v := range it {
			item[k] = serializeCell(v)
		}
		serialized = append(serialized, item)
	}
	return map[string]any{"items": serialized}, nil
}

func queryRDBMSRows(ctx context.Context, source string, config map[string]any, limit int) ([]string, []map[string]any, error) {
	db, query, scan, closer, err := openRDBMSForQuery(source, config)
	if err != nil {
		return nil, nil, err
	}
	defer closer()
	if strings.TrimSpace(query) == "" {
		return nil, nil, fmt.Errorf("%s connector query is required for value source / data preview", source)
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var columns []string
	var out []map[string]any
	for rows.Next() {
		row, cols, scanErr := scan(rows)
		if scanErr != nil {
			continue
		}
		if columns == nil {
			columns = cols
		}
		out = append(out, row)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if columns == nil {
		// Empty result: still expose column names when the driver provides them.
		if cols, colErr := rows.Columns(); colErr == nil {
			columns = cols
		}
	}
	return columns, out, nil
}

type rdbmsScanFunc func(rows *sql.Rows) (map[string]any, []string, error)

func openRDBMSForQuery(source string, config map[string]any) (*sql.DB, string, rdbmsScanFunc, func(), error) {
	switch source {
	case "mysql":
		c, err := NewMySQLConnector(config)
		if err != nil {
			return nil, "", nil, nil, err
		}
		db, err := c.open()
		if err != nil {
			return nil, "", nil, nil, err
		}
		return db, c.query, c.scanRow, func() { _ = db.Close() }, nil
	case "postgresql":
		c, err := NewPostgreSQLConnector(config)
		if err != nil {
			return nil, "", nil, nil, err
		}
		db, err := c.open()
		if err != nil {
			return nil, "", nil, nil, err
		}
		return db, c.query, c.scanRow, func() { _ = db.Close() }, nil
	default:
		return nil, "", nil, nil, fmt.Errorf("unsupported RDBMS source: %s", source)
	}
}

func restAPIFirstPageItems(ctx context.Context, config map[string]any) ([]map[string]any, error) {
	c, err := NewRestAPIConnector(config)
	if err != nil {
		return nil, err
	}
	if err := c.prepare(); err != nil {
		return nil, err
	}
	params := map[string]any{}
	switch c.cfg.PaginationType {
	case restAPIPaginationPage:
		page := configInt(c.cfg.PaginationConfig["start_page"], 1)
		perPage, err := c.resolvePageSize()
		if err != nil {
			perPage = c.cfg.BatchSize
		}
		c.applyPagePagination(params, page, perPage)
	case restAPIPaginationOffset:
		perPage, err := c.resolvePageSize()
		if err != nil {
			perPage = c.cfg.BatchSize
		}
		offset := configInt(c.cfg.PaginationConfig["start_offset"], 0)
		limit := configInt(c.cfg.PaginationConfig["limit"], perPage)
		if limit <= 0 {
			limit = perPage
		}
		c.applyOffsetPagination(params, offset, limit)
	case restAPIPaginationCursor:
		if cursor := strings.TrimSpace(stringConfig(c.cfg.PaginationConfig["initial_cursor"])); cursor != "" {
			c.applyCursorPagination(params, cursor)
		}
	}
	data, err := c.fetchPage(ctx, params)
	if err != nil {
		return nil, err
	}
	return restAPIExtractItems(data, c.cfg.ItemsPath), nil
}

func resolveResultColumnName(columnNames []string, requested string) string {
	req := strings.TrimSpace(requested)
	if req == "" {
		return ""
	}
	for _, cn := range columnNames {
		if cn == req {
			return cn
		}
	}
	rl := strings.ToLower(req)
	for _, cn := range columnNames {
		if strings.ToLower(cn) == rl {
			return cn
		}
	}
	return ""
}

func serializeCell(value any) any {
	switch v := value.(type) {
	case nil, string, int, int64, float64, bool:
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[k] = serializeCell(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = serializeCell(item)
		}
		return out
	default:
		return fmt.Sprint(v)
	}
}
