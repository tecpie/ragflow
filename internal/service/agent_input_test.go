// Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"ragflow/internal/agent/canvas"
	"ragflow/internal/common"
	"ragflow/internal/entity"
)

func TestNormalizeAgentInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		query  string
		inputs map[string]any
		want   any
	}{
		{name: "empty request"},
		{name: "query only", query: "1", want: "1"},
		{name: "true alongside query", query: "1", inputs: map[string]any{"bool": map[string]any{"type": "boolean", "value": true}}, want: map[string]any{"query": "1", "bool": true}},
		{name: "false without query", inputs: map[string]any{"bool": map[string]any{"type": "boolean", "value": false}}, want: map[string]any{"bool": false}},
		{name: "zero and empty values", inputs: map[string]any{"count": map[string]any{"value": 0}, "text": map[string]any{"value": ""}}, want: map[string]any{"count": 0, "text": ""}},
		{name: "unset optional field", inputs: map[string]any{"text": map[string]any{"type": "line", "optional": true}}, want: map[string]any{"text": nil}},
		{name: "raw object", inputs: map[string]any{"data": map[string]any{"enabled": false}}, want: map[string]any{"data": map[string]any{"enabled": false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeAgentInput(tc.query, tc.inputs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("normalized input = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestAgentPersistedInputsKeepsNamedFields(t *testing.T) {
	got := agentPersistedInputs(map[string]any{"query": "hello", "name": "Alice"})
	want := map[string]any{"query": "hello", "name": "Alice"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agentPersistedInputs = %#v, want %#v", got, want)
	}
	if got := agentSessionUserContent(map[string]any{"query": "hello", "name": "Alice"}); got != "hello" {
		t.Fatalf("agentSessionUserContent with query = %q, want hello", got)
	}
	if got := agentSessionUserContent(map[string]any{"name": "Alice", "age": 18}); got != "age: 18\nname: Alice" {
		t.Fatalf("agentSessionUserContent without query = %q", got)
	}
}

func TestCollectAgentComponentIO(t *testing.T) {
	state := canvas.NewCanvasState("run-1", "session-1")
	state.MergeNamespaces(map[string]any{
		"_node_inputs_begin":   map[string]any{"query": "q1", "name": "Alice"},
		"_node_inputs_agent_0": map[string]any{"user_prompt": "q1"},
	}, nil, nil)
	state.RecordOutput("agent_0", "content", "answer")
	state.RecordOutput("agent_0", "structured", map[string]any{"ok": true})

	dsl := map[string]any{
		"components": map[string]any{
			"begin": map[string]any{
				"obj": map[string]any{"component_name": "Begin", "name": "Begin"},
			},
			"agent_0": map[string]any{
				"obj": map[string]any{"component_name": "Agent", "name": "Reviewer"},
			},
			"message_0": map[string]any{
				"obj": map[string]any{"component_name": "Message"},
			},
		},
	}

	got := collectAgentComponentIO(state, dsl)
	if len(got) != 2 {
		t.Fatalf("component_io len = %d, want 2: %#v", len(got), got)
	}
	if got[0]["component_id"] != "agent_0" || got[1]["component_id"] != "begin" {
		t.Fatalf("unexpected order/ids: %#v", got)
	}
	agentInputs, _ := got[0]["inputs"].(map[string]any)
	if agentInputs["user_prompt"] != "q1" {
		t.Fatalf("agent inputs = %#v", agentInputs)
	}
	agentOutputs, _ := got[0]["outputs"].(map[string]any)
	if agentOutputs["content"] != "answer" {
		t.Fatalf("agent outputs = %#v", agentOutputs)
	}
}

func TestAgentbotCompletionPreservesBooleanBeginInput(t *testing.T) {
	db := setupServiceTestDB(t)
	if err := db.AutoMigrate(&entity.UserCanvasVersion{}); err != nil {
		t.Fatalf("migrate canvas versions: %v", err)
	}
	pushServiceDB(t, db)
	dsl := map[string]any{
		"components": map[string]any{
			"begin": map[string]any{
				"obj": map[string]any{"component_name": "Begin", "params": map[string]any{
					"inputs": map[string]any{"bool": map[string]any{"name": "bool", "type": "boolean"}},
				}},
				"downstream": []any{"message"},
			},
			"message": map[string]any{
				"obj":      map[string]any{"component_name": "Message", "params": map[string]any{"text": "{{begin@bool}}|{{sys.query}}"}},
				"upstream": []any{"begin"},
			},
		},
		"path": []any{"begin", "message"},
	}
	makeCanvasWithDSL(t, "canvas-bool", "user-1", "tenant-1", "version-bool", dsl)
	svc := NewBotService(NewAgentServiceWithOptions(nil, nil, nil))
	for _, value := range []bool{true, false} {
		t.Run(strconv.FormatBool(value), func(t *testing.T) {
			body := `{"stream":true,"running_hint_text":"正在运行中...🕞","openai-compatible":false,"query":"1","inputs":{"bool":{"name":"bool","optional":false,"options":[],"type":"boolean","value":` + strconv.FormatBool(value) + `}},"files":[],"session_id":null}`
			var req AgentbotCompletionRequest
			if err := json.Unmarshal([]byte(body), &req); err != nil {
				t.Fatal(err)
			}
			events, code, err := svc.AgentbotCompletion(serviceTestContext(t, "user-1", "tenant-1"), "canvas-bool", req)
			if err != nil || code != common.CodeSuccess {
				t.Fatalf("AgentbotCompletion = (%d, %v)", code, err)
			}
			messages, waiting, errs, _, done := drainAgentEvents(t, events)
			if len(errs) != 0 || len(waiting) != 0 || !done {
				t.Fatalf("unexpected run result: errors=%+v waiting=%+v done=%v", errs, waiting, done)
			}
			want := strconv.FormatBool(value) + "|1"
			if len(messages) != 1 || messages[0].Content != want {
				t.Fatalf("messages = %+v, want content %q", messages, want)
			}
		})
	}
}
