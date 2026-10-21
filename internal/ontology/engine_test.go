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

const sampleTurtle = `
@prefix ex: <http://example.org/onto#> .
@prefix owl: <http://www.w3.org/2002/07/owl#> .
@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .

ex:Person a owl:Class ;
  rdfs:label "Person" ;
  rdfs:comment "A natural person" .

ex:Employee a owl:Class ;
  rdfs:subClassOf ex:Person ;
  rdfs:label "Employee" .

ex:Organization a owl:Class ;
  rdfs:label "Organization" .

ex:worksFor a owl:ObjectProperty ;
  rdfs:domain ex:Person ;
  rdfs:range ex:Organization ;
  rdfs:label "works for" .
`

func TestParseTurtleToPlayground(t *testing.T) {
	doc, err := ParseTurtle(sampleTurtle, "Demo", "demo ontology", "http://example.org/onto#")
	if err != nil {
		t.Fatalf("ParseTurtle: %v", err)
	}
	pg := doc.ToPlayground()
	if pg.Name != "Demo" {
		t.Fatalf("name = %q", pg.Name)
	}
	if len(pg.EntityTypes) < 3 {
		t.Fatalf("entityTypes = %d, want >= 3", len(pg.EntityTypes))
	}
	var hasSubclass, hasAssoc bool
	for _, r := range pg.Relationships {
		if r.Kind == "subclass" {
			hasSubclass = true
			if r.Name != "子类于" {
				t.Fatalf("subclass name = %q, want 子类于", r.Name)
			}
		}
		if r.Kind == "association" && r.Name == "works for" {
			hasAssoc = true
		}
	}
	if !hasSubclass {
		t.Fatal("missing subclass relationship")
	}
	if !hasAssoc {
		t.Fatal("missing worksFor association")
	}
	xml, err := doc.ExportRDFXML()
	if err != nil {
		t.Fatalf("ExportRDFXML: %v", err)
	}
	if len(xml) == 0 {
		t.Fatal("empty rdf/xml")
	}
}
