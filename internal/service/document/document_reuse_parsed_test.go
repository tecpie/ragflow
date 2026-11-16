package document

import "testing"

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

func TestSplitImageID(t *testing.T) {
	bucket, key := splitImageID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if bucket != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || key != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("split = %q %q", bucket, key)
	}
	if b, k := splitImageID("nohyphen"); b != "" || k != "" {
		t.Fatalf("expected empty split, got %q %q", b, k)
	}
}
