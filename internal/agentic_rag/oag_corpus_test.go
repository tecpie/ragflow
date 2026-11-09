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

package agentic_rag

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"ragflow/internal/engine"
	enginetypes "ragflow/internal/engine/types"
	"ragflow/internal/ontology"
)

type oagQuestionFile struct {
	Questions []oagQuestion `yaml:"questions"`
}

type oagQuestion struct {
	ID               string   `yaml:"id"`
	Kind             string   `yaml:"kind"`
	Question         string   `yaml:"question"`
	ExpectClasses    []string `yaml:"expect_classes"`
	ExpectProperties []string `yaml:"expect_properties"`
	ExpectAnswers    []string `yaml:"expect_answers"`
	Sources          []string `yaml:"sources"`
}

type oagGraphFile struct {
	Entities  []oagNode `json:"entities"`
	Relations []oagEdge `json:"relations"`
}

type oagNode struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	DocID   string `json:"doc_id"`
	DocName string `json:"doc_name"`
	ChunkID string `json:"chunk_id"`
	Content string `json:"content"`
}

type oagEdge struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Type    string `json:"type"`
	DocID   string `json:"doc_id"`
	DocName string `json:"doc_name"`
	ChunkID string `json:"chunk_id"`
	Content string `json:"content"`
}

func TestOAGTemplateCoversQuestionVocabulary(t *testing.T) {
	questions := loadOAGQuestions(t)
	var file struct {
		Templates []struct {
			Kind   string `yaml:"kind"`
			Config struct {
				Entity struct {
					Fields []struct {
						Type string `yaml:"type"`
					} `yaml:"fields"`
				} `yaml:"entity"`
				Relation struct {
					Fields []struct {
						Type string `yaml:"type"`
					} `yaml:"fields"`
				} `yaml:"relation"`
			} `yaml:"config"`
		} `yaml:"templates"`
	}
	raw, err := os.ReadFile(oagTestdata(t, "template.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	classes := map[string]bool{}
	relations := map[string]bool{}
	var hasOntology bool
	for _, tmpl := range file.Templates {
		if tmpl.Kind == "ontology" {
			hasOntology = true
		}
		for _, field := range tmpl.Config.Entity.Fields {
			classes[field.Type] = true
		}
		for _, field := range tmpl.Config.Relation.Fields {
			relations[field.Type] = true
		}
	}
	if !hasOntology {
		t.Fatal("template group must include the ontology operator")
	}
	for _, q := range questions.Questions {
		for _, class := range q.ExpectClasses {
			if !classes[class] {
				t.Errorf("%s: class %q is not in the graph template", q.ID, class)
			}
		}
		for _, prop := range q.ExpectProperties {
			if !relations[prop] {
				t.Errorf("%s: relation %q is not in the graph template", q.ID, prop)
			}
		}
	}
	graph := loadOAGGraph(t)
	for _, node := range graph.Entities {
		if !classes[node.Type] {
			t.Errorf("fixture entity %s type %q is not in the template", node.Name, node.Type)
		}
	}
	for _, edge := range graph.Relations {
		if !relations[edge.Type] {
			t.Errorf("fixture relation %s is not in the template", edge.Type)
		}
	}
}

func TestOAGQuestionsWalkTheFixture(t *testing.T) {
	graph := loadOAGGraph(t)
	// A same-type edge whose endpoints violate domain/range. It is not part of
	// the documents; the walk must drop it.
	graph.Relations = append(graph.Relations, oagEdge{
		ID: "r-bad-domain", From: "江北220kV变电站", To: "城南500kV变电站", Type: "电源侧厂站",
		DocID: "02-lines.md", DocName: "02-lines.md", ChunkID: "c-bad", Content: "bad domain",
	})
	pg := playgroundFromGraph(t, graph)
	eng := newOAGEngine(graph)

	for _, q := range loadOAGQuestions(t).Questions {
		t.Run(q.ID, func(t *testing.T) {
			aligned := ontology.AlignLabels(q.Question, pg)
			for _, class := range q.ExpectClasses {
				if !alignmentHasClass(aligned, class) {
					t.Fatalf("classes = %+v, want %s", aligned.Classes, class)
				}
			}
			if q.Kind == "negative" {
				if !aligned.Empty() {
					t.Fatalf("negative question aligned to %+v", aligned)
				}
				out := renderKgExplore(context.Background(), q.Question, KgExploreResult{}, &aligned)
				if strings.Contains(out, "<subgraph>") {
					t.Fatalf("negative question rendered a subgraph:\n%s", out)
				}
				return
			}
			for _, prop := range aligned.Properties {
				found := false
				for _, want := range q.ExpectProperties {
					if prop.Label == want {
						found = true
					}
				}
				if !found && len(q.ExpectProperties) > 0 && q.Kind == "relation" {
					t.Fatalf("aligned property %q is not expected", prop.Label)
				}
			}
			got, err := NewKgAdapter(eng).Explore(context.Background(), KgExploreRequest{
				TenantID: "t1", DatasetIDs: []string{"kb1"}, Query: q.Question, Constraint: &aligned,
			})
			if err != nil {
				t.Fatalf("Explore: %v", err)
			}
			if got.GraphAbsent {
				t.Fatal("the fixture graph exists; a miss must not be a dataset absence")
			}
			blob := oagBlob(got)
			for _, answer := range q.ExpectAnswers {
				if !strings.Contains(blob, answer) {
					t.Errorf("answer %q missing from\n%s", answer, blob)
				}
			}
			for _, source := range q.Sources {
				if !strings.Contains(blob, source) {
					t.Errorf("source %q missing from\n%s", source, blob)
				}
			}
			if q.Kind == "class" && len(got.Relations) != 0 {
				t.Fatalf("class-only walk hopped: %+v", got.Relations)
			}
			if q.Kind == "relation" {
				var types []string
				for _, rel := range got.Relations {
					types = append(types, rel.Type)
					if rel.Type == "电源侧厂站" && rel.From == "江北220kV变电站" {
						t.Fatal("domain mismatch kept a 电源侧厂站 edge that does not start at 输电线路")
					}
				}
				for _, prop := range q.ExpectProperties {
					if !stringIn(types, prop) {
						t.Errorf("relations %v, want %s", types, prop)
					}
				}
				if stringIn(types, "负荷侧厂站") {
					t.Fatal("relation type 负荷侧厂站 is outside the alignment and must be dropped")
				}
			}
		})
	}
}

func playgroundFromGraph(t *testing.T, graph oagGraphFile) ontology.PlaygroundOntology {
	t.Helper()
	typeOf := map[string]string{}
	var classes []ontology.InducedClass
	seenClass := map[string]bool{}
	for _, node := range graph.Entities {
		typeOf[node.Name] = node.Type
		id := ontology.SafeLocalName(node.Type)
		if id == "" || seenClass[id] {
			continue
		}
		seenClass[id] = true
		classes = append(classes, ontology.InducedClass{ID: id, Label: node.Type})
	}
	var props []ontology.InducedProperty
	seenProp := map[string]bool{}
	for _, edge := range graph.Relations {
		if edge.ID == "r-bad-domain" {
			continue
		}
		id := ontology.SafeLocalName(edge.Type)
		if id == "" || seenProp[id] {
			continue
		}
		seenProp[id] = true
		props = append(props, ontology.InducedProperty{
			ID: id, Label: edge.Type,
			Domain: ontology.SafeLocalName(typeOf[edge.From]),
			Range:  ontology.SafeLocalName(typeOf[edge.To]),
		})
	}
	turtle := ontology.BuildTurtle("http://ragflow.local/ontology/power#", ontology.InducedSchema{Classes: classes, Properties: props})
	doc, err := ontology.ParseTurtle(turtle, "电力行业本体", "", "http://ragflow.local/ontology/power#")
	if err != nil {
		t.Fatalf("ParseTurtle: %v", err)
	}
	return doc.ToPlayground()
}

type oagFakeEngine struct {
	engine.DocEngine
	entityRows   []map[string]interface{}
	relationRows []map[string]interface{}
	passages     map[string]map[string]interface{}
}

func newOAGEngine(graph oagGraphFile) *oagFakeEngine {
	eng := &oagFakeEngine{passages: map[string]map[string]interface{}{}}
	for i, node := range graph.Entities {
		body, _ := json.Marshal(map[string]string{"name": node.Name, "type": node.Type})
		eng.entityRows = append(eng.entityRows, map[string]interface{}{
			"id":                  "e" + string(rune('a'+i)),
			"doc_id":              node.DocID,
			"docnm_kwd":           node.DocName,
			"kb_id":               "kb1",
			"content_with_weight": string(body),
			"source_chunk_ids":    []string{node.ChunkID},
			"name_kwd":            node.Name,
		})
		eng.passages[node.ChunkID] = map[string]interface{}{
			"id": node.ChunkID, "doc_id": node.DocID, "docnm_kwd": node.DocName, "kb_id": "kb1",
			"content_with_weight": node.Content,
		}
	}
	for _, edge := range graph.Relations {
		body, _ := json.Marshal(map[string]string{"type": edge.Type})
		eng.relationRows = append(eng.relationRows, map[string]interface{}{
			"id": edge.ID, "doc_id": edge.DocID, "docnm_kwd": edge.DocName, "kb_id": "kb1",
			"from_entity_kwd": edge.From, "to_entity_kwd": edge.To,
			"content_with_weight": string(body), "source_chunk_ids": []string{edge.ChunkID},
		})
		if _, ok := eng.passages[edge.ChunkID]; !ok {
			eng.passages[edge.ChunkID] = map[string]interface{}{
				"id": edge.ChunkID, "doc_id": edge.DocID, "docnm_kwd": edge.DocName, "kb_id": "kb1",
				"content_with_weight": edge.Content,
			}
		}
	}
	return eng
}

func (e *oagFakeEngine) Search(_ context.Context, req *enginetypes.SearchRequest) (*enginetypes.SearchResult, error) {
	if ids := oagStrings(req.Filter["id"]); len(ids) > 0 {
		var rows []map[string]interface{}
		for _, id := range ids {
			if row, ok := e.passages[id]; ok {
				rows = append(rows, row)
			}
		}
		return &enginetypes.SearchResult{Chunks: rows}, nil
	}
	kind, _ := req.Filter["knowledge_graph_kwd"].(string)
	var rows []map[string]interface{}
	switch kind {
	case "entity":
		rows = append([]map[string]interface{}(nil), e.entityRows...)
		if names := oagStrings(req.Filter["name_kwd"]); len(names) > 0 {
			rows = oagFilterNames(rows, names)
		}
		if text := oagMatchText(req); text != "" {
			rows = oagFilterEntityText(rows, text)
		}
	case "relation":
		rows = append([]map[string]interface{}(nil), e.relationRows...)
		if froms := oagStrings(req.Filter["from_entity_kwd"]); len(froms) > 0 {
			rows = oagFilterField(rows, "from_entity_kwd", froms)
		}
		if tos := oagStrings(req.Filter["to_entity_kwd"]); len(tos) > 0 {
			rows = oagFilterField(rows, "to_entity_kwd", tos)
		}
	}
	if req.Limit > 0 && len(rows) > req.Limit {
		rows = rows[:req.Limit]
	}
	return &enginetypes.SearchResult{Chunks: rows}, nil
}

func oagFilterEntityText(rows []map[string]interface{}, query string) []map[string]interface{} {
	var out []map[string]interface{}
	for _, row := range rows {
		var payload map[string]string
		_ = json.Unmarshal([]byte(kgStr(row["content_with_weight"])), &payload)
		if strings.Contains(query, payload["name"]) || strings.Contains(query, payload["type"]) {
			out = append(out, row)
		}
	}
	return out
}

func oagFilterNames(rows []map[string]interface{}, names []string) []map[string]interface{} {
	set := map[string]bool{}
	for _, name := range names {
		set[strings.ToLower(name)] = true
	}
	var out []map[string]interface{}
	for _, row := range rows {
		var payload map[string]string
		_ = json.Unmarshal([]byte(kgStr(row["content_with_weight"])), &payload)
		if set[strings.ToLower(payload["name"])] || set[strings.ToLower(kgStr(row["name_kwd"]))] {
			out = append(out, row)
		}
	}
	return out
}

func oagFilterField(rows []map[string]interface{}, field string, values []string) []map[string]interface{} {
	set := map[string]bool{}
	for _, value := range values {
		set[strings.ToLower(value)] = true
	}
	var out []map[string]interface{}
	for _, row := range rows {
		if set[strings.ToLower(kgStr(row[field]))] {
			out = append(out, row)
		}
	}
	return out
}

func oagMatchText(req *enginetypes.SearchRequest) string {
	for _, expr := range req.MatchExprs {
		if text, ok := expr.(*enginetypes.MatchTextExpr); ok {
			return text.MatchingText
		}
	}
	return ""
}

func oagStrings(v interface{}) []string {
	switch x := v.(type) {
	case []string:
		return x
	case string:
		if x != "" {
			return []string{x}
		}
	}
	return nil
}

func oagBlob(result KgExploreResult) string {
	var b strings.Builder
	for _, e := range result.Entities {
		b.WriteString(e.Name)
		b.WriteByte('\n')
	}
	for _, rel := range result.Relations {
		b.WriteString(rel.From)
		b.WriteByte(' ')
		b.WriteString(rel.Type)
		b.WriteByte(' ')
		b.WriteString(rel.To)
		b.WriteByte('\n')
	}
	for _, p := range result.Passages {
		b.WriteString(p.DocName)
		b.WriteByte('\n')
		b.WriteString(p.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func loadOAGQuestions(t *testing.T) oagQuestionFile {
	t.Helper()
	raw, err := os.ReadFile(oagTestdata(t, "questions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var file oagQuestionFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func loadOAGGraph(t *testing.T) oagGraphFile {
	t.Helper()
	raw, err := os.ReadFile(oagTestdata(t, "graph_fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file oagGraphFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func oagTestdata(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "ontology", "testdata", "oag", name)
}

func stringIn(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
