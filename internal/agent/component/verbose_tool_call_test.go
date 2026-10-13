package component

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestFormatVerboseToolCall(t *testing.T) {
	pending := map[string]schema.ToolCall{
		"call_1": {
			ID: "call_1",
			Function: schema.FunctionCall{
				Name:      "search_0",
				Arguments: `{"q":"ragflow"}`,
			},
		},
	}
	msg := &schema.Message{
		Role:       schema.Tool,
		ToolCallID: "call_1",
		Content:    "ok",
	}
	got := formatVerboseToolCall(msg, pending, nil)
	if !strings.HasPrefix(got, "<tool_call>") || !strings.HasSuffix(got, "</tool_call>") {
		t.Fatalf("missing tool_call wrappers: %q", got)
	}
	var payload map[string]any
	body := strings.TrimSuffix(strings.TrimPrefix(got, "<tool_call>"), "</tool_call>")
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("payload json: %v\n%s", err, body)
	}
	if payload["name"] != "search_0" {
		t.Errorf("name=%v, want search_0", payload["name"])
	}
	if payload["result"] != "ok" {
		t.Errorf("result=%v, want ok", payload["result"])
	}
	args, ok := payload["args"].(map[string]any)
	if !ok || args["q"] != "ragflow" {
		t.Errorf("args=%v, want {q:ragflow}", payload["args"])
	}
}

func TestFormatVerboseToolCall_DisplayName(t *testing.T) {
	pending := map[string]schema.ToolCall{
		"call_1": {
			ID: "call_1",
			Function: schema.FunctionCall{
				Name:      "search_my_dateset_0",
				Arguments: `{}`,
			},
		},
	}
	msg := &schema.Message{
		Role:       schema.Tool,
		ToolCallID: "call_1",
		Content:    "hits",
	}
	got := formatVerboseToolCall(msg, pending, map[string]string{
		"search_my_dateset_0": "知识库检索",
	})
	var payload map[string]any
	body := strings.TrimSuffix(strings.TrimPrefix(got, "<tool_call>"), "</tool_call>")
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("payload json: %v\n%s", err, body)
	}
	if payload["name"] != "知识库检索" {
		t.Errorf("name=%v, want 知识库检索", payload["name"])
	}
}

func TestFormatVerboseToolCall_MissingPending(t *testing.T) {
	got := formatVerboseToolCall(&schema.Message{Role: schema.Tool, Content: "x"}, nil, nil)
	if !strings.Contains(got, `"name": "tool"`) {
		t.Fatalf("expected fallback name, got %q", got)
	}
}

func TestAgentToolsFrom_CanvasDisplayNames(t *testing.T) {
	p := mergeAgentParam(AgentParam{}, map[string]any{
		"tools": []any{
			map[string]any{
				"component_name": "Wikipedia",
				"name":           "百科搜索",
				"params":         map[string]any{"top_n": 3},
			},
			"duckduckgo",
		},
	})
	if len(p.Tools) != 2 || len(p.ToolCanvasNames) != 2 {
		t.Fatalf("tools=%v canvasNames=%v", p.Tools, p.ToolCanvasNames)
	}
	if p.ToolCanvasNames[0] != "百科搜索" {
		t.Errorf("canvas name[0]=%q, want 百科搜索", p.ToolCanvasNames[0])
	}
	if p.ToolCanvasNames[1] != "" {
		t.Errorf("canvas name[1]=%q, want empty", p.ToolCanvasNames[1])
	}

	_, display, err := buildAgentTools(t.Context(), p)
	if err != nil {
		t.Fatalf("buildAgentTools: %v", err)
	}
	if display["wikipedia_search"] != "百科搜索" {
		t.Errorf("display[wikipedia_search]=%q, want 百科搜索", display["wikipedia_search"])
	}
	if display["duckduckgo_search"] != "duckduckgo_search" {
		t.Errorf("display[duckduckgo_search]=%q, want duckduckgo_search", display["duckduckgo_search"])
	}
}
