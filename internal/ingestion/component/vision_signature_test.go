//
// Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package component

import (
	"context"
	"strings"
	"testing"

	"ragflow/internal/dao"
	pdf "ragflow/internal/deepdoc/parser/pdf/type"
	"ragflow/internal/ingestion/component/progress"
	"ragflow/internal/parser/parser"
	"ragflow/internal/utility"

	"gorm.io/gorm"
)

func TestAlreadyHasSignatureName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		text string
		want bool
	}{
		{"批准：", false},
		{"批准:", false},
		{"审核", false},
		{"批准：李", false}, // only one CJK after label stripping
		{"批准：李明", true},
		{"审核 王芳", true},
		{"编制/张三", true},
		{"签字", false},
	}
	for _, tc := range cases {
		if got := alreadyHasSignatureName(tc.text); got != tc.want {
			t.Errorf("alreadyHasSignatureName(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestMergeSignatureVisionText(t *testing.T) {
	t.Parallel()
	if got := mergeSignatureVisionText("批准：", "批准: 李明"); got != "批准: 李明" {
		t.Errorf("label-only merge = %q, want replaced transcription", got)
	}
	if got := mergeSignatureVisionText("批准：李明", "Seal/stamp: 某某公司"); got != "批准：李明\nSeal/stamp: 某某公司" {
		t.Errorf("named merge = %q, want append", got)
	}
}

func TestExtendSignatureBandPositions(t *testing.T) {
	t.Parallel()
	in := []pdf.Position{{
		PageNumbers: []int{0},
		Left:        40,
		Right:       80,
		Top:         100,
		Bottom:      120,
	}}
	got := extendSignatureBandPositions(in, 500, 700)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	height := 20.0
	wantRight := 80.0 + 160.0 // max(120, height*8) = 160
	if pageFloor := 500.0 * 0.72; pageFloor > wantRight {
		wantRight = pageFloor
	}
	if got[0].Right != wantRight {
		t.Errorf("Right = %v, want %v", got[0].Right, wantRight)
	}
	if got[0].Left != 40 {
		t.Errorf("Left = %v, want 40", got[0].Left)
	}
	wantTop := 100 - height*0.35
	wantBottom := 120 + height*0.55
	if got[0].Top != wantTop || got[0].Bottom != wantBottom {
		t.Errorf("vertical band = (%v,%v), want (%v,%v)", got[0].Top, got[0].Bottom, wantTop, wantBottom)
	}
	// Original slice must stay unchanged.
	if in[0].Right != 80 {
		t.Errorf("input mutated: Right = %v", in[0].Right)
	}
}

func TestCollectVisionEnhanceTargets_SignatureAndTable(t *testing.T) {
	t.Parallel()
	items := []parser.DocItem{
		parser.RawItem{M: map[string]any{"text": "批准：", "doc_type_kwd": "text", "positions": [][]any{{1.0, 40.0, 80.0, 100.0, 120.0}}}},
		parser.RawItem{M: map[string]any{"text": "批准：李明", "doc_type_kwd": "text", "positions": [][]any{{1.0, 40.0, 80.0, 140.0, 160.0}}}},
		parser.RawItem{M: map[string]any{"text": "<table></table>", "doc_type_kwd": "table", "image": "aGVsbG8="}},
		parser.RawItem{M: map[string]any{"text": "普通文字", "doc_type_kwd": "text", "positions": [][]any{{1.0, 1.0, 2.0, 3.0, 4.0}}}},
	}
	got := collectVisionEnhanceTargets(items, true, true)
	if len(got) != 2 {
		t.Fatalf("targets = %#v, want table + signature", got)
	}
	// Media images/tables are collected first; signature labels are appended after.
	if got[0].kind != visionKindTable || got[0].idx != 2 {
		t.Errorf("first = %#v, want table idx 2", got[0])
	}
	if got[1].kind != visionKindSignature || got[1].idx != 0 {
		t.Errorf("second = %#v, want signature idx 0", got[1])
	}

	disabled := collectVisionEnhanceTargets(items, false, false)
	if len(disabled) != 0 {
		t.Errorf("disabled flags collected %#v, want empty", disabled)
	}
}

func TestVisionEnhancement_UsesTableAndSignaturePrompts(t *testing.T) {
	invoker := &visionEnhanceCaptureInvoker{}
	var figureCalls, tableCalls, signatureCalls int
	swapVisionGlobals(t, fakeResolver, invoker.invoke, nil)
	figureVisionPromptBuilder = func(language string) (string, error) {
		figureCalls++
		return "figure prompt " + language, nil
	}
	tableVisionPromptBuilder = func(language string) (string, error) {
		tableCalls++
		return "table prompt " + language, nil
	}
	signatureVisionPromptBuilder = func(language string) (string, error) {
		signatureCalls++
		return "signature prompt " + language, nil
	}

	imagePayload := visionTestPNGBase64(t)
	originalFactory := visionImageCropperFactory
	visionImageCropperFactory = func(context.Context, *gorm.DB, map[string]any) (visionImageCropper, error) {
		return stubVisionImageCropper{payload: imagePayload}, nil
	}
	t.Cleanup(func() { visionImageCropperFactory = originalFactory })

	dispatched := parser.ParseResult{
		JSON: []parser.DocItem{
			parser.RawItem{M: map[string]any{"text": "批准：", "doc_type_kwd": "text", "positions": [][]any{{1.0, 40.0, 80.0, 100.0, 120.0}}}},
			parser.RawItem{M: map[string]any{"text": "<table><tr><td>cell</td></tr></table>", "image": imagePayload, "doc_type_kwd": "table"}},
			parser.RawItem{M: map[string]any{"text": "", "image": imagePayload, "doc_type_kwd": "image"}},
		},
	}
	res, handled, err := maybeDispatchVisionEnhancement(
		t.Context(), dao.DB, utility.FileTypePDF, dispatched,
		map[string]any{"tenant_id": "t1", "lang": "Chinese"},
		visionSettings{}, progress.Window{},
	)
	if err != nil {
		t.Fatalf("maybeDispatchVisionEnhancement: %v", err)
	}
	if !handled {
		t.Fatal("handled = false, want true")
	}
	if figureCalls != 1 || tableCalls != 1 || signatureCalls != 1 {
		t.Fatalf("prompt builders figure=%d table=%d signature=%d, want 1 each", figureCalls, tableCalls, signatureCalls)
	}
	if got := res.JSON[0].ToMap()["text"]; got != "a diagram of a pipeline" {
		t.Errorf("signature text = %q, want replaced VLM output", got)
	}
	if got, _ := res.JSON[1].ToMap()["text"].(string); !strings.Contains(got, "a diagram of a pipeline") {
		t.Errorf("table text = %q, want appended VLM output", got)
	}
	if len(invoker.captured) != 3 {
		t.Fatalf("VLM messages = %d, want 3", len(invoker.captured))
	}
	promptTexts := make([]string, 0, 3)
	for _, msg := range invoker.captured {
		parts, ok := msg.Content.([]interface{})
		if !ok || len(parts) == 0 {
			continue
		}
		part, _ := parts[0].(map[string]any)
		text, _ := part["text"].(string)
		promptTexts = append(promptTexts, text)
	}
	joined := strings.Join(promptTexts, "\n")
	for _, want := range []string{"signature prompt Chinese", "table prompt Chinese", "figure prompt Chinese"} {
		if !strings.Contains(joined, want) {
			t.Errorf("prompt texts %q missing %q", joined, want)
		}
	}
}

type stubVisionImageCropper struct{ payload string }

func (stubVisionImageCropper) Prepare(context.Context, map[string]any) error { return nil }

func (c stubVisionImageCropper) Crop(_ context.Context, item map[string]any) (*visionImage, error) {
	if img, _ := item["image"].(string); img != "" {
		return &visionImage{VLMData: img, VLMDataValidated: true}, nil
	}
	if c.payload == "" {
		return nil, nil
	}
	return &visionImage{VLMData: c.payload, VLMDataValidated: true}, nil
}

func (stubVisionImageCropper) Close() error { return nil }
