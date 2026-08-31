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

package chunker

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"ragflow/internal/ingestion/component/schema"
)

func splitOneChunkByChildren(ck schema.ChunkDoc, parts []string) []schema.ChunkDoc {
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 {
		cp := cloneChunkDoc(ck)
		cp.Text = parts[0]
		cp.Mom = strings.TrimPrefix(ck.Text, "\n")
		return []schema.ChunkDoc{cp}
	}

	mom := strings.TrimPrefix(ck.Text, "\n")
	parentPDF := ck.PDFPositions
	parentPos := ck.Positions
	hasCoords := len(parentPDF) > 0 || len(parentPos) > 0

	lineIdx := childLineIndices(ck.Text, parts, parentPDF, parentPos)
	out := make([]schema.ChunkDoc, 0, len(parts))
	cumRunes := 0
	parentVisible := removeTag(ck.Text)
	runCount := utf8.RuneCountInString(parentVisible)
	if runCount == 0 {
		runCount = 1
	}

	for i, p := range parts {
		cp := cloneChunkDoc(ck)
		cp.Text = p
		cp.Mom = mom
		if !hasCoords {
			out = append(out, cp)
			continue
		}
		if lineIdx != nil && lineIdx[i] >= 0 {
			if row := positionMatrixRow(parentPDF, lineIdx[i]); len(row) > 0 {
				cp.PDFPositions = row
			}
			if row := positionMatrixRow(parentPos, lineIdx[i]); len(row) > 0 {
				cp.Positions = row
			}
		} else {
			partVisible := removeTag(p)
			partRunes := utf8.RuneCountInString(partVisible)
			startRatio := float64(cumRunes) / float64(runCount)
			endRatio := float64(cumRunes+partRunes) / float64(runCount)
			if sliced := slicePositionsByTextRatio(parentPDF, startRatio, endRatio); len(sliced) > 0 {
				cp.PDFPositions = sliced
			} else if len(parentPDF) > 0 {
				cp.PDFPositions = append(json.RawMessage(nil), parentPDF...)
			}
			if sliced := slicePositionsByTextRatio(parentPos, startRatio, endRatio); len(sliced) > 0 {
				cp.Positions = sliced
			} else if len(parentPos) > 0 {
				cp.Positions = append(json.RawMessage(nil), parentPos...)
			}
			cumRunes += partRunes
		}
		out = append(out, cp)
	}
	return out
}

func childLineIndices(parentText string, parts []string, pdfPos, pos json.RawMessage) []int {
	lines := nonEmptyLines(parentText)
	rowCount := positionMatrixLen(pdfPos)
	if rowCount == 0 {
		rowCount = positionMatrixLen(pos)
	}
	if rowCount <= 1 || len(lines) != rowCount || len(lines) != len(parts) {
		return nil
	}
	indices := make([]int, len(parts))
	used := make([]int, len(lines))
	for i := range used {
		used[i] = -1
	}
	for i, part := range parts {
		idx := indexMatchingLine(lines, strings.TrimSpace(part), used)
		if idx < 0 {
			return nil
		}
		used[idx] = i
		indices[i] = idx
	}
	return indices
}

func nonEmptyLines(text string) []string {
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var lines []string
	for _, line := range strings.Split(normalized, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

func indexMatchingLine(lines []string, want string, used []int) int {
	for i, line := range lines {
		if used[i] >= 0 {
			continue
		}
		if strings.TrimSpace(line) == want {
			return i
		}
	}
	dupSkip := 0
	for i, line := range lines {
		if strings.TrimSpace(line) != want {
			continue
		}
		count := 0
		for j := 0; j < i; j++ {
			if strings.TrimSpace(lines[j]) == want {
				count++
			}
		}
		if count == dupSkip {
			return i
		}
		dupSkip++
	}
	return -1
}

func positionMatrixLen(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var matrix [][]json.RawMessage
	if err := json.Unmarshal(raw, &matrix); err != nil {
		return 0
	}
	return len(matrix)
}

func positionMatrixRow(raw json.RawMessage, index int) json.RawMessage {
	if len(raw) == 0 || index < 0 {
		return nil
	}
	var matrix [][]json.RawMessage
	if err := json.Unmarshal(raw, &matrix); err != nil || index >= len(matrix) {
		return nil
	}
	out, err := json.Marshal([][]json.RawMessage{matrix[index]})
	if err != nil {
		return nil
	}
	return out
}
