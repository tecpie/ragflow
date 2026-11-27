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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ragflow/internal/ontology"
)

func TestClientGetAndPlayground(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ontologies/onto-1", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"id":          "onto-1",
				"name":        "电力",
				"document":    `{"conceptTypes":[{"id":"S","name":"变电站"}]}`,
				"draftTurtle": "@prefix : <http://ex#> .",
				"baseIri":     "http://ex#",
				"source":      "modeling",
			},
		})
	})
	mux.HandleFunc("/api/v1/ontologies/onto-1/playground", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": ontology.PlaygroundOntology{
				Name: "电力",
				EntityTypes: []ontology.PlaygroundEntityType{
					{ID: "S", Name: "变电站"},
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(srv.URL, "")
	row, err := c.Get(context.Background(), "onto-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	ent := ToEntity(row)
	if ent == nil || ent.Name != "电力" || ent.Turtle == "" || ent.Document == "" {
		t.Fatalf("entity = %+v", ent)
	}
	pg, err := c.Playground(context.Background(), "onto-1")
	if err != nil || pg == nil || len(pg.EntityTypes) != 1 {
		t.Fatalf("Playground: %#v err=%v", pg, err)
	}
}

func TestClientGetByNameAndUpsertInduced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ontologies", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": []map[string]any{{"id": "onto-2", "name": "Compiled Ontology"}},
		})
	})
	mux.HandleFunc("/api/v1/ontologies/onto-2", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"id": "onto-2", "name": "Compiled Ontology", "source": "knowledge",
				"draftTurtle": "ttl", "document": "{}",
			},
		})
	})
	mux.HandleFunc("/api/v1/ingest/upsert-induced", func(w http.ResponseWriter, r *http.Request) {
		var body UpsertInducedRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if len(body.Classes) == 0 {
			t.Errorf("missing classes")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"id": "onto-2", "name": body.Name, "source": "knowledge"},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(srv.URL, "")
	row, err := c.GetByName(context.Background(), "Compiled Ontology")
	if err != nil || row == nil || row.ID != "onto-2" {
		t.Fatalf("GetByName: %+v err=%v", row, err)
	}
	out, err := c.UpsertInduced(context.Background(), UpsertInducedRequest{
		Name:    "Compiled Ontology",
		Classes: []ontology.InducedClass{{ID: "Person", Label: "Person"}},
	})
	if err != nil || out == nil || out.ID != "onto-2" {
		t.Fatalf("UpsertInduced: %+v err=%v", out, err)
	}
}

func TestClientRejectsBusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 100001, "message": "not found"})
	}))
	t.Cleanup(srv.Close)
	_, err := New(srv.URL, "").Get(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
}
