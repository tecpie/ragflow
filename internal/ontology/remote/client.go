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

package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ragflow/internal/common"
	"ragflow/internal/ontology"
)

const (
	// EnvOntologyServiceURL is the base URL of tecpie-ontology-service
	// (e.g. http://172.16.0.102:18101). Empty disables the remote client.
	EnvOntologyServiceURL = "ONTOLOGY_SERVICE_URL"
	// EnvOntologyServiceToken is an optional Bearer token for the service.
	EnvOntologyServiceToken = "ONTOLOGY_SERVICE_TOKEN"
)

// Ontology is the wire shape returned by tecpie-ontology-service.
type Ontology struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Document    string `json:"document"`
	DraftTurtle string `json:"draftTurtle"`
	BaseIRI     string `json:"baseIri"`
	Source      string `json:"source"`
}

// UpsertInducedRequest is POST /api/v1/ingest/upsert-induced.
type UpsertInducedRequest struct {
	OntologyID  string                     `json:"ontologyId,omitempty"`
	Code        string                     `json:"code,omitempty"`
	Name        string                     `json:"name,omitempty"`
	Description string                     `json:"description,omitempty"`
	BaseIRI     string                     `json:"baseIri,omitempty"`
	Classes     []ontology.InducedClass    `json:"classes,omitempty"`
	Properties  []ontology.InducedProperty `json:"properties,omitempty"`
}

// Client talks to tecpie-ontology-service.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

var (
	defaultOnce   sync.Once
	defaultClient *Client
)

// Enabled reports whether ONTOLOGY_SERVICE_URL is set.
func Enabled() bool {
	return strings.TrimSpace(common.GetEnv(EnvOntologyServiceURL)) != ""
}

// Default returns the process-wide client configured from env.
func Default() *Client {
	defaultOnce.Do(func() {
		defaultClient = New(
			common.GetEnv(EnvOntologyServiceURL),
			common.GetEnv(EnvOntologyServiceToken),
		)
	})
	return defaultClient
}

// New builds a client. baseURL may include a trailing slash.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Get loads an ontology by id.
func (c *Client) Get(ctx context.Context, id string) (*Ontology, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("ontology id is empty")
	}
	var out Ontology
	if err := c.do(ctx, http.MethodGet, "/api/v1/ontologies/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.ID) == "" {
		out.ID = id
	}
	return &out, nil
}

// GetByName finds an ontology whose name matches exactly (via keywords list).
func (c *Client) GetByName(ctx context.Context, name string) (*Ontology, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("ontology name is empty")
	}
	var list []Ontology
	path := "/api/v1/ontologies?keywords=" + url.QueryEscape(name)
	if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	for i := range list {
		if strings.TrimSpace(list[i].Name) == name {
			item := list[i]
			// List payload may omit draftTurtle/document; reload detail.
			if strings.TrimSpace(item.ID) != "" {
				detail, err := c.Get(ctx, item.ID)
				if err == nil && detail != nil {
					return detail, nil
				}
			}
			return &item, nil
		}
	}
	return nil, nil
}

// Playground returns the Playground JSON for an ontology id.
func (c *Client) Playground(ctx context.Context, id string) (*ontology.PlaygroundOntology, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("ontology id is empty")
	}
	var out ontology.PlaygroundOntology
	if err := c.do(ctx, http.MethodGet, "/api/v1/ontologies/"+url.PathEscape(id)+"/playground", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpsertInduced merges induced TBox into a remote ontology.
func (c *Client) UpsertInduced(ctx context.Context, req UpsertInducedRequest) (*Ontology, error) {
	var out *Ontology
	if err := c.do(ctx, http.MethodPost, "/api/v1/ingest/upsert-induced", req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, dest any) error {
	if c == nil || c.baseURL == "" {
		return fmt.Errorf("ontology service url is not configured")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ontology service %s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(string(raw), 256))
	}
	var envelope struct {
		Code    json.RawMessage `json:"code"`
		Data    json.RawMessage `json:"data"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("ontology service decode: %w", err)
	}
	if !isSuccessCode(envelope.Code) {
		msg := strings.TrimSpace(envelope.Message)
		if msg == "" {
			msg = string(envelope.Code)
		}
		return fmt.Errorf("ontology service %s %s: %s", method, path, msg)
	}
	if dest == nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, dest); err != nil {
		return fmt.Errorf("ontology service data decode: %w", err)
	}
	return nil
}

func isSuccessCode(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n == 0 || n == 200
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s == "0" || s == "200" || strings.EqualFold(s, "ok")
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
