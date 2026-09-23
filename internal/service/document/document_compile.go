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

package document

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ragflow/internal/common"
	"ragflow/internal/dao"
	"ragflow/internal/entity"
	"ragflow/internal/service"
	"ragflow/internal/utility"

	"gorm.io/gorm"
)

const maxTaskPageNumber int64 = 100000000

// CompileDocumentsResult is the response payload for POST .../documents/compile.
type CompileDocumentsResult struct {
	SuccessCount int      `json:"success_count"`
	Skipped      []string `json:"skipped"`
}

// CompileDocuments queues knowledge-compilation-only tasks for the given documents.
// Source chunks are kept; the file is not re-parsed.
func (s *DocumentService) CompileDocuments(ctx context.Context, datasetID, userID string, docIDs []string) (*CompileDocumentsResult, error) {
	unique := common.Deduplicate(docIDs)
	if len(unique) == 0 {
		return nil, fmt.Errorf("`document_ids` is required")
	}

	publisher := service.NewMessageQueueTaskPublisher()
	result := &CompileDocumentsResult{Skipped: make([]string, 0)}
	now := time.Now()
	queuedMsg := now.Format("15:04:05") + " knowledge compile queued"

	notFound := make([]string, 0)
	for _, docID := range unique {
		doc, err := s.documentDAO.GetByID(ctx, dao.DB, docID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				notFound = append(notFound, docID)
				continue
			}
			return nil, err
		}
		if doc == nil || doc.KbID != datasetID {
			notFound = append(notFound, docID)
			continue
		}
		if doc.Status != nil && *doc.Status == "0" {
			result.Skipped = append(result.Skipped, docID)
			continue
		}
		if busy, err := s.documentCompileBlocked(ctx, docID); err != nil {
			return nil, err
		} else if busy {
			return nil, fmt.Errorf("Can't compile a document that is currently being processed")
		}

		if err := s.closeUnfinishedTasks(ctx, docID, now); err != nil {
			return nil, err
		}

		taskID := utility.GenerateUUID()
		progressMsg := now.Format("15:04:05") + " created task doc_compile"
		task := &entity.Task{
			ID:          taskID,
			DocID:       docID,
			FromPage:    maxTaskPageNumber,
			ToPage:      maxTaskPageNumber,
			TaskType:    common.TaskTypeDocCompile,
			BeginAt:     &now,
			Progress:    0,
			ProgressMsg: &progressMsg,
		}
		if err := s.taskDAO.Create(ctx, dao.DB, task); err != nil {
			return nil, fmt.Errorf("create compile task for %s: %w", docID, err)
		}

		beginAt := now
		if err := s.documentDAO.UpdateByID(ctx, dao.DB, docID, map[string]interface{}{
			"progress":         float64(0),
			"progress_msg":     queuedMsg,
			"process_begin_at": beginAt,
		}); err != nil {
			return nil, fmt.Errorf("mark document %s compiling: %w", docID, err)
		}

		if err := publisher.PublishTaskMessage(common.TaskSubject, common.TaskMessage{
			TaskID:   taskID,
			TaskType: common.TaskTypeDocCompile,
		}); err != nil {
			_ = s.taskDAO.UpdateProgress(ctx, dao.DB, taskID, -1, "Failed to enqueue knowledge compile: "+err.Error())
			_ = s.documentDAO.UpdateByID(ctx, dao.DB, docID, map[string]interface{}{
				"progress":     float64(-1),
				"progress_msg": "Failed to enqueue knowledge compile",
			})
			return nil, fmt.Errorf("enqueue compile task for %s: %w", docID, err)
		}
		result.SuccessCount++
	}

	if result.SuccessCount == 0 && len(result.Skipped) == 0 {
		return nil, fmt.Errorf("Documents not found: %v", notFound)
	}
	_ = userID
	return result, nil
}

func (s *DocumentService) documentCompileBlocked(ctx context.Context, docID string) (bool, error) {
	if s.ingestionTaskDAO != nil {
		existing, err := s.ingestionTaskDAO.GetByDocumentID(ctx, dao.DB, docID)
		if err != nil {
			return false, err
		}
		if existing != nil {
			switch existing.Status {
			case common.CREATED, common.SCHEDULED, common.RUNNING, common.STOPPING:
				return true, nil
			}
		}
	}
	tasks, err := s.taskDAO.GetByDocID(ctx, dao.DB, docID)
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		if t == nil {
			continue
		}
		if t.TaskType == common.TaskTypeDocCompile && t.Progress >= 0 && t.Progress < 1 {
			return true, nil
		}
	}
	return false, nil
}

func (s *DocumentService) closeUnfinishedTasks(ctx context.Context, docID string, now time.Time) error {
	tasks, err := s.taskDAO.GetByDocID(ctx, dao.DB, docID)
	if err != nil {
		return err
	}
	msg := now.Format("15:04:05") + " Closed: superseded by knowledge compile"
	for _, t := range tasks {
		if t == nil || t.Progress < 0 || t.Progress >= 1 {
			continue
		}
		if err := s.taskDAO.UpdateProgress(ctx, dao.DB, t.ID, 1, msg); err != nil {
			return err
		}
	}
	return nil
}
