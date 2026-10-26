//
//  Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.
//

package service

import (
	"math"
	"strings"
	"testing"
)

func TestTruncatePrefix(t *testing.T) {
	got, cut := truncatePrefix("abcdef", 3)
	if got != "abc" || !cut {
		t.Fatalf("truncatePrefix short: got %q cut=%v", got, cut)
	}
	got, cut = truncatePrefix("abc", 10)
	if got != "abc" || cut {
		t.Fatalf("truncatePrefix fit: got %q cut=%v", got, cut)
	}
	got, cut = truncatePrefix("继电保护现场", 2)
	if got != "继电" || !cut {
		t.Fatalf("truncatePrefix runes: got %q cut=%v", got, cut)
	}
}

func TestPrepareContextsTruncation(t *testing.T) {
	long := strings.Repeat("x", maxContextChars+100)
	used, meta := prepareContexts([]string{long, "short", strings.Repeat("y", 50)})
	if len(used) == 0 {
		t.Fatal("expected truncated contexts")
	}
	if meta["context_truncated"] != true {
		t.Fatalf("expected truncated=true, got %#v", meta)
	}
	if len(used[0]) > maxContextChars {
		t.Fatalf("context longer than max: %d", len(used[0]))
	}

	ok, meta2 := prepareContexts([]string{"a", "b"})
	if len(ok) != 2 || meta2["context_truncated"] != false {
		t.Fatalf("short contexts should not truncate: %#v %#v", ok, meta2)
	}
}

func TestPrepareScoringPayloadLimits(t *testing.T) {
	q := strings.Repeat("q", maxQuestionChars+10)
	a := "before<tool_call>{\"n\":1}</tool_call>after"
	p := prepareScoringPayload(q, a, []string{"ctx"}, strings.Repeat("g", maxReferenceChars+5))
	if len(p.Question) != maxQuestionChars {
		t.Fatalf("question len=%d", len(p.Question))
	}
	if p.Answer != "beforeafter" {
		t.Fatalf("answer=%q", p.Answer)
	}
	if len(p.Reference) != maxReferenceChars {
		t.Fatalf("reference len=%d", len(p.Reference))
	}
	if p.Meta["truncated"] != true {
		t.Fatalf("meta=%#v", p.Meta)
	}
}

func TestAveragePrecision(t *testing.T) {
	if got := averagePrecision(nil); got != 0 {
		t.Fatalf("empty=%v", got)
	}
	// [1,0,1]: AP = ((1/1)*1 + (1/2)*0 + (2/3)*1) / (2+1e-10)
	got := averagePrecision([]int{1, 0, 1})
	want := (1.0 + 0.0 + 2.0/3.0) / (2.0 + 1e-10)
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got=%v want=%v", got, want)
	}
	if got := averagePrecision([]int{0, 0}); math.Abs(got) > 1e-6 {
		t.Fatalf("all zero should be ~0, got %v", got)
	}
}

func TestCosineSimilarity(t *testing.T) {
	if got := cosineSimilarity([]float64{1, 0}, []float64{1, 0}); math.Abs(got-1) > 1e-9 {
		t.Fatalf("identical=%v", got)
	}
	if got := cosineSimilarity([]float64{1, 0}, []float64{0, 1}); math.Abs(got) > 1e-9 {
		t.Fatalf("orthogonal=%v", got)
	}
	if got := cosineSimilarity([]float64{1}, []float64{1, 2}); got != 0 {
		t.Fatalf("mismatched dims=%v", got)
	}
}

func TestAnswerCorrectnessF1(t *testing.T) {
	if got := answerCorrectnessF1(0, 0, 0); got != 1 {
		t.Fatalf("empty statements F1=%v", got)
	}
	// tp=1,fp=1,fn=1 → 2/4 = 0.5
	if got := answerCorrectnessF1(1, 1, 1); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("F1=%v", got)
	}
	if got := answerCorrectnessF1(2, 0, 0); got != 1 {
		t.Fatalf("perfect F1=%v", got)
	}
}

func TestStripToolCalls(t *testing.T) {
	got := stripToolCalls("hi<tool_call>{\"a\":1}</tool_call>there")
	if got != "hithere" {
		t.Fatalf("got=%q", got)
	}
	got = stripToolCalls("a|-tool_call-|secret\n\nb")
	if !strings.Contains(got, "a") || !strings.Contains(got, "b") {
		t.Fatalf("pipe form got=%q", got)
	}
	if strings.Contains(got, "secret") {
		t.Fatalf("secret leaked: %q", got)
	}
}

func TestEmptyContextsScoreZeroHelpers(t *testing.T) {
	// Pure helpers / early-return paths used by context metrics.
	if sc := averagePrecision(nil); sc != 0 {
		t.Fatalf("empty AP=%v", sc)
	}
	p := prepareScoringPayload("q", "a", nil, "gt")
	if len(p.Contexts) != 0 {
		t.Fatalf("contexts=%v", p.Contexts)
	}
	s := &ragasScorer{}
	if sc, err := s.scoreFaithfulness(nil, "q", "a", nil); err != nil || sc != 0 {
		t.Fatalf("faithfulness empty: %v %v", sc, err)
	}
	if sc, err := s.scoreContextPrecision(nil, "q", "gt", nil); err != nil || sc != 0 {
		t.Fatalf("precision empty: %v %v", sc, err)
	}
	if sc, err := s.scoreContextRecall(nil, "q", "gt", nil); err != nil || sc != 0 {
		t.Fatalf("recall empty: %v %v", sc, err)
	}
}

func TestExtractContextsFromReference(t *testing.T) {
	ref := map[string]any{
		"chunks": []any{
			map[string]any{"content": "hello"},
			map[string]any{"content_with_weight": "hello"},
			map[string]any{"text": "world"},
		},
	}
	got := extractContextsFromReference(ref)
	if len(got) != 2 || got[0] != "hello" || got[1] != "world" {
		t.Fatalf("got=%v", got)
	}
}
