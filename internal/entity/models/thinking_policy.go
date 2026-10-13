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
	"strings"
)

// Thinking wire families mirror common/model_thinking_utils.py.
const (
	thinkingFamilyDashscope           = "dashscope"
	thinkingFamilyGPUStackMultiEngine = "gpustack_multi_engine"
	thinkingFamilyGemini              = "gemini"
	thinkingFamilyOpenAIThinking      = "openai_thinking"
)

var kimiK2ModelHints = []string{"kimi-k2.5", "kimi-k2.6"}

// IsDeepseekHybridThinkingModel reports whether the model accepts a per-request
// thinking toggle. DashScope hybrids take enable_thinking; native DeepSeek uses
// thinking:{type}. R1 variants are thinking-only and excluded.
func IsDeepseekHybridThinkingModel(modelName string) bool {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	if !strings.Contains(name, "deepseek") {
		return false
	}
	if strings.Contains(name, "deepseek-r1") || strings.HasSuffix(name, "-r1") {
		return false
	}
	for _, hint := range []string{
		"v4-flash",
		"v4-pro",
		"v3.2",
		"v3.1",
		"deepseek-flash",
		"deepseek-pro",
	} {
		if strings.Contains(name, hint) {
			return true
		}
	}
	return false
}

func isKimiK2Model(modelName string) bool {
	name := strings.ToLower(modelName)
	for _, hint := range kimiK2ModelHints {
		if strings.Contains(name, hint) {
			return true
		}
	}
	return false
}

// detectThinkingFamily maps a provider name onto the request payload shape used
// by applyEnableThinkingPolicy. Matching is case-insensitive so Go driver
// Name() values like "gpustack" align with Python's "GPUStack".
func detectThinkingFamily(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "gpustack":
		return thinkingFamilyGPUStackMultiEngine
	case "tongyi-qianwen", "dashscope", "siliconflow":
		// SiliconFlow's public chat API takes root enable_thinking like DashScope.
		return thinkingFamilyDashscope
	case "gemini":
		return thinkingFamilyGemini
	default:
		return thinkingFamilyOpenAIThinking
	}
}

// qwen3RequiresThinking reports whether this Qwen3 SKU is thinking-only and
// must keep enable_thinking=true. Only names that advertise a thinking variant
// (e.g. qwen3-*-thinking, qwen3.5-*-thinking) are forced: hybrid dotted
// releases such as qwen3.5-plus / qwen3.7-plus accept enable_thinking=false on
// DashScope, so the chat "深度思考" toggle must be able to turn them off.
func qwen3RequiresThinking(modelName string) bool {
	name := strings.ToLower(modelName)
	if !strings.Contains(name, "qwen3") {
		return false
	}
	return strings.Contains(name, "thinking")
}

// applyQwen3ThinkingDefault ensures enable_thinking=false is sent for hybrid
// qwen3 models when it hasn't been explicitly configured. DashScope defaults
// enable_thinking to true for qwen3 models, which produces reasoning output
// that RAGFlow doesn't expect in most pipelines. Mirrors Python's
// chat_model.py default of enable_thinking=False for hybrid qwen3, and
// enable_thinking=True for thinking-only SKUs that reject any other value.
func applyQwen3ThinkingDefault(modelName string, reqBody map[string]interface{}) {
	if !strings.Contains(strings.ToLower(modelName), "qwen3") {
		return
	}
	if qwen3RequiresThinking(modelName) {
		reqBody["enable_thinking"] = true
		return
	}
	if _, alreadySet := reqBody["enable_thinking"]; alreadySet {
		return
	}
	reqBody["enable_thinking"] = false
}

func applyKimiK2Thinking(reqBody map[string]interface{}, thinkingEnabled bool) {
	typeStr := "disabled"
	temp := 0.6
	if thinkingEnabled {
		typeStr = "enabled"
		temp = 1.0
	}
	reqBody["thinking"] = map[string]interface{}{"type": typeStr}
	reqBody["temperature"] = temp
	reqBody["top_p"] = 0.95
	reqBody["n"] = 1
	reqBody["presence_penalty"] = 0.0
	reqBody["frequency_penalty"] = 0.0
}

func thinkingTypeString(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

// applyEnableThinkingPolicy maps ChatConfig.Thinking onto provider-specific
// request fields, mirroring Python apply_enable_thinking_policy.
//
// Go writes fields directly onto the HTTP body (Python puts them under
// extra_body for LiteLLM/OpenAI SDK merging). thinking==nil means unset: no
// payload is written except for Kimi K2, which defaults thinking on.
func applyEnableThinkingPolicy(modelName, provider string, thinking *bool, reqBody map[string]interface{}) {
	if reqBody == nil {
		return
	}

	if isKimiK2Model(modelName) {
		enabled := true
		if thinking != nil {
			enabled = *thinking
		}
		applyKimiK2Thinking(reqBody, enabled)
		return
	}

	if thinking == nil {
		return
	}

	enabled := *thinking
	if qwen3RequiresThinking(modelName) {
		enabled = true
	}

	family := detectThinkingFamily(provider)
	switch family {
	case thinkingFamilyGemini:
		if enabled {
			if _, ok := reqBody["thinking_budget"]; !ok {
				reqBody["thinking_budget"] = 1024
			}
		} else {
			reqBody["thinking_budget"] = 0
		}
	case thinkingFamilyGPUStackMultiEngine:
		reqBody["thinking"] = map[string]interface{}{"type": thinkingTypeString(enabled)}
		chatTemplateKwargs, ok := reqBody["chat_template_kwargs"].(map[string]interface{})
		if !ok {
			chatTemplateKwargs = map[string]interface{}{}
		}
		chatTemplateKwargs["enable_thinking"] = enabled
		chatTemplateKwargs["thinking"] = enabled
		reqBody["chat_template_kwargs"] = chatTemplateKwargs
	case thinkingFamilyOpenAIThinking:
		reqBody["thinking"] = map[string]interface{}{"type": thinkingTypeString(enabled)}
	default: // dashscope
		reqBody["enable_thinking"] = enabled
	}
}

// chatConfigThinking returns the Thinking pointer from config, or nil.
func chatConfigThinking(config *ChatConfig) *bool {
	if config == nil {
		return nil
	}
	return config.Thinking
}

// applyOpenAIChatThinking applies Qwen3 enable_thinking defaults for the OpenAI
// driver, and the openai_thinking family payload (thinking:{type}) for other
// models so an explicit thinking=false actually disables vendor defaults.
func applyOpenAIChatThinking(modelName string, config *ChatConfig, reqBody map[string]interface{}) {
	if strings.Contains(strings.ToLower(modelName), "qwen3") {
		if thinking := chatConfigThinking(config); thinking != nil {
			reqBody["enable_thinking"] = *thinking
		}
		applyQwen3ThinkingDefault(modelName, reqBody)
		return
	}
	applyEnableThinkingPolicy(modelName, "openai", chatConfigThinking(config), reqBody)
}
