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

//go:build !ee

package admin

import (
	"context"
	"fmt"

	"ragflow/internal/entity"

	"gorm.io/gorm"
)

// deleteEvaluationsEE removes evaluation datasets/cases/runs/results owned by
// the deleted user. Tecpie keeps evaluation in the open build; the official
// EE hook name is retained so the shared user_delete call site stays intact.
func deleteEvaluationsEE(ctx context.Context, tx *gorm.DB, userID, ownedTenantID string, result *DeleteUserResult) error {
	selectIDs := func(model interface{}, condition string, args ...interface{}) ([]string, error) {
		var ids []string
		err := tx.WithContext(ctx).Model(model).Where(condition, args...).Pluck("id", &ids).Error
		return ids, err
	}
	removeIDs := func(label string, model interface{}, column string, ids []string) error {
		var total int64
		for start := 0; start < len(ids); start += 1000 {
			end := min(start+1000, len(ids))
			deleted := tx.WithContext(ctx).Unscoped().Where(column+" IN ?", ids[start:end]).Delete(model)
			if deleted.Error != nil {
				return fmt.Errorf("delete %s: %w", label, deleted.Error)
			}
			total += deleted.RowsAffected
		}
		if total > 0 {
			result.DeletedDetails = append(result.DeletedDetails, fmt.Sprintf("- Deleted %d %s records.", total, label))
		}
		return nil
	}

	var datasetIDs, runIDs []string
	var err error
	if ownedTenantID != "" {
		datasetIDs, err = selectIDs(&entity.EvaluationDataset{}, "tenant_id = ? OR created_by = ?", ownedTenantID, userID)
	} else {
		datasetIDs, err = selectIDs(&entity.EvaluationDataset{}, "created_by = ?", userID)
	}
	if err != nil {
		return err
	}
	if len(datasetIDs) > 0 {
		runIDs, err = selectIDs(&entity.EvaluationRun{}, "dataset_id IN ? OR created_by = ?", datasetIDs, userID)
	} else {
		runIDs, err = selectIDs(&entity.EvaluationRun{}, "created_by = ?", userID)
	}
	if err != nil {
		return err
	}
	if err := removeIDs("evaluation results", &entity.EvaluationResult{}, "run_id", runIDs); err != nil {
		return err
	}
	if err := removeIDs("evaluation runs", &entity.EvaluationRun{}, "id", runIDs); err != nil {
		return err
	}
	if len(datasetIDs) == 0 {
		return nil
	}
	caseIDs, err := selectIDs(&entity.EvaluationCase{}, "dataset_id IN ?", datasetIDs)
	if err != nil {
		return err
	}
	if err := removeIDs("evaluation results", &entity.EvaluationResult{}, "case_id", caseIDs); err != nil {
		return err
	}
	if err := removeIDs("evaluation cases", &entity.EvaluationCase{}, "id", caseIDs); err != nil {
		return err
	}
	return removeIDs("evaluation datasets", &entity.EvaluationDataset{}, "id", datasetIDs)
}
