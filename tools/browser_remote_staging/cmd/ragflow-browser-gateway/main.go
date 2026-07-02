package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var safeNamePattern = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

type config struct {
	host         string
	port         int
	cdpUpstream  string
	stagingDir   string
	stagingToken string
	publicOrigin string
	maxBytes     int64
}

func loadConfig() config {
	port := 19080
	if raw := strings.TrimSpace(os.Getenv("BROWSER_GATEWAY_PORT")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			port = parsed
		}
	}

	maxBytes := int64(100 * 1024 * 1024)
	if raw := strings.TrimSpace(os.Getenv("BROWSER_STAGING_MAX_BYTES")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			maxBytes = parsed
		}
	}

	stagingDir := strings.TrimSpace(os.Getenv("BROWSER_STAGING_DIR"))
	if stagingDir == "" {
		programData := os.Getenv("ProgramData")
		if programData == "" {
			programData = `C:\ProgramData`
		}
		stagingDir = filepath.Join(programData, "ragflow", "browser-uploads")
	}

	return config{
		host:         envOr("BROWSER_GATEWAY_HOST", "0.0.0.0"),
		port:         port,
		cdpUpstream:  strings.TrimRight(envOr("BROWSER_CDP_UPSTREAM", "http://127.0.0.1:9222"), "/"),
		stagingDir:   stagingDir,
		stagingToken: strings.TrimSpace(os.Getenv("BROWSER_STAGING_TOKEN")),
		publicOrigin: strings.TrimRight(strings.TrimSpace(os.Getenv("BROWSER_GATEWAY_PUBLIC_ORIGIN")), "/"),
		maxBytes:     maxBytes,
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func safeFilename(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." {
		return fmt.Sprintf("upload_%d.bin", time.Now().UnixNano())
	}
	base = strings.ReplaceAll(base, "\\", "_")
	base = strings.ReplaceAll(base, "/", "_")
	base = strings.ReplaceAll(base, "\x00", "")
	base = strings.Trim(base, " .")
	if base == "" {
		return fmt.Sprintf("upload_%d.bin", time.Now().UnixNano())
	}
	return base
}

func safeSessionID(raw string) string {
	session := strings.Trim(safeNamePattern.ReplaceAllString(strings.TrimSpace(raw), "_"), "._")
	if session == "" {
		session = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return session
}

func authorized(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "Bearer "+token {
		return true
	}
	return strings.TrimSpace(r.Header.Get("X-Staging-Token")) == token
}

func resolvePublicOrigin(r *http.Request, cfg config) string {
	if cfg.publicOrigin != "" {
		return cfg.publicOrigin
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return fmt.Sprintf("http://127.0.0.1:%d", cfg.port)
	}
	scheme := "http"
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + host
}

func rewriteRemoteURL(rawURL, publicOrigin, upstreamOrigin string) string {
	token := strings.TrimSpace(rawURL)
	if token == "" {
		return token
	}
	parsed, err := url.Parse(token)
	if err != nil {
		return token
	}
	public, err := url.Parse(publicOrigin)
	if err != nil {
		return token
	}

	if parsed.Scheme == "ws" || parsed.Scheme == "wss" || parsed.Scheme == "http" || parsed.Scheme == "https" {
		scheme := public.Scheme
		if parsed.Scheme == "ws" || parsed.Scheme == "wss" {
			if public.Scheme == "https" {
				scheme = "wss"
			} else {
				scheme = "ws"
			}
		}
		parsed.Scheme = scheme
		if public.Host != "" {
			parsed.Host = public.Host
		}
		return parsed.String()
	}

	if strings.HasPrefix(token, "/") {
		scheme := "ws"
		if public.Scheme == "https" {
			scheme = "wss"
		}
		return scheme + "://" + public.Host + token
	}

	upstreamWS := strings.Replace(strings.Replace(upstreamOrigin, "https://", "wss://", 1), "http://", "ws://", 1)
	publicWS := strings.Replace(strings.Replace(publicOrigin, "https://", "wss://", 1), "http://", "ws://", 1)
	token = strings.ReplaceAll(token, upstreamOrigin, publicOrigin)
	token = strings.ReplaceAll(token, upstreamWS, publicWS)
	return token
}

func rewriteCDPJSON(body []byte, publicOrigin, upstreamOrigin string) []byte {
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return body
	}

	patch := func(item map[string]any) {
		for _, key := range []string{"webSocketDebuggerUrl", "devtoolsFrontendUrl"} {
			value, ok := item[key].(string)
			if ok {
				item[key] = rewriteRemoteURL(value, publicOrigin, upstreamOrigin)
			}
		}
	}

	switch typed := data.(type) {
	case map[string]any:
		patch(typed)
	case []any:
		for _, entry := range typed {
			if item, ok := entry.(map[string]any); ok {
				patch(item)
			}
		}
	}

	out, err := json.Marshal(data)
	if err != nil {
		return body
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func handleHealth(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      "ok",
			"staging_dir": cfg.stagingDir,
			"port":        cfg.port,
		})
	}
}

func handleStagingUpload(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, cfg.stagingToken) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		filename := safeFilename(r.URL.Query().Get("filename"))
		sessionID := safeSessionID(r.Header.Get("X-Staging-Session"))
		r.Body = http.MaxBytesReader(w, r.Body, cfg.maxBytes+1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": err.Error()})
			return
		}
		if len(body) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty body"})
			return
		}
		if int64(len(body)) > cfg.maxBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": fmt.Sprintf("file exceeds max size %d", cfg.maxBytes)})
			return
		}

		targetDir := filepath.Join(cfg.stagingDir, sessionID)
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		targetPath := filepath.Join(targetDir, filename)
		for index := 1; ; index++ {
			if _, err := os.Stat(targetPath); os.IsNotExist(err) {
				break
			}
			targetPath = filepath.Join(targetDir, fmt.Sprintf("%s_%d%s", strings.TrimSuffix(filename, filepath.Ext(filename)), index, filepath.Ext(filename)))
		}

		if err := os.WriteFile(targetPath, body, 0o644); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		absPath, err := filepath.Abs(targetPath)
		if err != nil {
			absPath = targetPath
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"path":       absPath,
			"name":       filepath.Base(targetPath),
			"size":       len(body),
			"session_id": sessionID,
		})
	}
}

func copyHeader(dst, src http.Header) {
	for key, values := range src {
		lkey := strings.ToLower(key)
		if lkey == "connection" || lkey == "keep-alive" || lkey == "transfer-encoding" || lkey == "upgrade" {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func handleHTTPProxy(cfg config) http.HandlerFunc {
	client := &http.Client{Timeout: 0, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return func(w http.ResponseWriter, r *http.Request) {
		upstreamURL, err := url.Parse(cfg.cdpUpstream)
		if err != nil {
			http.Error(w, "invalid cdp upstream", http.StatusInternalServerError)
			return
		}
		target := *upstreamURL
		target.Path = singleJoin(upstreamURL.Path, r.URL.Path)
		target.RawQuery = r.URL.RawQuery

		req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		copyHeader(req.Header, r.Header)
		req.Header.Del("Host")

		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		contentType := resp.Header.Get("Content-Type")
		if strings.Contains(strings.ToLower(contentType), "application/json") {
			body = rewriteCDPJSON(body, resolvePublicOrigin(r, cfg), cfg.cdpUpstream)
		}

		copyHeader(w.Header(), resp.Header)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
	}
}

func singleJoin(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

func dispatch(cfg config, wsProxy *websocketProxy) http.HandlerFunc {
	health := handleHealth(cfg)
	staging := handleStagingUpload(cfg)
	httpProxy := handleHTTPProxy(cfg)

	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			health(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/staging/upload":
			staging(w, r)
		case strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && strings.HasPrefix(r.URL.Path, "/devtools/"):
			wsProxy.serve(w, r)
		default:
			httpProxy(w, r)
		}
	}
}

func main() {
	cfg := loadConfig()
	if err := os.MkdirAll(cfg.stagingDir, 0o755); err != nil {
		log.Fatalf("create staging dir failed: %v", err)
	}

	addr := fmt.Sprintf("%s:%d", cfg.host, cfg.port)
	mux := http.NewServeMux()
	wsProxy := newWebSocketProxy(cfg.cdpUpstream)
	mux.Handle("/", dispatch(cfg, wsProxy))

	log.Printf("RAGFlow browser gateway listening on http://%s", addr)
	log.Printf("  cdp_upstream=%s", cfg.cdpUpstream)
	log.Printf("  staging_dir=%s", cfg.stagingDir)
	if cfg.stagingToken == "" {
		log.Printf("  auth=disabled (set BROWSER_STAGING_TOKEN in production)")
	} else {
		log.Printf("  auth=enabled")
	}
	if cfg.publicOrigin != "" {
		log.Printf("  public_origin=%s", cfg.publicOrigin)
	}
	log.Printf("Configure RAGFlow Browser node with the same URL for CDP and remote staging:")
	log.Printf("  http://<windows-host>:%d", cfg.port)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("gateway stopped: %v", err)
	}
}
