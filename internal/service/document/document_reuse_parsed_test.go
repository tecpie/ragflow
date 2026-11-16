package document

import (
	"errors"
	"testing"

	"ragflow/internal/entity"
)

func TestPrepareReusedChunksSkipsCompilationAndRemapsParent(t *testing.T) {
	chunks := []map[string]interface{}{
		{
			"id":                  "parent",
			"doc_id":              "src-doc",
			"kb_id":               "src-kb",
			"content_with_weight": "parent text",
			"q_4_vec":             []interface{}{0.1, 0.2, 0.3, 0.4},
			"_score":              1.2,
			"_id":                 "parent",
		},
		{
			"id":                  "child",
			"doc_id":              "src-doc",
			"kb_id":               "src-kb",
			"mom_id":              "parent",
			"content_with_weight": "child text",
			"img_id":              "src-kb-child",
		},
		{
			"id":                  "graph",
			"knowledge_graph_kwd": "entity",
			"content_with_weight": "entity",
		},
		{
			"id":         "raptor",
			"raptor_kwd": "tree",
		},
		{
			"id":          "wiki",
			"compile_kwd": "wiki_page",
			"token_num":   40,
		},
	}

	prepared := prepareReusedChunks(chunks, "dst-kb", "dst-doc", "moved.pdf")
	if len(prepared) != 2 {
		t.Fatalf("prepared %d chunks, want 2", len(prepared))
	}
	byContent := map[string]map[string]interface{}{}
	for _, chunk := range prepared {
		content, _ := chunk["content_with_weight"].(string)
		byContent[content] = chunk
		if _, ok := chunk["_score"]; ok {
			t.Fatalf("internal field leaked: %#v", chunk)
		}
		if chunk["kb_id"] != "dst-kb" || chunk["doc_id"] != "dst-doc" || chunk["docnm_kwd"] != "moved.pdf" {
			t.Fatalf("identity not rewritten: %#v", chunk)
		}
		if _, ok := chunk["q_4_vec"]; content == "parent text" && !ok {
			t.Fatal("vector was dropped")
		}
	}
	parentID, _ := byContent["parent text"]["id"].(string)
	childMom, _ := byContent["child text"]["mom_id"].(string)
	if parentID == "" || parentID == "parent" || childMom != parentID {
		t.Fatalf("mom_id = %q, parent id = %q", childMom, parentID)
	}
	if byContent["child text"]["img_id"] != "src-kb-child" {
		t.Fatalf("image id should stay until the copy step, got %#v", byContent["child text"]["img_id"])
	}
}

func TestImageKeyFromSourceKBUsesDatasetPrefix(t *testing.T) {
	bucket, key, ok := imageKeyFromSourceKB("dataset-abc-123", "dataset-abc-123-page-1-image")
	if !ok || bucket != "dataset-abc-123" || key != "page-1-image" {
		t.Fatalf("key = %q %q ok=%v", bucket, key, ok)
	}
	if _, _, ok := imageKeyFromSourceKB("kb-1", "other-kb-image"); ok {
		t.Fatal("a foreign prefix must not be treated as this dataset's object")
	}
	if _, _, ok := imageKeyFromSourceKB("kb-1", "kb-1-"); ok {
		t.Fatal("an empty object key must be rejected")
	}
}

func TestIsCompiledChunkIncludesCompileMarker(t *testing.T) {
	for _, kind := range []string{"wiki_page", "mind_map", "page_index"} {
		if !isCompiledChunk(map[string]interface{}{"id": "row", "compile_kwd": kind}) {
			t.Fatalf("compile_kwd %s was treated as a body chunk", kind)
		}
	}
	if isCompiledChunk(map[string]interface{}{"id": "body", "compile_kwd": "  "}) {
		t.Fatal("blank compile_kwd is not a compiled row")
	}
}

func TestReusedTokenCountIgnoresSkippedRows(t *testing.T) {
	prepared := prepareReusedChunks([]map[string]interface{}{
		{"id": "body", "token_num": 12, "q_4_vec": []interface{}{0.1}},
		{"id": "graph", "knowledge_graph_kwd": "entity", "token_num": 90},
		{"id": "wiki", "compile_kwd": "wiki", "tk_nums": 30},
	}, "dst", "doc", "moved.pdf")
	if got := reusedTokenCount(prepared); got != 12 {
		t.Fatalf("token count = %d, want 12", got)
	}
}

func TestReuseEmbeddingMatchesRequiresBothIDs(t *testing.T) {
	tenant := "tenant-embd"
	other := "other-embd"
	source := &entity.Knowledgebase{EmbdID: "text-embedding-v4", TenantEmbdID: &tenant}
	same := &entity.Knowledgebase{EmbdID: "text-embedding-v4", TenantEmbdID: &tenant}
	if err := reuseEmbeddingMatches(source, same); err != nil {
		t.Fatal(err)
	}
	differentModel := &entity.Knowledgebase{EmbdID: "bge-m3", TenantEmbdID: &tenant}
	if err := reuseEmbeddingMatches(source, differentModel); err == nil {
		t.Fatal("different embd_id was accepted")
	}
	differentTenant := &entity.Knowledgebase{EmbdID: "text-embedding-v4", TenantEmbdID: &other}
	if err := reuseEmbeddingMatches(source, differentTenant); err == nil {
		t.Fatal("different tenant embd id was accepted")
	}
	var argument *ReuseParsedError
	if !errors.As(reuseEmbeddingMatches(source, differentModel), &argument) || !argument.Argument {
		t.Fatal("model mismatch must be an argument error")
	}
}

func TestStaleChunkIDsKeepsInsertedBatch(t *testing.T) {
	stale := staleChunkIDs([]map[string]interface{}{
		{"id": "old-1"},
		{"id": "new-1"},
		{"id": "old-1"},
		{"id": ""},
		{"content_with_weight": "missing id"},
		{"id": "old-2"},
	}, []string{"new-1", "new-2", ""})
	if len(stale) != 2 || stale[0] != "old-1" || stale[1] != "old-2" {
		t.Fatalf("stale ids = %v", stale)
	}
}

func TestReuseChunkSelectFieldsRequestsVectorsAndCompileMarkers(t *testing.T) {
	fields := reuseChunkSelectFields()
	required := []string{"*", "q_1024_vec", "compile_kwd", "content_sm_ltks", "raptor_kwd"}
	for _, name := range required {
		found := false
		for _, field := range fields {
			if field == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("select fields %v missing %s", fields, name)
		}
	}
}
