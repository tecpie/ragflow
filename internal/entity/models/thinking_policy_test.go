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
	"testing"
)

func TestDetectThinkingFamily(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"Tongyi-Qianwen", thinkingFamilyDashscope},
		{"Dashscope", thinkingFamilyDashscope},
		{"SILICONFLOW", thinkingFamilyDashscope},
		{"DeepSeek", thinkingFamilyOpenAIThinking},
		{"Moonshot", thinkingFamilyOpenAIThinking},
		{"ZHIPU-AI", thinkingFamilyOpenAIThinking},
		{"openai", thinkingFamilyOpenAIThinking},
		{"GPUStack", thinkingFamilyGPUStackMultiEngine},
		{"gpustack", thinkingFamilyGPUStackMultiEngine},
		{"Gemini", thinkingFamilyGemini},
	}
	for _, tc := range cases {
		if got := detectThinkingFamily(tc.provider); got != tc.want {
			t.Errorf("detectThinkingFamily(%q)=%q, want %q", tc.provider, got, tc.want)
		}
	}
}

func TestIsDeepseekHybridThinkingModel(t *testing.T) {
	positives := []string{
		"deepseek-v4-flash",
		"DeepSeek-V4-Flash",
		"openai/deepseek-v4-pro",
		"deepseek-v3.2",
		"deepseek-v3.1",
		"deepseek-flash",
		"deepseek-pro",
	}
	for _, name := range positives {
		if !IsDeepseekHybridThinkingModel(name) {
			t.Errorf("IsDeepseekHybridThinkingModel(%q)=false, want true", name)
		}
	}
	negatives := []string{
		"deepseek-r1",
		"provider/deepseek-r1-distill",
		"deepseek-v3",
		"qwen3-32b",
	}
	for _, name := range negatives {
		if IsDeepseekHybridThinkingModel(name) {
			t.Errorf("IsDeepseekHybridThinkingModel(%q)=true, want false", name)
		}
	}
}

func TestIsQwen3RequiresThinking(t *testing.T) {
	if !qwen3RequiresThinking("qwen3-235b-a22b-thinking-2507") {
		t.Error("thinking SKU should require thinking")
	}
	if !qwen3RequiresThinking("qwen3-vl-30b-a3b-thinking") {
		t.Error("vl thinking SKU should require thinking")
	}
	// Hybrid dotted releases accept enable_thinking=false on DashScope; do not
	// treat them as thinking-only or the chat toggle cannot turn them off.
	if qwen3RequiresThinking("qwen3.8-max-preview") {
		t.Error("preview hybrid must not require thinking")
	}
	if qwen3RequiresThinking("qwen3.5-plus") {
		t.Error("qwen3.5-plus hybrid must not require thinking")
	}
	if qwen3RequiresThinking("qwen3.7-plus-2026-05-26") {
		t.Error("qwen3.7-plus hybrid must not require thinking")
	}
	if qwen3RequiresThinking("qwen3-32b") {
		t.Error("hybrid qwen3-32b must not require thinking")
	}
	if qwen3RequiresThinking("qwq-32b") {
		t.Error("non-qwen3 must not require thinking")
	}
}

func TestApplyEnableThinkingPolicyDashscopeQwen(t *testing.T) {
	body := map[string]interface{}{}
	applyEnableThinkingPolicy("qwen3-32b", "Tongyi-Qianwen", boolPointer(false), body)
	if got := body["enable_thinking"]; got != false {
		t.Errorf("enable_thinking=%#v, want false", got)
	}
	if _, ok := body["chat_template_kwargs"]; ok {
		t.Errorf("chat_template_kwargs must be absent for DashScope")
	}
	if _, ok := body["thinking"]; ok {
		t.Errorf("thinking must be absent for DashScope")
	}
}

func TestApplyEnableThinkingPolicyDashscopeDeepseekV4(t *testing.T) {
	body := map[string]interface{}{}
	applyEnableThinkingPolicy("deepseek-v4-flash", "Tongyi-Qianwen", boolPointer(false), body)
	if got := body["enable_thinking"]; got != false {
		t.Fatalf("enable_thinking=%#v, want false (ywqc109 disable must stick)", got)
	}
	if !IsDeepseekHybridThinkingModel("deepseek-v4-flash") {
		t.Fatal("deepseek-v4-flash should be classified as hybrid")
	}
}

func TestApplyEnableThinkingPolicyNativeDeepseekV4(t *testing.T) {
	body := map[string]interface{}{}
	applyEnableThinkingPolicy("deepseek-v4-flash", "DeepSeek", boolPointer(false), body)
	thinking, ok := body["thinking"].(map[string]interface{})
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("thinking=%#v, want {type: disabled}", body["thinking"])
	}
	if _, ok := body["enable_thinking"]; ok {
		t.Errorf("enable_thinking must be absent for openai_thinking family")
	}
}

func TestApplyEnableThinkingPolicyQwen3ThinkingForced(t *testing.T) {
	body := map[string]interface{}{}
	applyEnableThinkingPolicy("qwen3-235b-a22b-thinking-2507", "Tongyi-Qianwen", boolPointer(false), body)
	if got := body["enable_thinking"]; got != true {
		t.Fatalf("enable_thinking=%#v, want true for thinking-only Qwen3", got)
	}
}

func TestApplyEnableThinkingPolicyQwen37PlusRespectsDisable(t *testing.T) {
	body := map[string]interface{}{}
	applyEnableThinkingPolicy("qwen3.7-plus-2026-05-26", "Tongyi-Qianwen", boolPointer(false), body)
	if got := body["enable_thinking"]; got != false {
		t.Fatalf("enable_thinking=%#v, want false for qwen3.7-plus hybrid", got)
	}
}

func TestApplyEnableThinkingPolicyGPUStack(t *testing.T) {
	body := map[string]interface{}{}
	applyEnableThinkingPolicy("qwen3-32b", "GPUStack", boolPointer(false), body)
	thinking, ok := body["thinking"].(map[string]interface{})
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("thinking=%#v, want {type: disabled}", body["thinking"])
	}
	kwargs, ok := body["chat_template_kwargs"].(map[string]interface{})
	if !ok {
		t.Fatalf("chat_template_kwargs missing: %#v", body)
	}
	if kwargs["enable_thinking"] != false || kwargs["thinking"] != false {
		t.Errorf("chat_template_kwargs=%#v, want enable_thinking/thinking false", kwargs)
	}
}

func TestApplyEnableThinkingPolicyGPUStackQwen3ThinkingForced(t *testing.T) {
	body := map[string]interface{}{}
	applyEnableThinkingPolicy("qwen3-235b-a22b-thinking-2507", "gpustack", boolPointer(false), body)
	thinking, ok := body["thinking"].(map[string]interface{})
	if !ok || thinking["type"] != "enabled" {
		t.Fatalf("thinking=%#v, want {type: enabled}", body["thinking"])
	}
	kwargs, ok := body["chat_template_kwargs"].(map[string]interface{})
	if !ok || kwargs["enable_thinking"] != true {
		t.Fatalf("chat_template_kwargs=%#v, want enable_thinking true", body["chat_template_kwargs"])
	}
}

func TestApplyEnableThinkingPolicyNilIsNoop(t *testing.T) {
	body := map[string]interface{}{"model": "x"}
	applyEnableThinkingPolicy("deepseek-v4-flash", "Tongyi-Qianwen", nil, body)
	if _, ok := body["enable_thinking"]; ok {
		t.Errorf("unset thinking must not write enable_thinking")
	}
}

func TestApplyEnableThinkingPolicyKimi(t *testing.T) {
	body := map[string]interface{}{}
	applyEnableThinkingPolicy("kimi-k2.5", "Moonshot", boolPointer(false), body)
	thinking, ok := body["thinking"].(map[string]interface{})
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("thinking=%#v, want {type: disabled}", body["thinking"])
	}
	if body["temperature"] != 0.6 {
		t.Errorf("temperature=%#v, want 0.6", body["temperature"])
	}
}
