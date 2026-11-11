//
//  Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.
//

package knowledge_compiler

import (
	"strings"
	"testing"

	"ragflow/internal/ontology"
)

func TestParserConfigFromModelingIncludesProperties(t *testing.T) {
	cfg := parserConfigFromModeling(map[string]any{
		"kind":          "ontology",
		"ontology_name": "电力",
		"ontology_id":   "onto-1",
		"global_rules":  "keep names",
	}, ontology.ModelingDocument{
		Concepts: []ontology.ModelingConcept{
			{
				ID: "Substation", Name: "变电站", Description: "厂站",
				Properties: []ontology.ModelingProperty{
					{Key: "voltage", Label: "电压等级", Type: "enum", Required: true, EnumValues: []string{"220kV", "500kV"}},
				},
			},
			{ID: "Line", Name: "线路"},
		},
		Relations: []ontology.ModelingRelation{
			{
				ID: "feeds", Name: "供电", Source: []string{"Substation"}, Target: []string{"Line"},
				Properties: []ontology.ModelingProperty{
					{Key: "capacity", Label: "容量", Type: "number"},
				},
			},
		},
	})
	if cfg["kind"] != "ontology" {
		t.Fatalf("kind = %#v", cfg["kind"])
	}
	if cfg["ontology_id"] != "onto-1" {
		t.Fatalf("ontology_id lost: %#v", cfg["ontology_id"])
	}
	entity, _ := cfg["entity"].(map[string]any)
	fields, _ := entity["fields"].([]any)
	if len(fields) != 2 {
		t.Fatalf("entity fields = %#v", fields)
	}
	first, _ := fields[0].(map[string]any)
	if first["type"] != "变电站" {
		t.Fatalf("first field = %#v", first)
	}
	rule, _ := first["rule"].(string)
	if !strings.Contains(rule, "voltage") || !strings.Contains(rule, "500kV") {
		t.Fatalf("property rule missing: %s", rule)
	}
	outFields, _ := entity["output_fields"].([]any)
	if len(outFields) != 1 {
		t.Fatalf("entity output_fields = %#v", outFields)
	}
	relation, _ := cfg["relation"].(map[string]any)
	relFields, _ := relation["fields"].([]any)
	if len(relFields) != 1 {
		t.Fatalf("relation fields = %#v", relFields)
	}
	rel, _ := relFields[0].(map[string]any)
	if rel["type"] != "供电" {
		t.Fatalf("relation = %#v", rel)
	}
	desc, _ := rel["description"].(string)
	if !strings.Contains(desc, "变电站") || !strings.Contains(desc, "线路") || !strings.Contains(desc, "capacity") {
		t.Fatalf("relation description = %s", desc)
	}
}

func TestOverlayOntologyFieldsReplacesGraphTypes(t *testing.T) {
	pg := ontology.PlaygroundOntology{
		EntityTypes: []ontology.PlaygroundEntityType{
			{ID: "Substation", Name: "变电站", Description: "厂站"},
			{ID: "Line", Name: "线路"},
		},
		Relationships: []ontology.PlaygroundRelationship{
			{ID: "sub", Name: "子类于", From: "Line", To: "Substation", Kind: "subclass"},
			{ID: "feeds", Name: "供电", From: "Substation", To: "Line", Kind: "association", Description: "供电关系"},
		},
	}
	cfg := overlayOntologyFields(map[string]any{
		"kind":     "ontology",
		"entity":   map[string]any{"fields": []any{map[string]any{"type": "person"}}},
		"relation": map[string]any{"fields": []any{map[string]any{"type": "owns"}}},
	}, pg)
	entity, _ := cfg["entity"].(map[string]any)
	fields, _ := entity["fields"].([]any)
	if len(fields) != 2 {
		t.Fatalf("entity fields = %#v", fields)
	}
	first, _ := fields[0].(map[string]any)
	if first["type"] != "变电站" || first["description"] != "厂站" {
		t.Fatalf("first field = %#v", first)
	}
	relation, _ := cfg["relation"].(map[string]any)
	relFields, _ := relation["fields"].([]any)
	if len(relFields) != 1 {
		t.Fatalf("relation fields = %#v", relFields)
	}
	rel, _ := relFields[0].(map[string]any)
	if rel["type"] != "供电" {
		t.Fatalf("relation = %#v", rel)
	}
	if cfg["kind"] != "ontology" {
		t.Fatalf("kind overwritten: %#v", cfg["kind"])
	}
}

func TestOverlayOntologyFieldsDropsRelationsWhenOntologyHasNone(t *testing.T) {
	cfg := overlayOntologyFields(map[string]any{
		"relation": map[string]any{"fields": []any{map[string]any{"type": "owns"}}},
	}, ontology.PlaygroundOntology{
		EntityTypes: []ontology.PlaygroundEntityType{{ID: "Line", Name: "线路"}},
	})
	if _, ok := cfg["relation"]; ok {
		t.Fatalf("relation should be removed, cfg = %#v", cfg["relation"])
	}
}
