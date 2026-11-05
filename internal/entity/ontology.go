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

package entity

const (
	// OntologySourceKnowledge is a TBox induced from knowledge compilation or imported Turtle.
	OntologySourceKnowledge = "knowledge"
	// OntologySourceModeling is a workbench model stored as Turtle.
	OntologySourceModeling = "modeling"
	// OntologySourceBuiltin is the shipped workbench case, stored as Turtle.
	OntologySourceBuiltin = "builtin"
)

// Ontology stores a tenant-owned TBox as Turtle.
type Ontology struct {
	ID          string  `gorm:"column:id;primaryKey;size:32" json:"id"`
	TenantID    string  `gorm:"column:tenant_id;size:32;not null;index" json:"tenant_id"`
	Name        string  `gorm:"column:name;size:128;not null;index" json:"name"`
	Description *string `gorm:"column:description;type:text" json:"description,omitempty"`
	BaseIRI     string  `gorm:"column:base_iri;size:512;not null;default:''" json:"base_iri"`
	Turtle      string  `gorm:"column:turtle;type:longtext;not null" json:"turtle"`
	Source      string  `gorm:"column:source;size:32;not null;default:knowledge" json:"source"`
	Status      *string `gorm:"column:status;size:1;default:1;index" json:"status,omitempty"`
	BaseModel
}

// TableName returns the table name for Ontology.
func (Ontology) TableName() string {
	return "ontology"
}
