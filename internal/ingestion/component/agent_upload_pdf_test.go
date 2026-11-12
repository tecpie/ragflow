package component

import "testing"

func TestAgentPDFSetup(t *testing.T) {
	cases := []struct {
		layout string
		key    string
		value  string
	}{
		{layout: "", key: "parse_method", value: "deepdoc"},
		{layout: "DeepDOC", key: "parse_method", value: "DeepDOC"},
		{layout: "Plain Text", key: "parse_method", value: "Plain Text"},
		{layout: "MinerU", key: "parse_method", value: "MinerU"},
		{layout: "PaddleOCR", key: "parse_method", value: "PaddleOCR"},
		{layout: "PaddleOCR-VL-1.5@paddle@PaddleOCR", key: "layout_recognizer", value: "PaddleOCR-VL-1.5@paddle@PaddleOCR"},
		{layout: "8ef2275f8c4a43ca8c915929400ad63c", key: "layout_recognizer", value: "8ef2275f8c4a43ca8c915929400ad63c"},
	}
	for _, tc := range cases {
		setup := AgentPDFSetup(tc.layout)
		if got, _ := setup[tc.key].(string); got != tc.value || len(setup) != 1 {
			t.Fatalf("AgentPDFSetup(%q) = %#v, want %s=%s", tc.layout, setup, tc.key, tc.value)
		}
	}
}

func TestDispatchAgentPDFLeavesLocalEngines(t *testing.T) {
	for _, layout := range []string{"", "DeepDOC", "Plain Text"} {
		_, handled, err := DispatchAgentPDF(t.Context(), nil, "tenant", "a.pdf", []byte("%PDF"), layout)
		if err != nil || handled {
			t.Fatalf("layout %q handled=%v err=%v, want local parser", layout, handled, err)
		}
	}
}
