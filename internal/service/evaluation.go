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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
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
	// Bound concurrent case workers (chat + RAGAS judge). Higher values
	// speed large runs but stress the LLM provider and RAG backend.
	evalCaseConcurrency = 4
)

// EvaluationService manages evaluation datasets, cases, runs, and RAGAS scoring.
type EvaluationService struct {
	chatSession *ChatSessionService
	factory     *ModelFactory
	db          *gorm.DB
	// activeRuns prevents launching two workers for the same run in one process.
	activeRuns sync.Map
	// activeGens prevents launching two generate workers for the same dataset.
	activeGens sync.Map
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
	userID, datasetID, name, dialogID, llmID, judgeLLMID, judgeEmbdID string,
	metrics []string,
	kbIDs []string,
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
	llmID = strings.TrimSpace(llmID)
	judgeLLMID = strings.TrimSpace(judgeLLMID)
	judgeEmbdID = strings.TrimSpace(judgeEmbdID)
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
	kbIDs = normalizeEvalKBIDs(kbIDs)
	kbAny := make([]any, len(kbIDs))
	for i, id := range kbIDs {
		kbAny[i] = id
	}
	now := time.Now().UnixMilli()
	snapshot := entity.JSONMap{
		"dialog_id":     dialogID,
		"llm_id":        llmID,
		"judge_llm_id":  judgeLLMID,
		"judge_embd_id": judgeEmbdID,
		"metrics":       metricAny,
	}
	if len(kbIDs) > 0 {
		snapshot["kb_ids"] = kbAny
	}
	run := &entity.EvaluationRun{
		ID:             common.GenerateUUID(),
		DatasetID:      datasetID,
		DialogID:       dialogID,
		Name:           name,
		ConfigSnapshot: snapshot,
		Status:         evalRunPending,
		CreatedBy:      userID,
		CreateTime:     now,
	}
	if err := s.db.WithContext(ctx).Create(run).Error; err != nil {
		return nil, err
	}

	tenantID, err := requireCurrentTenantID(ctx, userID)
	if err != nil {
		return nil, err
	}
	s.launchWorker(context.WithoutCancel(ctx), userID, tenantID, run.ID, datasetID, dialogID, llmID, judgeLLMID, judgeEmbdID, metrics, kbIDs)
	return run, nil
}

// ResumeIncompleteRuns restarts PENDING/RUNNING workers after process restart,
// skipping cases that already have results.
func (s *EvaluationService) ResumeIncompleteRuns(ctx context.Context) {
	if s.db == nil {
		return
	}
	var runs []entity.EvaluationRun
	if err := s.db.WithContext(ctx).
		Where("status IN ?", []string{evalRunPending, evalRunRunning}).
		Order("create_time ASC").
		Find(&runs).Error; err != nil {
		common.Error("resume evaluation runs: list incomplete", err)
		return
	}
	if len(runs) == 0 {
		return
	}
	common.Info("resuming incomplete evaluation runs", zap.Int("count", len(runs)))
	for i := range runs {
		s.resumeRun(ctx, &runs[i])
	}
}

func (s *EvaluationService) resumeRun(ctx context.Context, run *entity.EvaluationRun) {
	cfg := run.ConfigSnapshot
	if cfg == nil {
		cfg = entity.JSONMap{}
	}
	dialogID := strings.TrimSpace(evalConfigString(cfg, "dialog_id"))
	if dialogID == "" {
		dialogID = strings.TrimSpace(run.DialogID)
	}
	llmID := strings.TrimSpace(evalConfigString(cfg, "llm_id"))
	judgeLLMID := strings.TrimSpace(evalConfigString(cfg, "judge_llm_id"))
	judgeEmbdID := strings.TrimSpace(evalConfigString(cfg, "judge_embd_id"))
	metrics := evalConfigMetrics(cfg)
	if len(metrics) == 0 {
		metrics = append([]string(nil), defaultEvalMetrics...)
	}
	kbIDs := evalConfigKBIDs(cfg)

	var ds entity.EvaluationDataset
	if err := s.db.WithContext(ctx).Where("id = ?", run.DatasetID).Take(&ds).Error; err != nil {
		common.Error(fmt.Sprintf("resume evaluation run %s: load dataset", run.ID), err)
		now := time.Now().UnixMilli()
		_ = s.db.WithContext(ctx).Model(&entity.EvaluationRun{}).
			Where("id = ?", run.ID).
			Updates(map[string]any{
				"status":          evalRunFailed,
				"complete_time":   now,
				"metrics_summary": entity.JSONMap{"error": "resume failed: " + err.Error()},
			}).Error
		return
	}
	userID := strings.TrimSpace(run.CreatedBy)
	if userID == "" {
		userID = strings.TrimSpace(ds.CreatedBy)
	}
	s.launchWorker(context.WithoutCancel(ctx), userID, ds.TenantID, run.ID, run.DatasetID, dialogID, llmID, judgeLLMID, judgeEmbdID, metrics, kbIDs)
}

func (s *EvaluationService) launchWorker(
	ctx context.Context,
	userID, tenantID, runID, datasetID, dialogID, llmID, judgeLLMID, judgeEmbdID string,
	metrics []string,
	kbIDs []string,
) {
	if _, loaded := s.activeRuns.LoadOrStore(runID, struct{}{}); loaded {
		return
	}
	go func() {
		defer s.activeRuns.Delete(runID)
		s.runWorker(ctx, userID, tenantID, runID, datasetID, dialogID, llmID, judgeLLMID, judgeEmbdID, metrics, kbIDs)
	}()
}

func (s *EvaluationService) runWorker(
	ctx context.Context,
	userID, tenantID, runID, datasetID, dialogID, llmID, judgeLLMID, judgeEmbdID string,
	metrics []string,
	kbIDs []string,
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

	if err := s.preflightDialogChatModel(ctx, tenantID, dialogID, llmID); err != nil {
		fail(err.Error())
		return
	}

	var existing []entity.EvaluationResult
	if err := s.db.WithContext(ctx).Where("run_id = ?", runID).Find(&existing).Error; err != nil {
		fail(err.Error())
		return
	}
	doneCases := make(map[string]struct{}, len(existing))
	sums := map[string]float64{}
	counts := map[string]int{}
	for _, row := range existing {
		doneCases[row.CaseID] = struct{}{}
		accumulateEvalMetrics(sums, counts, row.Metrics, metrics)
	}

	pending := make([]entity.EvaluationCase, 0, len(cases))
	for _, caze := range cases {
		if _, ok := doneCases[caze.ID]; ok {
			continue
		}
		pending = append(pending, caze)
	}
	if len(existing) > 0 {
		common.Info("evaluation run resume",
			zap.String("run_id", runID),
			zap.Int("done", len(doneCases)),
			zap.Int("pending", len(pending)),
			zap.Int("total", len(cases)),
		)
	}

	scorer := newRagasScorer(s.factory, tenantID, judgeLLMID, judgeEmbdID)
	var mu sync.Mutex

	if len(pending) > 0 {
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(evalCaseConcurrency)
		for _, caze := range pending {
			caze := caze
			g.Go(func() error {
				started := time.Now()
				gt := ""
				if caze.ReferenceAnswer != nil {
					gt = *caze.ReferenceAnswer
				}
				kwargs := map[string]any{"store_history_messages": false}
				if len(kbIDs) > 0 {
					kwargs["kb_ids"] = kbIDs
				}
				result, err := s.chatSession.ChatCompletions(
					gctx, userID, dialogID, "",
					[]map[string]any{{"role": "user", "content": caze.Question}},
					"", nil, llmID, nil,
					kwargs,
					false, false, nil,
				)
				answer := ""
				var ref map[string]any
				if err == nil && result != nil {
					answer, _ = result["answer"].(string)
					ref, _ = result["reference"].(map[string]any)
				}
				if err != nil || isChatPipelineErrorAnswer(answer) {
					chatErr := ""
					if err != nil {
						chatErr = err.Error()
					} else {
						chatErr = strings.TrimSpace(strings.TrimPrefix(answer, "**ERROR**:"))
						if chatErr == "" {
							chatErr = answer
						}
					}
					row := &entity.EvaluationResult{
						ID:              common.GenerateUUID(),
						RunID:           runID,
						CaseID:          caze.ID,
						GeneratedAnswer: answer,
						RetrievedChunks: entity.JSONMap{"contexts": []string{}},
						Metrics: entity.JSONMap{
							"errors": map[string]string{"chat": chatErr},
						},
						ExecutionTime: time.Since(started).Seconds(),
						CreateTime:    time.Now().UnixMilli(),
					}
					return s.db.WithContext(gctx).Create(row).Error
				}
				contexts := extractContextsFromReference(ref)
				payload := prepareScoringPayload(caze.Question, answer, contexts, gt)
				scores := scorer.scoreAll(gctx, payload, metrics)
				mu.Lock()
				accumulateEvalMetrics(sums, counts, scores, metrics)
				mu.Unlock()
				retrieved := entity.JSONMap{"contexts": payload.Contexts}
				if ref != nil {
					retrieved["reference"] = ref
				}
				row := &entity.EvaluationResult{
					ID:              common.GenerateUUID(),
					RunID:           runID,
					CaseID:          caze.ID,
					GeneratedAnswer: payload.Answer,
					RetrievedChunks: retrieved,
					Metrics:         entity.JSONMap(scores),
					ExecutionTime:   time.Since(started).Seconds(),
					CreateTime:      time.Now().UnixMilli(),
				}
				return s.db.WithContext(gctx).Create(row).Error
			})
		}
		if err := g.Wait(); err != nil {
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
	summary["concurrency"] = evalCaseConcurrency
	summary["resumed_from"] = len(doneCases)
	now := time.Now().UnixMilli()
	_ = s.db.WithContext(ctx).Model(&entity.EvaluationRun{}).
		Where("id = ?", runID).
		Updates(map[string]any{
			"status":          evalRunCompleted,
			"complete_time":   now,
			"metrics_summary": summary,
		}).Error
}

func evalConfigString(cfg entity.JSONMap, key string) string {
	if cfg == nil {
		return ""
	}
	v, ok := cfg[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func normalizeEvalKBIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func evalConfigKBIDs(cfg entity.JSONMap) []string {
	if cfg == nil {
		return nil
	}
	raw, ok := cfg["kb_ids"]
	if !ok || raw == nil {
		return nil
	}
	switch t := raw.(type) {
	case []string:
		return normalizeEvalKBIDs(t)
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s := strings.TrimSpace(fmt.Sprint(item))
			if s != "" {
				out = append(out, s)
			}
		}
		return normalizeEvalKBIDs(out)
	default:
		return nil
	}
}

func evalConfigMetrics(cfg entity.JSONMap) []string {
	if cfg == nil {
		return nil
	}
	raw, ok := cfg["metrics"]
	if !ok || raw == nil {
		return nil
	}
	switch t := raw.(type) {
	case []string:
		return append([]string(nil), t...)
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s := strings.TrimSpace(fmt.Sprint(item))
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func accumulateEvalMetrics(sums map[string]float64, counts map[string]int, metricsMap map[string]any, names []string) {
	if metricsMap == nil {
		return
	}
	for _, name := range names {
		v, ok := metricsMap[name]
		if !ok {
			continue
		}
		f, ok := evalMetricFloat(v)
		if !ok {
			continue
		}
		sums[name] += f
		counts[name]++
	}
}

func evalMetricFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	default:
		return 0, false
	}
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

func (s *EvaluationService) DeleteRun(ctx context.Context, userID, runID string) error {
	if _, err := s.GetRun(ctx, userID, runID); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("run_id = ?", runID).Delete(&entity.EvaluationResult{}).Error; err != nil {
			return err
		}
		res := tx.Where("id = ?", runID).Delete(&entity.EvaluationRun{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return common.NewCodedError(common.CodeNotFound, "run not found")
		}
		return nil
	})
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

func isChatPipelineErrorAnswer(answer string) bool {
	return strings.HasPrefix(strings.TrimSpace(answer), "**ERROR**")
}

// preflightDialogChatModel fails the run early when the chat model cannot be
// resolved, instead of scoring N pipeline "**ERROR**" answers. llmID overrides
// the dialog default (same as ChatCompletions per-turn llm_id).
func (s *EvaluationService) preflightDialogChatModel(ctx context.Context, tenantID, dialogID, llmID string) error {
	if s.factory == nil {
		return fmt.Errorf("model factory not configured")
	}
	modelRef := strings.TrimSpace(llmID)
	if modelRef == "" {
		var chat entity.Chat
		if err := s.db.WithContext(ctx).Where("id = ?", dialogID).Take(&chat).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("dialog %s not found", dialogID)
			}
			return err
		}
		modelRef = strings.TrimSpace(chat.LLMID)
		if chat.TenantLLMID != nil && strings.TrimSpace(*chat.TenantLLMID) != "" {
			modelRef = strings.TrimSpace(*chat.TenantLLMID)
		}
	}
	if modelRef == "" {
		return fmt.Errorf("dialog %s has empty llm_id; select llm_id for the run", dialogID)
	}
	_, err := s.factory.NewChatModel(ctx, ModelAccess{TenantID: tenantID}, modelRef)
	if err != nil {
		return fmt.Errorf("chat model unavailable (%s): %w", modelRef, err)
	}
	return nil
}
