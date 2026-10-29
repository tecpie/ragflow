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
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"ragflow/internal/common"
	pdflayout "ragflow/internal/deepdoc/parser/pdf/layout"
	"ragflow/internal/entity"
	modelModule "ragflow/internal/entity/models"
	"ragflow/internal/ingestion/component/progress"
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

// backendDescribedKey marks items whose text already carries a backend-provided
// vision description (MinerU folds vlm_description into text). The vision
// enhancement collector skips such items to avoid appending a second model
// description and spending an extra VLM call. The key is stripped before the
// result leaves the Parser component so it never reaches the chunker or index.
const backendDescribedKey = "_backend_described"

func isBackendDescribed(m map[string]any) bool {
	v, _ := m[backendDescribedKey].(bool)
	return v
}

// stripBackendDescribedResult removes the transient backend-description marker
// from every item. Enhancement skips marked items, but the marker must not leak
// to the chunker when enhancement is disabled or bails out early.
func stripBackendDescribedResult(result parser.ParseResult) parser.ParseResult {
	if len(result.JSON) == 0 {
		return result
	}
	needsStrip := false
	for _, item := range result.JSON {
		if item == nil {
			continue
		}
		if _, ok := item.ToMap()[backendDescribedKey]; ok {
			needsStrip = true
			break
		}
	}
	if !needsStrip {
		return result
	}
	stripped := make([]parser.DocItem, 0, len(result.JSON))
	for _, item := range result.JSON {
		if item == nil {
			continue
		}
		m := item.ToMap()
		if _, ok := m[backendDescribedKey]; !ok {
			stripped = append(stripped, item)
			continue
		}
		cloned := maps.Clone(m)
		delete(cloned, backendDescribedKey)
		stripped = append(stripped, parser.DocItemFromMap(cloned))
	}
	result.JSON = stripped
	return result
}

type visionEnhanceTarget struct {
	idx  int
	kind string
}

// isUsableVisionImage reports whether raw carries a valid image data URI or a
// base64 payload that can be sent to a vision model, and rejects oversized
// encodings beyond the limits shared by Parser and VLM materialization.
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

// visionImageCropper prepares any source document before image admission,
// materializes one item's VLM payload under image admission, and releases
// source resources when the batch finishes.
type visionImageCropper interface {
	Prepare(ctx context.Context, item map[string]any) error
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
// optional enhancement. Media materialization failures abort the enhancement.
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
	window progress.Window,
) (parser.ParseResult, bool, error) {
	w := window

	// Only enhance successful results that carry JSON items. Produced()
	// replaces the historical OutputFormat != "json" check.
	if dispatched.Err != nil || len(dispatched.JSON) == 0 {
		return stripBackendDescribedResult(dispatched), false, nil
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
	// is enabled still carry an inline image payload. Backend-described items
	// are skipped inside collectVisionEnhanceTargets.
	targets := collectVisionEnhanceTargets(dispatched.JSON, tableVisionOn, signatureVisionOn)
	if len(targets) == 0 {
		return stripBackendDescribedResult(dispatched), false, nil
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
		return stripBackendDescribedResult(dispatched), false, nil
	}

	// Prepare PDF sources outside image admission so slow storage reads do not
	// block unrelated image materialization. The separate VLM semaphore bounds
	// how many encoded payloads remain live while model requests run.
	cropper, cerr := visionImageCropperFactory(ctx, db, inputs)
	if cerr != nil {
		dispatched.Warnings = append(dispatched.Warnings, fmt.Sprintf("vision enhancement skipped: %v", cerr))
		return stripBackendDescribedResult(dispatched), false, nil
	}
	// Close is invoked explicitly after materialization so the engine and the
	// source bytes are released while model requests are still in flight. Keep
	// a deferred Close as a panic/early-return safety net: Close is idempotent
	// (engine is nilled, sourceErr cleared), so the second call is a no-op.
	defer cropper.Close()
	modified := false
	descriptions := make([]string, len(targets))
	failures := make([]string, len(targets))
	mediaFailures := make([]error, len(targets))
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
			// Clone before materializing: RawItem.ToMap returns the shared
			// map, and filling in image bytes must not mutate the parse result.
			item := visionCropItem(maps.Clone(dispatched.JSON[target.idx].ToMap()), target.kind)
			if err := cropper.Prepare(ctx, item); err != nil {
				if ctx.Err() == nil {
					if payload, _ := item["image"].(string); isUsableVisionImage(payload) {
						resource = &visionImage{VLMData: payload, VLMDataValidated: true}
					}
				}
				if resource == nil && ctx.Err() == nil {
					failures[slot] = fmt.Sprintf("image source preparation failed: %v", err)
				}
				return
			}

			itemCtx, cancelItem := context.WithTimeout(ctx, visionMediaItemBudget)
			defer cancelItem()
			release, err := parser.AcquireImageMedia(itemCtx)
			if err != nil {
				if ctx.Err() == nil && vlmReady {
					if payload, _ := item["image"].(string); isUsableVisionImage(payload) {
						resource = &visionImage{VLMData: payload, VLMDataValidated: true}
					}
				}
				if resource == nil && ctx.Err() == nil {
					failures[slot] = fmt.Sprintf("media admission failed: %v", err)
				}
				return
			}
			defer release()
			// Materialize under the same admission as the crop: the decode
			// and re-encode of a deferred image must not bypass the shared
			// image memory limit.
			if err := MaterializeMediaItem(itemCtx, item); err != nil {
				mediaFailures[slot] = err
				return
			}
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
			w.Report(ctx, float64(n)/float64(total))
			continue
		}
		if !resource.VLMDataValidated && !isUsableVisionImage(resource.VLMData) {
			if failures[slot] == "" {
				failures[slot] = "image payload is invalid or exceeds configured limits"
			}
			releaseSlot()
			n := settled.Add(1)
			w.Report(ctx, float64(n)/float64(total))
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
					w.Report(ctx, float64(n)/float64(total))
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
	if err := cropper.Close(); err != nil {
		dispatched.Warnings = append(dispatched.Warnings, fmt.Sprintf("vision enhancement cleanup failed: %v", err))
		common.Warn("vision enhancement: failed to close image cropper", zap.Error(err))
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return stripBackendDescribedResult(dispatched), modified, err
	}
	for _, err := range mediaFailures {
		if err != nil {
			return stripBackendDescribedResult(dispatched), modified, fmt.Errorf("vision enhancement: materialize media: %w", err)
		}
	}
	w.Report(ctx, 1.0)
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

	// Candidate indices are in document order. Build output once so a table's
	// description can follow it without changing later candidates' indices.
	// Signature lines replace label-only OCR; table structs get a note item;
	// the transient backend-description marker is stripped from every emitted item.
	targetSlot := make(map[int]int, len(targets))
	for slot, target := range targets {
		targetSlot[target.idx] = slot
	}
	enhanced := make([]parser.DocItem, 0, len(dispatched.JSON)+len(targets))
	for itemIdx, item := range dispatched.JSON {
		clean := item
		if m := item.ToMap(); isBackendDescribed(m) {
			cloned := maps.Clone(m)
			delete(cloned, backendDescribedKey)
			clean = parser.DocItemFromMap(cloned)
		}
		slot, ok := targetSlot[itemIdx]
		if !ok {
			enhanced = append(enhanced, clean)
			continue
		}
		desc := strings.TrimSpace(descriptions[slot])
		if desc == "" {
			enhanced = append(enhanced, clean)
			continue
		}
		m := maps.Clone(clean.ToMap())
		target := targets[slot]
		if target.kind == visionKindSignature {
			existing, _ := m["text"].(string)
			m["text"] = mergeSignatureVisionText(existing, desc)
			enhanced = append(enhanced, parser.DocItemFromMap(m))
		} else if table, ok := m["table"].(*entity.TableData); ok && table != nil && m["doc_type_kwd"] == "table" {
			enhanced = append(enhanced, clean, parser.NewTableNoteItem(m, desc))
		} else {
			appendItemText(m, desc)
			enhanced = append(enhanced, parser.DocItemFromMap(m))
		}
		modified = true
	}
	dispatched.JSON = enhanced

	return dispatched, modified, nil
}

func collectVisionEnhanceTargets(items []parser.DocItem, tableVisionOn, signatureVisionOn bool) []visionEnhanceTarget {
	seen := make(map[int]struct{}, len(items))
	var targets []visionEnhanceTarget
	add := func(idx int, kind string) {
		if _, ok := seen[idx]; ok {
			return
		}
		seen[idx] = struct{}{}
		targets = append(targets, visionEnhanceTarget{idx: idx, kind: kind})
	}
	for i, raw := range items {
		item := raw.ToMap()
		if isBackendDescribed(item) {
			continue
		}
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
			if parser.HasPDFCropRegion(item) {
				add(i, visionKindFigure)
			}
		case "table":
			if !tableVisionOn {
				continue
			}
			if parser.HasPDFCropRegion(item) {
				add(i, visionKindTable)
			}
		}
	}
	if !signatureVisionOn {
		return targets
	}
	for i, raw := range items {
		if _, ok := seen[i]; ok {
			continue
		}
		item := raw.ToMap()
		if isBackendDescribed(item) {
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
