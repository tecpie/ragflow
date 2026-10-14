package document

import "testing"

func TestValidateMetaFieldsAllowsBool(t *testing.T) {
	t.Parallel()

	meta := map[string]any{
		"_groupId":        "01m28662a70e08ac90347e5864",
		"_isCurrent":      true,
		"_processStatus":  float64(-1),
		"_status":         float64(1),
		"category_id":     "01m2694223cdfd0026be3098a5",
		"type_version":    float64(2),
		"_versionGroup":   "9171de24eb4c490c916b41dead6eca88",
		"_version":        float64(5),
		"_platformUserId": "1",
		"flags":           []any{true, false, "ok"},
	}
	if err := validateMetaFields(meta); err != nil {
		t.Fatalf("validateMetaFields rejected bool meta: %v", err)
	}
}

func TestValidateMetaFieldsRejectsNestedObject(t *testing.T) {
	t.Parallel()

	err := validateMetaFields(map[string]any{"obj": map[string]any{"x": 1}})
	if err == nil {
		t.Fatal("expected nested object to be rejected")
	}
}
