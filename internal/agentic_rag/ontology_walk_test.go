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

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type stubOntologyTool struct {
	called string
}

func (s *stubOntologyTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: ontologyExploreToolName}, nil
}

func (s *stubOntologyTool) InvokableRun(_ context.Context, args string, _ ...tool.Option) (string, error) {
	s.called = args
	return `<kg_explore mode="ontology"></kg_explore>`, nil
}

func TestOntologyWalkMessagesRunsWhenTheQuestionNamesARelation(t *testing.T) {
	stub := &stubOntologyTool{}
	var card string
	counts := map[string]int{}
	msgs := ontologyWalkMessages(context.Background(), []tool.BaseTool{stub}, "负荷侧厂站由谁受管辖", func(content, _ string) {
		card += content
	}, counts)
	if !strings.Contains(stub.called, "负荷侧厂站由谁受管辖") {
		t.Fatalf("walk args = %s", stub.called)
	}
	if counts[ontologyExploreToolName] != 1 {
		t.Fatalf("counts = %v", counts)
	}
	if !strings.Contains(card, "本体图谱") || !strings.Contains(card, "kg_explore") {
		t.Fatalf("card = %s", card)
	}
	if len(msgs) != 2 || msgs[1].Role != schema.Tool {
		t.Fatalf("messages = %#v", msgs)
	}
}

func TestOntologyWalkMessagesSkipsWhenTheToolIsAbsent(t *testing.T) {
	msgs := ontologyWalkMessages(context.Background(), nil, "问题", func(string, string) {
		t.Fatal("no card without the tool")
	}, map[string]int{})
	if msgs != nil {
		t.Fatalf("messages = %#v", msgs)
	}
}

func TestOntologyWalkMessagesRunsWhenNoRelationIsNamed(t *testing.T) {
	stub := &stubOntologyTool{}
	msgs := ontologyWalkMessages(context.Background(), []tool.BaseTool{stub}, "今天食堂供应什么", func(string, string) {}, map[string]int{})
	if !strings.Contains(stub.called, "今天食堂供应什么") {
		t.Fatalf("walk args = %s", stub.called)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %#v", msgs)
	}
}
