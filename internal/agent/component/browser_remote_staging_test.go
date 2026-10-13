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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagingBaseURLFromCDP(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"http://192.168.1.10:19080", "http://192.168.1.10:19080"},
		{"ws://host:9222/devtools/browser/abc", "http://host:9222"},
		{"wss://host:9222/devtools/browser/abc", "https://host:9222"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := StagingBaseURLFromCDP(tc.in); got != tc.want {
			t.Fatalf("StagingBaseURLFromCDP(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeUploadFilenameStripsUUIDPrefix(t *testing.T) {
	t.Parallel()
	got := NormalizeUploadFilename("a1b2c3d4e5f64789a1b2c3d4e5f64789_报告.pdf")
	if got != "报告.pdf" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveRemoteUploadMode(t *testing.T) {
	t.Setenv("RAGFLOW_BROWSER_REMOTE_UPLOAD_MODE", "")
	if got := ResolveRemoteUploadMode("blob"); got != "blob_cdp" {
		t.Fatalf("got %q", got)
	}
	if got := ResolveRemoteUploadMode(""); got != "auto" {
		t.Fatalf("got %q", got)
	}
}

func TestRemoteStagingClientUploadAndHealth(t *testing.T) {
	var gotSession, gotAuth, gotFilename string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case r.URL.Path == "/staging/upload":
			gotSession = r.Header.Get("X-Staging-Session")
			gotAuth = r.Header.Get("Authorization")
			gotFilename = r.URL.Query().Get("filename")
			gotBody, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"path":       `C:\ProgramData\ragflow\browser-uploads\` + gotFilename,
				"name":       gotFilename,
				"size":       len(gotBody),
				"session_id": gotSession,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	local := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(local, []byte("hello-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	client := NewRemoteStagingClient(RemoteStagingConfig{
		BaseURL:  srv.URL,
		Token:    "secret",
		Timeout:  0,
		MaxBytes: 1024 * 1024,
	})
	ok, reason := client.HealthCheckDetail(context.Background())
	if !ok {
		t.Fatalf("health: %s", reason)
	}
	staged, err := client.StagePreparedFiles(context.Background(), []map[string]any{
		{"local_path": local, "name": "hello.txt", "file_id": "f1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 1 {
		t.Fatalf("staged=%d", len(staged))
	}
	if !strings.Contains(fmtSprint(staged[0]["remote_path"]), "hello.txt") {
		t.Fatalf("remote_path=%v", staged[0]["remote_path"])
	}
	if gotAuth != "Bearer secret" || gotSession == "" || string(gotBody) != "hello-bytes" {
		t.Fatalf("auth=%q session=%q body=%q filename=%q", gotAuth, gotSession, gotBody, gotFilename)
	}
}

func fmtSprint(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(toString(v), `\`, `/`), "//", "/"))
}

func toString(v any) string {
	b, _ := json.Marshal(v)
	return strings.Trim(string(b), `"`)
}

func TestBuildDispatchInputEventsJS(t *testing.T) {
	t.Parallel()
	js := BuildDispatchInputEventsJS(42)
	if !strings.Contains(js, "42") || !strings.Contains(js, "dispatchEvent") {
		t.Fatalf("unexpected js: %s", js)
	}
	params, err := BuildCDPSetFileInputFilesParams(7, []string{`C:\a.pdf`, " "})
	if err != nil || len(params.Files) != 1 || params.BackendNodeID != 7 {
		t.Fatalf("params=%+v err=%v", params, err)
	}
}
