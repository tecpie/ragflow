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

package knowledge_compile

import (
	"testing"

	"ragflow/internal/ontology"
)

func TestInduceBucketIntoSchema(t *testing.T) {
	buckets := []StructureBucket{
		{Name: "Alice", Type: "Person"},
		{Name: "Acme", Type: "Organization"},
		{Name: "Alice -> Acme", Type: "relation", FromEntity: "Alice", ToEntity: "Acme", RelationType: "works for"},
	}
	var schema ontology.InducedSchema
	for _, b := range buckets {
		induceBucketIntoSchema(&schema, buckets, b)
	}
	ttl := ontology.BuildTurtle("http://example.org/onto#", schema)
	doc, err := ontology.ParseTurtle(ttl, "t", "", "http://example.org/onto#")
	if err != nil {
		t.Fatalf("ParseTurtle: %v\n%s", err, ttl)
	}
	pg := doc.ToPlayground()
	if len(pg.EntityTypes) < 2 {
		t.Fatalf("classes=%d turtle=\n%s", len(pg.EntityTypes), ttl)
	}
	var hasWorksFor bool
	for _, r := range pg.Relationships {
		if r.Kind == "association" && (r.Name == "works for" || r.ID == "works_for") {
			hasWorksFor = true
		}
	}
	if !hasWorksFor {
		t.Fatalf("missing works for: %+v\n%s", pg.Relationships, ttl)
	}
}

func TestConfigBoolFalse(t *testing.T) {
	if !configBoolFalse(false) {
		t.Fatal("want true for false")
	}
	if configBoolFalse(true) {
		t.Fatal("want false for true")
	}
	if !configBoolFalse("false") {
		t.Fatal("want true for string false")
	}
}
