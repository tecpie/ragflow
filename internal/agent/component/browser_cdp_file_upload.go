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
	"encoding/json"
	"fmt"
	"strings"
)

// CdpFileUploadError is returned when CDP DOM.setFileInputFiles helpers fail.
type CdpFileUploadError struct {
	Msg string
}

func (e *CdpFileUploadError) Error() string { return e.Msg }

// BuildDispatchInputEventsJS returns JS that fires composed input/change after
// DOM.setFileInputFiles (Shadow DOM safe). Mirrors Python
// browser_cdp_file_upload.build_dispatch_input_events_js.
func BuildDispatchInputEventsJS(backendNodeID int) string {
	return fmt.Sprintf(`
(() => {
  const findByBackend = (root, id) => {
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT);
    let node = walker.currentNode;
    while (node) {
      if (node.backendNodeId === id) return node;
      node = walker.nextNode();
    }
    return null;
  };
  let input = null;
  for (const root of [document, ...Array.from(document.querySelectorAll('*')).flatMap(el => {
    try { return el.shadowRoot ? [el.shadowRoot] : []; } catch (e) { return []; }
  })]) {
    input = root.querySelector('input[type=file]') || findByBackend(root, %d);
    if (input) break;
  }
  if (!input) {
    const all = Array.from(document.querySelectorAll('input[type=file]'));
    input = all.length === 1 ? all[0] : null;
  }
  if (!input) return 'no file input found';
  input.dispatchEvent(new Event('input', { bubbles: true, composed: true }));
  input.dispatchEvent(new Event('change', { bubbles: true, composed: true }));
  return 'ok';
})()
`, backendNodeID)
}

// CDPSetFileInputFilesParams is the CDP DOM.setFileInputFiles payload.
type CDPSetFileInputFilesParams struct {
	Files         []string `json:"files"`
	BackendNodeID int      `json:"backendNodeId"`
}

// BuildCDPSetFileInputFilesParams validates and builds CDP upload params.
func BuildCDPSetFileInputFilesParams(backendNodeID int, filePaths []string) (*CDPSetFileInputFilesParams, error) {
	paths := make([]string, 0, len(filePaths))
	for _, p := range filePaths {
		if s := strings.TrimSpace(p); s != "" {
			paths = append(paths, s)
		}
	}
	if len(paths) == 0 {
		return nil, &CdpFileUploadError{Msg: "no file paths provided for CDP upload"}
	}
	if backendNodeID == 0 {
		return nil, &CdpFileUploadError{Msg: "backend_node_id is required for CDP upload"}
	}
	return &CDPSetFileInputFilesParams{Files: paths, BackendNodeID: backendNodeID}, nil
}

// FormatCDPEvaluateResultError extracts exceptionDetails from a CDP Runtime.evaluate result.
func FormatCDPEvaluateResultError(result map[string]any) error {
	if result == nil {
		return nil
	}
	if details, ok := result["exceptionDetails"]; ok && details != nil {
		raw, _ := json.Marshal(details)
		return &CdpFileUploadError{Msg: fmt.Sprintf("failed to dispatch input/change after CDP upload: %s", string(raw))}
	}
	return nil
}
