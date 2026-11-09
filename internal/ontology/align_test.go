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

func TestAlignLabelsNamesClassAndRelation(t *testing.T) {
	pg := samplePlayground(t)
	got := AlignLabels("城南—江北Ⅰ线的电源侧厂站是哪一座？", pg)
	if len(got.Properties) != 1 || got.Properties[0].Label != "电源侧厂站" {
		t.Fatalf("properties = %+v", got.Properties)
	}
	if got.Properties[0].Domain != "输电线路" || got.Properties[0].Range != "变电站" {
		t.Fatalf("domain/range = %s -> %s", got.Properties[0].Domain, got.Properties[0].Range)
	}
	if alignmentHasClass(got, "输电线路") {
		t.Fatal("输电线路 is not named by this question")
	}
}

func TestAlignLabelsClassSuffixAndNegativePrefix(t *testing.T) {
	pg := samplePlayground(t)
	listed := AlignLabels("有哪些变电站？", pg)
	if !alignmentHasClass(listed, "变电站") {
		t.Fatalf("classes = %+v, want 变电站", listed.Classes)
	}
	if len(listed.Properties) != 0 {
		t.Fatalf("a class question must not pull relations, got %+v", listed.Properties)
	}
	negative := AlignLabels("变电站食堂今天供应什么？", pg)
	if !negative.Empty() {
		t.Fatalf("glued content word must not align, got %+v", negative)
	}
}

func TestAlignLabelsSkipsSubclassAndIsCaseInsensitive(t *testing.T) {
	pg := samplePlayground(t)
	pg.Relationships = append(pg.Relationships, PlaygroundRelationship{
		ID: "输电线路__subclass__电力资产", Name: "子类于", From: "输电线路", To: "电力资产", Kind: "subclass",
	})
	got := AlignLabels("What is a SUBSTATION?", PlaygroundOntology{
		EntityTypes: []PlaygroundEntityType{{ID: "Substation", Name: "Substation"}},
	})
	if !alignmentHasClass(got, "Substation") {
		t.Fatalf("classes = %+v", got.Classes)
	}
	skipped := AlignLabels("输电线路子类于电力资产", pg)
	for _, p := range skipped.Properties {
		if p.Label == "子类于" {
			t.Fatal("subclass edges are not object properties")
		}
	}
}

func samplePlayground(t *testing.T) PlaygroundOntology {
	t.Helper()
	turtle := BuildTurtle("http://ragflow.local/ontology/power#", InducedSchema{
		Classes: []InducedClass{
			{ID: "变电站", Label: "变电站"},
			{ID: "输电线路", Label: "输电线路"},
		},
		Properties: []InducedProperty{{
			ID: "电源侧厂站", Label: "电源侧厂站", Domain: "输电线路", Range: "变电站",
		}},
	})
	doc, err := ParseTurtle(turtle, "电力", "", "http://ragflow.local/ontology/power#")
	if err != nil {
		t.Fatalf("ParseTurtle: %v", err)
	}
	return doc.ToPlayground()
}

func alignmentHasClass(a Alignment, label string) bool {
	for _, c := range a.Classes {
		if c.Label == label || c.ID == label {
			return true
		}
	}
	return false
}
