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
	"fmt"
	"strings"

	"ragflow/internal/common"
	"ragflow/internal/dao"
	"ragflow/internal/entity"
	"ragflow/internal/ontology"
)

// OntologyService manages ontology CRUD and Playground projection.
type OntologyService struct {
	dao *dao.OntologyDAO
}

// NewOntologyService creates an OntologyService.
func NewOntologyService() *OntologyService {
	return &OntologyService{dao: dao.NewOntologyDAO()}
}

// CreateOntologyRequest is the create payload.
type CreateOntologyRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	BaseIRI     string `json:"base_iri"`
	Turtle      string `json:"turtle"`
}

// UpdateOntologyRequest is the update payload.
type UpdateOntologyRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	BaseIRI     *string `json:"base_iri"`
	Turtle      *string `json:"turtle"`
}

// OntologyItem is the API list/detail shape.
type OntologyItem struct {
	ID          string  `json:"id"`
	TenantID    string  `json:"tenant_id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	BaseIRI     string  `json:"base_iri"`
	Turtle      string  `json:"turtle,omitempty"`
	CreateTime  *int64  `json:"create_time,omitempty"`
	UpdateTime  *int64  `json:"update_time,omitempty"`
}

func toOntologyItem(row *entity.Ontology, includeTurtle bool) *OntologyItem {
	item := &OntologyItem{
		ID:          row.ID,
		TenantID:    row.TenantID,
		Name:        row.Name,
		Description: row.Description,
		BaseIRI:     row.BaseIRI,
		CreateTime:  row.CreateTime,
		UpdateTime:  row.UpdateTime,
	}
	if includeTurtle {
		item.Turtle = row.Turtle
	}
	return item
}

func (s *OntologyService) validateTurtle(name, description, baseIRI, turtleText string) (*ontology.Document, error) {
	return ontology.ParseTurtle(turtleText, name, description, baseIRI)
}

// Create stores a new ontology after parsing Turtle.
func (s *OntologyService) Create(ctx context.Context, tenantID string, req CreateOntologyRequest) (*OntologyItem, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if _, err := s.validateTurtle(name, req.Description, req.BaseIRI, req.Turtle); err != nil {
		return nil, err
	}
	status := string(entity.StatusValid)
	desc := strings.TrimSpace(req.Description)
	var descPtr *string
	if desc != "" {
		descPtr = &desc
	}
	row := &entity.Ontology{
		ID:          common.GenerateUUID(),
		TenantID:    tenantID,
		Name:        name,
		Description: descPtr,
		BaseIRI:     strings.TrimSpace(req.BaseIRI),
		Turtle:      strings.TrimSpace(req.Turtle),
		Status:      &status,
	}
	if err := s.dao.Create(ctx, dao.DB, row); err != nil {
		return nil, err
	}
	return toOntologyItem(row, true), nil
}

// List returns ontologies for the tenant.
func (s *OntologyService) List(ctx context.Context, tenantID, keywords string) ([]*OntologyItem, error) {
	rows, err := s.dao.List(ctx, dao.DB, tenantID, keywords)
	if err != nil {
		return nil, err
	}
	out := make([]*OntologyItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, toOntologyItem(row, false))
	}
	return out, nil
}

// Get returns one ontology including Turtle.
func (s *OntologyService) Get(ctx context.Context, tenantID, id string) (*OntologyItem, error) {
	row, err := s.dao.GetByID(ctx, dao.DB, tenantID, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return toOntologyItem(row, true), nil
}

// Update patches an ontology.
func (s *OntologyService) Update(ctx context.Context, tenantID, id string, req UpdateOntologyRequest) (*OntologyItem, error) {
	row, err := s.dao.GetByID(ctx, dao.DB, tenantID, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return nil, fmt.Errorf("name is required")
		}
		row.Name = name
	}
	if req.Description != nil {
		desc := strings.TrimSpace(*req.Description)
		if desc == "" {
			row.Description = nil
		} else {
			row.Description = &desc
		}
	}
	if req.BaseIRI != nil {
		row.BaseIRI = strings.TrimSpace(*req.BaseIRI)
	}
	if req.Turtle != nil {
		row.Turtle = strings.TrimSpace(*req.Turtle)
	}
	desc := ""
	if row.Description != nil {
		desc = *row.Description
	}
	if _, err := s.validateTurtle(row.Name, desc, row.BaseIRI, row.Turtle); err != nil {
		return nil, err
	}
	if err := s.dao.Update(ctx, dao.DB, row); err != nil {
		return nil, err
	}
	return toOntologyItem(row, true), nil
}

// Delete soft-deletes an ontology.
func (s *OntologyService) Delete(ctx context.Context, tenantID, id string) error {
	row, err := s.dao.GetByID(ctx, dao.DB, tenantID, id)
	if err != nil {
		return err
	}
	if row == nil {
		return fmt.Errorf("ontology not found")
	}
	return s.dao.SoftDelete(ctx, dao.DB, tenantID, id)
}

// Playground returns the Microsoft Ontology Playground JSON for an ontology.
func (s *OntologyService) Playground(ctx context.Context, tenantID, id string) (*ontology.PlaygroundOntology, error) {
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
	return &pg, nil
}

// UpsertInducedRequest merges knowledge-compile induced TBox into an ontology.
type UpsertInducedRequest struct {
	OntologyID  string // optional explicit id
	Name        string // used when OntologyID empty: find-or-create by name
	Description string
	BaseIRI     string
	Schema      ontology.InducedSchema
}

// UpsertInduced merges induced classes/properties into an ontology and persists Turtle.
func (s *OntologyService) UpsertInduced(ctx context.Context, tenantID string, req UpsertInducedRequest) (*OntologyItem, error) {
	if len(req.Schema.Classes) == 0 && len(req.Schema.Properties) == 0 {
		return nil, nil
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Compiled Ontology"
	}
	baseIRI := strings.TrimSpace(req.BaseIRI)
	if baseIRI == "" {
		baseIRI = "http://ragflow.local/ontology#"
	}
	desc := strings.TrimSpace(req.Description)

	var row *entity.Ontology
	var err error
	if id := strings.TrimSpace(req.OntologyID); id != "" {
		row, err = s.dao.GetByID(ctx, dao.DB, tenantID, id)
		if err != nil {
			return nil, err
		}
		if row == nil {
			return nil, fmt.Errorf("ontology %s not found", id)
		}
	} else {
		row, err = s.dao.GetByName(ctx, dao.DB, tenantID, name)
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

	turtle, err := ontology.MergeTurtleSchemas(existingTurtle, baseIRI, name, desc, req.Schema)
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
		if err := s.dao.Create(ctx, dao.DB, row); err != nil {
			return nil, err
		}
		return toOntologyItem(row, true), nil
	}
	row.Turtle = turtle
	row.BaseIRI = baseIRI
	if descPtr != nil {
		row.Description = descPtr
	}
	if err := s.dao.Update(ctx, dao.DB, row); err != nil {
		return nil, err
	}
	return toOntologyItem(row, true), nil
}

// ExportRDFXML returns RDF/XML bytes for an ontology.
func (s *OntologyService) ExportRDFXML(ctx context.Context, tenantID, id string) ([]byte, string, error) {
	row, err := s.dao.GetByID(ctx, dao.DB, tenantID, id)
	if err != nil {
		return nil, "", err
	}
	if row == nil {
		return nil, "", nil
	}
	desc := ""
	if row.Description != nil {
		desc = *row.Description
	}
	doc, err := ontology.ParseTurtle(row.Turtle, row.Name, desc, row.BaseIRI)
	if err != nil {
		return nil, "", err
	}
	data, err := doc.ExportRDFXML()
	if err != nil {
		return nil, "", err
	}
	return data, row.Name, nil
}
