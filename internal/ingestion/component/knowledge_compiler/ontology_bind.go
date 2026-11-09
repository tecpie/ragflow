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

package knowledge_compiler

import (
	"context"
	"maps"
	"strings"

	"ragflow/internal/dao"
	"ragflow/internal/entity"
	"ragflow/internal/ingestion/component/knowledge_compiler/common"
	"ragflow/internal/ontology"

	"gorm.io/gorm"
)

// bindOntologyToGraphSpecs replaces a knowledge-graph template's entity and
// relation types with the ontology named by the ontology operator in the same
// group. ontology_id wins; otherwise ontology_name is used. The knowledge-graph
// operator's own type list is not the extraction schema.
func bindOntologyToGraphSpecs(ctx context.Context, db *gorm.DB, tenantID string, specs []common.TemplateInfo) {
	if db == nil || tenantID == "" || len(specs) == 0 {
		return
	}
	cache := map[string]ontology.PlaygroundOntology{}
	for i := range specs {
		if !graphExtractionKind(specs[i].Kind) {
			continue
		}
		pg, ok := playgroundForGraphSpec(ctx, db, tenantID, specs[i].ID, cache)
		if !ok {
			continue
		}
		specs[i].Config = overlayOntologyFields(specs[i].Config, pg)
	}
}

func graphExtractionKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "knowledge_graph", "knowledgegraph", "graph":
		return true
	default:
		return false
	}
}

func playgroundForGraphSpec(ctx context.Context, db *gorm.DB, tenantID, templateID string, cache map[string]ontology.PlaygroundOntology) (ontology.PlaygroundOntology, bool) {
	row, err := dao.NewCompilationTemplateDAO().GetTemplate(ctx, db, tenantID, templateID)
	if err != nil || row == nil || row.GroupID == nil || strings.TrimSpace(*row.GroupID) == "" {
		return ontology.PlaygroundOntology{}, false
	}
	groupID := strings.TrimSpace(*row.GroupID)
	if pg, ok := cache[groupID]; ok {
		return pg, len(pg.EntityTypes) > 0
	}
	siblings, err := dao.NewCompilationTemplateDAO().ListByGroup(ctx, db, groupID)
	if err != nil {
		cache[groupID] = ontology.PlaygroundOntology{}
		return ontology.PlaygroundOntology{}, false
	}
	ontDAO := dao.NewOntologyDAO()
	for _, sibling := range siblings {
		if sibling == nil || !strings.EqualFold(strings.TrimSpace(sibling.Kind), "ontology") {
			continue
		}
		pg, ok := playgroundForOntologyTemplate(ctx, db, ontDAO, tenantID, sibling)
		if !ok {
			continue
		}
		cache[groupID] = pg
		return pg, true
	}
	cache[groupID] = ontology.PlaygroundOntology{}
	return ontology.PlaygroundOntology{}, false
}

func playgroundForOntologyTemplate(ctx context.Context, db *gorm.DB, ontDAO *dao.OntologyDAO, tenantID string, tmpl *entity.CompilationTemplate) (ontology.PlaygroundOntology, bool) {
	cfg := map[string]any(tmpl.Config)
	id := configString(cfg, "ontology_id")
	name := configString(cfg, "ontology_name")
	if id == "" && name == "" {
		return ontology.PlaygroundOntology{}, false
	}
	var row *entity.Ontology
	var err error
	if id != "" {
		row, err = ontDAO.GetByID(ctx, db, tenantID, id)
	} else {
		row, err = ontDAO.GetByName(ctx, db, tenantID, name)
	}
	if err != nil || row == nil || strings.TrimSpace(row.Turtle) == "" {
		return ontology.PlaygroundOntology{}, false
	}
	desc := ""
	if row.Description != nil {
		desc = *row.Description
	}
	doc, err := ontology.ParseTurtle(row.Turtle, row.Name, desc, row.BaseIRI)
	if err != nil {
		return ontology.PlaygroundOntology{}, false
	}
	pg := doc.ToPlayground()
	return pg, len(pg.EntityTypes) > 0
}

func overlayOntologyFields(cfg map[string]any, pg ontology.PlaygroundOntology) map[string]any {
	out := maps.Clone(cfg)
	if out == nil {
		out = map[string]any{}
	}
	labels := map[string]string{}
	var entityFields []any
	for _, et := range pg.EntityTypes {
		label := strings.TrimSpace(et.Name)
		if label == "" {
			label = strings.TrimSpace(et.ID)
		}
		if label == "" {
			continue
		}
		labels[et.ID] = label
		item := map[string]any{"type": label}
		if d := strings.TrimSpace(et.Description); d != "" {
			item["description"] = d
		}
		entityFields = append(entityFields, item)
	}
	out["entity"] = map[string]any{
		"description": "Extract instances of the bound ontology. type must be one of the listed classes.",
		"fields":      entityFields,
	}
	var relationFields []any
	for _, rel := range pg.Relationships {
		if rel.Kind == "subclass" || rel.Name == "子类于" {
			continue
		}
		label := strings.TrimSpace(rel.Name)
		if label == "" {
			label = strings.TrimSpace(rel.ID)
		}
		if label == "" {
			continue
		}
		item := map[string]any{"type": label}
		from := labels[rel.From]
		to := labels[rel.To]
		desc := strings.TrimSpace(rel.Description)
		if from != "" && to != "" {
			arrow := from + " → " + to
			if desc != "" {
				desc = arrow + ". " + desc
			} else {
				desc = arrow
			}
		}
		if desc != "" {
			item["description"] = desc
		}
		relationFields = append(relationFields, item)
	}
	if len(relationFields) == 0 {
		delete(out, "relation")
		return out
	}
	out["relation"] = map[string]any{
		"description": "Extract relations of the bound ontology. type must be one of the listed relations, and the direction must follow the description.",
		"fields":      relationFields,
	}
	return out
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
