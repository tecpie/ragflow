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

// Package component — Browser (T3).
//
// Browser is an LLM-driven single-shot web extraction canvas node
// built on `github.com/browserbase/stagehand-go/v3` in local mode.
// It uses `RunExtract` (not the multi-step agent `RunTask`) to
// navigate to a page and extract structured content against a
// `{"type": "string"}` JSON schema.
//
// It mirrors the Python `agent/component/browser.py` param surface
// (`llm_id`, `prompts`, `max_steps`, `headless`, `persist_session`,
// `upload_sources`, etc.) so the v1 fixture
// (`internal/agent/dsl/testdata/browser.json`) loads without
// fixture-side changes.
//
// LLM dispatch is delegated to `StagehandInvoker` (see
// `stagehand_runtime.go`), which owns the stagehand-server child
// process and the session lifecycle. The component itself is a thin
// orchestrator: parse → resolve template → look up tenant model
// config → call runtime.RunExtract → emit Python-shaped outputs.
//
// Upload sources are prepared locally, then optionally staged to a remote
// Chrome host via HTTP gateway (tools/browser_remote_staging) or CDP blob
// download before the stagehand agent runs with those remote paths.
package component

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"ragflow/internal/agent/runtime"
	"ragflow/internal/dao"
	"ragflow/internal/entity"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const componentNameBrowser = "Browser"

var stagehandNativeProviders = map[string]string{
	"anthropic": "anthropic",
	"google":    "google",
	"openai":    "openai",
}

var browserFactoryDefaultBaseURL = map[string]string{
	"302.ai":         "https://api.302.ai/v1",
	"anthropic":      "https://api.anthropic.com/",
	"astraflow":      "https://api-us-ca.umodelverse.ai/v1",
	"astraflow-cn":   "https://api.modelverse.cn/v1",
	"avian":          "https://api.avian.io/v1",
	"cometapi":       "https://api.cometapi.com/v1",
	"dashscope":      "https://dashscope.aliyuncs.com/compatible-mode/v1",
	"deepseek":       "https://api.deepseek.com/v1",
	"deerapi":        "https://api.deerapi.com/v1",
	"futurmix":       "https://futurmix.ai/v1",
	"giteeai":        "https://ai.gitee.com/v1/",
	"hunyuan":        "https://api.hunyuan.cloud.tencent.com/v1",
	"jiekouai":       "https://api.jiekou.ai/openai",
	"lingyi-ai":      "https://api.lingyiwanwu.com/v1",
	"longcat":        "https://api.longcat.chat/openai",
	"minimax":        "https://api.minimaxi.com/v1",
	"moonshot":       "https://api.moonshot.cn/v1",
	"n1n":            "https://api.n1n.ai/v1",
	"novitaai":       "https://api.novita.ai/v3/openai",
	"openai":         "https://api.openai.com/v1",
	"openrouter":     "https://openrouter.ai/api/v1",
	"perfxcloud":     "https://cloud.perfxlab.cn/v1",
	"ppio":           "https://api.ppinfra.com/v3/openai",
	"siliconflow":    "https://api.siliconflow.cn/v1",
	"stepfun":        "https://api.stepfun.com/v1",
	"tongyi-qianwen": "https://dashscope.aliyuncs.com/compatible-mode/v1",
	"upstage":        "https://api.upstage.ai/v1/solar",
	"zhipu-ai":       "https://open.bigmodel.cn/api/paas/v4",
}

// browserParam is the static DSL param surface for the Browser
// component. Mirrors Python `browser.py:LLMParam + browser knobs`.
type browserParam struct {
	LLMID            string `json:"llm_id"`
	ModelID          string `json:"model_id"` // alias for llm_id
	Prompts          string `json:"prompts"`
	Prompt           string `json:"prompt"` // alias for prompts
	MaxSteps         int    `json:"max_steps"`
	Headless         *bool  `json:"headless"`
	EnableDefaultExt *bool  `json:"enable_default_extensions"`
	ChromiumSandbox  *bool  `json:"chromium_sandbox"`
	PersistSession   *bool  `json:"persist_session"`
	UseCDP           *bool  `json:"use_cdp"`
	CDPURL           string `json:"cdp_url"`
	UseVision        *bool  `json:"use_vision"`
	UploadSources    any    `json:"upload_sources"`
	RemoteStagingURL string `json:"remote_staging_url"`
	RemoteStagingTok string `json:"remote_staging_token"`
	RemoteUploadMode string `json:"remote_upload_mode"`

	// Go-only fields (kept for backward compat with the existing
	// test file; optional URL form some operators still wire up).
	URL     string `json:"url"`
	Timeout int    `json:"timeout"`
}

// Update copies a fresh param map into the receiver. The
// `llm_id`/`model_id` and `prompts`/`prompt` alias pairs collapse
// onto the same field; the first non-empty value wins.
func (p *browserParam) Update(conf map[string]any) error {
	if conf == nil {
		conf = map[string]any{}
	}
	if v, ok := stringFrom(conf, "llm_id"); ok && v != "" {
		p.LLMID = v
	}
	if v, ok := stringFrom(conf, "model_id"); ok && v != "" && p.LLMID == "" {
		p.LLMID = v
	}
	if v, ok := stringFrom(conf, "prompts"); ok && v != "" {
		p.Prompts = v
	}
	if v, ok := stringFrom(conf, "prompt"); ok && v != "" && p.Prompts == "" {
		p.Prompts = v
	}
	if v, ok := intFrom(conf, "max_steps"); ok {
		p.MaxSteps = v
	} else {
		p.MaxSteps = 0
	}
	if v, ok := boolFrom(conf, "headless"); ok {
		p.Headless = &v
	}
	if v, ok := boolFrom(conf, "enable_default_extensions"); ok {
		p.EnableDefaultExt = &v
	}
	if v, ok := boolFrom(conf, "chromium_sandbox"); ok {
		p.ChromiumSandbox = &v
	}
	if v, ok := boolFrom(conf, "persist_session"); ok {
		p.PersistSession = &v
	}
	if v, ok := boolFrom(conf, "use_cdp"); ok {
		p.UseCDP = &v
	}
	if v, ok := stringFrom(conf, "cdp_url"); ok {
		p.CDPURL = v
	}
	if v, ok := boolFrom(conf, "use_vision"); ok {
		p.UseVision = &v
	}
	if raw, ok := conf["upload_sources"]; ok {
		p.UploadSources = raw
	}
	if v, ok := stringFrom(conf, "remote_staging_url"); ok {
		p.RemoteStagingURL = v
	}
	if v, ok := stringFrom(conf, "remote_staging_token"); ok {
		p.RemoteStagingTok = v
	}
	if v, ok := stringFrom(conf, "remote_upload_mode"); ok {
		p.RemoteUploadMode = v
	}
	if v, ok := stringFrom(conf, "url"); ok {
		p.URL = v
	}
	if v, ok := intFrom(conf, "timeout"); ok {
		p.Timeout = v
	} else {
		p.Timeout = 0
	}
	return nil
}

// Check validates the param. The accepted-but-ignored Python
// fields are NOT validated here — the v1 fixture is allowed to set
// them; we only reject structurally invalid values for fields we
// actually use (`llm_id`, `prompts`).
func (p *browserParam) Check() error {
	if p.LLMID == "" {
		return &ParamError{Field: "llm_id", Reason: "required (or model_id alias)"}
	}
	if p.Prompts == "" {
		return &ParamError{Field: "prompts", Reason: "required (or prompt alias)"}
	}
	if p.MaxSteps < 0 {
		return &ParamError{Field: "max_steps", Reason: "must be non-negative"}
	}
	if p.Timeout < 0 {
		return &ParamError{Field: "timeout", Reason: "must be non-negative"}
	}
	if p.UseCDP != nil && *p.UseCDP && strings.TrimSpace(p.CDPURL) == "" {
		return &ParamError{Field: "cdp_url", Reason: "required when use_cdp is true"}
	}
	return nil
}

// AsDict returns the param as a plain map (for serialization / debug).
func (p *browserParam) AsDict() map[string]any {
	out := map[string]any{
		"llm_id":    p.LLMID,
		"model_id":  p.LLMID, // alias echoed
		"prompts":   p.Prompts,
		"prompt":    p.Prompts, // alias echoed
		"max_steps": p.MaxSteps,
		"url":       p.URL,
		"timeout":   p.Timeout,
	}
	if p.Headless != nil {
		out["headless"] = *p.Headless
	}
	if p.EnableDefaultExt != nil {
		out["enable_default_extensions"] = *p.EnableDefaultExt
	}
	if p.ChromiumSandbox != nil {
		out["chromium_sandbox"] = *p.ChromiumSandbox
	}
	if p.PersistSession != nil {
		out["persist_session"] = *p.PersistSession
	}
	if p.UseCDP != nil {
		out["use_cdp"] = *p.UseCDP
	}
	out["cdp_url"] = p.CDPURL
	if p.UseVision != nil {
		out["use_vision"] = *p.UseVision
	}
	if p.UploadSources != nil {
		out["upload_sources"] = p.UploadSources
	}
	out["remote_staging_url"] = p.RemoteStagingURL
	out["remote_staging_token"] = p.RemoteStagingTok
	out["remote_upload_mode"] = p.RemoteUploadMode
	return out
}

// BrowserComponent is the canvas Browser node. Owns its static
// param; delegates the multistep agent run to StagehandInvoker.
type BrowserComponent struct {
	name  string
	param browserParam
}

// NewBrowserComponent constructs a Browser from the DSL param map.
func NewBrowserComponent(params map[string]any) (Component, error) {
	p := &browserParam{}
	if err := p.Update(params); err != nil {
		return nil, fmt.Errorf("browser: param update: %w", err)
	}
	if err := p.Check(); err != nil {
		return nil, fmt.Errorf("browser: param check: %w", err)
	}
	return &BrowserComponent{
		name:  componentNameBrowser,
		param: *p,
	}, nil
}

// Name returns the registered component name.
func (b *BrowserComponent) Name() string { return b.name }

// Invoke prepares upload_sources (optional remote staging), then runs
// stagehand: RunTask when CDP/uploads/vision/agent steps are needed,
// otherwise RunExtract for single-shot extraction.
func (b *BrowserComponent) Invoke(ctx context.Context, db *gorm.DB, inputs map[string]any) (map[string]any, error) {
	state, err := runtime.GetStateFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("browser: %w", err)
	}
	if state == nil {
		return nil, errors.New("browser: nil canvas state")
	}

	tenantID, _ := state.Sys["tenant_id"].(string)
	if tenantID == "" {
		return nil, errors.New("browser: tenant_id missing from canvas state (state.Sys[\"tenant_id\"])")
	}

	prompts := b.param.Prompts
	if v, ok := inputs["prompts"].(string); ok && strings.TrimSpace(v) != "" {
		prompts = v
	}
	resolvedPrompts, err := runtime.ResolveTemplate(prompts, state)
	if err != nil {
		return nil, fmt.Errorf("browser: resolve prompts template: %w", err)
	}

	providerName, modelName, apiKey, baseURL, err := resolveBrowserLLM(ctx, db, tenantID, b.param.LLMID)
	if err != nil {
		return nil, fmt.Errorf("browser: tenant llm lookup (%q): %w", b.param.LLMID, err)
	}
	baseURL = strings.TrimSpace(baseURL)

	cdpURL := strings.TrimSpace(b.param.CDPURL)
	if v, ok := stringFrom(inputs, "cdp_url"); ok && strings.TrimSpace(v) != "" {
		cdpURL = strings.TrimSpace(v)
	}
	useCDP := b.param.UseCDP != nil && *b.param.UseCDP && cdpURL != ""
	useVision := b.param.UseVision != nil && *b.param.UseVision

	uploadSources := b.param.UploadSources
	if raw, ok := inputs["upload_sources"]; ok && raw != nil {
		uploadSources = raw
	}

	uploadDir, err := os.MkdirTemp("", "ragflow-browser-upload-*")
	if err != nil {
		return nil, fmt.Errorf("browser: create upload dir: %w", err)
	}
	defer os.RemoveAll(uploadDir)

	prepared, err := prepareUploadFiles(ctx, db, state, uploadSources, uploadDir)
	if err != nil {
		return nil, fmt.Errorf("browser: prepare upload files: %w", err)
	}
	remoteHost := useCDP && len(prepared) > 0
	if remoteHost {
		prepared, err = stageUploadFilesOnRemoteHost(
			ctx, prepared, cdpURL, b.param.RemoteStagingURL, b.param.RemoteStagingTok, b.param.RemoteUploadMode,
		)
		if err != nil {
			return nil, fmt.Errorf("browser: remote staging: %w", err)
		}
	}

	instruction := resolvedPrompts + buildUploadTaskAppendix(prepared, remoteHost)
	instruction += buildUploadSystemExtension(prepared, remoteHost)
	if useVision {
		instruction += "\n\n[RAGFlow Browser vision]\n- use_vision is enabled: inspect screenshots when text/DOM is insufficient.\n"
	}

	invoker := getDefaultStagehandInvoker()
	needAgent := useCDP || useVision || len(prepared) > 0 || b.param.MaxSteps > 0 ||
		(b.param.PersistSession != nil && *b.param.PersistSession)

	var content string
	if needAgent {
		taskReq := RunTaskRequest{
			TenantID:    tenantID,
			LLMID:       b.param.LLMID,
			ModelName:   stagehandModelName(providerName, modelName),
			BaseURL:     baseURL,
			APIKey:      apiKey,
			Instruction: instruction,
			MaxSteps:    b.param.MaxSteps,
			Headless:    b.param.Headless,
			CDPURL:      "",
			UseVision:   useVision,
		}
		if useCDP {
			taskReq.CDPURL = cdpURL
		}
		if b.param.PersistSession != nil && *b.param.PersistSession {
			profileDir := filepath.Join(os.TempDir(), "ragflow-browser-profiles", tenantID, sanitizeProfileSegment(b.param.LLMID))
			_ = os.MkdirAll(profileDir, 0o755)
			taskReq.UserDataDir = profileDir
			taskReq.PreserveUserDataDir = true
		}
		content, err = invoker.RunTask(ctx, taskReq)
		if err != nil {
			return nil, fmt.Errorf("browser: stagehand task (model=%q, base_url=%s): %w",
				taskReq.ModelName, browserBaseURLForLog(taskReq.BaseURL), err)
		}
	} else {
		req := RunExtractRequest{
			TenantID:    tenantID,
			LLMID:       b.param.LLMID,
			ModelName:   stagehandModelName(providerName, modelName),
			BaseURL:     baseURL,
			APIKey:      apiKey,
			Headless:    b.param.Headless,
			Instruction: instruction,
			Schema:      map[string]any{"type": "string"},
			URL:         b.param.URL,
			CDPURL:      "",
		}
		rawJSON, xerr := invoker.RunExtract(ctx, req)
		if xerr != nil {
			return nil, fmt.Errorf("browser: stagehand extract (model=%q, base_url=%s): %w",
				req.ModelName, browserBaseURLForLog(req.BaseURL), xerr)
		}
		if err = json.Unmarshal([]byte(rawJSON), &content); err != nil {
			return nil, fmt.Errorf("browser: unmarshal extract result: %w", err)
		}
	}

	return map[string]any{
		"content":          content,
		"downloaded_files": []map[string]any{},
		"url":              "",
		"status":           0,
		"size":             len(content),
		"model_id":         b.param.LLMID,
		"prompt":           prompts,
	}, nil
}

func sanitizeProfileSegment(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return uuid.NewString()[:8]
	}
	replacer := strings.NewReplacer(`/`, `_`, `\`, `_`, `:`, `_`, `@`, `_`, ` `, `_`)
	return replacer.Replace(raw)
}

func browserBaseURLForLog(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "<provider default>"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid>"
	}
	u.User = nil
	return u.String()
}

func stagehandModelName(providerName, modelName string) string {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return ""
	}
	if strings.Contains(modelName, "/") {
		prefix, _, _ := strings.Cut(modelName, "/")
		if stagehandNativeProviders[strings.ToLower(strings.TrimSpace(prefix))] != "" {
			return modelName
		}
		return "openai/" + modelName
	}
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	if nativeProvider := stagehandNativeProviders[providerName]; nativeProvider != "" {
		return nativeProvider + "/" + modelName
	}
	return "openai/" + modelName
}

// Stream mirrors Invoke; Browser is a single-shot generator.
func (b *BrowserComponent) Stream(ctx context.Context, db *gorm.DB, inputs map[string]any) (<-chan map[string]any, error) {
	out, err := b.Invoke(ctx, db, inputs)
	if err != nil {
		return nil, err
	}
	ch := make(chan map[string]any, 1)
	ch <- out
	close(ch)
	return ch, nil
}

// Inputs returns the parameter metadata for tooling.
func (b *BrowserComponent) Inputs() map[string]string {
	return map[string]string{
		"llm_id":                    "Required: tenant model id, e.g. \"deepseek-v4-pro@DeepSeek\". model_id accepted as alias.",
		"prompts":                   "Required: natural-language extraction task. {sys.query} and other canvas refs are resolved. prompt accepted as alias.",
		"max_steps":                 "Accepted for fixture compat; ignored at Invoke.",
		"headless":                  "Browser launch mode (default true).",
		"enable_default_extensions": "Accepted for fixture compat; ignored at Invoke.",
		"chromium_sandbox":          "Accepted for fixture compat; ignored at Invoke.",
		"persist_session":           "Reuse Chromium user-data-dir across runs when true.",
		"use_cdp":                   "Connect to an existing Chrome via CDP instead of launching local.",
		"cdp_url":                   "CDP HTTP/WS endpoint (required when use_cdp=true).",
		"use_vision":                "Ask the agent to use screenshots when DOM text is insufficient.",
		"upload_sources":            "File ids / session blobs / URLs to stage for upload_file.",
		"remote_staging_url":        "HTTP staging gateway base URL (tools/browser_remote_staging).",
		"remote_staging_token":      "Optional bearer token for the staging gateway.",
		"remote_upload_mode":        "auto | staging | blob_cdp.",
		"url":                       "Optional navigate URL for RunExtract path.",
		"timeout":                   "Go-only; reserved.",
	}
}

func (b *BrowserComponent) GetInputForm() map[string]any {
	return map[string]any{
		"prompts": map[string]any{
			"type": "paragraph",
			"name": "Prompts",
		},
		"upload_sources": map[string]any{
			"type": "line",
			"name": "Upload sources",
		},
		"remote_staging_url": map[string]any{
			"type": "line",
			"name": "Remote staging URL",
		},
		"remote_staging_token": map[string]any{
			"type": "line",
			"name": "Remote staging token",
		},
	}
}

// Outputs returns the response surface.
func (b *BrowserComponent) Outputs() map[string]string {
	return map[string]string{
		"content":          "Extracted plain text (Sessions.Extract result with schema {\"type\":\"string\"}).",
		"downloaded_files": "Downloaded file descriptors (currently [] until download harvest is wired).",
		"url":              "Go-native compat key; always \"\".",
		"status":           "Go-native compat key; always 0.",
		"size":             "Bytes in content.",
		"model_id":         "Resolved llm_id (echoed back).",
		"prompt":           "Resolved prompts (echoed back).",
	}
}

// resolveBrowserLLM resolves tenant model credentials exclusively through the
// model_provider series tables (tenant_model → tenant_model_provider →
// tenant_model_instance). It no longer falls back to the legacy tenant_llm path.
//
// Tests override the lookup via `browserLLMLookupForTest` (a package-level
// function variable) so they don't need a real DB. Production code leaves the
// variable unset.
func resolveBrowserLLM(ctx context.Context, db *gorm.DB, tenantID, llmID string) (providerName, modelName, apiKey, baseURL string, err error) {
	if browserLLMLookupForTest != nil {
		return browserLLMLookupForTest(ctx, db, tenantID, llmID)
	}

	providerName, modelName, apiKey, baseURL, err = resolveTenantModelBrowserLLM(ctx, db, tenantID, llmID)
	if err != nil {
		return "", "", "", "", fmt.Errorf("tenant model lookup (%q): %w", llmID, err)
	}
	baseURL = browserOpenAICompatibleBaseURL(baseURL, providerName)
	return providerName, modelName, apiKey, baseURL, nil
}

func resolveTenantModelBrowserLLM(ctx context.Context, db *gorm.DB, tenantID, modelID string) (providerName, modelName, apiKey, baseURL string, err error) {
	modelRow, err := dao.NewTenantModelDAO().GetByID(ctx, db, modelID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", "", "", err
		}
		return "", "", "", "", fmt.Errorf("tenant model id=%s lookup failed: %w", modelID, err)
	}
	if modelRow.Status != "active" {
		return "", "", "", "", fmt.Errorf("tenant model id=%s is disabled", modelID)
	}
	if !entity.ModelType(modelRow.ModelType).Has(entity.ModelTypeChat) {
		return "", "", "", "", fmt.Errorf("tenant model id=%s cannot be used as %s model", modelID, entity.ModelTypeChat.String())
	}

	provider, err := dao.NewTenantModelProviderDAO().GetByID(ctx, db, modelRow.ProviderID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", "", "", fmt.Errorf("provider id=%s not found for model id=%s", modelRow.ProviderID, modelID)
		}
		return "", "", "", "", err
	}
	if provider == nil {
		return "", "", "", "", fmt.Errorf("provider id=%s not found for model id=%s", modelRow.ProviderID, modelID)
	}
	if provider.TenantID != tenantID {
		return "", "", "", "", fmt.Errorf("tenant %s has no access to provider owned by tenant %s", tenantID, provider.TenantID)
	}

	instance, err := dao.NewTenantModelInstanceDAO().GetByID(ctx, db, modelRow.InstanceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", "", "", fmt.Errorf("instance id=%s not found for model id=%s", modelRow.InstanceID, modelID)
		}
		return "", "", "", "", err
	}
	if instance == nil {
		return "", "", "", "", fmt.Errorf("instance id=%s not found for model id=%s", modelRow.InstanceID, modelID)
	}

	apiKey = instance.APIKey
	if strings.TrimSpace(instance.Extra) != "" {
		var extra map[string]string
		if err = json.Unmarshal([]byte(instance.Extra), &extra); err != nil {
			return "", "", "", "", err
		}
		baseURL = extra["base_url"]
	}
	return provider.ProviderName, modelRow.ModelName, apiKey, baseURL, nil
}

func browserOpenAICompatibleBaseURL(baseURL, provider string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL != "" {
		return baseURL
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ""
	}
	return browserFactoryDefaultBaseURL[strings.ToLower(provider)]
}

// browserLLMLookupForTest is the test seam for `resolveBrowserLLM`.
// When non-nil, it's called instead of the real DAO lookup.
// Production leaves this nil; tests set it via `defer ... = nil`.
var browserLLMLookupForTest func(ctx context.Context, db *gorm.DB, tenantID, llmID string) (providerName, modelName, apiKey, baseURL string, err error)

func init() {
	Register(componentNameBrowser, NewBrowserComponent)
}
