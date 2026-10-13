package chunker

import (
	"encoding/json"
	"testing"

	"ragflow/internal/ingestion/component/schema"
)

func TestSplitOneChunkByChildren_LineAlignedPositions(t *testing.T) {
	positions, err := json.Marshal([][]float64{
		{1, 10, 100, 20, 30},
		{1, 10, 100, 40, 50},
		{1, 10, 100, 60, 70},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := schema.ChunkDoc{
		Text:         "第一行\n第二行\n第三行",
		DocType:      "text",
		PDFPositions: positions,
		Positions:    positions,
	}
	parts := []string{"第一行", "第二行", "第三行"}
	children := splitOneChunkByChildren(parent, parts)
	if len(children) != 3 {
		t.Fatalf("want 3 children got %d", len(children))
	}
	for i, child := range children {
		if child.Text != parts[i] {
			t.Errorf("child[%d] text: want %q got %q", i, parts[i], child.Text)
		}
		row := positionMatrixRow(parent.PDFPositions, i)
		if string(child.PDFPositions) != string(row) {
			t.Errorf("child[%d] pdf positions mismatch", i)
		}
	}
}

func TestSplitOneChunkByChildren_RatioFallback(t *testing.T) {
	positions, err := json.Marshal([][]float64{{1, 0, 100, 0, 100}})
	if err != nil {
		t.Fatal(err)
	}
	parent := schema.ChunkDoc{
		Text:         "aaaa\nbbbb",
		DocType:      "text",
		PDFPositions: positions,
	}
	children := splitOneChunkByChildren(parent, []string{"aaaa", "bbbb"})
	if len(children) != 2 {
		t.Fatalf("want 2 children got %d", len(children))
	}
	if len(children[0].PDFPositions) == 0 || len(children[1].PDFPositions) == 0 {
		t.Fatal("expected sliced positions on both children")
	}
}
