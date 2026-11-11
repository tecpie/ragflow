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

func TestTurtleFromModelingJSONCaseBundle(t *testing.T) {
	raw := `{
	  "caseFormatVersion": "1.0",
	  "ontology": {
	    "name": "瓦斯保护",
	    "conceptTypes": [
	      {"id": "Device", "name": "设备", "properties": [{"key": "voltage", "type": "string"}]},
	      {"id": "Line", "name": "线路", "properties": []}
	    ],
	    "relationTypes": [
	      {"id": "feeds", "name": "供电", "sourceConcepts": ["Device"], "targetConcepts": ["Line"]}
	    ]
	  }
	}`
	concepts, props, rels, present, err := CountModelingDocument(raw)
	if err != nil || !present || concepts != 2 || props != 1 || rels != 1 {
		t.Fatalf("counts = %d %d %d present=%v err=%v", concepts, props, rels, present, err)
	}
	text, err := TurtleFromModelingJSON("http://example.org/onto#", raw)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ParseTurtle(text, "瓦斯保护", "", "http://example.org/onto#")
	if err != nil {
		t.Fatal(err)
	}
	pg := doc.ToPlayground()
	if len(pg.EntityTypes) != 2 || len(pg.Relationships) != 1 {
		t.Fatalf("playground types=%d rels=%d", len(pg.EntityTypes), len(pg.Relationships))
	}
}

func TestTurtleFromModelingJSONRejectsNonModel(t *testing.T) {
	if _, _, _, present, err := CountModelingDocument(""); err != nil || present {
		t.Fatalf("empty present=%v err=%v", present, err)
	}
	if _, err := TurtleFromModelingJSON("", `{"name":"x"}`); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseModelingDocumentKeepsPropertyDefs(t *testing.T) {
	raw := `{
	  "name": "电网",
	  "conceptTypes": [
	    {
	      "id": "Substation",
	      "name": "变电站",
	      "description": "厂站",
	      "properties": [
	        {"key": "voltage", "label": "电压", "type": "enum", "required": true, "enumValues": ["220kV", "500kV"]},
	        {"key": "station", "label": "所属站", "type": "ref", "refConceptTypeId": "Substation"}
	      ]
	    }
	  ],
	  "relationTypes": [
	    {
	      "id": "feeds",
	      "name": "供电",
	      "sourceConcepts": ["Substation"],
	      "targetConcepts": ["Substation"],
	      "properties": [{"key": "capacity", "type": "number"}]
	    }
	  ]
	}`
	doc, present, err := ParseModelingDocument(raw)
	if err != nil || !present {
		t.Fatalf("present=%v err=%v", present, err)
	}
	if doc.Name != "电网" || len(doc.Concepts) != 1 || len(doc.Relations) != 1 {
		t.Fatalf("doc = %+v", doc)
	}
	props := doc.Concepts[0].Properties
	if len(props) != 2 || props[0].Key != "voltage" || props[0].Type != "enum" || len(props[0].EnumValues) != 2 {
		t.Fatalf("concept props = %+v", props)
	}
	if props[1].RefConceptTypeID != "Substation" {
		t.Fatalf("ref prop = %+v", props[1])
	}
	if len(doc.Relations[0].Properties) != 1 || doc.Relations[0].Properties[0].Key != "capacity" {
		t.Fatalf("relation props = %+v", doc.Relations[0].Properties)
	}
}
