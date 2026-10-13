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
	"math"
	"regexp"
	"strings"

	pdf "ragflow/internal/deepdoc/parser/pdf/type"
	"ragflow/internal/ingestion/component/schema"
)

// Vision enhancement kinds for DeepDOC media / signature fields.
// Mirrors Python deepdoc/parser/figure_parser.py table + signature paths.
const (
	visionKindFigure    = "figure"
	visionKindTable     = "table"
	visionKindSignature = "signature"

	visionSignatureBandKey = "_vision_signature_band"
)

// Labels that usually sit to the left of handwritten signatures / seals.
// Python: _SIGNATURE_LABEL_RE in deepdoc/parser/figure_parser.py.
var (
	signatureLabelRE   = regexp.MustCompile(`批准|审核|校核|编制|签字|签名|签章|盖章`)
	signatureCleanupRE = regexp.MustCompile(`[:：\s\-_/|]+`)
	cjkCharRE          = regexp.MustCompile(`[\x{4e00}-\x{9fff}]`)
)

func isSignatureLabelText(text string) bool {
	return signatureLabelRE.MatchString(text)
}

// alreadyHasSignatureName skips Vision when OCR already captured a plausible
// name after the role label. Python: _already_has_signature_name.
func alreadyHasSignatureName(text string) bool {
	cleaned := signatureLabelRE.ReplaceAllString(text, "")
	cleaned = signatureCleanupRE.ReplaceAllString(cleaned, "")
	return len(cjkCharRE.FindAllString(cleaned, -1)) >= 2
}

func mergeSignatureVisionText(existing, desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return existing
	}
	// Candidates are label-only OCR lines; Vision already emits "批准: 姓名".
	// Replace instead of appending to avoid "批准:\n批准: 李明".
	if !alreadyHasSignatureName(existing) {
		return desc
	}
	if strings.TrimSpace(existing) == "" {
		return desc
	}
	return existing + "\n" + desc
}

func visionEnhanceFlagEnabled(setup schema.ParserSetup, key string, def bool) bool {
	if setup == nil {
		return def
	}
	raw, ok := setup[key]
	if !ok || raw == nil {
		return def
	}
	switch v := raw.(type) {
	case bool:
		return v
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "0", "false", "no", "off":
			return false
		default:
			return true
		}
	default:
		return def
	}
}

// extendSignatureBandPositions widens a label crop rightward so handwritten
// signatures beside approval labels are included.
// Python: _crop_signature_band in deepdoc/parser/figure_parser.py.
//
// pageW/pageH are PDF-point page dimensions (same space as Position coords).
// When either is <= 0, clamping to page bounds / the 0.72 width floor is skipped
// and only the rightward/vertical padding relative to the box is applied.
func extendSignatureBandPositions(positions []pdf.Position, pageW, pageH float64) []pdf.Position {
	if len(positions) == 0 {
		return positions
	}
	out := make([]pdf.Position, len(positions))
	copy(out, positions)
	pos := out[0]
	height := math.Max(pos.Bottom-pos.Top, 8.0)
	cropLeft := math.Max(0.0, pos.Left)
	extendRight := pos.Right + math.Max(120.0, height*8.0)
	cropRight := extendRight
	if pageW > 0 {
		cropRight = math.Min(pageW, math.Max(extendRight, pageW*0.72))
	}
	cropTop := math.Max(0.0, pos.Top-height*0.35)
	cropBottom := pos.Bottom + height*0.55
	if pageH > 0 {
		cropBottom = math.Min(pageH, cropBottom)
	}
	if cropRight-cropLeft < 8 || cropBottom-cropTop < 8 {
		return positions
	}
	pos.Left = cropLeft
	pos.Right = cropRight
	pos.Top = cropTop
	pos.Bottom = cropBottom
	out[0] = pos
	return out
}
