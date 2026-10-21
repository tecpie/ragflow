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

package dao

import (
	"context"
	"errors"
	"strings"

	"ragflow/internal/entity"

	"gorm.io/gorm"
)

// OntologyDAO is the data-access object for ontologies.
type OntologyDAO struct{}

// NewOntologyDAO creates an OntologyDAO.
func NewOntologyDAO() *OntologyDAO {
	return &OntologyDAO{}
}

// Create inserts an ontology row.
func (d *OntologyDAO) Create(ctx context.Context, db *gorm.DB, row *entity.Ontology) error {
	return db.WithContext(ctx).Create(row).Error
}

// Update saves an existing ontology.
func (d *OntologyDAO) Update(ctx context.Context, db *gorm.DB, row *entity.Ontology) error {
	return db.WithContext(ctx).Save(row).Error
}

// GetByID returns an ontology by id for a tenant, or nil if missing.
func (d *OntologyDAO) GetByID(ctx context.Context, db *gorm.DB, tenantID, id string) (*entity.Ontology, error) {
	var row entity.Ontology
	err := db.WithContext(ctx).
		Where("id = ? AND tenant_id = ? AND status = ?", id, tenantID, string(entity.StatusValid)).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// List returns ontologies for a tenant.
func (d *OntologyDAO) List(ctx context.Context, db *gorm.DB, tenantID, keywords string) ([]*entity.Ontology, error) {
	q := db.WithContext(ctx).Model(&entity.Ontology{}).
		Where("tenant_id = ? AND status = ?", tenantID, string(entity.StatusValid))
	if kw := strings.TrimSpace(keywords); kw != "" {
		like := "%" + strings.ToLower(kw) + "%"
		q = q.Where("LOWER(name) LIKE ?", like)
	}
	var rows []*entity.Ontology
	if err := q.Order("create_time desc").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// SoftDelete marks an ontology invalid.
func (d *OntologyDAO) SoftDelete(ctx context.Context, db *gorm.DB, tenantID, id string) error {
	status := string(entity.StatusInvalid)
	return db.WithContext(ctx).Model(&entity.Ontology{}).
		Where("id = ? AND tenant_id = ?", id, tenantID).
		Update("status", status).Error
}

// GetByName returns the first valid ontology with the given name for a tenant.
func (d *OntologyDAO) GetByName(ctx context.Context, db *gorm.DB, tenantID, name string) (*entity.Ontology, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	var row entity.Ontology
	err := db.WithContext(ctx).
		Where("tenant_id = ? AND name = ? AND status = ?", tenantID, name, string(entity.StatusValid)).
		Order("create_time desc").
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
