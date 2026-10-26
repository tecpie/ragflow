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
	"regexp"
	"strings"
)

const (
	maxContextItems      = 8
	maxContextChars      = 8000
	maxContextTotalChars = 32000
	maxQuestionChars     = 8000
	maxAnswerChars       = 8000
	maxReferenceChars    = 8000
)

var (
	toolCallTagRE   = regexp.MustCompile(`(?is)<tool_call\b[^>]*>.*?</tool_call>`)
	pipeToolStartRE = regexp.MustCompile(`(?i)\|-tool_call-\|`)
	multiNewline    = regexp.MustCompile(`\n{3,}`)
)

// scoringPayload is the truncated text fed into RAGAS scorers.
type scoringPayload struct {
	Question  string
	Answer    string
	Reference string
	Contexts  []string
	Meta      map[string]any
}

func stripToolCalls(answer string) string {
	cleaned := toolCallTagRE.ReplaceAllString(answer, "")
	for {
		loc := pipeToolStartRE.FindStringIndex(cleaned)
		if loc == nil {
			break
		}
		rest := cleaned[loc[1]:]
		end := strings.Index(rest, "\n\n")
		if end < 0 {
			cleaned = cleaned[:loc[0]]
			break
		}
		cleaned = cleaned[:loc[0]] + rest[end:]
	}
	cleaned = multiNewline.ReplaceAllString(cleaned, "\n\n")
	return strings.TrimSpace(cleaned)
}

func normalizeScoringText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func truncatePrefix(text string, maxChars int) (string, bool) {
	if maxChars <= 0 {
		return "", text != ""
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text, false
	}
	return string(runes[:maxChars]), true
}

func prepareContexts(contexts []string) ([]string, map[string]any) {
	raw := make([]string, 0, len(contexts))
	for _, item := range contexts {
		text := normalizeScoringText(item)
		if text != "" {
			raw = append(raw, text)
		}
	}
	runeLen := func(s string) int { return len([]rune(s)) }
	charsIn := 0
	for _, x := range raw {
		charsIn += runeLen(x)
	}
	fits := len(raw) <= maxContextItems && charsIn <= maxContextTotalChars
	if fits {
		for _, x := range raw {
			if runeLen(x) > maxContextChars {
				fits = false
				break
			}
		}
	}
	if fits {
		itemChars := make([]int, len(raw))
		for i, x := range raw {
			itemChars[i] = runeLen(x)
		}
		return append([]string(nil), raw...), map[string]any{
			"context_items_in":   len(raw),
			"context_items_used": len(raw),
			"context_chars_in":   charsIn,
			"context_chars_used": charsIn,
			"context_item_chars": itemChars,
			"context_truncated":  false,
		}
	}

	used := make([]string, 0, maxContextItems)
	total := 0
	limit := len(raw)
	if limit > maxContextItems {
		limit = maxContextItems
	}
	for i := 0; i < limit; i++ {
		piece, _ := truncatePrefix(raw[i], maxContextChars)
		remain := maxContextTotalChars - total
		if remain <= 0 {
			break
		}
		if runeLen(piece) > remain {
			piece, _ = truncatePrefix(piece, remain)
		}
		if piece == "" {
			continue
		}
		used = append(used, piece)
		total += runeLen(piece)
	}
	itemChars := make([]int, len(used))
	for i, x := range used {
		itemChars[i] = runeLen(x)
	}
	return used, map[string]any{
		"context_items_in":   len(raw),
		"context_items_used": len(used),
		"context_chars_in":   charsIn,
		"context_chars_used": total,
		"context_item_chars": itemChars,
		"context_truncated":  true,
	}
}

func prepareScoringPayload(question, answer string, contexts []string, groundTruth string) scoringPayload {
	q, qCut := truncatePrefix(strings.TrimSpace(question), maxQuestionChars)
	ans, aCut := truncatePrefix(stripToolCalls(answer), maxAnswerChars)
	ref, rCut := truncatePrefix(strings.TrimSpace(groundTruth), maxReferenceChars)
	used, ctxMeta := prepareContexts(contexts)
	meta := map[string]any{}
	for k, v := range ctxMeta {
		meta[k] = v
	}
	meta["question_truncated"] = qCut
	meta["answer_truncated"] = aCut
	meta["reference_truncated"] = rCut
	meta["truncated"] = ctxMeta["context_truncated"] == true || qCut || aCut || rCut
	return scoringPayload{
		Question:  q,
		Answer:    ans,
		Reference: ref,
		Contexts:  used,
		Meta:      meta,
	}
}

func extractContextsFromReference(reference map[string]any) []string {
	if reference == nil {
		return nil
	}
	var chunks []any
	for _, key := range []string{"chunks", "chunk_list", "doc_aggs", "documents"} {
		if raw, ok := reference[key]; ok {
			switch v := raw.(type) {
			case []any:
				chunks = append(chunks, v...)
			case []map[string]any:
				for _, m := range v {
					chunks = append(chunks, m)
				}
			}
		}
	}
	if len(chunks) == 0 && len(reference) > 0 {
		// Single chunk-shaped reference.
		if _, hasContent := reference["content"]; hasContent {
			chunks = append(chunks, reference)
		} else if _, hasCW := reference["content_with_weight"]; hasCW {
			chunks = append(chunks, reference)
		}
	}
	out := make([]string, 0, len(chunks))
	seen := map[string]struct{}{}
	for _, chunk := range chunks {
		text := chunkText(chunk)
		if text == "" {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out
}

func chunkText(chunk any) string {
	switch v := chunk.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		for _, key := range []string{"content", "content_with_weight", "text", "document_name"} {
			if s, ok := v[key].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}
