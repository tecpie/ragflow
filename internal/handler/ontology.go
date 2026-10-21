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

package handler

import (
	"fmt"
	"net/http"
	"net/url"

	"ragflow/internal/common"
	"ragflow/internal/service"

	"github.com/gin-gonic/gin"
)

// OntologyHandler serves ontology REST APIs.
type OntologyHandler struct {
	svc *service.OntologyService
}

// NewOntologyHandler creates an OntologyHandler.
func NewOntologyHandler(svc *service.OntologyService) *OntologyHandler {
	return &OntologyHandler{svc: svc}
}

// List lists ontologies for the current user tenant.
func (h *OntologyHandler) List(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	items, err := h.svc.List(c.Request.Context(), user.ID, c.Query("keywords"))
	if err != nil {
		common.ResponseWithCodeData(c, common.CodeExceptionError, nil, err.Error())
		return
	}
	common.SuccessWithData(c, gin.H{"ontologies": items}, "success")
}

// Create creates an ontology from Turtle.
func (h *OntologyHandler) Create(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	var req service.CreateOntologyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ResponseWithCodeData(c, common.CodeArgumentError, nil, err.Error())
		return
	}
	item, err := h.svc.Create(c.Request.Context(), user.ID, req)
	if err != nil {
		common.ResponseWithCodeData(c, common.CodeDataError, nil, err.Error())
		return
	}
	common.SuccessWithData(c, item, "success")
}

// Get returns one ontology.
func (h *OntologyHandler) Get(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	item, err := h.svc.Get(c.Request.Context(), user.ID, c.Param("ontology_id"))
	if err != nil {
		common.ResponseWithCodeData(c, common.CodeExceptionError, nil, err.Error())
		return
	}
	if item == nil {
		common.ResponseWithCodeData(c, common.CodeNotFound, nil, "ontology not found")
		return
	}
	common.SuccessWithData(c, item, "success")
}

// Update updates an ontology.
func (h *OntologyHandler) Update(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	var req service.UpdateOntologyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ResponseWithCodeData(c, common.CodeArgumentError, nil, err.Error())
		return
	}
	item, err := h.svc.Update(c.Request.Context(), user.ID, c.Param("ontology_id"), req)
	if err != nil {
		common.ResponseWithCodeData(c, common.CodeDataError, nil, err.Error())
		return
	}
	if item == nil {
		common.ResponseWithCodeData(c, common.CodeNotFound, nil, "ontology not found")
		return
	}
	common.SuccessWithData(c, item, "success")
}

// Delete soft-deletes an ontology.
func (h *OntologyHandler) Delete(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	if err := h.svc.Delete(c.Request.Context(), user.ID, c.Param("ontology_id")); err != nil {
		common.ResponseWithCodeData(c, common.CodeDataError, nil, err.Error())
		return
	}
	common.SuccessWithData(c, true, "success")
}

// Playground returns Playground-compatible JSON for embed inline loading.
func (h *OntologyHandler) Playground(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	pg, err := h.svc.Playground(c.Request.Context(), user.ID, c.Param("ontology_id"))
	if err != nil {
		common.ResponseWithCodeData(c, common.CodeDataError, nil, err.Error())
		return
	}
	if pg == nil {
		common.ResponseWithCodeData(c, common.CodeNotFound, nil, "ontology not found")
		return
	}
	common.SuccessWithData(c, pg, "success")
}

// ExportRDFXML downloads RDF/XML for the ontology.
func (h *OntologyHandler) ExportRDFXML(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	data, name, err := h.svc.ExportRDFXML(c.Request.Context(), user.ID, c.Param("ontology_id"))
	if err != nil {
		common.ResponseWithCodeData(c, common.CodeDataError, nil, err.Error())
		return
	}
	if data == nil {
		common.ResponseWithCodeData(c, common.CodeNotFound, nil, "ontology not found")
		return
	}
	filename := url.PathEscape(fmt.Sprintf("%s.rdf", name))
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Data(http.StatusOK, "application/rdf+xml; charset=utf-8", data)
}
