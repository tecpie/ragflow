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
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	cdpBlobChunkVar             = "__ragflow_upload_chunks"
	defaultRemoteDownloadDir    = `C:\ProgramData\ragflow\browser-uploads`
	defaultCdpBlobChunkChars    = 40_000
	defaultCdpBlobMaxBytes      = int64(100 * 1024 * 1024)
	defaultCdpBlobTimeout       = 120 * time.Second
)

var winAbsPathRE = regexp.MustCompile(`^[A-Za-z]:\\`)

// CdpBlobStagingError wraps RemoteStagingError for CDP blob path failures.
type CdpBlobStagingError struct {
	Msg string
}

func (e *CdpBlobStagingError) Error() string { return e.Msg }

// ResolveRemoteUploadMode normalizes param/env upload mode.
func ResolveRemoteUploadMode(paramMode string) string {
	raw := strings.ToLower(strings.TrimSpace(paramMode))
	if raw == "" {
		raw = strings.ToLower(strings.TrimSpace(os.Getenv("RAGFLOW_BROWSER_REMOTE_UPLOAD_MODE")))
	}
	if raw == "" {
		raw = "auto"
	}
	switch raw {
	case "auto", "staging":
		return raw
	case "blob_cdp", "blob", "cdp_blob":
		return "blob_cdp"
	default:
		return "auto"
	}
}

// CdpBlobStagingConfig configures CDP blob→download staging onto a remote Chrome host.
type CdpBlobStagingConfig struct {
	CDPURL              string
	DownloadDir         string
	Timeout             time.Duration
	MaxBytes            int64
	ChunkChars          int
	RequireDownloadEvent bool
}

// ResolveCdpBlobStagingConfig builds config from CDP URL + env overrides.
func ResolveCdpBlobStagingConfig(cdpURL string) (*CdpBlobStagingConfig, error) {
	token := strings.TrimRight(strings.TrimSpace(cdpURL), "/")
	if token == "" {
		return nil, &CdpBlobStagingError{Msg: "CDP URL is required for blob staging"}
	}
	downloadDir := strings.TrimSpace(os.Getenv("RAGFLOW_BROWSER_CDP_BLOB_DOWNLOAD_DIR"))
	if downloadDir == "" {
		downloadDir = defaultRemoteDownloadDir
	}
	cfg := &CdpBlobStagingConfig{
		CDPURL:               token,
		DownloadDir:          normalizeRemoteDownloadDir(downloadDir),
		Timeout:              defaultCdpBlobTimeout,
		MaxBytes:             defaultCdpBlobMaxBytes,
		ChunkChars:           defaultCdpBlobChunkChars,
		RequireDownloadEvent: true,
	}
	if raw := strings.TrimSpace(os.Getenv("RAGFLOW_BROWSER_CDP_BLOB_TIMEOUT")); raw != "" {
		if sec, err := strconv.ParseFloat(raw, 64); err == nil && sec >= 5 {
			cfg.Timeout = time.Duration(sec * float64(time.Second))
		}
	}
	maxRaw := strings.TrimSpace(os.Getenv("RAGFLOW_BROWSER_CDP_BLOB_MAX_BYTES"))
	if maxRaw == "" {
		maxRaw = strings.TrimSpace(os.Getenv("RAGFLOW_BROWSER_REMOTE_STAGING_MAX_BYTES"))
	}
	if maxRaw != "" {
		if n, err := strconv.ParseInt(maxRaw, 10, 64); err == nil && n > 0 {
			cfg.MaxBytes = n
		}
	}
	if raw := strings.TrimSpace(os.Getenv("RAGFLOW_BROWSER_CDP_BLOB_CHUNK_CHARS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			cfg.ChunkChars = n
		}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RAGFLOW_BROWSER_CDP_BLOB_REQUIRE_DOWNLOAD_EVENT"))) {
	case "0", "false", "no", "off":
		cfg.RequireDownloadEvent = false
	}
	return cfg, nil
}

func normalizeHTTPCDPBase(cdpURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(cdpURL))
	if err != nil {
		return "", &CdpBlobStagingError{Msg: fmt.Sprintf("unsupported CDP URL for blob staging: %s", cdpURL)}
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return strings.TrimRight(cdpURL, "/"), nil
	case "ws", "wss":
		port := parsed.Port()
		if port == "" {
			if strings.EqualFold(parsed.Scheme, "wss") {
				port = "443"
			} else {
				port = "80"
			}
		}
		scheme := "http"
		if strings.EqualFold(parsed.Scheme, "wss") {
			scheme = "https"
		}
		return fmt.Sprintf("%s://%s:%s", scheme, parsed.Hostname(), port), nil
	default:
		return "", &CdpBlobStagingError{Msg: fmt.Sprintf("unsupported CDP URL for blob staging: %s", cdpURL)}
	}
}

func normalizeRemoteDownloadDir(path string) string {
	token := strings.Trim(strings.TrimSpace(path), `"'`)
	if token == "" {
		return defaultRemoteDownloadDir
	}
	token = strings.ReplaceAll(token, "/", `\`)
	if winAbsPathRE.MatchString(token) {
		return strings.TrimRight(token, `\`)
	}
	return strings.TrimRight(token, `\`)
}

func joinRemoteWinPath(base string, parts ...string) string {
	segments := []string{normalizeRemoteDownloadDir(base)}
	for _, part := range parts {
		token := strings.Trim(strings.TrimSpace(part), `\/`)
		if token != "" {
			segments = append(segments, token)
		}
	}
	return strings.Join(segments, `\`)
}

func cdpPathForChrome(path string) string {
	token := normalizeRemoteDownloadDir(path)
	if winAbsPathRE.MatchString(token) {
		return token
	}
	return strings.ReplaceAll(token, "/", `\`)
}

func fetchCDPJSON(ctx context.Context, client *http.Client, rawURL string) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("CDP HTTP %d: %s", resp.StatusCode, truncateForErr(string(body), 200))
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func resolveBrowserWSURL(ctx context.Context, client *http.Client, httpBase string) (string, error) {
	version, err := fetchCDPJSON(ctx, client, httpBase+"/json/version")
	if err != nil {
		return "", err
	}
	m, ok := version.(map[string]any)
	if !ok {
		return "", &CdpBlobStagingError{Msg: fmt.Sprintf("CDP /json/version unexpected: %v", version)}
	}
	wsURL, _ := m["webSocketDebuggerUrl"].(string)
	wsURL = strings.TrimSpace(wsURL)
	if wsURL == "" {
		return "", &CdpBlobStagingError{Msg: fmt.Sprintf("CDP /json/version missing webSocketDebuggerUrl: %s", httpBase)}
	}
	return wsURL, nil
}

type cdpWSSession struct {
	conn     *websocket.Conn
	timeout  time.Duration
	mu       sync.Mutex
	nextID   int
	pending  map[int]chan cdpReply
	handlers []func(map[string]any)
	closed   chan struct{}
}

type cdpReply struct {
	result map[string]any
	err    error
}

func newCDPWSSession(conn *websocket.Conn, timeout time.Duration) *cdpWSSession {
	s := &cdpWSSession{
		conn:    conn,
		timeout: timeout,
		pending: make(map[int]chan cdpReply),
		closed:  make(chan struct{}),
	}
	go s.readLoop()
	return s
}

func (s *cdpWSSession) addEventHandler(h func(map[string]any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers = append(s.handlers, h)
}

func (s *cdpWSSession) close() {
	_ = s.conn.Close()
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
}

func (s *cdpWSSession) send(ctx context.Context, method string, params map[string]any, sessionID string) (map[string]any, error) {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan cdpReply, 1)
	s.pending[id] = ch
	payload := map[string]any{"id": id, "method": method, "params": params}
	if params == nil {
		payload["params"] = map[string]any{}
	}
	if sessionID != "" {
		payload["sessionId"] = sessionID
	}
	err := s.conn.WriteJSON(payload)
	s.mu.Unlock()
	if err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, ctx.Err()
	case <-timer.C:
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, &CdpBlobStagingError{Msg: fmt.Sprintf("CDP command timed out: %s", method)}
	case reply := <-ch:
		return reply.result, reply.err
	}
}

func (s *cdpWSSession) readLoop() {
	defer func() {
		s.mu.Lock()
		for id, ch := range s.pending {
			ch <- cdpReply{err: &CdpBlobStagingError{Msg: "CDP websocket closed"}}
			delete(s.pending, id)
		}
		s.mu.Unlock()
		s.close()
	}()
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			continue
		}
		if rawID, ok := payload["id"]; ok {
			id := int(asFloat64(rawID))
			s.mu.Lock()
			ch := s.pending[id]
			delete(s.pending, id)
			s.mu.Unlock()
			if ch == nil {
				continue
			}
			if errObj, ok := payload["error"]; ok {
				raw, _ := json.Marshal(errObj)
				ch <- cdpReply{err: &CdpBlobStagingError{Msg: string(raw)}}
				continue
			}
			result, _ := payload["result"].(map[string]any)
			if result == nil {
				result = map[string]any{}
			}
			ch <- cdpReply{result: result}
			continue
		}
		if _, ok := payload["method"]; ok {
			s.mu.Lock()
			handlers := append([]func(map[string]any){}, s.handlers...)
			s.mu.Unlock()
			for _, h := range handlers {
				h(payload)
			}
		}
	}
}

func asFloat64(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		return 0
	}
}

func buildTriggerDownloadJS(filename, mimeType string) string {
	safeName, _ := json.Marshal(filename)
	safeMime, _ := json.Marshal(mimeType)
	if safeMime == nil || string(safeMime) == `""` {
		safeMime, _ = json.Marshal("application/octet-stream")
	}
	return fmt.Sprintf(`
(() => {
  const chunks = window.%s || [];
  const binary = atob(chunks.join(''));
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  const blob = new Blob([bytes], { type: %s });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = %s;
  a.style.display = 'none';
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
  delete window.%s;
  return 'ok';
})()
`, cdpBlobChunkVar, string(safeMime), string(safeName), cdpBlobChunkVar)
}

// CdpBlobStagingClient stages files onto a remote Chrome download directory via CDP.
type CdpBlobStagingClient struct {
	config CdpBlobStagingConfig
}

// NewCdpBlobStagingClient constructs a CDP blob staging client.
func NewCdpBlobStagingClient(cfg CdpBlobStagingConfig) *CdpBlobStagingClient {
	return &CdpBlobStagingClient{config: cfg}
}

// StagePreparedFilesViaCDPBlob stages files using CDP blob download onto the remote host.
func StagePreparedFilesViaCDPBlob(ctx context.Context, cdpURL string, prepared []map[string]any) ([]map[string]any, error) {
	cfg, err := ResolveCdpBlobStagingConfig(cdpURL)
	if err != nil {
		return nil, err
	}
	return NewCdpBlobStagingClient(*cfg).StagePreparedFiles(ctx, prepared)
}

// StagePreparedFiles implements the CDP blob staging protocol from Python.
func (c *CdpBlobStagingClient) StagePreparedFiles(ctx context.Context, prepared []map[string]any) ([]map[string]any, error) {
	if len(prepared) == 0 {
		return nil, nil
	}
	httpBase, err := normalizeHTTPCDPBase(c.config.CDPURL)
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{Timeout: c.config.Timeout}
	browserWS, err := resolveBrowserWSURL(ctx, httpClient, httpBase)
	if err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{HandshakeTimeout: c.config.Timeout}
	conn, _, err := dialer.DialContext(ctx, browserWS, nil)
	if err != nil {
		return nil, &CdpBlobStagingError{Msg: fmt.Sprintf("CDP websocket dial failed: %v", err)}
	}
	cdp := newCDPWSSession(conn, c.config.Timeout)
	defer cdp.close()

	stagingPrefix := strings.ReplaceAll(uuid.NewString(), "-", "")
	remoteDir := cdpPathForChrome(c.config.DownloadDir)

	created, err := cdp.send(ctx, "Target.createTarget", map[string]any{"url": "about:blank"}, "")
	if err != nil {
		return nil, err
	}
	targetID, _ := created["targetId"].(string)
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return nil, &CdpBlobStagingError{Msg: "Target.createTarget(about:blank) returned no targetId"}
	}
	defer func() {
		_, _ = cdp.send(context.WithoutCancel(ctx), "Target.closeTarget", map[string]any{"targetId": targetID}, "")
	}()

	attach, err := cdp.send(ctx, "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, "")
	if err != nil {
		return nil, err
	}
	pageSessionID, _ := attach["sessionId"].(string)
	pageSessionID = strings.TrimSpace(pageSessionID)
	if pageSessionID == "" {
		return nil, &CdpBlobStagingError{Msg: "Target.attachToTarget returned no sessionId"}
	}
	if _, err := cdp.send(ctx, "Runtime.enable", nil, pageSessionID); err != nil {
		return nil, err
	}
	if _, err := cdp.send(ctx, "Page.enable", nil, pageSessionID); err != nil {
		return nil, err
	}
	downloadBehavior := map[string]any{
		"behavior":      "allow",
		"downloadPath":  remoteDir,
		"eventsEnabled": true,
	}
	if _, err := cdp.send(ctx, "Browser.setDownloadBehavior", downloadBehavior, ""); err != nil {
		return nil, err
	}
	_, _ = cdp.send(ctx, "Page.setDownloadBehavior", downloadBehavior, pageSessionID)

	staged := make([]map[string]any, 0, len(prepared))
	for _, item := range prepared {
		one, err := c.stageSingle(ctx, cdp, pageSessionID, remoteDir, item, stagingPrefix)
		if err != nil {
			return nil, err
		}
		staged = append(staged, one)
	}
	return staged, nil
}

type downloadTracker struct {
	expectedBytes int64
	completedPath string
	canceled      bool
	done          chan struct{}
	once          sync.Once
}

func (t *downloadTracker) markDone() {
	t.once.Do(func() { close(t.done) })
}

func (c *CdpBlobStagingClient) stageSingle(
	ctx context.Context,
	cdp *cdpWSSession,
	pageSessionID, remoteDir string,
	item map[string]any,
	stagingPrefix string,
) (map[string]any, error) {
	localPath, _ := item["local_path"].(string)
	localPath = strings.TrimSpace(localPath)
	info, err := os.Stat(localPath)
	if err != nil || info.IsDir() {
		return nil, &CdpBlobStagingError{Msg: fmt.Sprintf("local upload file does not exist: %s", localPath)}
	}
	if info.Size() <= 0 {
		return nil, &CdpBlobStagingError{Msg: fmt.Sprintf("local upload file is empty: %s", localPath)}
	}
	if info.Size() > c.config.MaxBytes {
		return nil, &CdpBlobStagingError{Msg: fmt.Sprintf(
			"local upload file exceeds blob staging max size (%d bytes): %s", c.config.MaxBytes, localPath)}
	}
	filename := safeUploadFilename(fmt.Sprint(item["name"]))
	if filename == "" || fmt.Sprint(item["name"]) == "" {
		filename = safeUploadFilename(filepath.Base(localPath))
	}
	mimeType := mime.TypeByExtension(filepath.Ext(filename))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	raw, err := os.ReadFile(localPath)
	if err != nil {
		return nil, &CdpBlobStagingError{Msg: fmt.Sprintf("read local upload file failed: %v", err)}
	}

	tracker := &downloadTracker{expectedBytes: info.Size(), done: make(chan struct{})}
	expectedRemote := cdpPathForChrome(joinRemoteWinPath(remoteDir, filename))
	cdp.addEventHandler(func(payload map[string]any) {
		method, _ := payload["method"].(string)
		params, _ := payload["params"].(map[string]any)
		if params == nil {
			params = map[string]any{}
		}
		switch method {
		case "Page.downloadWillBegin", "Browser.downloadWillBegin":
			suggested, _ := params["suggestedFilename"].(string)
			if suggested == "" {
				suggested, _ = params["fileName"].(string)
			}
			if suggested == "" {
				suggested = filename
			}
			tracker.completedPath = cdpPathForChrome(joinRemoteWinPath(remoteDir, suggested))
		case "Page.downloadProgress", "Browser.downloadProgress":
			state, _ := params["state"].(string)
			switch state {
			case "completed":
				if tracker.completedPath == "" {
					tracker.completedPath = expectedRemote
				}
				tracker.markDone()
			case "canceled":
				tracker.canceled = true
				tracker.markDone()
			}
		}
	})

	if _, err := cdp.send(ctx, "Runtime.evaluate", map[string]any{
		"expression":  fmt.Sprintf("window.%s = [];", cdpBlobChunkVar),
		"awaitPromise": false,
	}, pageSessionID); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	for i := 0; i < len(encoded); i += c.config.ChunkChars {
		end := i + c.config.ChunkChars
		if end > len(encoded) {
			end = len(encoded)
		}
		chunkJSON, _ := json.Marshal(encoded[i:end])
		if _, err := cdp.send(ctx, "Runtime.evaluate", map[string]any{
			"expression":   fmt.Sprintf("window.%s.push(%s);", cdpBlobChunkVar, string(chunkJSON)),
			"awaitPromise": false,
		}, pageSessionID); err != nil {
			return nil, err
		}
	}
	result, err := cdp.send(ctx, "Runtime.evaluate", map[string]any{
		"expression":    buildTriggerDownloadJS(filename, mimeType),
		"awaitPromise":  false,
		"returnByValue": true,
	}, pageSessionID)
	if err != nil {
		return nil, err
	}
	if err := FormatCDPEvaluateResultError(result); err != nil {
		return nil, &CdpBlobStagingError{Msg: fmt.Sprintf("blob download trigger failed: %v", err)}
	}
	if resultObj, ok := result["result"].(map[string]any); ok {
		if v := resultObj["value"]; v != nil && v != "ok" {
			return nil, &CdpBlobStagingError{Msg: fmt.Sprintf("blob download trigger returned unexpected value: %v", v)}
		}
	}

	wait := c.config.Timeout / 4
	if wait < 5*time.Second {
		wait = 5 * time.Second
	}
	if wait > 30*time.Second {
		wait = 30 * time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-tracker.done:
	case <-timer.C:
		if c.config.RequireDownloadEvent {
			return nil, &CdpBlobStagingError{Msg: fmt.Sprintf(
				"Blob+CDP staging failed: no download completion for %s. The file was NOT written to %s. "+
					"Ensure this Windows folder exists and is writable, then retry. "+
					"If it still fails, switch Browser remote upload mode to HTTP staging and run "+
					"tools/browser_remote_staging gateway on the Chrome host (recommended).",
				filename, remoteDir)}
		}
		tracker.completedPath = expectedRemote
	}
	if tracker.canceled {
		return nil, &CdpBlobStagingError{Msg: fmt.Sprintf("CDP download canceled for %s", filename)}
	}
	remotePath := tracker.completedPath
	if remotePath == "" {
		remotePath = expectedRemote
	}
	out := cloneStringAnyMap(item)
	out["local_path"] = remotePath
	out["remote_path"] = remotePath
	out["staging_session_id"] = stagingPrefix
	out["staging_mode"] = "blob_cdp"
	out["name"] = filename
	out["size"] = info.Size()
	return out, nil
}
