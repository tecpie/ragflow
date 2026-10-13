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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ragflow/internal/agent/runtime"
	"ragflow/internal/dao"
	"ragflow/internal/storage"

	"gorm.io/gorm"
)

var opaqueIDRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}$|^[0-9a-f]{32}$`)

type browserUploadSourceItem struct {
	Kind      string
	FileID    string
	Name      string
	CreatedBy string
	URL       string
}

func looksLikeOpaqueID(s string) bool {
	return opaqueIDRE.MatchString(strings.TrimSpace(s))
}

func isHTTPURL(s string) bool {
	token := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(token, "http://") || strings.HasPrefix(token, "https://")
}

func resolveUploadURLMaxBytes() int64 {
	const def = int64(100 * 1024 * 1024)
	raw := strings.TrimSpace(os.Getenv("RAGFLOW_BROWSER_UPLOAD_URL_MAX_BYTES"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("RAGFLOW_BROWSER_REMOTE_STAGING_MAX_BYTES"))
	}
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func resolveUploadSourceItems(raw any, state *runtime.CanvasState) []browserUploadSourceItem {
	items := make([]browserUploadSourceItem, 0)
	seen := map[string]struct{}{}
	tenantID := ""
	if state != nil {
		tenantID, _ = state.Sys["tenant_id"].(string)
	}

	appendItem := func(item browserUploadSourceItem) {
		keyBytes, _ := json.Marshal(item)
		key := string(keyBytes)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		items = append(items, item)
	}

	var collect func(any)
	collect = func(item any) {
		if item == nil {
			return
		}
		switch v := item.(type) {
		case string:
			token := strings.TrimSpace(v)
			if token == "" {
				return
			}
			if strings.Contains(token, "{") && state != nil {
				if resolved, err := runtime.ResolveTemplate(token, state); err == nil && resolved != "" {
					token = strings.TrimSpace(resolved)
				}
			}
			if (strings.HasPrefix(token, "[") && strings.HasSuffix(token, "]")) ||
				(strings.HasPrefix(token, "{") && strings.HasSuffix(token, "}")) {
				var parsed any
				if json.Unmarshal([]byte(token), &parsed) == nil {
					collect(parsed)
					return
				}
			}
			if isHTTPURL(token) {
				appendItem(browserUploadSourceItem{Kind: "url", URL: token})
				return
			}
			if strings.Contains(token, ",") {
				for _, part := range strings.Split(token, ",") {
					collect(part)
				}
				return
			}
			appendItem(browserUploadSourceItem{Kind: "file_id", FileID: token})
		case map[string]any:
			fileID := strings.TrimSpace(fmt.Sprint(firstNonEmpty(v["file_id"], v["id"])))
			displayName := strings.TrimSpace(fmt.Sprint(firstNonEmpty(v["name"], v["filename"])))
			createdBy := strings.TrimSpace(fmt.Sprint(firstNonEmpty(v["created_by"], v["tenant_id"], tenantID)))
			if fileID != "" && displayName != "" {
				appendItem(browserUploadSourceItem{Kind: "session_blob", FileID: fileID, Name: displayName, CreatedBy: createdBy})
				return
			}
			if fileID != "" {
				appendItem(browserUploadSourceItem{Kind: "file_id", FileID: fileID, Name: displayName, CreatedBy: createdBy})
				return
			}
			u := strings.TrimSpace(fmt.Sprint(firstNonEmpty(v["url"], v["value"])))
			if isHTTPURL(u) {
				appendItem(browserUploadSourceItem{Kind: "url", URL: u})
				return
			}
			for _, k := range []string{"file_id", "id", "url", "value"} {
				if _, ok := v[k]; ok {
					collect(v[k])
					return
				}
			}
			for _, child := range v {
				collect(child)
			}
		case []any:
			for _, child := range v {
				collect(child)
			}
		default:
			token := strings.TrimSpace(fmt.Sprint(v))
			if token != "" {
				collect(token)
			}
		}
	}
	collect(raw)
	return items
}

func firstNonEmpty(values ...any) any {
	for _, v := range values {
		if v == nil {
			continue
		}
		s := strings.TrimSpace(fmt.Sprint(v))
		if s != "" && s != "<nil>" {
			return v
		}
	}
	return ""
}

func uniqueLocalPath(dir, name string) string {
	base := filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	if base == "" || base == "." {
		base = "upload.bin"
	}
	path := filepath.Join(dir, base)
	for i := 1; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		ext := filepath.Ext(base)
		path = filepath.Join(dir, fmt.Sprintf("%s_%d%s", stem, i, ext))
	}
}

func writeBlobFile(path string, blob []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, blob, 0o644)
}

func fetchSessionBlob(ctx context.Context, createdBy, fileID string) ([]byte, error) {
	if createdBy == "" || fileID == "" {
		return nil, fmt.Errorf("missing created_by/file_id")
	}
	store := storage.GetStorageFactory().GetStorage()
	if store == nil {
		return nil, fmt.Errorf("storage not initialized")
	}
	return store.Get(ctx, createdBy+"-downloads", fileID)
}

func prepareUploadURLFile(ctx context.Context, rawURL, uploadDir string) (map[string]any, error) {
	maxBytes := resolveUploadURLMaxBytes()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("url upload exceeds max bytes (%d)", maxBytes)
	}
	name := filepath.Base(strings.Split(rawURL, "?")[0])
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := parseContentDisposition(cd); err == nil {
			if fn := params["filename"]; fn != "" {
				name = fn
			}
		}
	}
	name = NormalizeUploadFilename(name)
	if name == "" {
		name = "download.bin"
	}
	localPath := uniqueLocalPath(uploadDir, name)
	if err := writeBlobFile(localPath, data); err != nil {
		return nil, err
	}
	return map[string]any{
		"file_id":    rawURL,
		"name":       name,
		"size":       len(data),
		"local_path": localPath,
	}, nil
}

func parseContentDisposition(header string) (string, map[string]string, error) {
	// Minimal parser: filename="..."
	params := map[string]string{}
	lower := strings.ToLower(header)
	idx := strings.Index(lower, "filename=")
	if idx < 0 {
		return "", params, fmt.Errorf("no filename")
	}
	rest := strings.TrimSpace(header[idx+len("filename="):])
	if strings.HasPrefix(rest, `"`) {
		end := strings.Index(rest[1:], `"`)
		if end >= 0 {
			params["filename"] = rest[1 : 1+end]
			return "attachment", params, nil
		}
	}
	params["filename"] = strings.Split(rest, ";")[0]
	return "attachment", params, nil
}

func prepareUploadFiles(ctx context.Context, db *gorm.DB, state *runtime.CanvasState, sources any, uploadDir string) ([]map[string]any, error) {
	items := resolveUploadSourceItems(sources, state)
	prepared := make([]map[string]any, 0, len(items))
	tenantID := ""
	if state != nil {
		tenantID, _ = state.Sys["tenant_id"].(string)
	}
	fileDAO := dao.NewFileDAO()
	store := storage.GetStorageFactory().GetStorage()

	for _, item := range items {
		switch item.Kind {
		case "url":
			one, err := prepareUploadURLFile(ctx, item.URL, uploadDir)
			if err != nil || one == nil {
				continue
			}
			prepared = append(prepared, one)
		case "session_blob":
			if item.FileID == "" || item.Name == "" {
				continue
			}
			createdBy := item.CreatedBy
			if createdBy == "" {
				createdBy = tenantID
			}
			blob, err := fetchSessionBlob(ctx, createdBy, item.FileID)
			if err != nil || len(blob) == 0 {
				continue
			}
			localPath := uniqueLocalPath(uploadDir, item.Name)
			if err := writeBlobFile(localPath, blob); err != nil {
				continue
			}
			prepared = append(prepared, map[string]any{
				"file_id":    item.FileID,
				"name":       item.Name,
				"size":       len(blob),
				"local_path": localPath,
			})
		default:
			fileID := item.FileID
			if fileID == "" {
				continue
			}
			if isHTTPURL(fileID) {
				one, err := prepareUploadURLFile(ctx, fileID, uploadDir)
				if err != nil || one == nil {
					continue
				}
				prepared = append(prepared, one)
				continue
			}
			file, err := fileDAO.GetByID(ctx, db, fileID)
			if err != nil || file == nil {
				createdBy := item.CreatedBy
				if createdBy == "" {
					createdBy = tenantID
				}
				blob, berr := fetchSessionBlob(ctx, createdBy, fileID)
				if berr != nil || len(blob) == 0 {
					continue
				}
				name := item.Name
				if name == "" || looksLikeOpaqueID(name) {
					name = fileID + ".bin"
				}
				localPath := uniqueLocalPath(uploadDir, name)
				if err := writeBlobFile(localPath, blob); err != nil {
					continue
				}
				prepared = append(prepared, map[string]any{
					"file_id":    fileID,
					"name":       name,
					"size":       len(blob),
					"local_path": localPath,
				})
				continue
			}
			if store == nil || file.Location == nil || strings.TrimSpace(*file.Location) == "" {
				continue
			}
			blob, err := store.Get(ctx, file.ParentID, *file.Location)
			if err != nil || len(blob) == 0 {
				continue
			}
			name := item.Name
			if name == "" || looksLikeOpaqueID(name) {
				name = file.Name
			}
			if name == "" {
				name = file.ID + ".bin"
			}
			localPath := uniqueLocalPath(uploadDir, name)
			if err := writeBlobFile(localPath, blob); err != nil {
				continue
			}
			prepared = append(prepared, map[string]any{
				"file_id":    file.ID,
				"name":       name,
				"size":       file.Size,
				"local_path": localPath,
			})
		}
	}
	return prepared, nil
}

func buildUploadTaskAppendix(uploaded []map[string]any, remoteHost bool) string {
	if len(uploaded) == 0 {
		return ""
	}
	pathLabel := "local_path"
	locationHint := "Preloaded upload files (from Browser node upload_sources). Use upload_file with these exact local paths (CDP DOM.setFileInputFiles):\n"
	if remoteHost {
		pathLabel = "remote_path"
		locationHint = "Preloaded upload files (from Browser node upload_sources, staged via HTTP to remote Chrome host). Use upload_file with the exact remote_path values (CDP DOM.setFileInputFiles):\n"
	}
	lines := make([]string, 0, len(uploaded))
	for _, item := range uploaded {
		path, _ := item["local_path"].(string)
		if strings.TrimSpace(path) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- file_id=%v, name=%v, %s=%s", item["file_id"], item["name"], pathLabel, path))
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n" + locationHint + strings.Join(lines, "\n") + buildUploadExecutionHints(remoteHost)
}

func buildUploadSystemExtension(uploaded []map[string]any, remoteHost bool) string {
	if len(uploaded) == 0 {
		return ""
	}
	names := make([]string, 0, len(uploaded))
	for _, item := range uploaded {
		name := strings.TrimSpace(fmt.Sprint(firstNonEmpty(item["name"], item["file_id"], "file")))
		names = append(names, name)
	}
	hostNote := "Files were prepared locally before this task started."
	pickerRule := "- Prefer upload_file on input[type=file] without opening the OS file picker when possible.\n"
	if remoteHost {
		hostNote = "Files were pushed to the remote Chrome host via HTTP staging before this task started."
		pickerRule = "- CRITICAL (remote CDP): NEVER click Upload / Browse / Choose file / any button that opens the OS file picker.\n" +
			"  That native Windows dialog CANNOT be controlled by CDP and will block the task.\n" +
			"- Find the hidden input[type=file] in the target row (selector map index) and call upload_file(index, remote_path) DIRECTLY without opening the file dialog.\n"
	}
	return "\n\n[RAGFlow Browser upload policy]\n" +
		"- Upload sources (configured on this Browser node): " + strings.Join(names, ", ") + "\n" +
		"- " + hostNote + "\n" +
		"- Follow the user task to navigate to the correct page and table row.\n" +
		pickerRule +
		"- upload_file uses CDP DOM.setFileInputFiles; RAGFlow dispatches composed input/change events automatically.\n" +
		"- After upload_file, click only the confirm/submit control for that upload slot if the UI requires a separate confirm step.\n" +
		"- BEFORE uploading: locate the correct target slot/row/field. If success criteria already appear satisfied, call done immediately — do NOT upload again.\n" +
		"- After ONE upload_file + optional confirm + short wait: re-check using the success criteria from the USER task. If satisfied OR upload_file returned success, call done. Do NOT loop indefinitely."
}

func buildUploadExecutionHints(remoteHost bool) string {
	if remoteHost {
		return "\n\nUpload execution rules:\n" +
			"1. Navigate using the user task.\n" +
			"2. Locate the target upload slot from the user task context.\n" +
			"3. REMOTE CDP: Do NOT click buttons that open the native OS file picker.\n" +
			"4. Locate input[type=file] and call upload_file(index, remote_path) directly.\n" +
			"5. If multiple file inputs exist, pick the one that matches the user task context.\n" +
			"6. RAGFlow automatically dispatches composed input/change events after upload_file (CDP).\n" +
			"7. Only AFTER upload_file, click that slot's confirm/submit control if required.\n" +
			"8. Perform upload_file (+ confirm if needed) at most TWICE for the same target, then call done if upload_file succeeded.\n"
	}
	return "\n\nUpload execution rules:\n" +
		"1. Navigate using the user task.\n" +
		"2. Prefer upload_file on input[type=file] without opening the OS file picker.\n" +
		"3. Call upload_file on the file input index in that same context only.\n" +
		"4. RAGFlow automatically dispatches composed input/change events after upload_file (CDP).\n" +
		"5. Then click that slot's confirm control if needed and wait briefly.\n" +
		"6. Perform upload_file at most TWICE for the same target, then call done if upload_file succeeded.\n"
}

func stageUploadFilesOnRemoteHost(
	ctx context.Context,
	prepared []map[string]any,
	cdpURL, stagingURL, stagingToken, uploadMode string,
) ([]map[string]any, error) {
	if len(prepared) == 0 {
		return prepared, nil
	}
	mode := ResolveRemoteUploadMode(uploadMode)
	if mode == "auto" {
		if ResolveRemoteStagingConfig(stagingURL, stagingToken, cdpURL) != nil {
			mode = "staging"
		} else {
			mode = "blob_cdp"
		}
	}
	if mode == "staging" {
		cfg := ResolveRemoteStagingConfig(stagingURL, stagingToken, cdpURL)
		if cfg == nil {
			return nil, &RemoteStagingError{Msg: "Remote CDP browser requires remote staging for file uploads. " +
				"Configure Browser node remote_staging_url or env RAGFLOW_BROWSER_REMOTE_STAGING_URL, " +
				"and deploy tools/browser_remote_staging gateway on the Chrome host."}
		}
		client := NewRemoteStagingClient(*cfg)
		if ok, reason := client.HealthCheckDetail(ctx); !ok {
			return nil, &RemoteStagingError{Msg: fmt.Sprintf("Remote staging server is unreachable or unhealthy: %s/health (%s)", cfg.BaseURL, reason)}
		}
		return client.StagePreparedFiles(ctx, prepared)
	}
	staged, err := StagePreparedFilesViaCDPBlob(ctx, cdpURL, prepared)
	if err != nil {
		if _, ok := err.(*CdpBlobStagingError); ok {
			return nil, err
		}
		return nil, &CdpBlobStagingError{Msg: fmt.Sprintf("CDP blob staging failed: %v", err)}
	}
	return staged, nil
}
