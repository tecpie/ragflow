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

	"gorm.io/gorm"
)

const maxTaskPageNumber int64 = 100000000

// unclaimedDocCompileGrace is how long a doc_compile row may stay at progress 0
// before it is treated as abandoned. A live worker claims the row (progress
// moves off 0) within seconds. Rows left at 0 were published and then dropped,
// which used to block every later compile with "currently being processed".
const unclaimedDocCompileGrace = 30 * time.Second

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

		taskID := common.GenerateUUID()
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
	now := time.Now()
	for _, t := range tasks {
		if docCompileInFlight(t, now) {
			return true, nil
		}
	}
	return false, nil
}

// docCompileInFlight reports whether a knowledge-compile task still owns the
// document. A worker claim moves progress off 0 and stays busy until the task
// finishes or fails. A row left at 0 is only busy during the queue grace; after
// that the caller may supersede it and publish a new task.
func docCompileInFlight(t *entity.Task, now time.Time) bool {
	if t == nil || t.TaskType != common.TaskTypeDocCompile {
		return false
	}
	if t.Progress < 0 || t.Progress >= 1 {
		return false
	}
	if t.Progress > 0 {
		return true
	}
	return !unclaimedDocCompileStale(t, now)
}

func unclaimedDocCompileStale(t *entity.Task, now time.Time) bool {
	var begin time.Time
	if t.BeginAt != nil {
		begin = *t.BeginAt
	} else if t.CreateDate != nil {
		begin = *t.CreateDate
	}
	if begin.IsZero() {
		return true
	}
	return now.Sub(begin) >= unclaimedDocCompileGrace
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
		if t.TaskType == common.TaskTypeDocCompile {
			if t.Progress > 0 {
				return fmt.Errorf("Can't compile a document that is currently being processed")
			}
			closed, err := s.taskDAO.SupersedeUnclaimedDocCompile(ctx, dao.DB, t.ID, msg)
			if err != nil {
				return err
			}
			if !closed {
				return fmt.Errorf("Can't compile a document that is currently being processed")
			}
			continue
		}
		if err := s.taskDAO.UpdateProgress(ctx, dao.DB, t.ID, 1, msg); err != nil {
			return err
		}
	}
	return nil
}
