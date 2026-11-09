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
	"strings"
	"testing"
)

func TestParseKgFocusJSONStripsFence(t *testing.T) {
	raw := "```json\n{\"entities\":[\"城南—城西II线\"],\"relations\":[{\"from\":\"城南—城西II线\",\"to\":\"城西110kV变电站\",\"type\":\"负荷侧厂站\"}]}\n```"
	focus, err := parseKgFocusJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(focus.Entities) != 1 || focus.Entities[0] != "城南—城西II线" {
		t.Fatalf("entities = %#v", focus.Entities)
	}
	if len(focus.Relations) != 1 || focus.Relations[0].Type != "负荷侧厂站" {
		t.Fatalf("relations = %#v", focus.Relations)
	}
}

func TestApplyKgFocusMarksEntitiesAndRelations(t *testing.T) {
	entities := []KgEntity{
		{Name: "城南—城西II线", Type: "输电线路", Seed: true},
		{Name: "城西110kV变电站", Type: "变电站"},
		{Name: "城西县调", Type: "调度机构"},
		{Name: "湾东—湾西II线", Type: "输电线路"},
	}
	relations := []KgRelation{
		{From: "城南—城西II线", To: "城西110kV变电站", Type: "负荷侧厂站"},
		{From: "城西110kV变电站", To: "城西县调", Type: "受管辖"},
		{From: "湾东—湾西II线", To: "湾西110kV变电站", Type: "负荷侧厂站"},
	}
	applyKgFocusMarks(entities, relations, kgFocus{
		Entities: []string{"城南—城西II线"},
		Relations: []kgFocusRel{
			{From: "城南—城西II线", To: "城西110kV变电站", Type: "负荷侧厂站"},
			{From: "城西110kV变电站", To: "城西县调", Type: "受管辖"},
		},
	})
	wantEnt := map[string]bool{
		"城南—城西II线":    true,
		"城西110kV变电站": true,
		"城西县调":      true,
		"湾东—湾西II线":   false,
	}
	for _, e := range entities {
		if e.Relevant != wantEnt[e.Name] {
			t.Errorf("%s relevant=%v, want %v", e.Name, e.Relevant, wantEnt[e.Name])
		}
	}
	if !relations[0].Relevant || !relations[1].Relevant || relations[2].Relevant {
		t.Fatalf("relation marks = %+v / %+v / %+v", relations[0], relations[1], relations[2])
	}
}

func TestApplyKgModelFocusClearsWithoutModel(t *testing.T) {
	entities := []KgEntity{{Name: "A", Seed: true, Relevant: true}}
	relations := []KgRelation{{From: "A", To: "B", Type: "r", Relevant: true}}
	applyKgModelFocus(context.Background(), "q", entities, relations)
	if entities[0].Seed || entities[0].Relevant || relations[0].Relevant {
		t.Fatalf("marks must clear without a chat model: %+v / %+v", entities[0], relations[0])
	}
}

func TestKgFocusUserPromptListsSubgraph(t *testing.T) {
	got := kgFocusUserPrompt("城南—城西Ⅱ线谁管？",
		[]KgEntity{{Name: "城南—城西II线", Type: "输电线路"}},
		[]KgRelation{{From: "城南—城西II线", To: "城西110kV变电站", Type: "负荷侧厂站"}},
	)
	for _, want := range []string{
		"城南—城西Ⅱ线谁管？",
		"城南—城西II线 (输电线路)",
		"城南—城西II线 -[负荷侧厂站]-> 城西110kV变电站",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}
