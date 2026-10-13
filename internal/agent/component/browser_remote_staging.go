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

package component

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	unsafePathCharsRE   = regexp.MustCompile(`[\\/:\x00-\x1f\x7f<>|?*"]`)
	uuidFilenamePrefixRE = regexp.MustCompile(`(?i)^(?:[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})_`)
)

// RemoteStagingConfig mirrors Python browser_remote_staging.RemoteStagingConfig.
type RemoteStagingConfig struct {
	BaseURL  string
	Token    string
	Timeout  time.Duration
	MaxBytes int64
}

// RemoteStagingUploadResult is the gateway response after a successful upload.
type RemoteStagingUploadResult struct {
	Path      string
	Name      string
	Size      int64
	SessionID string
}

// RemoteStagingError is returned for HTTP staging failures.
type RemoteStagingError struct {
	Msg string
}

func (e *RemoteStagingError) Error() string { return e.Msg }

// StagingBaseURLFromCDP maps a Browser CDP URL to the HTTP staging base
// (single-port gateway). Mirrors Python staging_base_url_from_cdp.
func StagingBaseURLFromCDP(cdpURL string) string {
	token := strings.TrimRight(strings.TrimSpace(cdpURL), "/")
	if token == "" {
		return ""
	}
	parsed, err := url.Parse(token)
	if err != nil || parsed.Host == "" {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Scheme + "://" + parsed.Host
	case "ws":
		return "http://" + parsed.Host
	case "wss":
		return "https://" + parsed.Host
	default:
		return ""
	}
}

// ResolveRemoteStagingConfig resolves staging URL from param, CDP fallback, or env.
func ResolveRemoteStagingConfig(paramURL, paramToken, cdpURLFallback string) *RemoteStagingConfig {
	explicit := strings.TrimRight(strings.TrimSpace(paramURL), "/")
	cdpBase := StagingBaseURLFromCDP(cdpURLFallback)
	envURL := strings.TrimRight(strings.TrimSpace(os.Getenv("RAGFLOW_BROWSER_REMOTE_STAGING_URL")), "/")
	baseURL := explicit
	if baseURL == "" {
		baseURL = cdpBase
	}
	if baseURL == "" {
		baseURL = envURL
	}
	if baseURL == "" {
		return nil
	}
	token := strings.TrimSpace(paramToken)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("RAGFLOW_BROWSER_REMOTE_STAGING_TOKEN"))
	}
	timeout := 120 * time.Second
	if raw := strings.TrimSpace(os.Getenv("RAGFLOW_BROWSER_REMOTE_STAGING_TIMEOUT")); raw != "" {
		if sec, err := strconv.ParseFloat(raw, 64); err == nil && sec >= 5 {
			timeout = time.Duration(sec * float64(time.Second))
		}
	}
	maxBytes := int64(100 * 1024 * 1024)
	if raw := strings.TrimSpace(os.Getenv("RAGFLOW_BROWSER_REMOTE_STAGING_MAX_BYTES")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			maxBytes = n
		}
	}
	return &RemoteStagingConfig{
		BaseURL:  baseURL,
		Token:    token,
		Timeout:  timeout,
		MaxBytes: maxBytes,
	}
}

// NormalizeUploadFilename strips UUID prefixes and path components.
func NormalizeUploadFilename(name string) string {
	base := filepath.Base(strings.TrimSpace(strings.ReplaceAll(name, `\`, "/")))
	for base != "" {
		cleaned := uuidFilenamePrefixRE.ReplaceAllString(base, "")
		if cleaned == base {
			break
		}
		base = strings.TrimSpace(cleaned)
	}
	return base
}

func safeUploadFilename(name string) string {
	base := NormalizeUploadFilename(name)
	if base == "" || base == "." {
		return fmt.Sprintf("upload_%s.bin", uuid.NewString()[:8])
	}
	cleaned := strings.Trim(unsafePathCharsRE.ReplaceAllString(base, "_"), " .")
	if cleaned == "" {
		return fmt.Sprintf("upload_%s.bin", uuid.NewString()[:8])
	}
	return cleaned
}

// ValidateRemoteStagingURL reports whether url looks like an HTTP(S) staging endpoint.
func ValidateRemoteStagingURL(raw string) bool {
	token := strings.TrimSpace(raw)
	if token == "" {
		return false
	}
	parsed, err := url.Parse(token)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return (scheme == "http" || scheme == "https") && parsed.Host != ""
}

// RemoteStagingClient talks to tools/browser_remote_staging gateway protocol.
type RemoteStagingClient struct {
	config RemoteStagingConfig
	client *http.Client
}

// NewRemoteStagingClient constructs a client for the given config.
func NewRemoteStagingClient(cfg RemoteStagingConfig) *RemoteStagingClient {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &RemoteStagingClient{
		config: cfg,
		client: &http.Client{Timeout: timeout},
	}
}

func (c *RemoteStagingClient) buildHeaders(sessionID string, extra map[string]string) http.Header {
	h := make(http.Header)
	h.Set("User-Agent", "RAGFlow-Browser-Remote-Staging/1.0")
	h.Set("X-Staging-Session", sessionID)
	if c.config.Token != "" {
		h.Set("Authorization", "Bearer "+c.config.Token)
	}
	for k, v := range extra {
		h.Set(k, v)
	}
	return h
}

func (c *RemoteStagingClient) requestJSON(ctx context.Context, method, rawURL string, headers http.Header, body []byte) (map[string]any, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, &RemoteStagingError{Msg: fmt.Sprintf("remote staging request build failed: %v", err)}
	}
	req.Header = headers
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, &RemoteStagingError{Msg: fmt.Sprintf("remote staging request failed: %v", err)}
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &RemoteStagingError{Msg: fmt.Sprintf("remote staging read failed: %v", err)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(payload))
		if detail == "" {
			detail = resp.Status
		}
		return nil, &RemoteStagingError{Msg: fmt.Sprintf("remote staging HTTP %d: %s", resp.StatusCode, detail)}
	}
	if len(payload) == 0 {
		return map[string]any{}, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, &RemoteStagingError{Msg: fmt.Sprintf("remote staging returned invalid JSON: %s", truncateForErr(string(payload), 200))}
	}
	return parsed, nil
}

// HealthCheckDetail probes GET /health.
func (c *RemoteStagingClient) HealthCheckDetail(ctx context.Context) (bool, string) {
	u := strings.TrimRight(c.config.BaseURL, "/") + "/health"
	payload, err := c.requestJSON(ctx, http.MethodGet, u, c.buildHeaders("health-check", nil), nil)
	if err != nil {
		return false, err.Error()
	}
	status, _ := payload["status"].(string)
	if strings.EqualFold(status, "ok") {
		return true, "ok"
	}
	return false, fmt.Sprintf("unexpected health payload: %v", payload)
}

// UploadFile POSTs a local file to /staging/upload.
func (c *RemoteStagingClient) UploadFile(ctx context.Context, localPath, sessionID, filename string) (*RemoteStagingUploadResult, error) {
	info, err := os.Stat(localPath)
	if err != nil || info.IsDir() {
		return nil, &RemoteStagingError{Msg: fmt.Sprintf("local upload file does not exist: %s", localPath)}
	}
	if info.Size() <= 0 {
		return nil, &RemoteStagingError{Msg: fmt.Sprintf("local upload file is empty: %s", localPath)}
	}
	if info.Size() > c.config.MaxBytes {
		return nil, &RemoteStagingError{Msg: fmt.Sprintf(
			"local upload file exceeds remote staging max size (%d bytes): %s", c.config.MaxBytes, localPath)}
	}
	session := strings.TrimSpace(sessionID)
	if session == "" {
		session = strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	uploadName := safeUploadFilename(filename)
	if uploadName == "" || filename == "" {
		uploadName = safeUploadFilename(filepath.Base(localPath))
	}
	raw, err := os.ReadFile(localPath)
	if err != nil {
		return nil, &RemoteStagingError{Msg: fmt.Sprintf("read local upload file failed: %v", err)}
	}
	endpoint := strings.TrimRight(c.config.BaseURL, "/") + "/staging/upload?filename=" + url.QueryEscape(uploadName)
	payload, err := c.requestJSON(ctx, http.MethodPost, endpoint, c.buildHeaders(session, map[string]string{
		"Content-Type": "application/octet-stream",
	}), raw)
	if err != nil {
		return nil, err
	}
	remotePath, _ := payload["path"].(string)
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "" {
		return nil, &RemoteStagingError{Msg: fmt.Sprintf("remote staging upload missing path field: %v", payload)}
	}
	name, _ := payload["name"].(string)
	if strings.TrimSpace(name) == "" {
		name = uploadName
	}
	size := info.Size()
	if v, ok := payload["size"].(float64); ok && v > 0 {
		size = int64(v)
	}
	sid, _ := payload["session_id"].(string)
	if strings.TrimSpace(sid) == "" {
		sid = session
	}
	return &RemoteStagingUploadResult{
		Path:      remotePath,
		Name:      name,
		Size:      size,
		SessionID: sid,
	}, nil
}

// StagePreparedFiles uploads each prepared local file and rewrites local_path
// to the remote Chrome path expected by CDP DOM.setFileInputFiles.
func (c *RemoteStagingClient) StagePreparedFiles(ctx context.Context, prepared []map[string]any) ([]map[string]any, error) {
	if len(prepared) == 0 {
		return nil, nil
	}
	sessionID := strings.ReplaceAll(uuid.NewString(), "-", "")
	staged := make([]map[string]any, 0, len(prepared))
	for _, item := range prepared {
		localPath, _ := item["local_path"].(string)
		localPath = strings.TrimSpace(localPath)
		if localPath == "" {
			continue
		}
		originalName := NormalizeUploadFilename(fmt.Sprint(item["name"]))
		if originalName == "" {
			originalName = filepath.Base(localPath)
		}
		result, err := c.UploadFile(ctx, localPath, sessionID, originalName)
		if err != nil {
			return nil, err
		}
		out := cloneStringAnyMap(item)
		out["local_path"] = result.Path
		out["remote_path"] = result.Path
		if result.Name != "" {
			out["name"] = result.Name
		} else {
			out["name"] = originalName
		}
		out["staging_session_id"] = result.SessionID
		out["staging_mode"] = "staging"
		staged = append(staged, out)
	}
	return staged, nil
}

func cloneStringAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+4)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func truncateForErr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
