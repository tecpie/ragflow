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

package models

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"strings"

	"golang.org/x/image/draw"

	"ragflow/internal/common"
)

// sgccMaxImageBase64Chars keeps one image inside the SGGM 262144-token
// window. The gateway counts the raw base64 text as input tokens, and a
// full-page PNG is large enough to exceed that window by itself.
const sgccMaxImageBase64Chars = 120_000

// adaptSGCCVisionMessages rewrites OpenAI image_url blocks for the SGCC
// (光明) vision gateway. A vision message keeps its text prompt and sends the
// picture as {"type":"image_base64","image":"data:image/...;base64,..."}.
// Plain chat without an image is left unchanged, and other providers keep
// image_url.
func adaptSGCCVisionMessages(modelName, requestURL string, providerBaseURL map[string]string, messages []Message) ([]Message, error) {
	if !sgccVisionGateway(modelName, requestURL, providerBaseURL) {
		return messages, nil
	}
	out := make([]Message, len(messages))
	copy(out, messages)
	for i := range out {
		rewritten, changed, err := rewriteSGCCContent(out[i].Content)
		if err != nil {
			return nil, err
		}
		if changed {
			out[i].Content = rewritten
		}
	}
	return out, nil
}

func sgccVisionGateway(modelName, requestURL string, providerBaseURL map[string]string) bool {
	if strings.Contains(strings.ToUpper(modelName), "SGGM") {
		return true
	}
	if sgccVisionURL(requestURL) {
		return true
	}
	for _, rawURL := range providerBaseURL {
		if sgccVisionURL(rawURL) {
			return true
		}
	}
	return false
}

func sgccVisionURL(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	return strings.Contains(lower, "lmp-cloud-ias-server") || strings.Contains(lower, "ai.sgcc.com.cn")
}

func rewriteSGCCContent(content interface{}) (interface{}, bool, error) {
	parts, ok := contentParts(content)
	if !ok {
		return content, false, nil
	}
	hasImage := false
	for _, part := range parts {
		switch partType(part) {
		case "image_url", "image_base64":
			hasImage = true
		}
	}
	if !hasImage {
		return content, false, nil
	}

	out := make([]interface{}, 0, len(parts))
	for _, part := range parts {
		switch partType(part) {
		case "text", "input_text":
			text := textOf(part)
			if text == "" {
				continue
			}
			out = append(out, map[string]interface{}{
				"type": "text",
				"text": text,
			})
		case "image_url":
			imageURL, ok := imageURLFromPart(part)
			if !ok {
				return nil, false, fmt.Errorf("sgcc vision: image_url part missing url")
			}
			dataURL, err := sgccImageDataURL(imageURL)
			if err != nil {
				return nil, false, err
			}
			out = append(out, map[string]interface{}{
				"type":  "image_base64",
				"image": dataURL,
			})
		case "image_base64":
			imageURL, ok := imageFieldFromPart(part)
			if !ok {
				return nil, false, fmt.Errorf("sgcc vision: image_base64 part missing image")
			}
			dataURL, err := sgccImageDataURL(imageURL)
			if err != nil {
				return nil, false, err
			}
			out = append(out, map[string]interface{}{
				"type":  "image_base64",
				"image": dataURL,
			})
		default:
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return nil, false, fmt.Errorf("sgcc vision: no image payload")
	}
	return out, true, nil
}

func contentParts(content interface{}) ([]map[string]interface{}, bool) {
	switch v := content.(type) {
	case []map[string]interface{}:
		return v, true
	case []interface{}:
		parts := make([]map[string]interface{}, 0, len(v))
		for _, item := range v {
			part, ok := asStringMap(item)
			if !ok {
				return nil, false
			}
			parts = append(parts, part)
		}
		return parts, true
	default:
		return nil, false
	}
}

func asStringMap(item interface{}) (map[string]interface{}, bool) {
	switch m := item.(type) {
	case map[string]interface{}:
		return m, true
	case map[string]string:
		out := make(map[string]interface{}, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out, true
	default:
		return nil, false
	}
}

func partType(part map[string]interface{}) string {
	t, _ := part["type"].(string)
	return strings.ToLower(strings.TrimSpace(t))
}

func textOf(part map[string]interface{}) string {
	text, _ := part["text"].(string)
	return text
}

func imageFieldFromPart(part map[string]interface{}) (string, bool) {
	raw, ok := part["image"]
	if !ok || raw == nil {
		return "", false
	}
	text, ok := raw.(string)
	return text, ok && strings.TrimSpace(text) != ""
}

// sgccImageDataURL builds the image field 光明 accepts:
// data:image/<mime>;base64,<payload>. Shrunk images are JPEG.
func sgccImageDataURL(imageURL string) (string, error) {
	payload, err := rawImageBase64(imageURL)
	if err != nil {
		return "", err
	}
	return "data:image/" + imageMIME(payload, imageURL) + ";base64," + payload, nil
}

func imageMIME(payload, original string) string {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err == nil {
		if len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF {
			return "jpeg"
		}
		if len(raw) >= 8 && bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
			return "png"
		}
	}
	lower := strings.ToLower(original)
	switch {
	case strings.Contains(lower, "image/jpeg"), strings.Contains(lower, "image/jpg"):
		return "jpeg"
	case strings.Contains(lower, "image/png"):
		return "png"
	default:
		return "jpeg"
	}
}

func imageURLFromPart(part map[string]interface{}) (string, bool) {
	raw, ok := part["image_url"]
	if !ok || raw == nil {
		return "", false
	}
	switch v := raw.(type) {
	case string:
		return v, strings.TrimSpace(v) != ""
	case map[string]interface{}:
		url, _ := v["url"].(string)
		return url, strings.TrimSpace(url) != ""
	case common.ImageURL:
		return v.URL, strings.TrimSpace(v.URL) != ""
	case *common.ImageURL:
		if v == nil {
			return "", false
		}
		return v.URL, strings.TrimSpace(v.URL) != ""
	default:
		return "", false
	}
}

func rawImageBase64(imageURL string) (string, error) {
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return "", fmt.Errorf("sgcc vision: empty image")
	}
	lower := strings.ToLower(imageURL)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return "", fmt.Errorf("sgcc vision: remote image URL is not supported, need inline base64")
	}
	payload := imageURL
	if strings.HasPrefix(lower, "data:") {
		marker := ";base64,"
		idx := strings.Index(lower, marker)
		if idx < 0 {
			return "", fmt.Errorf("sgcc vision: image data URL is not base64")
		}
		payload = imageURL[idx+len(marker):]
	}
	cleaned := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, payload)
	if cleaned == "" {
		return "", fmt.Errorf("sgcc vision: empty image base64")
	}
	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(cleaned)
		if err != nil {
			return "", fmt.Errorf("sgcc vision: image is not valid base64")
		}
		cleaned = base64.StdEncoding.EncodeToString(decoded)
	}
	if len(cleaned) <= sgccMaxImageBase64Chars {
		return cleaned, nil
	}
	shrunk, err := shrinkSGCCImage(decoded)
	if err != nil {
		return "", err
	}
	return shrunk, nil
}

func shrinkSGCCImage(raw []byte) (string, error) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("sgcc vision: decode image: %w", err)
	}
	for _, edge := range []int{1600, 1280, 1024, 768, 512, 384} {
		resized := resizeLongEdge(img, edge)
		for _, quality := range []int{75, 60, 45, 30} {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: quality}); err != nil {
				return "", fmt.Errorf("sgcc vision: encode jpeg: %w", err)
			}
			encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
			if len(encoded) <= sgccMaxImageBase64Chars {
				return encoded, nil
			}
		}
	}
	return "", fmt.Errorf("sgcc vision: image still exceeds the %d character context budget", sgccMaxImageBase64Chars)
}

func resizeLongEdge(src image.Image, maxEdge int) image.Image {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 || (width <= maxEdge && height <= maxEdge) {
		return src
	}
	newWidth, newHeight := maxEdge, maxEdge
	if width >= height {
		newHeight = max(1, height*maxEdge/width)
	} else {
		newWidth = max(1, width*maxEdge/height)
	}
	dst := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)
	return dst
}
