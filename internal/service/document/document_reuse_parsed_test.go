package document

import (
	"errors"
	"strings"
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
		{
			"id":                  "map",
			"compile_kwd":         "mind_map",
			"knowledge_graph_kwd": "entity",
			"content_with_weight": `{"name":"主题","type":"mind_map","source_chunk_ids":["parent","missing"]}`,
			"source_chunk_ids":    []interface{}{"parent", "missing"},
			"source_doc_ids":      []interface{}{"src-doc"},
			"token_num":           8,
		},
	}

	prepared, err := prepareReusedChunks(chunks, "dst-kb", "dst-doc", "moved.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared) != 3 {
		t.Fatalf("prepared %d chunks, want 3", len(prepared))
	}
	if reusedBodyCount(prepared) != 2 {
		t.Fatalf("body count = %d, want 2", reusedBodyCount(prepared))
	}
	if got := reusedTokenCount(prepared); got != 0 {
		t.Fatalf("token count = %d, want 0 (mind map token_num excluded)", got)
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
	var mind map[string]interface{}
	for _, chunk := range prepared {
		if isMindMapChunk(chunk) {
			mind = chunk
			break
		}
	}
	if mind == nil {
		t.Fatal("mind map row was dropped")
	}
	if mind["id"] == "map" || mind["doc_id"] != "dst-doc" {
		t.Fatalf("mind map identity not rewritten: %#v", mind)
	}
	ids, _ := mind["source_chunk_ids"].([]string)
	if len(ids) != 1 || ids[0] != parentID {
		t.Fatalf("mind map source chunks = %#v, want %s", mind["source_chunk_ids"], parentID)
	}
	docs, _ := mind["source_doc_ids"].([]string)
	if len(docs) != 1 || docs[0] != "dst-doc" {
		t.Fatalf("mind map source docs = %#v", mind["source_doc_ids"])
	}
	payload, _ := mind["content_with_weight"].(string)
	if !strings.Contains(payload, parentID) || strings.Contains(payload, "missing") || strings.Contains(payload, `"parent"`) {
		t.Fatalf("mind map payload ids not remapped: %s", payload)
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
	prepared, err := prepareReusedChunks([]map[string]interface{}{
		{"id": "body", "token_num": 12, "q_4_vec": []interface{}{0.1}},
		{"id": "graph", "knowledge_graph_kwd": "entity", "token_num": 90},
		{"id": "wiki", "compile_kwd": "wiki", "tk_nums": 30},
		{"id": "map", "compile_kwd": "mind_map", "token_num": 8},
	}, "dst", "doc", "moved.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if got := reusedTokenCount(prepared); got != 12 {
		t.Fatalf("token count = %d, want 12", got)
	}
	if reusedBodyCount(prepared) != 1 {
		t.Fatalf("body count = %d, want 1", reusedBodyCount(prepared))
	}
}

func TestChunksCarryVectorIgnoresCompiledRows(t *testing.T) {
	if chunksCarryVector([]map[string]interface{}{
		{"id": "body"},
		{"id": "map", "compile_kwd": "mind_map", "q_4_vec": []interface{}{0.1}},
	}) {
		t.Fatal("a mind map vector must not satisfy the body vector guard")
	}
	if !chunksCarryVector([]map[string]interface{}{
		{"id": "body", "q_4_vec": []interface{}{0.2}},
		{"id": "map", "compile_kwd": "mind_map"},
	}) {
		t.Fatal("a body vector was ignored")
	}
}

func TestRemapMindMapJSONChunkIDsAndDropsStaleTokens(t *testing.T) {
	prepared, err := prepareReusedChunks([]map[string]interface{}{
		{"id": "parent", "content_with_weight": "body", "q_4_vec": []interface{}{0.1}},
		{
			"id":                  "map",
			"compile_kwd":         "mind_map",
			"content_with_weight": `{"name":"A&B","type":"mind_map","source_chunk_ids":["parent"]}`,
			"source_chunk_ids":    `["parent"]`,
			"content_ltks":        "parent",
			"content_sm_ltks":     "parent",
		},
		{
			"id":               "broken",
			"compile_kwd":      "mind_map",
			"source_chunk_ids": "[not-json",
		},
	}, "dst", "doc", "moved.pdf")
	if err != nil {
		t.Fatal(err)
	}
	var mind, broken map[string]interface{}
	for _, chunk := range prepared {
		switch chunkID(chunk) {
		case "":
		default:
			if compileKeyword(chunk) != "mind_map" {
				continue
			}
			if _, ok := chunk["content_with_weight"]; ok {
				mind = chunk
			} else {
				broken = chunk
			}
		}
	}
	if mind == nil || broken == nil {
		t.Fatal("mind map rows were dropped")
	}
	ids, _ := mind["source_chunk_ids"].([]string)
	if len(ids) != 1 || ids[0] == "" || ids[0] == "parent" {
		t.Fatalf("json chunk ids = %#v", mind["source_chunk_ids"])
	}
	payload, _ := mind["content_with_weight"].(string)
	if !strings.Contains(payload, "A&B") || strings.Contains(payload, `\u0026`) || !strings.Contains(payload, ids[0]) {
		t.Fatalf("payload = %s", payload)
	}
	if _, ok := mind["content_ltks"]; ok {
		t.Fatal("stale content_ltks was kept")
	}
	if _, ok := mind["content_sm_ltks"]; ok {
		t.Fatal("stale content_sm_ltks was kept")
	}
	if broken["source_chunk_ids"] != "[not-json" {
		t.Fatalf("unparsed chunk ids = %#v", broken["source_chunk_ids"])
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
