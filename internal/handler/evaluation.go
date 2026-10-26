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
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/gin-gonic/gin"

	"ragflow/internal/common"
	"ragflow/internal/service"
)

// EvaluationHandler serves evaluation REST APIs.
type EvaluationHandler struct {
	svc *service.EvaluationService
}

// NewEvaluationHandler creates an EvaluationHandler.
func NewEvaluationHandler(svc *service.EvaluationService) *EvaluationHandler {
	return &EvaluationHandler{svc: svc}
}

func evaluationError(c *gin.Context, err error) {
	var coded *common.CodedError
	if errors.As(err, &coded) {
		common.ErrorWithCode(c, coded.Code, coded.Message)
		return
	}
	common.ErrorWithCode(c, common.CodeExceptionError, err.Error())
}

type createDatasetReq struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	KbIDs       []string `json:"kb_ids"`
}

type addCaseReq struct {
	Question        string `json:"question"`
	GroundTruth     string `json:"ground_truth"`
	ReferenceAnswer string `json:"reference_answer"`
}

type importCasesReq struct {
	Cases []service.ImportCaseItem `json:"cases"`
}

type startRunReq struct {
	Name        string   `json:"name"`
	DialogID    string   `json:"dialog_id"`
	JudgeLLMID  string   `json:"judge_llm_id"`
	JudgeEmbdID string   `json:"judge_embd_id"`
	Metrics     []string `json:"metrics"`
}

// ListDatasets GET /evaluations/datasets
func (h *EvaluationHandler) ListDatasets(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	rows, err := h.svc.ListDatasets(c.Request.Context(), user.ID)
	if err != nil {
		evaluationError(c, err)
		return
	}
	common.SuccessWithData(c, gin.H{"datasets": rows}, "success")
}

// CreateDataset POST /evaluations/datasets
func (h *EvaluationHandler) CreateDataset(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	var req createDatasetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorWithCode(c, common.CodeArgumentError, err.Error())
		return
	}
	row, err := h.svc.CreateDataset(c.Request.Context(), user.ID, req.Name, req.Description, req.KbIDs)
	if err != nil {
		evaluationError(c, err)
		return
	}
	common.SuccessWithData(c, row, "success")
}

// GetDataset GET /evaluations/datasets/:dataset_id
func (h *EvaluationHandler) GetDataset(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	row, err := h.svc.GetDataset(c.Request.Context(), user.ID, c.Param("dataset_id"))
	if err != nil {
		evaluationError(c, err)
		return
	}
	common.SuccessWithData(c, row, "success")
}

// DeleteDataset DELETE /evaluations/datasets/:dataset_id
func (h *EvaluationHandler) DeleteDataset(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	if err := h.svc.DeleteDataset(c.Request.Context(), user.ID, c.Param("dataset_id")); err != nil {
		evaluationError(c, err)
		return
	}
	common.SuccessWithData(c, true, "success")
}

// ListCases GET /evaluations/datasets/:dataset_id/cases
func (h *EvaluationHandler) ListCases(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	rows, err := h.svc.ListCases(c.Request.Context(), user.ID, c.Param("dataset_id"))
	if err != nil {
		evaluationError(c, err)
		return
	}
	common.SuccessWithData(c, gin.H{"cases": rows}, "success")
}

// AddCase POST /evaluations/datasets/:dataset_id/cases
func (h *EvaluationHandler) AddCase(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	var req addCaseReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorWithCode(c, common.CodeArgumentError, err.Error())
		return
	}
	ref := req.ReferenceAnswer
	if ref == "" {
		ref = req.GroundTruth
	}
	row, err := h.svc.AddCase(c.Request.Context(), user.ID, c.Param("dataset_id"), req.Question, ref)
	if err != nil {
		evaluationError(c, err)
		return
	}
	common.SuccessWithData(c, row, "success")
}

// ImportCases POST /evaluations/datasets/:dataset_id/cases/import
func (h *EvaluationHandler) ImportCases(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		common.ErrorWithCode(c, common.CodeArgumentError, err.Error())
		return
	}
	var items []service.ImportCaseItem
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(body, &items); err != nil {
			common.ErrorWithCode(c, common.CodeArgumentError, err.Error())
			return
		}
	} else {
		var wrapped importCasesReq
		if err := json.Unmarshal(body, &wrapped); err != nil {
			common.ErrorWithCode(c, common.CodeArgumentError, err.Error())
			return
		}
		items = wrapped.Cases
	}
	rows, err := h.svc.ImportCases(c.Request.Context(), user.ID, c.Param("dataset_id"), items)
	if err != nil {
		evaluationError(c, err)
		return
	}
	common.SuccessWithData(c, gin.H{"cases": rows}, "success")
}

// DeleteCase DELETE /evaluations/datasets/:dataset_id/cases/:case_id
func (h *EvaluationHandler) DeleteCase(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	if err := h.svc.DeleteCase(c.Request.Context(), user.ID, c.Param("dataset_id"), c.Param("case_id")); err != nil {
		evaluationError(c, err)
		return
	}
	common.SuccessWithData(c, true, "success")
}

// ListRuns GET /evaluations/datasets/:dataset_id/runs
func (h *EvaluationHandler) ListRuns(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	rows, err := h.svc.ListRuns(c.Request.Context(), user.ID, c.Param("dataset_id"))
	if err != nil {
		evaluationError(c, err)
		return
	}
	common.SuccessWithData(c, gin.H{"runs": rows}, "success")
}

// StartRun POST /evaluations/datasets/:dataset_id/runs
func (h *EvaluationHandler) StartRun(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	var req startRunReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorWithCode(c, common.CodeArgumentError, err.Error())
		return
	}
	row, err := h.svc.StartRun(
		c.Request.Context(),
		user.ID,
		c.Param("dataset_id"),
		req.Name,
		req.DialogID,
		req.JudgeLLMID,
		req.JudgeEmbdID,
		req.Metrics,
	)
	if err != nil {
		evaluationError(c, err)
		return
	}
	common.SuccessWithData(c, row, "success")
}

// GetRun GET /evaluations/runs/:run_id
func (h *EvaluationHandler) GetRun(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	row, err := h.svc.GetRun(c.Request.Context(), user.ID, c.Param("run_id"))
	if err != nil {
		evaluationError(c, err)
		return
	}
	common.SuccessWithData(c, row, "success")
}

// ListResults GET /evaluations/runs/:run_id/results
func (h *EvaluationHandler) ListResults(c *gin.Context) {
	user, code, msg := GetUser(c)
	if code != common.CodeSuccess {
		common.ErrorWithCode(c, code, msg)
		return
	}
	rows, err := h.svc.ListResults(c.Request.Context(), user.ID, c.Param("run_id"))
	if err != nil {
		evaluationError(c, err)
		return
	}
	common.SuccessWithData(c, gin.H{"results": rows}, "success")
}
