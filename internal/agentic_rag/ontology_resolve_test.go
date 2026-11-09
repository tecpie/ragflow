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
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ragflow/internal/dao"
	"ragflow/internal/entity"
	"ragflow/internal/ontology"
)

func TestPlaygroundForDatasetsUsesOntologyOperator(t *testing.T) {
	db := openOAGDB(t)
	seedPowerOntology(t, db)
	tenant := "tenant-1"

	pg, covered := playgroundForDatasets(context.Background(), tenant, []string{"kb-with", "kb-without"})
	if len(covered) != 1 || covered[0] != "kb-with" {
		t.Fatalf("covered = %v, want [kb-with]", covered)
	}
	if !playgroundHas(pg, "变电站") || !playgroundHasRelation(pg, "电源侧厂站") {
		t.Fatalf("playground = %+v", pg)
	}
	if !datasetsHaveOntology(context.Background(), tenant, []string{"kb-with"}) {
		t.Fatal("dataset with an ontology operator must report an ontology")
	}
	if datasetsHaveOntology(context.Background(), tenant, []string{"kb-without"}) {
		t.Fatal("dataset without an ontology operator must not report an ontology")
	}
}

func seedPowerOntology(t *testing.T, db *gorm.DB) {
	t.Helper()
	tenant := "tenant-1"
	status := string(entity.StatusValid)
	groupID := "group-power"
	turtle := ontology.BuildTurtle("http://ragflow.local/ontology/power#", ontology.InducedSchema{
		Classes: []ontology.InducedClass{
			{ID: "变电站", Label: "变电站"},
			{ID: "输电线路", Label: "输电线路"},
		},
		Properties: []ontology.InducedProperty{{ID: "电源侧厂站", Label: "电源侧厂站", Domain: "输电线路", Range: "变电站"}},
	})
	if err := db.Create(&entity.Ontology{
		ID: "onto-1", TenantID: tenant, Name: "电力行业本体", BaseIRI: "http://ragflow.local/ontology/power#",
		Turtle: turtle, Status: &status,
	}).Error; err != nil {
		t.Fatalf("create ontology: %v", err)
	}
	desc := "group child"
	if err := db.Create(&entity.CompilationTemplate{
		ID: "tpl-onto", TenantID: &tenant, GroupID: &groupID, Name: "电力本体", Kind: "ontology",
		Description: &desc, Status: &status,
		Config: entity.JSONMap{"kind": "ontology", "ontology_name": "电力行业本体", "ontology_base_iri": "http://ragflow.local/ontology/power#"},
	}).Error; err != nil {
		t.Fatalf("create template: %v", err)
	}
	if err := db.Create(&entity.Knowledgebase{
		ID: "kb-with", TenantID: tenant, Name: "with", EmbdID: "emb", Permission: "me", CreatedBy: "u", ParserID: "naive",
		Status: &status, ParserConfig: entity.JSONMap{"compilation_template_group_id": groupID},
	}).Error; err != nil {
		t.Fatalf("create dataset: %v", err)
	}
	if err := db.Create(&entity.Knowledgebase{
		ID: "kb-without", TenantID: tenant, Name: "without", EmbdID: "emb", Permission: "me", CreatedBy: "u", ParserID: "naive",
		Status: &status, ParserConfig: entity.JSONMap{},
	}).Error; err != nil {
		t.Fatalf("create dataset without ontology: %v", err)
	}
}

func openOAGDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&entity.Knowledgebase{}, &entity.CompilationTemplate{}, &entity.Ontology{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	orig := dao.DB
	dao.DB = db
	t.Cleanup(func() { dao.DB = orig })
	return db
}

func playgroundHas(pg ontology.PlaygroundOntology, label string) bool {
	for _, et := range pg.EntityTypes {
		if et.ID == label || et.Name == label {
			return true
		}
	}
	return false
}

func playgroundHasRelation(pg ontology.PlaygroundOntology, label string) bool {
	for _, rel := range pg.Relationships {
		if rel.Kind != "subclass" && rel.Name == label {
			return true
		}
	}
	return false
}
