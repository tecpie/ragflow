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
	"strings"

	"ragflow/internal/dao"
	"ragflow/internal/entity"
	"ragflow/internal/ontology"
)

// playgroundForDatasets loads the ontologies named by each dataset's compilation
// template group and returns their merged playground projection plus the dataset
// ids that actually resolved one. A dataset whose group has no ontology operator
// is left out of the covered set.
func playgroundForDatasets(ctx context.Context, tenantID string, datasetIDs []string) (ontology.PlaygroundOntology, []string) {
	merged := ontology.PlaygroundOntology{}
	if dao.DB == nil || tenantID == "" || len(datasetIDs) == 0 {
		return merged, nil
	}
	kbs, err := dao.NewKnowledgebaseDAO().GetByIDs(ctx, dao.DB, datasetIDs)
	if err != nil || len(kbs) == 0 {
		return merged, nil
	}
	tmplDAO := dao.NewCompilationTemplateDAO()
	ontDAO := dao.NewOntologyDAO()
	cache := map[string]ontology.PlaygroundOntology{}
	var covered []string
	for _, kb := range kbs {
		if kb == nil || kb.TenantID != tenantID {
			continue
		}
		hit := false
		for _, groupID := range compilationGroupIDsOf(kb.ParserConfig) {
			templates, err := tmplDAO.ListByGroup(ctx, dao.DB, groupID)
			if err != nil {
				continue
			}
			for _, tmpl := range templates {
				if tmpl == nil || !strings.EqualFold(strings.TrimSpace(tmpl.Kind), "ontology") {
					continue
				}
				pg, ok := ontologyPlayground(ctx, ontDAO, tenantID, tmpl, cache)
				if !ok {
					continue
				}
				merged = mergePlayground(merged, pg)
				hit = true
			}
		}
		if hit {
			covered = append(covered, kb.ID)
		}
	}
	return merged, covered
}

func datasetsHaveOntology(ctx context.Context, tenantID string, datasetIDs []string) bool {
	_, covered := playgroundForDatasets(ctx, tenantID, datasetIDs)
	return len(covered) > 0
}

func ontologyPlayground(ctx context.Context, ontDAO *dao.OntologyDAO, tenantID string, tmpl *entity.CompilationTemplate, cache map[string]ontology.PlaygroundOntology) (ontology.PlaygroundOntology, bool) {
	cfg := map[string]any(tmpl.Config)
	id := configString(cfg, "ontology_id")
	name := configString(cfg, "ontology_name")
	if id == "" && name == "" {
		name = "Compiled Ontology"
	}
	key := id
	if key == "" {
		key = "name:" + name
	}
	if pg, ok := cache[key]; ok {
		return pg, len(pg.EntityTypes) > 0 || len(pg.Relationships) > 0
	}
	var row *entity.Ontology
	var err error
	if id != "" {
		row, err = ontDAO.GetByID(ctx, dao.DB, tenantID, id)
	} else {
		row, err = ontDAO.GetByName(ctx, dao.DB, tenantID, name)
	}
	if err != nil || row == nil || strings.TrimSpace(row.Turtle) == "" {
		cache[key] = ontology.PlaygroundOntology{}
		return ontology.PlaygroundOntology{}, false
	}
	desc := ""
	if row.Description != nil {
		desc = *row.Description
	}
	doc, err := ontology.ParseTurtle(row.Turtle, row.Name, desc, row.BaseIRI)
	if err != nil {
		cache[key] = ontology.PlaygroundOntology{}
		return ontology.PlaygroundOntology{}, false
	}
	pg := doc.ToPlayground()
	cache[key] = pg
	return pg, len(pg.EntityTypes) > 0 || len(pg.Relationships) > 0
}

func mergePlayground(dst, src ontology.PlaygroundOntology) ontology.PlaygroundOntology {
	if dst.Name == "" {
		dst.Name = src.Name
	}
	if dst.Description == "" {
		dst.Description = src.Description
	}
	seenClass := map[string]bool{}
	for _, et := range dst.EntityTypes {
		seenClass[et.ID] = true
	}
	for _, et := range src.EntityTypes {
		if et.ID == "" || seenClass[et.ID] {
			continue
		}
		seenClass[et.ID] = true
		dst.EntityTypes = append(dst.EntityTypes, et)
	}
	seenRel := map[string]bool{}
	for _, rel := range dst.Relationships {
		seenRel[rel.ID] = true
	}
	for _, rel := range src.Relationships {
		if rel.ID == "" || seenRel[rel.ID] {
			continue
		}
		seenRel[rel.ID] = true
		dst.Relationships = append(dst.Relationships, rel)
	}
	return dst
}

func compilationGroupIDsOf(cfg entity.JSONMap) []string {
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
			s, ok := item.(string)
			if ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}

func configString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}
