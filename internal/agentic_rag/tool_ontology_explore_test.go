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
	"strings"
	"testing"
)

func TestOntologyExploreUnalignedQuestionStillWalks(t *testing.T) {
	seedPowerOntology(t, openOAGDB(t))
	fake := &fakeKgService{}
	installFakeKgService(t, fake)
	tool := NewOntologyExploreTool("tenant-1", []string{"kb-with", "kb-without"})
	ctx := WithConversationKey(kgExploreContext(t), "oag-miss")
	out, err := tool.InvokableRun(ctx, `{"query":"变电站食堂今天供应什么？"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if fake.gotReq.Query != "变电站食堂今天供应什么？" {
		t.Fatalf("an unaligned question must still walk, got %+v", fake.gotReq)
	}
	if fake.gotReq.Constraint != nil {
		t.Fatalf("unaligned walk must not carry a constraint, got %+v", fake.gotReq.Constraint)
	}
	if strings.Contains(out, "was not walked") {
		t.Fatalf("unaligned question skipped the walk:\n%s", out)
	}
	if !navToolEnabled(ctx, ontologyExploreToolName) {
		t.Fatal("a query-level alignment miss must not disable ontology_explore")
	}
}

func TestOntologyExplorePassesConstraintAndSkipsUnboundDataset(t *testing.T) {
	seedPowerOntology(t, openOAGDB(t))
	fake := &fakeKgService{result: KgExploreResult{
		Entities: []KgEntity{{Name: "城南500kV变电站", Type: "变电站"}},
	}}
	installFakeKgService(t, fake)
	tool := NewOntologyExploreTool("tenant-1", []string{"kb-with", "kb-without"})
	out, err := tool.InvokableRun(kgExploreContext(t), `{"query":"城南—江北Ⅰ线的电源侧厂站是哪一座？"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if len(fake.gotReq.DatasetIDs) != 1 || fake.gotReq.DatasetIDs[0] != "kb-with" {
		t.Fatalf("walk datasets = %v, want only the dataset that resolved an ontology", fake.gotReq.DatasetIDs)
	}
	if fake.gotReq.Constraint == nil || len(fake.gotReq.Constraint.Properties) != 1 || fake.gotReq.Constraint.Properties[0].Label != "电源侧厂站" {
		t.Fatalf("constraint = %+v", fake.gotReq.Constraint)
	}
	if !strings.Contains(out, `<property id="电源侧厂站"`) || !strings.Contains(out, `name="城南500kV变电站"`) {
		t.Fatalf("output = %s", out)
	}
}
