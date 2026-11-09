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
	"testing"

	"ragflow/internal/dao"
	"ragflow/internal/entity"
	"ragflow/internal/ontology"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestInduceBucketIntoSchema(t *testing.T) {
	buckets := []StructureBucket{
		{Name: "Alice", Type: "Person"},
		{Name: "Acme", Type: "Organization"},
		{Name: "Alice -> Acme", Type: "relation", FromEntity: "Alice", ToEntity: "Acme", RelationType: "works for"},
	}
	var schema ontology.InducedSchema
	for _, b := range buckets {
		induceBucketIntoSchema(&schema, buckets, b)
	}
	ttl := ontology.BuildTurtle("http://example.org/onto#", schema)
	doc, err := ontology.ParseTurtle(ttl, "t", "", "http://example.org/onto#")
	if err != nil {
		t.Fatalf("ParseTurtle: %v\n%s", err, ttl)
	}
	pg := doc.ToPlayground()
	if len(pg.EntityTypes) < 2 {
		t.Fatalf("classes=%d turtle=\n%s", len(pg.EntityTypes), ttl)
	}
	var hasWorksFor bool
	for _, r := range pg.Relationships {
		if r.Kind == "association" && (r.Name == "works for" || r.ID == "works_for") {
			hasWorksFor = true
		}
	}
	if !hasWorksFor {
		t.Fatalf("missing works for: %+v\n%s", pg.Relationships, ttl)
	}
}

func setupOntologySyncDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err = db.AutoMigrate(&entity.CompilationTemplate{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	orig := dao.DB
	dao.DB = db
	t.Cleanup(func() { dao.DB = orig })
	return db
}

func TestGroupBucketsByOntologyTarget_RequiresOntologySibling(t *testing.T) {
	db := setupOntologySyncDB(t)
	status := string(entity.StatusValid)
	tenant := "tenant-1"
	groupID := "group-1"
	graphID := "tpl-graph"
	ontoID := "tpl-ontology"

	if err := db.Create([]*entity.CompilationTemplate{
		{
			ID: graphID, TenantID: &tenant, GroupID: &groupID, Name: "Graph", Kind: "knowledge_graph",
			Config: entity.JSONMap{"kind": "knowledge_graph", "ontology_name": "FROM_GRAPH"},
			Status: &status,
		},
		{
			ID: ontoID, TenantID: &tenant, GroupID: &groupID, Name: "Ontology", Kind: "ontology",
			Config: entity.JSONMap{
				"kind":              "ontology",
				"ontology_name":     "FROM_ONTOLOGY",
				"ontology_base_iri": "http://example.org/induced#",
			},
			Status: &status,
		},
	}).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}

	buckets := []StructureBucket{
		{Name: "Alice", Type: "Person", TemplateID: graphID, TemplateKind: "knowledge_graph"},
		{Name: "Acme", Type: "Organization", TemplateID: graphID, TemplateKind: "knowledge_graph"},
	}
	targets := groupBucketsByOntologyTarget(t.Context(), tenant, buckets)
	if len(targets) != 1 {
		t.Fatalf("targets=%d, want 1", len(targets))
	}
	if targets[0].name != "FROM_ONTOLOGY" {
		t.Fatalf("name=%q, want FROM_ONTOLOGY (config must come from ontology template)", targets[0].name)
	}
	if targets[0].baseIRI != "http://example.org/induced#" {
		t.Fatalf("baseIRI=%q", targets[0].baseIRI)
	}
	if len(targets[0].schema.Classes) == 0 {
		t.Fatal("expected induced classes")
	}
}

func TestGroupBucketsByOntologyTarget_SkipWithoutOntologyOperator(t *testing.T) {
	db := setupOntologySyncDB(t)
	status := string(entity.StatusValid)
	tenant := "tenant-1"
	groupID := "group-only-graph"
	graphID := "tpl-graph-only"

	if err := db.Create(&entity.CompilationTemplate{
		ID: graphID, TenantID: &tenant, GroupID: &groupID, Name: "Graph", Kind: "knowledge_graph",
		Config: entity.JSONMap{"kind": "knowledge_graph", "auto_ontology": true, "ontology_name": "SHOULD_NOT_INDUCE"},
		Status: &status,
	}).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}

	buckets := []StructureBucket{
		{Name: "Alice", Type: "Person", TemplateID: graphID, TemplateKind: "knowledge_graph"},
	}
	targets := groupBucketsByOntologyTarget(t.Context(), tenant, buckets)
	if len(targets) != 0 {
		t.Fatalf("targets=%d, want 0 without ontology sibling (got %+v)", len(targets), targets)
	}
}

func TestGroupBucketsByOntologyTarget_SkipUngroupedTemplate(t *testing.T) {
	db := setupOntologySyncDB(t)
	status := string(entity.StatusValid)
	tenant := "tenant-1"
	graphID := "tpl-solo"

	if err := db.Create(&entity.CompilationTemplate{
		ID: graphID, TenantID: &tenant, Name: "Solo Graph", Kind: "knowledge_graph",
		Config: entity.JSONMap{"kind": "knowledge_graph"},
		Status: &status,
	}).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}

	buckets := []StructureBucket{
		{Name: "Alice", Type: "Person", TemplateID: graphID},
	}
	targets := groupBucketsByOntologyTarget(t.Context(), tenant, buckets)
	if len(targets) != 0 {
		t.Fatalf("targets=%d, want 0 for ungrouped template", len(targets))
	}
}

func TestUpsertInducedOntologySkipsModeledSource(t *testing.T) {
	db := setupOntologySyncDB(t)
	if err := db.AutoMigrate(&entity.Ontology{}); err != nil {
		t.Fatalf("migrate ontology: %v", err)
	}
	status := string(entity.StatusValid)
	turtle := ontology.BuildTurtle("http://example.org/onto#", ontology.InducedSchema{
		Classes: []ontology.InducedClass{{ID: "Substation", Label: "变电站"}},
	})
	if err := db.Create(&entity.Ontology{
		ID: "onto-model", TenantID: "tenant-1", Name: "电力", BaseIRI: "http://example.org/onto#",
		Turtle: turtle, Source: entity.OntologySourceModeling, Status: &status,
	}).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := upsertInducedOntology(t.Context(), dao.NewOntologyDAO(), "tenant-1", ontologyTarget{
		ontologyID: "onto-model",
		name:       "电力",
		schema:     ontology.InducedSchema{Classes: []ontology.InducedClass{{ID: "Person", Label: "Person"}}},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got != nil {
		t.Fatalf("modeled ontology was rewritten: %+v", got)
	}
	var saved entity.Ontology
	if err := db.Where("id = ?", "onto-model").Take(&saved).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if saved.Turtle != turtle {
		t.Fatalf("turtle changed:\n%s", saved.Turtle)
	}
}
