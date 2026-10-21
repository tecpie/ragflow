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

package knowledge_compile

import (
	"context"
	"fmt"
	"strings"

	"ragflow/internal/common"
	"ragflow/internal/dao"
	"ragflow/internal/entity"
	"ragflow/internal/ontology"

	"go.uber.org/zap"
)

// syncOntologyFromBuckets induces TBox from structure merge buckets and upserts
// tenant ontologies. Failures are logged and do not fail the compile batch.
func (c *Consumer) syncOntologyFromBuckets(ctx context.Context, tenant, kb string, buckets []StructureBucket) {
	if len(buckets) == 0 || dao.DB == nil {
		return
	}
	targets := groupBucketsByOntologyTarget(ctx, tenant, buckets)
	if len(targets) == 0 {
		return
	}
	ontDAO := dao.NewOntologyDAO()
	for _, t := range targets {
		if len(t.schema.Classes) == 0 && len(t.schema.Properties) == 0 {
			continue
		}
		item, err := upsertInducedOntology(ctx, ontDAO, tenant, t)
		if err != nil {
			common.Warn("knowledge_compile: ontology induce failed",
				zap.String("tenant_id", tenant),
				zap.String("kb_id", kb),
				zap.String("ontology_name", t.name),
				zap.String("ontology_id", t.ontologyID),
				zap.Error(err))
			continue
		}
		if item == nil {
			continue
		}
		common.Info("knowledge_compile: ontology induced",
			zap.String("tenant_id", tenant),
			zap.String("kb_id", kb),
			zap.String("ontology_id", item.ID),
			zap.String("ontology_name", item.Name),
			zap.Int("classes", len(t.schema.Classes)),
			zap.Int("properties", len(t.schema.Properties)))
	}
}

type ontologyTarget struct {
	ontologyID  string
	name        string
	description string
	baseIRI     string
	schema      ontology.InducedSchema
}

func groupBucketsByOntologyTarget(ctx context.Context, tenant string, buckets []StructureBucket) []ontologyTarget {
	type key struct {
		id   string
		name string
	}
	grouped := map[key]*ontologyTarget{}
	order := make([]key, 0)
	bucketGroups := map[key][]StructureBucket{}

	tmplDAO := dao.NewCompilationTemplateDAO()
	cfgCache := map[string]map[string]any{}

	for _, b := range buckets {
		cfg := loadTemplateOntologyConfig(ctx, tmplDAO, tenant, b.TemplateID, cfgCache)
		if cfg != nil && configBoolFalse(cfg["auto_ontology"]) {
			continue
		}
		ontologyID := ""
		name := ""
		baseIRI := ""
		if cfg != nil {
			ontologyID = configString(cfg, "ontology_id")
			name = configString(cfg, "ontology_name")
			baseIRI = configString(cfg, "ontology_base_iri")
		}
		if ontologyID == "" && name == "" {
			name = "Compiled Ontology"
		}
		if baseIRI == "" {
			baseIRI = "http://ragflow.local/ontology#"
		}
		k := key{id: ontologyID, name: name}
		if grouped[k] == nil {
			grouped[k] = &ontologyTarget{
				ontologyID:  ontologyID,
				name:        name,
				description: "Auto-induced from knowledge compilation",
				baseIRI:     baseIRI,
			}
			order = append(order, k)
		}
		bucketGroups[k] = append(bucketGroups[k], b)
	}

	out := make([]ontologyTarget, 0, len(order))
	for _, k := range order {
		t := *grouped[k]
		groupBuckets := bucketGroups[k]
		for _, b := range groupBuckets {
			induceBucketIntoSchema(&t.schema, groupBuckets, b)
		}
		out = append(out, t)
	}
	return out
}

func upsertInducedOntology(ctx context.Context, ontDAO *dao.OntologyDAO, tenantID string, t ontologyTarget) (*entity.Ontology, error) {
	name := strings.TrimSpace(t.name)
	if name == "" {
		name = "Compiled Ontology"
	}
	baseIRI := strings.TrimSpace(t.baseIRI)
	if baseIRI == "" {
		baseIRI = "http://ragflow.local/ontology#"
	}
	desc := strings.TrimSpace(t.description)

	var row *entity.Ontology
	var err error
	if id := strings.TrimSpace(t.ontologyID); id != "" {
		row, err = ontDAO.GetByID(ctx, dao.DB, tenantID, id)
		if err != nil {
			return nil, err
		}
		if row == nil {
			return nil, fmt.Errorf("ontology %s not found", id)
		}
	} else {
		row, err = ontDAO.GetByName(ctx, dao.DB, tenantID, name)
		if err != nil {
			return nil, err
		}
	}

	existingTurtle := ""
	if row != nil {
		existingTurtle = row.Turtle
		if row.BaseIRI != "" {
			baseIRI = row.BaseIRI
		}
		if name == "Compiled Ontology" && row.Name != "" {
			name = row.Name
		}
		if desc == "" && row.Description != nil {
			desc = *row.Description
		}
	}

	turtle, err := ontology.MergeTurtleSchemas(existingTurtle, baseIRI, name, desc, t.schema)
	if err != nil {
		return nil, err
	}

	status := string(entity.StatusValid)
	var descPtr *string
	if desc != "" {
		descPtr = &desc
	}
	if row == nil {
		row = &entity.Ontology{
			ID:          common.GenerateUUID(),
			TenantID:    tenantID,
			Name:        name,
			Description: descPtr,
			BaseIRI:     baseIRI,
			Turtle:      turtle,
			Status:      &status,
		}
		if err := ontDAO.Create(ctx, dao.DB, row); err != nil {
			return nil, err
		}
		return row, nil
	}
	row.Turtle = turtle
	row.BaseIRI = baseIRI
	if descPtr != nil {
		row.Description = descPtr
	}
	if err := ontDAO.Update(ctx, dao.DB, row); err != nil {
		return nil, err
	}
	return row, nil
}

func loadTemplateOntologyConfig(
	ctx context.Context,
	tmplDAO *dao.CompilationTemplateDAO,
	tenant, templateID string,
	cache map[string]map[string]any,
) map[string]any {
	templateID = strings.TrimSpace(templateID)
	if templateID == "" {
		return nil
	}
	if cfg, ok := cache[templateID]; ok {
		return cfg
	}
	row, err := tmplDAO.GetTemplate(ctx, dao.DB, tenant, templateID)
	if err != nil || row == nil || row.Config == nil {
		row, err = tmplDAO.GetByID(ctx, dao.DB, templateID)
		if err != nil || row == nil || row.Config == nil {
			cache[templateID] = nil
			return nil
		}
	}
	cache[templateID] = map[string]any(row.Config)
	return cache[templateID]
}

func configString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	default:
		s := strings.TrimSpace(fmt.Sprint(x))
		if s == "<nil>" {
			return ""
		}
		return s
	}
}

func configBoolFalse(v any) bool {
	switch x := v.(type) {
	case bool:
		return !x
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		return s == "false" || s == "0" || s == "no"
	default:
		return false
	}
}

func induceBucketIntoSchema(schema *ontology.InducedSchema, all []StructureBucket, b StructureBucket) {
	entityType := map[string]string{}
	for _, x := range all {
		if isRelationBucket(x) {
			continue
		}
		name := normalizedStructureEntityName(x.Name)
		typ := strings.TrimSpace(x.Type)
		if name == "" || typ == "" || strings.EqualFold(typ, "other") {
			continue
		}
		if prev, ok := entityType[name]; !ok || strings.EqualFold(prev, "other") {
			entityType[name] = typ
		}
	}

	if isRelationBucket(b) {
		relType := strings.TrimSpace(b.RelationType)
		if relType == "" {
			relType = "related"
		}
		fromKey := normalizedStructureEntityName(b.FromEntity)
		toKey := normalizedStructureEntityName(b.ToEntity)
		domainLabel := entityType[fromKey]
		rangeLabel := entityType[toKey]
		domain := ontology.SafeLocalName(domainLabel)
		rng := ontology.SafeLocalName(rangeLabel)
		if domain != "" {
			schema.Classes = append(schema.Classes, ontology.InducedClass{ID: domain, Label: domainLabel})
		}
		if rng != "" {
			schema.Classes = append(schema.Classes, ontology.InducedClass{ID: rng, Label: rangeLabel})
		}
		schema.Properties = append(schema.Properties, ontology.InducedProperty{
			ID:     ontology.SafeLocalName(relType),
			Label:  relType,
			Domain: domain,
			Range:  rng,
		})
		return
	}

	typ := strings.TrimSpace(b.Type)
	if typ == "" || strings.EqualFold(typ, "other") || strings.EqualFold(typ, "relation") {
		return
	}
	schema.Classes = append(schema.Classes, ontology.InducedClass{
		ID:    ontology.SafeLocalName(typ),
		Label: typ,
	})
}

func isRelationBucket(b StructureBucket) bool {
	if strings.EqualFold(strings.TrimSpace(b.Type), "relation") {
		return true
	}
	return strings.TrimSpace(b.FromEntity) != "" && strings.TrimSpace(b.ToEntity) != ""
}
