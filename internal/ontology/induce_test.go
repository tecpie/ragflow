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

package ontology

import "testing"

func TestBuildTurtleAndMerge(t *testing.T) {
	schema := InducedSchema{
		Classes: []InducedClass{
			{ID: "Person", Label: "Person"},
			{ID: "Organization", Label: "Organization"},
		},
		Properties: []InducedProperty{
			{ID: "works_for", Label: "works for", Domain: "Person", Range: "Organization"},
		},
	}
	ttl := BuildTurtle("http://example.org/onto#", schema)
	doc, err := ParseTurtle(ttl, "Demo", "", "http://example.org/onto#")
	if err != nil {
		t.Fatalf("ParseTurtle: %v", err)
	}
	pg := doc.ToPlayground()
	if len(pg.EntityTypes) < 2 {
		t.Fatalf("entityTypes=%d", len(pg.EntityTypes))
	}
	var hasAssoc bool
	for _, r := range pg.Relationships {
		if r.Kind == "association" {
			hasAssoc = true
		}
	}
	if !hasAssoc {
		t.Fatal("missing association")
	}

	merged, err := MergeTurtleSchemas(ttl, "http://example.org/onto#", "Demo", "", InducedSchema{
		Classes: []InducedClass{{ID: "Employee", Label: "Employee"}},
		Properties: []InducedProperty{
			{ID: "works_for", Label: "works for", Domain: "Person", Range: "Organization"},
		},
	})
	if err != nil {
		t.Fatalf("MergeTurtleSchemas: %v", err)
	}
	doc2, err := ParseTurtle(merged, "Demo", "", "http://example.org/onto#")
	if err != nil {
		t.Fatalf("parse merged: %v", err)
	}
	pg2 := doc2.ToPlayground()
	if len(pg2.EntityTypes) < 3 {
		t.Fatalf("merged entityTypes=%d want >=3", len(pg2.EntityTypes))
	}
}

func TestSafeLocalName(t *testing.T) {
	if got := SafeLocalName("works for"); got != "works_for" {
		t.Fatalf("got %q", got)
	}
	if got := SafeLocalName("123Bad"); got != "_123Bad" && got != "_123_Bad" {
		// digits prefix gets underscore
		if got == "" || got[0] != '_' {
			t.Fatalf("got %q", got)
		}
	}
}
