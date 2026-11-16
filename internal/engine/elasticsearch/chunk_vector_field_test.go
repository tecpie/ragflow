package elasticsearch

import (
	"encoding/json"
	"testing"
)

func TestConvertESResponseMergesDenseVectorFromFields(t *testing.T) {
	var response SearchResponse
	raw := []byte(`{
		"hits": {
			"hits": [{
				"_id": "chunk-1",
				"_index": "ragflow_tenant",
				"_source": {"content_with_weight": "body"},
				"fields": {"q_4_vec": [[0.1, 0.2, 0.3]]}
			}]
		}
	}`)
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}

	chunks := convertESResponse(&response, "q_4_vec")
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	got, ok := chunks[0]["q_4_vec"].([]interface{})
	if !ok || len(got) != 3 {
		t.Fatalf("vector = %#v", chunks[0]["q_4_vec"])
	}
	if chunks[0]["content_with_weight"] != "body" || chunks[0]["id"] != "chunk-1" {
		t.Fatalf("source fields changed: %#v", chunks[0])
	}
}
