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

package agentic_rag

import (
	"context"
	"testing"

	"ragflow/internal/ontology"
)

func TestAlignQueryAddsLabelAboveCosineFloor(t *testing.T) {
	adapter := NewKgAdapter(nil)
	adapter.SetSeedEncoder(func(_ context.Context, _, text string) []float64 {
		switch text {
		case "哪条馈线负责送电", "输电线路":
			return []float64{1, 0}
		default:
			return []float64{0, 1}
		}
	})
	previous := GetKgService()
	SetKgService(adapter)
	t.Cleanup(func() { SetKgService(previous) })

	pg := ontology.PlaygroundOntology{
		EntityTypes: []ontology.PlaygroundEntityType{
			{ID: "输电线路", Name: "输电线路"},
			{ID: "变电站", Name: "变电站"},
		},
	}
	got := alignQuery(context.Background(), "tenant", "哪条馈线负责送电", pg)
	if !alignmentHasClass(got, "输电线路") {
		t.Fatalf("classes = %+v, want 输电线路 from the label vector", got.Classes)
	}
	if alignmentHasClass(got, "变电站") {
		t.Fatal("变电站 is orthogonal to the question and must stay out")
	}
}

func TestAlignQueryWithoutEncoderStaysLexical(t *testing.T) {
	previous := GetKgService()
	SetKgService(NewKgAdapter(nil))
	t.Cleanup(func() { SetKgService(previous) })

	pg := ontology.PlaygroundOntology{
		EntityTypes: []ontology.PlaygroundEntityType{{ID: "输电线路", Name: "输电线路"}},
	}
	got := alignQuery(context.Background(), "tenant", "哪条馈线负责送电", pg)
	if !got.Empty() {
		t.Fatalf("without an encoder a non-lexical question must not align, got %+v", got)
	}
}

func alignmentHasClass(a ontology.Alignment, label string) bool {
	for _, class := range a.Classes {
		if class.ID == label || class.Label == label {
			return true
		}
	}
	return false
}
