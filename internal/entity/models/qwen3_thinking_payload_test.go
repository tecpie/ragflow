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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newThinkingPayloadServer(t *testing.T, requestBody chan<- map[string]interface{}) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requestBody <- body

		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
}

func boolPointer(value bool) *bool {
	return &value
}

func runThinkingPayloadRequest(t *testing.T, driver ModelDriver, modelName string, stream bool, config *ChatConfig) {
	t.Helper()

	apiKey := "test-key"
	messages := []Message{{Role: "user", Content: "hello"}}
	apiConfig := &APIConfig{ApiKey: &apiKey}
	if stream {
		if err := driver.ChatStreamlyWithSender(t.Context(), modelName, messages, apiConfig, config, nil, func(*string, *string) error { return nil }); err != nil {
			t.Fatalf("ChatStreamlyWithSender: %v", err)
		}
		return
	}

	if _, err := driver.ChatWithMessages(t.Context(), modelName, messages, apiConfig, config, nil); err != nil {
		t.Fatalf("ChatWithMessages: %v", err)
	}
}

func TestVllmCompatibleQwen3ThinkingPayload(t *testing.T) {
	withSSRFBypass(t)

	drivers := []struct {
		name string
		new  func(string) ModelDriver
	}{
		{
			name: "vllm",
			new: func(baseURL string) ModelDriver {
				return NewVllmModel(map[string]string{"default": baseURL}, URLSuffix{Chat: "chat/completions", AsyncChat: "chat/completions"})
			},
		},
		{
			name: "openai-compatible",
			new: func(baseURL string) ModelDriver {
				return NewOpenAIAPICompatibleModel(map[string]string{"default": baseURL}, URLSuffix{Chat: "chat/completions"})
			},
		},
	}
	cases := []struct {
		name     string
		model    string
		thinking *bool
		want     bool
	}{
		{name: "default disabled", model: "qwen3-8b", want: false},
		{name: "explicit enabled", model: "Qwen/Qwen3-8B", thinking: boolPointer(true), want: true},
		{name: "explicit disabled", model: "qwen3-8b", thinking: boolPointer(false), want: false},
		{name: "preview respects disable", model: "qwen3.8-max-preview", thinking: boolPointer(false), want: false},
		// vLLM still forces the 2.4t-a95b flagship template to thinking-on.
		{name: "flagship forced enabled", model: "qwen3.8-2.4t-a95b", thinking: boolPointer(false), want: true},
		{name: "qwen3.7-plus respects disable", model: "qwen3.7-plus-2026-05-26", thinking: boolPointer(false), want: false},
	}

	for _, driverCase := range drivers {
		for _, stream := range []bool{false, true} {
			for _, testCase := range cases {
				name := driverCase.name + "/" + testCase.name
				if stream {
					name = driverCase.name + "/stream/" + testCase.name
				}
				t.Run(name, func(t *testing.T) {
					requestBody := make(chan map[string]interface{}, 1)
					server := newThinkingPayloadServer(t, requestBody)
					defer server.Close()

					var config *ChatConfig
					if testCase.thinking != nil {
						config = &ChatConfig{Thinking: testCase.thinking}
					}
					runThinkingPayloadRequest(t, driverCase.new(server.URL), testCase.model, stream, config)

					body := <-requestBody
					templateKwargs, ok := body["chat_template_kwargs"].(map[string]interface{})
					if !ok {
						t.Fatalf("chat_template_kwargs=%#v, want object", body["chat_template_kwargs"])
					}
					if got := templateKwargs["enable_thinking"]; got != testCase.want {
						t.Errorf("enable_thinking=%#v, want %v", got, testCase.want)
					}
					for _, field := range []string{"thinking", "enable_thinking", "extra_body"} {
						if _, exists := body[field]; exists {
							t.Errorf("unexpected root-level %s in request: %#v", field, body[field])
						}
					}
				})
			}
		}
	}
}

func TestVllmCompatibleNonQwenThinkingPayloadUnchanged(t *testing.T) {
	withSSRFBypass(t)

	drivers := []struct {
		name string
		new  func(string) ModelDriver
	}{
		{name: "vllm", new: func(baseURL string) ModelDriver {
			return NewVllmModel(map[string]string{"default": baseURL}, URLSuffix{Chat: "chat/completions"})
		}},
		{name: "openai-compatible", new: func(baseURL string) ModelDriver {
			return NewOpenAIAPICompatibleModel(map[string]string{"default": baseURL}, URLSuffix{Chat: "chat/completions"})
		}},
	}

	for _, driverCase := range drivers {
		t.Run(driverCase.name, func(t *testing.T) {
			requestBody := make(chan map[string]interface{}, 1)
			server := newThinkingPayloadServer(t, requestBody)
			defer server.Close()

			runThinkingPayloadRequest(t, driverCase.new(server.URL), "deepseek-r1", false, &ChatConfig{Thinking: boolPointer(true)})

			body := <-requestBody
			thinking, ok := body["thinking"].(map[string]interface{})
			if !ok || thinking["type"] != "enabled" {
				t.Fatalf("thinking=%#v, want {type: enabled}", body["thinking"])
			}
			if _, exists := body["chat_template_kwargs"]; exists {
				t.Errorf("chat_template_kwargs should be absent for non-Qwen3 model")
			}
		})
	}
}

func TestAliyunQwen3ThinkingPayloadRemainsProviderNative(t *testing.T) {
	withSSRFBypass(t)

	requestBody := make(chan map[string]interface{}, 1)
	server := newThinkingPayloadServer(t, requestBody)
	defer server.Close()

	driver := NewAliyunModel(map[string]string{"default": server.URL}, URLSuffix{Chat: "chat/completions"})
	runThinkingPayloadRequest(t, driver, "qwen3-8b", false, nil)

	body := <-requestBody
	if got := body["enable_thinking"]; got != false {
		t.Fatalf("enable_thinking=%#v, want false", got)
	}
	if _, exists := body["chat_template_kwargs"]; exists {
		t.Errorf("chat_template_kwargs should be absent for native Aliyun")
	}
	if _, exists := body["thinking"]; exists {
		t.Errorf("thinking should be absent for native Aliyun Qwen3")
	}
}

func TestAliyunDeepseekHybridThinkingDisable(t *testing.T) {
	withSSRFBypass(t)

	for _, stream := range []bool{false, true} {
		name := "non-stream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			requestBody := make(chan map[string]interface{}, 1)
			server := newThinkingPayloadServer(t, requestBody)
			defer server.Close()

			driver := NewAliyunModel(map[string]string{"default": server.URL}, URLSuffix{Chat: "chat/completions"})
			runThinkingPayloadRequest(t, driver, "deepseek-v4-flash", stream, &ChatConfig{Thinking: boolPointer(false)})

			body := <-requestBody
			if got := body["enable_thinking"]; got != false {
				t.Fatalf("enable_thinking=%#v, want false", got)
			}
			if _, exists := body["chat_template_kwargs"]; exists {
				t.Errorf("chat_template_kwargs must be absent for DashScope deepseek")
			}
			if _, exists := body["thinking"]; exists {
				t.Errorf("thinking must be absent for DashScope deepseek")
			}
		})
	}
}

func TestAliyunQwen3ThinkingOnlyForcesEnable(t *testing.T) {
	withSSRFBypass(t)

	requestBody := make(chan map[string]interface{}, 1)
	server := newThinkingPayloadServer(t, requestBody)
	defer server.Close()

	driver := NewAliyunModel(map[string]string{"default": server.URL}, URLSuffix{Chat: "chat/completions"})
	runThinkingPayloadRequest(t, driver, "qwen3-235b-a22b-thinking-2507", false, &ChatConfig{Thinking: boolPointer(false)})

	body := <-requestBody
	if got := body["enable_thinking"]; got != true {
		t.Fatalf("enable_thinking=%#v, want true for thinking-only Qwen3", got)
	}
}

func TestAliyunQwen37PlusRespectsThinkingDisable(t *testing.T) {
	withSSRFBypass(t)

	requestBody := make(chan map[string]interface{}, 1)
	server := newThinkingPayloadServer(t, requestBody)
	defer server.Close()

	driver := NewAliyunModel(map[string]string{"default": server.URL}, URLSuffix{Chat: "chat/completions"})
	runThinkingPayloadRequest(t, driver, "qwen3.7-plus-2026-05-26", false, &ChatConfig{Thinking: boolPointer(false)})

	body := <-requestBody
	if got := body["enable_thinking"]; got != false {
		t.Fatalf("enable_thinking=%#v, want false for qwen3.7-plus hybrid", got)
	}
}

func TestOpenAIThinkingFamilyDisablePayload(t *testing.T) {
	withSSRFBypass(t)

	requestBody := make(chan map[string]interface{}, 1)
	server := newThinkingPayloadServer(t, requestBody)
	defer server.Close()

	driver := NewOpenAIModel(map[string]string{"default": server.URL}, URLSuffix{Chat: "chat/completions"})
	runThinkingPayloadRequest(t, driver, "deepseek-v4-flash", false, &ChatConfig{Thinking: boolPointer(false)})

	body := <-requestBody
	thinking, ok := body["thinking"].(map[string]interface{})
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("thinking=%#v, want {type: disabled}", body["thinking"])
	}
	if _, exists := body["enable_thinking"]; exists {
		t.Errorf("enable_thinking must be absent for openai_thinking non-qwen3")
	}
}

func TestGPUStackMultiEngineThinkingPayload(t *testing.T) {
	withSSRFBypass(t)

	requestBody := make(chan map[string]interface{}, 1)
	server := newThinkingPayloadServer(t, requestBody)
	defer server.Close()

	driver := NewGPUStackModel(map[string]string{"default": server.URL}, URLSuffix{Chat: "v1/chat/completions"})
	runThinkingPayloadRequest(t, driver, "qwen3-32b", false, &ChatConfig{Thinking: boolPointer(false)})

	body := <-requestBody
	thinking, ok := body["thinking"].(map[string]interface{})
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("thinking=%#v, want {type: disabled}", body["thinking"])
	}
	kwargs, ok := body["chat_template_kwargs"].(map[string]interface{})
	if !ok {
		t.Fatalf("chat_template_kwargs missing: %#v", body)
	}
	if kwargs["enable_thinking"] != false || kwargs["thinking"] != false {
		t.Errorf("chat_template_kwargs=%#v, want both false", kwargs)
	}
}

func TestSiliconflowDeepseekHybridThinkingDisable(t *testing.T) {
	withSSRFBypass(t)

	requestBody := make(chan map[string]interface{}, 1)
	server := newThinkingPayloadServer(t, requestBody)
	defer server.Close()

	driver := NewSiliconflowModel(map[string]string{"default": server.URL}, URLSuffix{Chat: "chat/completions"})
	runThinkingPayloadRequest(t, driver, "deepseek-ai/DeepSeek-V3.2", false, &ChatConfig{Thinking: boolPointer(false)})

	body := <-requestBody
	if got := body["enable_thinking"]; got != false {
		t.Fatalf("enable_thinking=%#v, want false", got)
	}
}
