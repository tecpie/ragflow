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

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"ragflow/internal/dao"
	"ragflow/internal/engine"
	enginetypes "ragflow/internal/engine/types"
	"ragflow/internal/entity"
	"ragflow/internal/ontology"
)

const (
	ontologyInstanceLimit = 800
	ontologyInstancePage  = 200
)

// OntologyEntityInstance is one compiled-graph entity whose type is a class of the ontology.
type OntologyEntityInstance struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	TypeID      string `json:"type_id"`
	Description string `json:"description,omitempty"`
	DatasetID   string `json:"dataset_id"`
}

// OntologyRelationInstance is one compiled-graph relation whose type is a relation of the ontology.
type OntologyRelationInstance struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Type      string `json:"type"`
	TypeID    string `json:"type_id"`
	DatasetID string `json:"dataset_id"`
}

// OntologyInstances is the ABox extracted for one ontology.
type OntologyInstances struct {
	Entities  []OntologyEntityInstance   `json:"entities"`
	Relations []OntologyRelationInstance `json:"relations"`
}

// Instances lists compiled-graph entities and relations for datasets whose
// compilation template binds this ontology.
func (s *OntologyService) Instances(ctx context.Context, tenantID, id string) (*OntologyInstances, error) {
	row, err := s.dao.GetByID(ctx, dao.DB, tenantID, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	desc := ""
	if row.Description != nil {
		desc = *row.Description
	}
	doc, err := ontology.ParseTurtle(row.Turtle, row.Name, desc, row.BaseIRI)
	if err != nil {
		return nil, err
	}
	pg := doc.ToPlayground()
	out := &OntologyInstances{
		Entities:  []OntologyEntityInstance{},
		Relations: []OntologyRelationInstance{},
	}
	datasetIDs, err := datasetsBoundToOntology(ctx, tenantID, row)
	if err != nil {
		return nil, err
	}
	if len(datasetIDs) == 0 {
		return out, nil
	}
	de := engine.Get()
	if de == nil {
		return nil, errors.New("document engine is unavailable")
	}
	for _, kbID := range datasetIDs {
		if len(out.Entities) >= ontologyInstanceLimit {
			break
		}
		entityRows, err := searchGraphRows(ctx, de, tenantID, kbID, "entity", ontologyInstanceLimit-len(out.Entities))
		if err != nil {
			return nil, err
		}
		out.Entities = append(out.Entities, projectEntities(pg, kbID, entityRows)...)
		if len(out.Entities) > ontologyInstanceLimit {
			out.Entities = out.Entities[:ontologyInstanceLimit]
		}
	}
	names := map[string]bool{}
	for _, e := range out.Entities {
		names[strings.ToLower(e.Name)] = true
	}
	for _, kbID := range datasetIDs {
		if len(out.Relations) >= ontologyInstanceLimit {
			break
		}
		relRows, err := searchGraphRows(ctx, de, tenantID, kbID, "relation", ontologyInstanceLimit-len(out.Relations))
		if err != nil {
			return nil, err
		}
		out.Relations = append(out.Relations, projectRelations(pg, kbID, names, relRows)...)
		if len(out.Relations) > ontologyInstanceLimit {
			out.Relations = out.Relations[:ontologyInstanceLimit]
		}
	}
	return out, nil
}

func datasetsBoundToOntology(ctx context.Context, tenantID string, row *entity.Ontology) ([]string, error) {
	ids, err := dao.NewKnowledgebaseDAO().GetKBIDsByTenantIDSimple(ctx, dao.DB, tenantID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	kbs, err := dao.NewKnowledgebaseDAO().GetByIDs(ctx, dao.DB, ids)
	if err != nil {
		return nil, err
	}
	tmplDAO := dao.NewCompilationTemplateDAO()
	var bound []string
	for _, kb := range kbs {
		if kb == nil || kb.TenantID != tenantID {
			continue
		}
		if datasetBindsOntology(ctx, tmplDAO, kb, row) {
			bound = append(bound, kb.ID)
		}
	}
	return bound, nil
}

func datasetBindsOntology(ctx context.Context, tmplDAO *dao.CompilationTemplateDAO, kb *entity.Knowledgebase, row *entity.Ontology) bool {
	for _, groupID := range compilationGroupIDs(kb.ParserConfig) {
		templates, err := tmplDAO.ListByGroup(ctx, dao.DB, groupID)
		if err != nil {
			continue
		}
		for _, tmpl := range templates {
			if tmpl == nil || !strings.EqualFold(strings.TrimSpace(tmpl.Kind), "ontology") {
				continue
			}
			cfg := map[string]any(tmpl.Config)
			id := strings.TrimSpace(instanceField(cfg["ontology_id"]))
			name := strings.TrimSpace(instanceField(cfg["ontology_name"]))
			if id != "" && id == row.ID {
				return true
			}
			if id == "" && name != "" && strings.EqualFold(name, row.Name) {
				return true
			}
		}
	}
	return false
}

func searchGraphRows(ctx context.Context, de engine.DocEngine, tenantID, kbID, kind string, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		return nil, nil
	}
	var out []map[string]interface{}
	for offset := 0; len(out) < limit; offset += ontologyInstancePage {
		page := ontologyInstancePage
		if page > limit-len(out) {
			page = limit - len(out)
		}
		res, err := de.Search(ctx, &enginetypes.SearchRequest{
			IndexNames: []string{"ragflow_" + tenantID},
			KbIDs:      []string{kbID},
			SelectFields: []string{
				"id", "content_with_weight", "name_kwd", "entity_type_kwd",
				"from_entity_kwd", "to_entity_kwd", "type_kwd", "knowledge_graph_kwd",
			},
			Filter: map[string]interface{}{
				"or": []map[string]interface{}{
					{"type_kwd": kind},
					{"knowledge_graph_kwd": kind},
				},
			},
			Offset:             offset,
			Limit:              page,
			IncludeUnavailable: true,
		})
		if err != nil {
			if errors.Is(err, enginetypes.ErrIndexNotFound) {
				return out, nil
			}
			return nil, err
		}
		if res == nil || len(res.Chunks) == 0 {
			break
		}
		out = append(out, res.Chunks...)
		if len(res.Chunks) < page {
			break
		}
	}
	return out, nil
}

func projectEntities(pg ontology.PlaygroundOntology, datasetID string, rows []map[string]interface{}) []OntologyEntityInstance {
	seen := map[string]bool{}
	var out []OntologyEntityInstance
	for _, row := range rows {
		if graphRowKind(row) != "entity" {
			continue
		}
		payload := rowPayload(row)
		name := firstString(payload["name"], row["name_kwd"])
		if name == "" {
			continue
		}
		typeID, label, ok := matchClass(pg, firstString(payload["type"], row["entity_type_kwd"]))
		if !ok {
			continue
		}
		key := strings.ToLower(name) + "\x00" + strings.ToLower(label)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, OntologyEntityInstance{
			Name:        name,
			Type:        label,
			TypeID:      typeID,
			Description: instanceField(payload["description"]),
			DatasetID:   datasetID,
		})
	}
	return out
}

func projectRelations(pg ontology.PlaygroundOntology, datasetID string, names map[string]bool, rows []map[string]interface{}) []OntologyRelationInstance {
	seen := map[string]bool{}
	var out []OntologyRelationInstance
	for _, row := range rows {
		if graphRowKind(row) != "relation" {
			continue
		}
		payload := rowPayload(row)
		from := firstString(row["from_entity_kwd"], payload["from"])
		to := firstString(row["to_entity_kwd"], payload["to"])
		if from == "" || to == "" {
			continue
		}
		if !names[strings.ToLower(from)] || !names[strings.ToLower(to)] {
			continue
		}
		typeID, label, ok := matchRelation(pg, firstString(payload["type"], payload["relation"]))
		if !ok {
			continue
		}
		key := strings.ToLower(from) + "\x00" + strings.ToLower(to) + "\x00" + strings.ToLower(label)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, OntologyRelationInstance{
			From:      from,
			To:        to,
			Type:      label,
			TypeID:    typeID,
			DatasetID: datasetID,
		})
	}
	return out
}

func matchClass(pg ontology.PlaygroundOntology, raw string) (string, string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	for _, et := range pg.EntityTypes {
		label := strings.TrimSpace(et.Name)
		if label == "" {
			label = et.ID
		}
		if strings.EqualFold(et.ID, raw) || strings.EqualFold(label, raw) {
			return et.ID, label, true
		}
	}
	return "", "", false
}

func matchRelation(pg ontology.PlaygroundOntology, raw string) (string, string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	for _, rel := range pg.Relationships {
		if rel.Kind == "subclass" || rel.Name == "子类于" {
			continue
		}
		label := strings.TrimSpace(rel.Name)
		if label == "" {
			label = rel.ID
		}
		if strings.EqualFold(rel.ID, raw) || strings.EqualFold(label, raw) {
			return rel.ID, label, true
		}
	}
	return "", "", false
}

func graphRowKind(row map[string]interface{}) string {
	if kind := instanceField(row["type_kwd"]); kind != "" {
		return strings.ToLower(kind)
	}
	return strings.ToLower(instanceField(row["knowledge_graph_kwd"]))
}

func rowPayload(row map[string]interface{}) map[string]interface{} {
	raw := instanceField(row["content_with_weight"])
	if raw == "" {
		return map[string]interface{}{}
	}
	var payload map[string]interface{}
	if json.Unmarshal([]byte(raw), &payload) != nil || payload == nil {
		return map[string]interface{}{}
	}
	return payload
}

func firstString(values ...any) string {
	for _, v := range values {
		if s := instanceField(v); s != "" {
			return s
		}
	}
	return ""
}

func instanceField(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case []string:
		if len(t) > 0 {
			return strings.TrimSpace(t[0])
		}
	case []any:
		if len(t) > 0 {
			return instanceField(t[0])
		}
	}
	return ""
}

func compilationGroupIDs(cfg entity.JSONMap) []string {
	if len(cfg) == 0 {
		return nil
	}
	var out []string
	out = append(out, stringList(cfg["compilation_template_group_id"])...)
	out = append(out, stringList(cfg["compilation_template_group_ids"])...)
	return out
}

func stringList(raw any) []string {
	switch v := raw.(type) {
	case string:
		if s := strings.TrimSpace(v); s != "" {
			return []string{s}
		}
	case []string:
		var out []string
		for _, s := range v {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		var out []string
		for _, item := range v {
			if s := instanceField(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
