package pipeline

import (
	"encoding/json"
	"testing"
)

func TestAttachCompilationCompilerInsertsBeforeTokenizer(t *testing.T) {
	dsl := `{
		"components": {
			"File": {"downstream": ["Extractor:A"], "upstream": [], "obj": {"component_name": "File", "params": {}}},
			"Extractor:A": {"downstream": ["Tokenizer:B"], "upstream": ["File"], "obj": {"component_name": "Extractor", "params": {}}},
			"Tokenizer:B": {"downstream": [], "upstream": ["Extractor:A"], "obj": {"component_name": "Tokenizer", "params": {}}}
		}
	}`
	out, err := AttachCompilationCompiler(dsl, map[string]any{
		"compilation_template_group_id": []any{"group-mindmap"},
		"llm_id":                        "chat-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	components := parsed["components"].(map[string]any)
	compiler := components["Compiler:KnowledgeCompile"].(map[string]any)
	obj := compiler["obj"].(map[string]any)
	if obj["component_name"] != "Compiler" {
		t.Fatalf("component_name = %v", obj["component_name"])
	}
	params := obj["params"].(map[string]any)
	if params["compilation_template_group_id"] != "group-mindmap" || params["llm_id"] != "chat-1" {
		t.Fatalf("params = %#v", params)
	}
	extractor := components["Extractor:A"].(map[string]any)
	if got := stringList(extractor["downstream"]); len(got) != 1 || got[0] != "Compiler:KnowledgeCompile" {
		t.Fatalf("extractor downstream = %#v", extractor["downstream"])
	}
	tokenizer := components["Tokenizer:B"].(map[string]any)
	if got := stringList(tokenizer["upstream"]); len(got) != 1 || got[0] != "Compiler:KnowledgeCompile" {
		t.Fatalf("tokenizer upstream = %#v", tokenizer["upstream"])
	}
	ids, err := TerminalComponentIDs([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "Tokenizer:B" {
		t.Fatalf("terminals = %#v", ids)
	}
}

func TestAttachCompilationCompilerSkipsConfiguredPipeline(t *testing.T) {
	dsl := `{
		"components": {
			"Compiler:Existing": {"downstream": [], "upstream": [], "obj": {"component_name": "Compiler", "params": {"compilation_template_group_id": "kept"}}}
		}
	}`
	out, err := AttachCompilationCompiler(dsl, map[string]any{
		"compilation_template_group_id": "other",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != dsl {
		t.Fatal("configured compiler pipeline was rewritten")
	}
}

func TestBuildParserConfigKeepsCompilationGroup(t *testing.T) {
	dsl := []byte(`{"components":{"Tokenizer:B":{"obj":{"component_name":"Tokenizer","params":{"fields":"text"}},"downstream":[],"upstream":[]}}}`)
	result, err := BuildParserConfig(dsl, map[string]any{
		"compilation_template_group_id": []any{"g1"},
		"chunk_token_num":               128,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := result["compilation_template_group_id"].([]any)
	if len(got) != 1 || got[0] != "g1" {
		t.Fatalf("compilation_template_group_id = %#v", result["compilation_template_group_id"])
	}
	if _, ok := result["chunk_token_num"]; ok {
		t.Fatal("flat chunk_token_num should still be dropped")
	}
}

func TestAttachCompilationCompilerDocumentEmptyWinsOverKB(t *testing.T) {
	dsl := `{"components":{"File":{"downstream":[],"upstream":[],"obj":{"component_name":"File","params":{}}}}}`
	out, err := AttachCompilationCompiler(dsl,
		map[string]any{"compilation_template_group_id": []any{}},
		map[string]any{"compilation_template_group_id": "kb-group"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if out != dsl {
		t.Fatal("empty document selection should not inherit the knowledge base group")
	}
}
