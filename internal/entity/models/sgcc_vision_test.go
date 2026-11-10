package models

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ragflow/internal/common"
)

func TestAdaptSGCCVisionMessagesKeepsOpenAIImageURL(t *testing.T) {
	messages := []Message{{
		Role: "user",
		Content: []interface{}{
			map[string]any{"type": "text", "text": "describe"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AA=="}},
		},
	}}
	got, err := adaptSGCCVisionMessages("gpt-4o", "https://api.openai.com/v1/chat/completions", nil, messages)
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	parts := got[0].Content.([]interface{})
	image := parts[1].(map[string]any)
	if image["type"] != "image_url" {
		t.Fatalf("type = %v, want image_url", image["type"])
	}
}

func TestAdaptSGCCVisionMessagesRewritesImageToRawBase64(t *testing.T) {
	image := &common.ImageURL{URL: "data:image/png;base64,AA\n=="}
	messages := []Message{{
		Role: "user",
		Content: []interface{}{
			map[string]any{"type": "text", "text": "描述这张图"},
			map[string]any{"type": "image_url", "image_url": image},
		},
	}}
	got, err := adaptSGCCVisionMessages("SGGM-VL-27B-R", "http://127.0.0.1/v1/chat/completions", nil, messages)
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	parts, ok := got[0].Content.([]interface{})
	if !ok || len(parts) != 2 {
		t.Fatalf("content = %#v, want prompt plus image_base64", got[0].Content)
	}
	prompt := parts[0].(map[string]interface{})
	if prompt["type"] != "text" || prompt["text"] != "描述这张图" {
		t.Fatalf("prompt = %#v", prompt)
	}
	payload := parts[1].(map[string]interface{})
	if payload["type"] != "image_base64" || payload["image"] != "data:image/png;base64,AA==" {
		t.Fatalf("image = %#v, want image_base64 data URL", payload)
	}
	if _, exists := payload["image_url"]; exists {
		t.Fatalf("image part still has image_url: %#v", payload)
	}
}

func TestAdaptSGCCVisionMessagesMatchesGatewayURL(t *testing.T) {
	messages := []Message{{
		Role: "user",
		Content: []interface{}{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "iVBORw0KGgo="}},
		},
	}}
	got, err := adaptSGCCVisionMessages("custom-vl", "http://ai.sgcc.com.cn:19000/lmp-cloud-ias-server/api/vlm/chat/completions/V2", nil, messages)
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	parts := got[0].Content.([]interface{})
	if len(parts) != 1 {
		t.Fatalf("len = %d, want 1", len(parts))
	}
	part := parts[0].(map[string]interface{})
	if part["type"] != "image_base64" || part["image"] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("part = %#v", part)
	}
}

func TestAdaptSGCCVisionMessagesRejectsRemoteImage(t *testing.T) {
	messages := []Message{{
		Role: "user",
		Content: []interface{}{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/a.png"}},
		},
	}}
	_, err := adaptSGCCVisionMessages("SGGM-VL-27B-R", "http://127.0.0.1/v1/chat/completions", nil, messages)
	if err == nil || !strings.Contains(err.Error(), "remote image URL") {
		t.Fatalf("err = %v, want remote image URL", err)
	}
}

func TestRawImageBase64ShrinksOversizedPNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 900, 900))
	seed := 1
	for i := 0; i < len(img.Pix); i++ {
		seed = seed*1103515245 + 12345
		img.Pix[i] = byte(seed >> 16)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	payload := base64.StdEncoding.EncodeToString(buf.Bytes())
	if len(payload) <= sgccMaxImageBase64Chars {
		t.Fatalf("fixture base64 len = %d, want > %d", len(payload), sgccMaxImageBase64Chars)
	}
	got, err := rawImageBase64("data:image/png;base64," + payload)
	if err != nil {
		t.Fatalf("rawImageBase64: %v", err)
	}
	if len(got) > sgccMaxImageBase64Chars {
		t.Fatalf("shrunk len = %d, want <= %d", len(got), sgccMaxImageBase64Chars)
	}
	decoded, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("decode shrunk: %v", err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(decoded))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("format = %s, want jpeg", format)
	}
	if cfg.Width > 1600 || cfg.Height > 1600 {
		t.Fatalf("size = %dx%d, want within 1600", cfg.Width, cfg.Height)
	}
}

func TestAdaptSGCCVisionMessagesLeavesPlainText(t *testing.T) {
	messages := []Message{{Role: "user", Content: "你好"}}
	got, err := adaptSGCCVisionMessages("SGGM-VL-27B-R", "http://ai.sgcc.com.cn/v1/chat/completions", nil, messages)
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	if got[0].Content != "你好" {
		t.Fatalf("content = %#v, want plain text", got[0].Content)
	}
}

func TestOpenAICompatibleSendsSGCCBase64Text(t *testing.T) {
	withSSRFBypass(t)
	var request map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "ok"}}},
		})
	}))
	defer srv.Close()

	key := "test-key"
	_, err := NewOpenAIAPICompatibleModel(map[string]string{"default": srv.URL}, URLSuffix{Chat: "chat/completions"}).ChatWithMessages(
		t.Context(),
		"SGGM-VL-27B-R",
		[]Message{{Role: "user", Content: []interface{}{
			map[string]any{"type": "text", "text": "描述这张图"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AA=="}},
		}}},
		&APIConfig{ApiKey: &key},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	raw, err := json.Marshal(request["messages"])
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, "image_url") || !strings.Contains(body, "image_base64") {
		t.Fatalf("request = %s", raw)
	}
	if !strings.Contains(body, "描述这张图") || !strings.Contains(body, "data:image/png;base64,AA==") {
		t.Fatalf("request = %s", raw)
	}
}
