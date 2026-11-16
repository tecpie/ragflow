package elasticsearch

import (
	"reflect"
	"testing"

	"ragflow/internal/engine/types"
)

// TestParseOrderByExprSortsStoredID guards that the logical "id" column sorts
// the stored keyword field and is NOT translated into an Elasticsearch _id
// sort. Ordering on _id requires indices.id_field_data, which is disabled by
// default on the bundled Elasticsearch 8.x deployment.
func TestParseOrderByExprSortsStoredID(t *testing.T) {
	idAsc := map[string]interface{}{
		"id": map[string]interface{}{"order": "asc", "unmapped_type": "keyword"},
	}
	idDesc := map[string]interface{}{
		"id": map[string]interface{}{"order": "desc", "unmapped_type": "keyword"},
	}
	cases := []struct {
		name string
		expr *types.OrderByExpr
		want []map[string]interface{}
	}{
		{
			name: "asc id sorts the keyword field",
			expr: (&types.OrderByExpr{}).Asc("id"),
			want: []map[string]interface{}{idAsc},
		},
		{
			name: "desc id sorts the keyword field",
			expr: (&types.OrderByExpr{}).Desc("id"),
			want: []map[string]interface{}{idDesc},
		},
		{
			name: "id mixed with other fields keeps both",
			expr: (&types.OrderByExpr{}).Asc("weight_flt").Asc("id"),
			want: []map[string]interface{}{
				{"weight_flt": map[string]interface{}{"order": "asc", "unmapped_type": "float"}},
				idAsc,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseOrderByExpr(tc.expr)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseOrderByExpr(%v) = %#v, want %#v", tc.expr, got, tc.want)
			}
		})
	}
}

// TestParseOrderByExprIDNotDroppedToIDMetadata is the explicit regression guard:
// parseOrderByExpr must never emit a {"_id": ...} sort for the logical id column.
func TestParseOrderByExprIDNotDroppedToIDMetadata(t *testing.T) {
	got := parseOrderByExpr((&types.OrderByExpr{}).Asc("id"))
	for _, sort := range got {
		if _, ok := sort["_id"]; ok {
			t.Fatalf("parseOrderByExpr(id) emitted a _id sort %#v; this breaks on default ES 8.x", sort)
		}
	}
}
