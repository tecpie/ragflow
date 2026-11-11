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

package service

import (
	"strings"
	"testing"

	"ragflow/internal/ontology"
)

func TestProjectInstancesKeepsOntologyTypes(t *testing.T) {
	pg := ontology.PlaygroundOntology{
		EntityTypes: []ontology.PlaygroundEntityType{
			{ID: "Substation", Name: "变电站"},
			{ID: "Line", Name: "线路"},
		},
		Relationships: []ontology.PlaygroundRelationship{
			{ID: "feeds", Name: "供电", From: "Substation", To: "Line", Kind: "association"},
			{ID: "sub", Name: "子类于", From: "Line", To: "Substation", Kind: "subclass"},
		},
	}
	entities := projectEntities(pg, "kb1", []map[string]interface{}{
		{"type_kwd": "entity", "content_with_weight": `{"name":"城南变","type":"变电站","description":"主变","properties":{"voltage":"500kV"}}`},
		{"type_kwd": "entity", "content_with_weight": `{"name":"Alice","type":"person"}`},
		{"type_kwd": "entity", "content_with_weight": `{"name":"城南变","type":"变电站"}`},
	})
	if len(entities) != 1 || entities[0].Name != "城南变" || entities[0].TypeID != "Substation" {
		t.Fatalf("entities = %+v", entities)
	}
	if entities[0].Properties["voltage"] != "500kV" {
		t.Fatalf("entity properties = %+v", entities[0].Properties)
	}
	names := map[string]bool{
		strings.ToLower(entities[0].Name): true,
		"江北线":                             true,
	}
	relations := projectRelations(pg, "kb1", names, []map[string]interface{}{
		{"type_kwd": "relation", "from_entity_kwd": "城南变", "to_entity_kwd": "江北线", "content_with_weight": `{"type":"供电","properties":{"capacity":120}}`},
		{"type_kwd": "relation", "from_entity_kwd": "城南变", "to_entity_kwd": "江北线", "content_with_weight": `{"type":"子类于"}`},
		{"type_kwd": "relation", "from_entity_kwd": "城南变", "to_entity_kwd": "不存在", "content_with_weight": `{"type":"供电"}`},
	})
	if len(relations) != 1 || relations[0].TypeID != "feeds" {
		t.Fatalf("relations = %+v", relations)
	}
	if relations[0].Properties["capacity"] != float64(120) {
		t.Fatalf("relation properties = %+v", relations[0].Properties)
	}
}
