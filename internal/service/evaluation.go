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
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"ragflow/internal/common"
	"ragflow/internal/dao"
	"ragflow/internal/entity"
	"ragflow/internal/permission"
)

const (
	evalRunPending   = "PENDING"
	evalRunRunning   = "RUNNING"
	evalRunCompleted = "COMPLETED"
	evalRunFailed    = "FAILED"
	evalDatasetValid = int64(1)
)

// EvaluationService manages evaluation datasets, cases, runs, and RAGAS scoring.
type EvaluationService struct {
	chatSession *ChatSessionService
	factory     *ModelFactory
	db          *gorm.DB
}

// NewEvaluationService creates an EvaluationService.
func NewEvaluationService(chatSession *ChatSessionService, factory *ModelFactory) *EvaluationService {
	return &EvaluationService{
		chatSession: chatSession,
		factory:     factory,
		db:          dao.DB,
	}
}

func kbIDsToJSONSlice(kbIDs []string) entity.JSONSlice {
	out := make(entity.JSONSlice, 0, len(kbIDs))
	for _, id := range kbIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

func (s *EvaluationService) CreateDataset(ctx context.Context, userID, name, description string, kbIDs []string) (*entity.EvaluationDataset, error) {
	tenantID, err := requireCurrentTenantID(ctx, userID)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, common.NewCodedError(common.CodeArgumentError, "name is required")
	}
	now := time.Now().UnixMilli()
	var descPtr *string
	if d := strings.TrimSpace(description); d != "" {
		descPtr = &d
	}
	row := &entity.EvaluationDataset{
		ID:          common.GenerateUUID(),
		TenantID:    tenantID,
		Name:        name,
		Description: descPtr,
		KbIDs:       kbIDsToJSONSlice(kbIDs),
		CreatedBy:   userID,
		CreateTime:  now,
		UpdateTime:  now,
		Status:      evalDatasetValid,
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

func (s *EvaluationService) ListDatasets(ctx context.Context, userID string) ([]entity.EvaluationDataset, error) {
	tenantID, err := requireCurrentTenantID(ctx, userID)
	if err != nil {
		return nil, err
	}
	var rows []entity.EvaluationDataset
	err = s.db.WithContext(ctx).
		Where("tenant_id = ? AND status = ?", tenantID, evalDatasetValid).
		Order("create_time DESC").
		Find(&rows).Error
	return rows, err
}

func (s *EvaluationService) GetDataset(ctx context.Context, userID, datasetID string) (*entity.EvaluationDataset, error) {
	tenantID, err := requireCurrentTenantID(ctx, userID)
	if err != nil {
		return nil, err
	}
	var row entity.EvaluationDataset
	err = s.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ? AND status = ?", datasetID, tenantID, evalDatasetValid).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, common.NewCodedError(common.CodeNotFound, "dataset not found")
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *EvaluationService) DeleteDataset(ctx context.Context, userID, datasetID string) error {
	ds, err := s.GetDataset(ctx, userID, datasetID)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	return s.db.WithContext(ctx).Model(ds).Updates(map[string]any{
		"status":      int64(0),
		"update_time": now,
	}).Error
}

func (s *EvaluationService) AddCase(ctx context.Context, userID, datasetID, question, referenceAnswer string) (*entity.EvaluationCase, error) {
	if _, err := s.GetDataset(ctx, userID, datasetID); err != nil {
		return nil, err
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, common.NewCodedError(common.CodeArgumentError, "question is required")
	}
	var refPtr *string
	if r := strings.TrimSpace(referenceAnswer); r != "" {
		refPtr = &r
	}
	row := &entity.EvaluationCase{
		ID:              common.GenerateUUID(),
		DatasetID:       datasetID,
		Question:        question,
		ReferenceAnswer: refPtr,
		CreateTime:      time.Now().UnixMilli(),
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

// ImportCaseItem is one row for bulk import (ground_truth or reference_answer).
type ImportCaseItem struct {
	Question         string         `json:"question"`
	GroundTruth      string         `json:"ground_truth"`
	ReferenceAnswer  string         `json:"reference_answer"`
	RelevantDocIDs   []any          `json:"relevant_doc_ids"`
	RelevantChunkIDs []any          `json:"relevant_chunk_ids"`
	Metadata         map[string]any `json:"metadata"`
}

func (s *EvaluationService) ImportCases(ctx context.Context, userID, datasetID string, items []ImportCaseItem) ([]entity.EvaluationCase, error) {
	if _, err := s.GetDataset(ctx, userID, datasetID); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, common.NewCodedError(common.CodeArgumentError, "cases is required")
	}
	now := time.Now().UnixMilli()
	rows := make([]entity.EvaluationCase, 0, len(items))
	for _, item := range items {
		q := strings.TrimSpace(item.Question)
		if q == "" {
			continue
		}
		ref := strings.TrimSpace(item.ReferenceAnswer)
		if ref == "" {
			ref = strings.TrimSpace(item.GroundTruth)
		}
		var refPtr *string
		if ref != "" {
			refPtr = &ref
		}
		row := entity.EvaluationCase{
			ID:              common.GenerateUUID(),
			DatasetID:       datasetID,
			Question:        q,
			ReferenceAnswer: refPtr,
			CreateTime:      now,
		}
		if len(item.RelevantDocIDs) > 0 {
			sl := entity.JSONSlice(item.RelevantDocIDs)
			row.RelevantDocIDs = &sl
		}
		if len(item.RelevantChunkIDs) > 0 {
			sl := entity.JSONSlice(item.RelevantChunkIDs)
			row.RelevantChunkIDs = &sl
		}
		if len(item.Metadata) > 0 {
			m := entity.JSONMap(item.Metadata)
			row.Metadata = &m
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, common.NewCodedError(common.CodeArgumentError, "no valid cases")
	}
	if err := s.db.WithContext(ctx).Create(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *EvaluationService) ListCases(ctx context.Context, userID, datasetID string) ([]entity.EvaluationCase, error) {
	if _, err := s.GetDataset(ctx, userID, datasetID); err != nil {
		return nil, err
	}
	var rows []entity.EvaluationCase
	err := s.db.WithContext(ctx).
		Where("dataset_id = ?", datasetID).
		Order("create_time ASC").
		Find(&rows).Error
	return rows, err
}

func (s *EvaluationService) DeleteCase(ctx context.Context, userID, datasetID, caseID string) error {
	if _, err := s.GetDataset(ctx, userID, datasetID); err != nil {
		return err
	}
	res := s.db.WithContext(ctx).
		Where("id = ? AND dataset_id = ?", caseID, datasetID).
		Delete(&entity.EvaluationCase{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return common.NewCodedError(common.CodeNotFound, "case not found")
	}
	return nil
}

func (s *EvaluationService) StartRun(
	ctx context.Context,
	userID, datasetID, name, dialogID, judgeLLMID, judgeEmbdID string,
	metrics []string,
) (*entity.EvaluationRun, error) {
	if _, err := s.GetDataset(ctx, userID, datasetID); err != nil {
		return nil, err
	}
	dialogID = strings.TrimSpace(dialogID)
	if dialogID == "" {
		return nil, common.NewCodedError(common.CodeArgumentError, "dialog_id is required")
	}
	if err := checkChatPermission(ctx, nil, userID, dialogID, permission.OperationRun); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = fmt.Sprintf("run-%d", time.Now().Unix())
	}
	if len(metrics) == 0 {
		metrics = append([]string(nil), defaultEvalMetrics...)
	}
	metricAny := make([]any, len(metrics))
	for i, m := range metrics {
		metricAny[i] = m
	}
	now := time.Now().UnixMilli()
	run := &entity.EvaluationRun{
		ID:        common.GenerateUUID(),
		DatasetID: datasetID,
		DialogID:  dialogID,
		Name:      name,
		ConfigSnapshot: entity.JSONMap{
			"dialog_id":     dialogID,
			"judge_llm_id":  judgeLLMID,
			"judge_embd_id": judgeEmbdID,
			"metrics":       metricAny,
		},
		Status:     evalRunPending,
		CreatedBy:  userID,
		CreateTime: now,
	}
	if err := s.db.WithContext(ctx).Create(run).Error; err != nil {
		return nil, err
	}

	tenantID, err := requireCurrentTenantID(ctx, userID)
	if err != nil {
		return nil, err
	}
	workerCtx := context.WithoutCancel(ctx)
	go s.runWorker(workerCtx, userID, tenantID, run.ID, datasetID, dialogID, judgeLLMID, judgeEmbdID, metrics)
	return run, nil
}

func (s *EvaluationService) runWorker(
	ctx context.Context,
	userID, tenantID, runID, datasetID, dialogID, judgeLLMID, judgeEmbdID string,
	metrics []string,
) {
	fail := func(msg string) {
		now := time.Now().UnixMilli()
		_ = s.db.WithContext(ctx).Model(&entity.EvaluationRun{}).
			Where("id = ?", runID).
			Updates(map[string]any{
				"status":          evalRunFailed,
				"complete_time":   now,
				"metrics_summary": entity.JSONMap{"error": msg},
			}).Error
		common.Error(fmt.Sprintf("evaluation run %s failed", runID), errors.New(msg))
	}

	if err := s.db.WithContext(ctx).Model(&entity.EvaluationRun{}).
		Where("id = ?", runID).
		Update("status", evalRunRunning).Error; err != nil {
		fail(err.Error())
		return
	}

	var cases []entity.EvaluationCase
	if err := s.db.WithContext(ctx).Where("dataset_id = ?", datasetID).
		Order("create_time ASC").Find(&cases).Error; err != nil {
		fail(err.Error())
		return
	}
	if len(cases) == 0 {
		fail("dataset has no cases")
		return
	}
	if s.chatSession == nil {
		fail("chat session service not configured")
		return
	}

	scorer := newRagasScorer(s.factory, tenantID, judgeLLMID, judgeEmbdID)
	sums := map[string]float64{}
	counts := map[string]int{}

	for _, caze := range cases {
		started := time.Now()
		gt := ""
		if caze.ReferenceAnswer != nil {
			gt = *caze.ReferenceAnswer
		}
		result, err := s.chatSession.ChatCompletions(
			ctx, userID, dialogID, "",
			[]map[string]any{{"role": "user", "content": caze.Question}},
			"", nil, "", nil,
			map[string]any{"store_history_messages": false},
			false, false, nil,
		)
		answer := ""
		var ref map[string]any
		if err != nil {
			execSec := time.Since(started).Seconds()
			metricsMap := entity.JSONMap{
				"errors": map[string]string{"chat": err.Error()},
			}
			row := &entity.EvaluationResult{
				ID:              common.GenerateUUID(),
				RunID:           runID,
				CaseID:          caze.ID,
				GeneratedAnswer: "",
				RetrievedChunks: entity.JSONMap{"contexts": []string{}},
				Metrics:         metricsMap,
				ExecutionTime:   execSec,
				CreateTime:      time.Now().UnixMilli(),
			}
			_ = s.db.WithContext(ctx).Create(row).Error
			continue
		}
		if result != nil {
			answer, _ = result["answer"].(string)
			ref, _ = result["reference"].(map[string]any)
		}
		contexts := extractContextsFromReference(ref)
		payload := prepareScoringPayload(caze.Question, answer, contexts, gt)
		scores := scorer.scoreAll(ctx, payload, metrics)
		for _, name := range metrics {
			if v, ok := scores[name].(float64); ok {
				sums[name] += v
				counts[name]++
			}
		}
		retrieved := entity.JSONMap{"contexts": payload.Contexts}
		if ref != nil {
			retrieved["reference"] = ref
		}
		execSec := time.Since(started).Seconds()
		row := &entity.EvaluationResult{
			ID:              common.GenerateUUID(),
			RunID:           runID,
			CaseID:          caze.ID,
			GeneratedAnswer: payload.Answer,
			RetrievedChunks: retrieved,
			Metrics:         entity.JSONMap(scores),
			ExecutionTime:   execSec,
			CreateTime:      time.Now().UnixMilli(),
		}
		if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
			fail(err.Error())
			return
		}
	}

	summary := entity.JSONMap{}
	avgs := map[string]any{}
	for _, name := range metrics {
		if counts[name] > 0 {
			avgs[name] = sums[name] / float64(counts[name])
		} else {
			avgs[name] = nil
		}
		summary[name+"_count"] = counts[name]
	}
	summary["averages"] = avgs
	summary["case_count"] = len(cases)
	now := time.Now().UnixMilli()
	_ = s.db.WithContext(ctx).Model(&entity.EvaluationRun{}).
		Where("id = ?", runID).
		Updates(map[string]any{
			"status":          evalRunCompleted,
			"complete_time":   now,
			"metrics_summary": summary,
		}).Error
}

func (s *EvaluationService) ListRuns(ctx context.Context, userID, datasetID string) ([]entity.EvaluationRun, error) {
	if _, err := s.GetDataset(ctx, userID, datasetID); err != nil {
		return nil, err
	}
	var rows []entity.EvaluationRun
	err := s.db.WithContext(ctx).
		Where("dataset_id = ?", datasetID).
		Order("create_time DESC").
		Find(&rows).Error
	return rows, err
}

func (s *EvaluationService) GetRun(ctx context.Context, userID, runID string) (*entity.EvaluationRun, error) {
	tenantID, err := requireCurrentTenantID(ctx, userID)
	if err != nil {
		return nil, err
	}
	var run entity.EvaluationRun
	err = s.db.WithContext(ctx).Where("id = ?", runID).Take(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, common.NewCodedError(common.CodeNotFound, "run not found")
	}
	if err != nil {
		return nil, err
	}
	var ds entity.EvaluationDataset
	err = s.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ?", run.DatasetID, tenantID).
		Take(&ds).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, common.NewCodedError(common.CodeNotFound, "run not found")
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *EvaluationService) ListResults(ctx context.Context, userID, runID string) ([]entity.EvaluationResult, error) {
	if _, err := s.GetRun(ctx, userID, runID); err != nil {
		return nil, err
	}
	var rows []entity.EvaluationResult
	err := s.db.WithContext(ctx).
		Where("run_id = ?", runID).
		Order("create_time ASC").
		Find(&rows).Error
	return rows, err
}
