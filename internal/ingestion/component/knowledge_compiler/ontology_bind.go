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
	"fmt"
	"maps"
	"strings"

	"ragflow/internal/dao"
	"ragflow/internal/entity"
	"ragflow/internal/ontology"

	"gorm.io/gorm"
)

// prepareOntologyExtractionConfig resolves the ontology named by the template
// config and builds a structure hypergraph parser_config from the modeling
// document (preferred) or Turtle playground projection (fallback without
// datatype properties).
func prepareOntologyExtractionConfig(ctx context.Context, db *gorm.DB, tenantID string, cfg map[string]any) (map[string]any, error) {
	if db == nil {
		return nil, fmt.Errorf("ontology extraction: database is unavailable")
	}
	row, err := resolveOntologyRow(ctx, db, tenantID, cfg)
	if err != nil {
		return nil, err
	}
	if modeling, ok, err := ontology.ParseModelingDocument(row.Document); err == nil && ok {
		if len(modeling.Concepts) == 0 {
			return nil, fmt.Errorf("ontology %q has no concept types", row.Name)
		}
		return parserConfigFromModeling(cfg, modeling), nil
	}
	pg, err := playgroundFromOntologyRow(row)
	if err != nil {
		return nil, err
	}
	if len(pg.EntityTypes) == 0 {
		return nil, fmt.Errorf("ontology %q has no concept types", row.Name)
	}
	return overlayOntologyFields(cfg, pg), nil
}

func resolveOntologyRow(ctx context.Context, db *gorm.DB, tenantID string, cfg map[string]any) (*entity.Ontology, error) {
	id := configString(cfg, "ontology_id")
	name := configString(cfg, "ontology_name")
	if id == "" && name == "" {
		name = "Compiled Ontology"
	}
	ontDAO := dao.NewOntologyDAO()
	var row *entity.Ontology
	var err error
	if id != "" {
		row, err = ontDAO.GetByID(ctx, db, tenantID, id)
	} else {
		row, err = ontDAO.GetByName(ctx, db, tenantID, name)
	}
	if err != nil {
		return nil, err
	}
	if row == nil {
		if id != "" {
			return nil, fmt.Errorf("ontology %s not found", id)
		}
		return nil, fmt.Errorf("ontology %q not found", name)
	}
	return row, nil
}

func playgroundFromOntologyRow(row *entity.Ontology) (ontology.PlaygroundOntology, error) {
	if row == nil || strings.TrimSpace(row.Turtle) == "" {
		return ontology.PlaygroundOntology{}, fmt.Errorf("ontology turtle is empty")
	}
	desc := ""
	if row.Description != nil {
		desc = *row.Description
	}
	doc, err := ontology.ParseTurtle(row.Turtle, row.Name, desc, row.BaseIRI)
	if err != nil {
		return ontology.PlaygroundOntology{}, err
	}
	return doc.ToPlayground(), nil
}

func parserConfigFromModeling(base map[string]any, modeling ontology.ModelingDocument) map[string]any {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]any{}
	}
	out["kind"] = "ontology"
	nameByID := map[string]string{}
	var entityFields []any
	hasConceptProps := false
	for _, c := range modeling.Concepts {
		label := strings.TrimSpace(c.Name)
		if label == "" {
			label = strings.TrimSpace(c.ID)
		}
		if label == "" {
			continue
		}
		if c.ID != "" {
			nameByID[c.ID] = label
		}
		nameByID[label] = label
		item := map[string]any{"type": label}
		desc := strings.TrimSpace(c.Description)
		if desc != "" {
			item["description"] = desc
		}
		if rule := propertyRule(c.Properties); rule != "" {
			item["rule"] = rule
			hasConceptProps = true
		}
		entityFields = append(entityFields, item)
	}
	entityCfg := map[string]any{
		"description": "Extract instances of the bound ontology concepts. type must be one of the listed classes; fill properties when the source states them.",
		"fields":      entityFields,
	}
	if hasConceptProps {
		entityCfg["output_fields"] = []any{propertiesOutputField()}
	}
	out["entity"] = entityCfg

	var relationFields []any
	hasRelationProps := false
	for _, r := range modeling.Relations {
		label := strings.TrimSpace(r.Name)
		if label == "" {
			label = strings.TrimSpace(r.ID)
		}
		if label == "" {
			continue
		}
		item := map[string]any{"type": label}
		fromLabels := conceptLabels(nameByID, r.Source)
		toLabels := conceptLabels(nameByID, r.Target)
		var parts []string
		if d := strings.TrimSpace(r.Description); d != "" {
			parts = append(parts, d)
		}
		if len(fromLabels) > 0 && len(toLabels) > 0 {
			parts = append(parts, "Direction: ("+strings.Join(fromLabels, "|")+") → ("+strings.Join(toLabels, "|")+").")
		}
		if rule := propertyRule(r.Properties); rule != "" {
			parts = append(parts, rule)
			hasRelationProps = true
		}
		if len(parts) > 0 {
			item["description"] = strings.Join(parts, " ")
		}
		relationFields = append(relationFields, item)
	}
	if len(relationFields) == 0 {
		delete(out, "relation")
		return out
	}
	relationCfg := map[string]any{
		"description": "Extract relations of the bound ontology. type must be one of the listed relations, and the direction must follow the description.",
		"fields":      relationFields,
	}
	if hasRelationProps {
		relationCfg["output_fields"] = []any{propertiesOutputField()}
	}
	out["relation"] = relationCfg
	return out
}

func propertiesOutputField() map[string]any {
	return map[string]any{
		"name":        "properties",
		"type":        "object",
		"required":    false,
		"description": "Map of property key to value for this instance. Use only keys listed for the chosen type; omit keys that are not stated in the source.",
		"shape":       `{"<property key>": "<value>"}`,
	}
}

func propertyRule(props []ontology.ModelingProperty) string {
	if len(props) == 0 {
		return ""
	}
	var lines []string
	lines = append(lines, "Properties (emit under the properties object when present in the source):")
	for _, p := range props {
		key := strings.TrimSpace(p.Key)
		if key == "" {
			continue
		}
		label := strings.TrimSpace(p.Label)
		if label == "" {
			label = key
		}
		typ := strings.TrimSpace(p.Type)
		if typ == "" {
			typ = "string"
		}
		req := "optional"
		if p.Required {
			req = "required when stated"
		}
		line := fmt.Sprintf("- %s (%s, %s, %s)", key, label, typ, req)
		if typ == "enum" && len(p.EnumValues) > 0 {
			line += "; one of: " + strings.Join(p.EnumValues, "|")
		}
		if typ == "ref" && strings.TrimSpace(p.RefConceptTypeID) != "" {
			line += "; ref concept: " + strings.TrimSpace(p.RefConceptTypeID)
		}
		if d := strings.TrimSpace(p.Description); d != "" {
			line += ": " + d
		}
		lines = append(lines, line)
	}
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func conceptLabels(nameByID map[string]string, ids []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range ids {
		label := strings.TrimSpace(nameByID[id])
		if label == "" {
			label = strings.TrimSpace(id)
		}
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, label)
	}
	return out
}

// overlayOntologyFields replaces entity/relation type lists with the playground
// projection of an ontology (no datatype properties). Used when the ontology
// row has Turtle but no modeling document.
func overlayOntologyFields(cfg map[string]any, pg ontology.PlaygroundOntology) map[string]any {
	out := maps.Clone(cfg)
	if out == nil {
		out = map[string]any{}
	}
	out["kind"] = "ontology"
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
