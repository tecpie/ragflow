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
	"time"

	"ragflow/internal/common"
	"ragflow/internal/dao"
	"ragflow/internal/entity"
	taskpkg "ragflow/internal/ingestion/task"

	"gorm.io/gorm"
)

// executeDocCompile runs one knowledge-compile wake-up to a terminal task row.
// The broker message is only a signal; the task table holds the document id.
// A dropped message used to leave progress at 0 forever, and the next compile
// request was rejected with "currently being processed".
func (e *Ingestor) executeDocCompile(ctx context.Context, taskID string, hb *Heartbeat, handle common.TaskHandle) {
	claimed := false
	docID := ""
	defer func() {
		if recovered := recover(); recovered != nil {
			common.Error(fmt.Sprintf("doc compile task %s panicked: %v", taskID, recovered), fmt.Errorf("%v", recovered))
			_ = e.finishDocCompile(ctx, taskID, docID, fmt.Errorf("panic: %v", recovered))
			e.ackHandle(hb, handle, taskID)
		}
	}()

	if taskID == "" {
		common.Warn("doc compile task with empty task id received, ack")
		e.ackHandle(hb, handle, taskID)
		return
	}

	taskDAO := dao.NewTaskDAO()
	task, err := taskDAO.GetByID(ctx, dao.DB, taskID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			common.Warn(fmt.Sprintf("doc compile task %s not found, ack", taskID))
			e.ackHandle(hb, handle, taskID)
			return
		}
		common.Error(fmt.Sprintf("load doc compile task %s", taskID), err)
		e.nackHandle(hb, handle, taskID)
		return
	}
	docID = task.DocID
	if task.TaskType != common.TaskTypeDocCompile || task.Progress < 0 || task.Progress >= 1 {
		e.ackHandle(hb, handle, taskID)
		return
	}
	if !e.claimTask(task.ID) {
		common.Warn(fmt.Sprintf("doc compile task %s redelivered while worker still processing, renew lease", taskID))
		e.renewDuplicateHandle(hb, handle, taskID)
		return
	}
	claimed = true
	defer func() {
		if claimed {
			e.releaseTask(task.ID)
		}
	}()

	runningMsg := time.Now().Format("15:04:05") + " knowledge compile running"
	touched, err := taskDAO.TouchDocCompileRunning(ctx, dao.DB, task.ID, runningMsg)
	if err != nil {
		common.Error(fmt.Sprintf("claim doc compile task %s", taskID), err)
		e.nackHandle(hb, handle, taskID)
		return
	}
	if !touched {
		reloaded, reloadErr := taskDAO.GetByID(ctx, dao.DB, task.ID)
		if reloadErr != nil {
			common.Error(fmt.Sprintf("reload doc compile task %s", taskID), reloadErr)
			e.nackHandle(hb, handle, taskID)
			return
		}
		if reloaded.Progress < 0 || reloaded.Progress >= 1 {
			e.ackHandle(hb, handle, taskID)
			return
		}
	}
	_ = dao.NewDocumentDAO().UpdateByID(ctx, dao.DB, task.DocID, map[string]interface{}{
		"progress":     0.01,
		"progress_msg": runningMsg,
	})

	runner := e.runDocCompile
	if runner == nil {
		runner = e.defaultRunDocCompile
	}
	runErr := runner(ctx, task)
	if runErr != nil && (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) {
		common.Warn(fmt.Sprintf("doc compile task %s interrupted, leave unsettled for redelivery", taskID))
		e.nackHandle(hb, handle, taskID)
		return
	}
	if err := e.finishDocCompile(ctx, task.ID, task.DocID, runErr); err != nil {
		common.Error(fmt.Sprintf("settle doc compile task %s", taskID), err)
		e.nackHandle(hb, handle, taskID)
		return
	}
	e.ackHandle(hb, handle, taskID)
}

func (e *Ingestor) finishDocCompile(ctx context.Context, taskID, docID string, runErr error) error {
	now := time.Now()
	progress := 1.0
	msg := now.Format("15:04:05") + " knowledge compile finished"
	if runErr != nil {
		progress = -1
		msg = now.Format("15:04:05") + " knowledge compile failed: " + runErr.Error()
		common.Error(fmt.Sprintf("doc compile task %s failed", taskID), runErr)
	}
	if docID != "" {
		_ = dao.NewDocumentDAO().UpdateByID(ctx, dao.DB, docID, map[string]interface{}{
			"progress":     progress,
			"progress_msg": msg,
		})
	}
	return dao.NewTaskDAO().UpdateProgress(ctx, dao.DB, taskID, progress, msg)
}

// defaultRunDocCompile loads the document's dataset tenant and recompiles from
// source chunks. Dataset-level graph merge is woken by the compile event.
func (e *Ingestor) defaultRunDocCompile(ctx context.Context, task *entity.Task) error {
	if task == nil || task.DocID == "" {
		return fmt.Errorf("doc compile: task has no document")
	}
	doc, err := dao.NewDocumentDAO().GetByID(ctx, dao.DB, task.DocID)
	if err != nil {
		return fmt.Errorf("doc compile: load document: %w", err)
	}
	kb, err := dao.NewKnowledgebaseDAO().GetByID(ctx, dao.DB, doc.KbID)
	if err != nil {
		return fmt.Errorf("doc compile: load dataset: %w", err)
	}
	return taskpkg.CompileDocumentFromSourceChunks(ctx, kb.TenantID, doc.KbID, doc.ID)
}
