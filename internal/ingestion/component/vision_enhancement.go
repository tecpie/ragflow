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

// Package component — vision media enhancement: enriches parsed JSON items
// with vision-model descriptions of embedded images/tables.
// Mirrors Python's enhance_media_sections_with_vision
// (rag/flow/parser/utils.py:162, called at parser.py:772/978/1115).

package component

import (
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"ragflow/internal/common"
	pdflayout "ragflow/internal/deepdoc/parser/pdf/layout"
	"ragflow/internal/entity"
	modelModule "ragflow/internal/entity/models"
	"ragflow/internal/parser/parser"
	"ragflow/internal/rag/prompts"
	"ragflow/internal/utility"

	"gorm.io/gorm"
)

var (
	figureVisionPromptBuilder    func(language string) (string, error) = buildFigureVisionPrompt
	tableVisionPromptBuilder     func(language string) (string, error) = buildTableVisionPrompt
	signatureVisionPromptBuilder func(language string) (string, error) = buildSignatureVisionPrompt
	visionChatInvoker                                                  = defaultVisionChatInvoker
	visionImageCropperFactory                                          = newVisionImageCropper
)

const (
	visionEnhancementConcurrency int = 10
	// visionChatTimeout bounds a single VLM call so a hung endpoint cannot
	// occupy one of the concurrency slots indefinitely. Python wraps the
	// per-image call in @timeout(30, 3) (deepdoc/parser/figure_parser.py).
	visionChatTimeout     = 30 * time.Second
	visionMediaItemBudget = 10 * time.Second
)

type visionEnhanceTarget struct {
	idx  int
	kind string
}

// isUsableVisionImage reports whether raw carries a valid image data URI or a
// base64 payload that can be sent to a vision model.
func isUsableVisionImage(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if len(raw) > maxVLMEncodedBytes+256 {
		return false
	}
	if strings.HasPrefix(raw, "data:image/") {
		idx := strings.Index(raw, "base64,")
		if idx < 0 {
			return false
		}
		return isValidBase64(raw[idx+len("base64,"):], false)
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return true
	}
	if len(raw) > maxVLMEncodedBytes {
		return false
	}
	return isValidBase64(raw, true)
}

func isValidBase64(s string, stripWhitespace bool) bool {
	if s == "" || len(s) > maxVLMEncodedBytes {
		return false
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		var source io.Reader = strings.NewReader(s)
		if stripWhitespace {
			source = &visionPayloadReader{source: s}
		}
		decoded := base64.NewDecoder(encoding, source)
		n, err := io.CopyN(io.Discard, decoded, int64(maxVisionImageBytes)+1)
		if n > int64(maxVisionImageBytes) {
			return false
		}
		if err == io.EOF {
			return n > 0
		}
	}
	return false
}

// visionImageCropper materializes the VLM payload for a parsed item. Under
// cgo it crops PDF sections on demand; inline images stay encoded. Close
// releases any re-acquired native engine.
type visionImageCropper interface {
	Crop(ctx context.Context, item map[string]any) (*visionImage, error)
	Close() error
}

type visionImage struct {
	Raster           image.Image
	VLMData          string
	VLMDataValidated bool
}

// maybeDispatchVisionEnhancement appends VLM descriptions to parsed image
// resources and table regions, and transcribes DeepDOC signature-label bands.
// Table crops use the table-seal prompt; approval/signature labels use a
// rightward-extended crop plus the signature prompt (replace label-only OCR).
// PDF OCR remains in the PDF parser; non-PDF image text comes from this
// optional enhancement.
// Mirrors Python enhance_media_sections_with_vision plus
// enhance_signature_sections_with_vision / _enhance_pdf_tables_with_vision
// (deepdoc/parser/figure_parser.py).
func maybeDispatchVisionEnhancement(
	ctx context.Context,
	db *gorm.DB,
	fileType utility.FileType,
	dispatched parser.ParseResult,
	inputs map[string]any,
	vision visionSettings,
	window fractionWindow,
) (parser.ParseResult, bool, error) {
	w := window

	// Only enhance successful JSON output format containing items.
	if dispatched.Err != nil || dispatched.OutputFormat != "json" || len(dispatched.JSON) == 0 {
		return dispatched, false, nil
	}

	tenantID := getStringOr(inputs, "tenant_id", "")
	family := resolveParserFamily(fileType)
	// Captions follow the knowledge base language, which also drives
	// tokenization. The family setup's lang is an OCR engine parameter
	// (pdf_vision_dispatch.go), not an input here - see issue #20727.
	language := resolveVisionLanguage(inputs)
	tableVisionOn := !vision.skipTableEnhance
	signatureVisionOn := !vision.skipSignatureEnhance

	// Collect visual resources (figures/tables) and DeepDOC signature-label
	// lines. Markdown images whose type is text because flatten_media_to_text
	// is enabled still carry an inline image payload.
	targets := collectVisionEnhanceTargets(dispatched.JSON, tableVisionOn, signatureVisionOn)
	if len(targets) == 0 {
		return dispatched, false, nil
	}
	// Resolve VLM before materializing image payloads, so disabled or
	// unavailable enhancement does not spend time loading media.
	var driver modelModule.ModelDriver
	var modelName string
	var apiConfig *modelModule.APIConfig
	prompts := make(map[string]string)
	vlmReady := false
	var resolveErr error
	if tenantID != "" {
		var err error
		if vision.modelID != "" {
			driver, modelName, apiConfig, _, err = resolveModelConfig(ctx, db, tenantID, entity.ModelTypeImage2Text, vision.modelID)
			if err != nil {
				common.Warn("vision enhancement: per-call VLM resolve failed, falling back to tenant default",
					zap.String("family", family), zap.String("modelRef", vision.modelID), zap.String("tenant", tenantID), zap.Error(err))
				driver, modelName, apiConfig, _, err = resolveTenantModelByType(ctx, db, tenantID, entity.ModelTypeImage2Text)
			}
		} else {
			driver, modelName, apiConfig, _, err = resolveTenantModelByType(ctx, db, tenantID, entity.ModelTypeImage2Text)
		}
		if err == nil && driver != nil {
			prompts, err = buildVisionPromptsForTargets(targets, language)
			vlmReady = err == nil && len(prompts) > 0
		}
		resolveErr = err
	}
	if !vlmReady {
		warning := "vision enhancement skipped: tenant ID is missing"
		if tenantID != "" {
			warning = "vision enhancement skipped: no usable vision model"
			if resolveErr != nil {
				warning = fmt.Sprintf("vision enhancement skipped: %v", resolveErr)
			}
		}
		dispatched.Warnings = append(dispatched.Warnings, warning)
		return dispatched, false, nil
	}

	// Materialize one resource at a time. The VLM semaphore is acquired before
	// materialization, so at most visionEnhancementConcurrency encoded payloads
	// remain live while model requests run.
	cropper, cerr := visionImageCropperFactory(ctx, db, inputs)
	if cerr != nil {
		dispatched.Warnings = append(dispatched.Warnings, fmt.Sprintf("vision enhancement skipped: %v", cerr))
		return dispatched, false, nil
	}
	defer cropper.Close()
	modified := false
	descriptions := make([]string, len(targets))
	failures := make([]string, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, visionEnhancementConcurrency)
	var settled atomic.Int64
	total := int64(len(targets))
	for slot, target := range targets {
		if err := ctx.Err(); err != nil {
			break
		}
		vlmSlot := false
		select {
		case sem <- struct{}{}:
			vlmSlot = true
		case <-ctx.Done():
			// The loop exits below when no slot was acquired.
		}
		if !vlmSlot {
			break
		}
		var releaseSlotOnce sync.Once
		releaseSlot := func() {
			releaseSlotOnce.Do(func() { <-sem })
		}
		var resource *visionImage
		func() {
			itemCtx, cancelItem := context.WithTimeout(ctx, visionMediaItemBudget)
			defer cancelItem()
			release, err := parser.AcquireImageMedia(itemCtx)
			if err != nil {
				if ctx.Err() == nil && vlmReady {
					if payload, _ := dispatched.JSON[target.idx]["image"].(string); payload != "" {
						resource = &visionImage{VLMData: payload}
					}
				}
				if resource == nil && ctx.Err() == nil {
					failures[slot] = fmt.Sprintf("media admission failed: %v", err)
				}
				return
			}
			defer release()
			item := visionCropItem(dispatched.JSON[target.idx], target.kind)
			resource, err = cropper.Crop(itemCtx, item)
			if err != nil || resource == nil {
				cropErr := err
				if ctx.Err() == nil && vlmReady {
					if payload, _ := item["image"].(string); isUsableVisionImage(payload) {
						resource = &visionImage{VLMData: payload, VLMDataValidated: true}
					}
				}
				if resource == nil && ctx.Err() == nil {
					if cropErr != nil {
						failures[slot] = fmt.Sprintf("image materialization failed: %v", cropErr)
					} else {
						failures[slot] = "no usable image payload"
					}
				}
				return
			}
			if ctx.Err() == nil && vlmReady && resource.VLMData == "" && resource.Raster != nil {
				resource.VLMData, err = encodeVisionRaster(resource.Raster)
				if err != nil {
					resource.VLMData = ""
					failures[slot] = fmt.Sprintf("image encoding failed: %v", err)
				} else {
					resource.VLMDataValidated = resource.VLMData != ""
				}
			}
		}()
		if err := ctx.Err(); err != nil {
			releaseSlot()
			break
		}
		if resource == nil {
			releaseSlot()
			n := settled.Add(1)
			w.report(ctx, float64(n)/float64(total))
			continue
		}
		if !resource.VLMDataValidated && !isUsableVisionImage(resource.VLMData) {
			if failures[slot] == "" {
				failures[slot] = "image payload is invalid or exceeds configured limits"
			}
			releaseSlot()
			n := settled.Add(1)
			w.report(ctx, float64(n)/float64(total))
			continue
		}
		prompt := prompts[target.kind]
		wg.Add(1)
		go func(slot int, imageData, prompt string) {
			defer wg.Done()
			defer releaseSlot()
			defer func() {
				if ctx.Err() == nil {
					n := settled.Add(1)
					w.report(ctx, float64(n)/float64(total))
				}
			}()

			messages := buildVisionMessages(prompt, imageData)
			if len(messages) == 0 {
				failures[slot] = "could not build a VLM request"
				return
			}
			resp, ierr := visionChatInvoker(ctx, driver, modelName, messages, apiConfig)
			if ierr != nil {
				failures[slot] = fmt.Sprintf("VLM request failed: %v", ierr)
				return
			}
			descriptions[slot] = extractVisionAnswer(resp)
			if strings.TrimSpace(descriptions[slot]) == "" {
				failures[slot] = "VLM returned an empty description"
			}
		}(slot, resource.VLMData, prompt)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return dispatched, modified, err
	}
	w.report(ctx, 1.0)
	failedItems := 0
	firstFailure := ""
	for _, failure := range failures {
		if failure == "" {
			continue
		}
		failedItems++
		if firstFailure == "" {
			firstFailure = failure
		}
	}
	if failedItems > 0 {
		warning := fmt.Sprintf("vision enhancement failed for %d item(s): %s", failedItems, firstFailure)
		dispatched.Warnings = append(dispatched.Warnings, warning)
		common.Warn("vision enhancement: one or more image descriptions failed",
			zap.Int("failed_items", failedItems), zap.String("first_failure", firstFailure))
	}

	// Append (or replace, for label-only signature lines) VLM transcriptions.
	for slot, target := range targets {
		desc := strings.TrimSpace(descriptions[slot])
		if desc == "" {
			continue
		}
		item := dispatched.JSON[target.idx]
		if target.kind == visionKindSignature {
			existing, _ := item["text"].(string)
			item["text"] = mergeSignatureVisionText(existing, desc)
		} else {
			appendItemText(item, desc)
		}
		modified = true
	}

	return dispatched, modified, nil
}

func collectVisionEnhanceTargets(items []map[string]any, tableVisionOn, signatureVisionOn bool) []visionEnhanceTarget {
	seen := make(map[int]struct{}, len(items))
	var targets []visionEnhanceTarget
	add := func(idx int, kind string) {
		if _, ok := seen[idx]; ok {
			return
		}
		seen[idx] = struct{}{}
		targets = append(targets, visionEnhanceTarget{idx: idx, kind: kind})
	}
	for i, item := range items {
		kd, _ := item["doc_type_kwd"].(string)
		if img, _ := item["image"].(string); img != "" {
			kind := visionKindFigure
			if kd == "table" {
				if !tableVisionOn {
					continue
				}
				kind = visionKindTable
			}
			add(i, kind)
			continue
		}
		switch kd {
		case "image":
			if _, ok := parser.ExtractPDFPositions(item); ok {
				add(i, visionKindFigure)
			}
		case "table":
			if !tableVisionOn {
				continue
			}
			if _, ok := parser.ExtractPDFPositions(item); ok {
				add(i, visionKindTable)
			}
		}
	}
	if !signatureVisionOn {
		return targets
	}
	for i, item := range items {
		if _, ok := seen[i]; ok {
			continue
		}
		text, _ := item["text"].(string)
		if !isSignatureLabelText(text) || alreadyHasSignatureName(text) {
			continue
		}
		if _, ok := parser.ExtractPDFPositions(item); !ok {
			continue
		}
		add(i, visionKindSignature)
	}
	return targets
}

func visionCropItem(item map[string]any, kind string) map[string]any {
	if kind != visionKindSignature {
		return item
	}
	cloned := make(map[string]any, len(item)+1)
	for k, v := range item {
		cloned[k] = v
	}
	cloned[visionSignatureBandKey] = true
	return cloned
}

func buildVisionPromptsForTargets(targets []visionEnhanceTarget, language string) (map[string]string, error) {
	needed := make(map[string]struct{}, 3)
	for _, target := range targets {
		needed[target.kind] = struct{}{}
	}
	out := make(map[string]string, len(needed))
	for kind := range needed {
		prompt, err := visionPromptForKind(kind, language)
		if err != nil {
			return nil, err
		}
		out[kind] = prompt
	}
	return out, nil
}

func visionPromptForKind(kind, language string) (string, error) {
	switch kind {
	case visionKindTable:
		return tableVisionPromptBuilder(language)
	case visionKindSignature:
		return signatureVisionPromptBuilder(language)
	default:
		return figureVisionPromptBuilder(language)
	}
}

func appendItemText(item map[string]any, text string) {
	existing, _ := item["text"].(string)
	if existing == "" {
		item["text"] = text
		return
	}
	item["text"] = existing + "\n" + text
}

func buildFigureVisionPrompt(language string) (string, error) {
	return buildVisionPromptTemplate("vision_llm_figure_describe_prompt", language)
}

func buildTableVisionPrompt(language string) (string, error) {
	return buildVisionPromptTemplate("vision_llm_table_describe_prompt", language)
}

func buildSignatureVisionPrompt(language string) (string, error) {
	return buildVisionPromptTemplate("vision_llm_signature_describe_prompt", language)
}

func buildVisionPromptTemplate(name, language string) (string, error) {
	template, err := (prompts.EmbeddedPromptLoader{}).Load(name)
	if err != nil {
		return "", err
	}
	return renderFigureVisionLanguage(template, language), nil
}

func buildVisionMessages(prompt, imageBase64 string) []modelModule.Message {
	dataURI := pdflayout.InlinePNGDataURL(imageBase64)
	if dataURI == "" {
		return nil
	}
	return []modelModule.Message{{
		Role: "user",
		Content: []interface{}{
			map[string]any{"type": "text", "text": prompt},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURI}},
		},
	}}
}

// extractVisionAnswer strips ```markdown fences from the VLM response.
// Python calls clean_markdown_block inside vision_llm_chunk (rag/app/picture.py:198,
// i.e., inside the invoker wrapper). Go applies it here post-invoker so that
// visionChatInvoker stays a pure, swappable test seam — output is equivalent.
func extractVisionAnswer(resp *modelModule.ChatResponse) string {
	if resp == nil || resp.Answer == nil {
		return ""
	}
	return common.CleanMarkdownBlock(*resp.Answer)
}

func defaultVisionChatInvoker(
	ctx context.Context,
	driver modelModule.ModelDriver,
	modelName string,
	messages []modelModule.Message,
	apiConfig *modelModule.APIConfig,
) (*modelModule.ChatResponse, error) {
	chatCtx, cancel := context.WithTimeout(ctx, visionChatTimeout)
	defer cancel()
	vision := true
	config := &modelModule.ChatConfig{Vision: &vision}
	if _, ok := driver.(*modelModule.OllamaModel); ok {
		thinking := false
		config.Thinking = &thinking
	}
	chatModel := modelModule.NewChatModel(driver, &modelName, apiConfig)
	return chatModel.ChatWithMessages(chatCtx, messages, config, nil)
}
