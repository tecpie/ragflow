package document

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ragflow/internal/common"
	"ragflow/internal/dao"
	enginetypes "ragflow/internal/engine/types"
	"ragflow/internal/entity"
	"ragflow/internal/storage"

	"gorm.io/gorm"
)

const (
	reuseParsedPageSize  = 200
	reuseParsedMaxChunks = 20000
	// reusedChunksProgressMsg is the document progress text and the ingestion
	// event message written after a successful copy.
	reusedChunksProgressMsg = "Reused parsed chunks from the source document"
)

// ReuseParsedError marks a reuse request the caller can correct: the document
// does not belong to the dataset, a required field is missing, the embedding
// model differs, or the source and target are the same document.
type ReuseParsedError struct {
	Argument bool
	Err      error
}

func (e *ReuseParsedError) Error() string {
	if e == nil || e.Err == nil {
		return "reuse parsed chunks"
	}
	return e.Err.Error()
}

func (e *ReuseParsedError) Unwrap() error { return e.Err }

func reuseArgument(err error) error {
	return &ReuseParsedError{Argument: true, Err: err}
}

// ReuseParsedChunksResult is the outcome of copying a source document's parsed
// chunks, including their vectors, onto an already uploaded target document.
type ReuseParsedChunksResult struct {
	DocumentID string `json:"document_id"`
	ChunkCount int    `json:"chunk_count"`
	TokenCount int64  `json:"token_count"`
}

// ReuseParsedChunks copies the source document's file chunks into the target
// document without running the parser. Vectors are kept as stored. The mind
// map is copied with the body chunks and its source chunk ids are remapped.
// Other compiled rows (graph, raptor, wiki, page index) are skipped. An active
// ingestion task is rejected. A finished task is written or updated to
// COMPLETED so the file list shows the document as parsed; a later reparse
// with delete removes that terminal task before enqueueing.
func (s *DocumentService) ReuseParsedChunks(ctx context.Context, targetDatasetID, targetDocumentID, sourceDatasetID, sourceDocumentID string) (result *ReuseParsedChunksResult, err error) {
	if s.docEngine == nil {
		return nil, errors.New("doc engine not initialized")
	}
	targetKB, targetDoc, sourceKB, sourceDoc, err := s.loadReuseDocuments(ctx, targetDatasetID, targetDocumentID, sourceDatasetID, sourceDocumentID)
	if err != nil {
		return nil, err
	}
	if err = s.rejectActiveReuseTask(ctx, targetDoc.ID); err != nil {
		return nil, err
	}

	rawChunks, err := s.listDocumentChunks(ctx, sourceKB, sourceDoc.ID)
	if err != nil {
		return nil, err
	}
	prepared := prepareReusedChunks(rawChunks, targetKB.ID, targetDoc.ID, documentName(targetDoc))
	if reusedBodyCount(prepared) == 0 {
		return nil, errors.New("source document has no reusable parsed chunks")
	}
	if !chunksCarryVector(prepared) {
		return nil, errors.New("source chunks have no vectors to reuse")
	}

	copiedImages, err := s.copyReusedChunkImages(ctx, sourceKB.ID, prepared)
	if err != nil {
		s.removeCopiedImages(ctx, copiedImages)
		return nil, err
	}

	targetIndex := chunkIndexName(targetKB.TenantID)
	insertedIDs := reusedChunkIDs(prepared)
	replaced := false
	defer func() {
		if err == nil {
			return
		}
		if len(insertedIDs) > 0 {
			if _, delErr := s.docEngine.DeleteChunks(ctx, map[string]interface{}{"id": insertedIDs}, targetIndex, targetKB.ID); delErr != nil && !errors.Is(delErr, enginetypes.ErrIndexNotFound) {
				common.Warn(fmt.Sprintf("reuse parsed chunks: failed to roll back inserted chunks %s: %v", targetDoc.ID, delErr))
			}
		}
		if replaced {
			if countErr := s.ApplyDocCounts(ctx, targetDoc.ID, targetKB.ID, 0, 0, 0); countErr != nil {
				common.Warn(fmt.Sprintf("reuse parsed chunks: failed to roll back counts %s: %v", targetDoc.ID, countErr))
			}
		}
		s.removeCopiedImages(ctx, copiedImages)
	}()

	language := ""
	if targetKB.Language != nil {
		language = *targetKB.Language
	}
	// Insert the new ids first. A failed insert leaves the target's existing
	// chunks in place; only this batch is rolled back.
	if _, err = s.docEngine.InsertChunks(ctx, prepared, targetIndex, targetKB.ID, language); err != nil {
		return nil, fmt.Errorf("insert reused chunks: %w", err)
	}
	if err = s.deleteTargetChunksExcept(ctx, targetKB, targetDoc.ID, targetIndex, insertedIDs); err != nil {
		return nil, err
	}
	replaced = true

	tokenNum := reusedTokenCount(prepared)
	if err = s.ApplyDocCounts(ctx, targetDoc.ID, targetKB.ID, reusedBodyCount(prepared), int(tokenNum), 0); err != nil {
		return nil, err
	}
	if err = s.markReusedDocumentParsed(ctx, targetDoc, targetKB); err != nil {
		return nil, err
	}
	return &ReuseParsedChunksResult{
		DocumentID: targetDoc.ID,
		ChunkCount: reusedBodyCount(prepared),
		TokenCount: tokenNum,
	}, nil
}

func (s *DocumentService) loadReuseDocuments(ctx context.Context, targetDatasetID, targetDocumentID, sourceDatasetID, sourceDocumentID string) (*entity.Knowledgebase, *entity.Document, *entity.Knowledgebase, *entity.Document, error) {
	if targetDocumentID == sourceDocumentID {
		return nil, nil, nil, nil, reuseArgument(errors.New("source document and target document are the same"))
	}
	targetKB, err := s.kbDAO.GetByID(ctx, dao.DB, targetDatasetID)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("target dataset: %w", err)
	}
	sourceKB, err := s.kbDAO.GetByID(ctx, dao.DB, sourceDatasetID)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("source dataset: %w", err)
	}
	if err = reuseEmbeddingMatches(sourceKB, targetKB); err != nil {
		return nil, nil, nil, nil, err
	}
	targetDoc, err := s.documentDAO.GetByID(ctx, dao.DB, targetDocumentID)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("target document: %w", err)
	}
	if targetDoc.KbID != targetKB.ID {
		return nil, nil, nil, nil, reuseArgument(errors.New("target document does not belong to the target dataset"))
	}
	sourceDoc, err := s.documentDAO.GetByID(ctx, dao.DB, sourceDocumentID)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("source document: %w", err)
	}
	if sourceDoc.KbID != sourceKB.ID {
		return nil, nil, nil, nil, reuseArgument(errors.New("source document does not belong to the source dataset"))
	}
	return targetKB, targetDoc, sourceKB, sourceDoc, nil
}

func reuseEmbeddingMatches(source, target *entity.Knowledgebase) error {
	if source == nil || target == nil {
		return reuseArgument(errors.New("embedding model is not set"))
	}
	sourceEmbd := strings.TrimSpace(source.EmbdID)
	targetEmbd := strings.TrimSpace(target.EmbdID)
	if sourceEmbd == "" || targetEmbd == "" {
		return reuseArgument(errors.New("embedding model is not set"))
	}
	if sourceEmbd != targetEmbd || embeddingTenantID(source) != embeddingTenantID(target) {
		return reuseArgument(errors.New("source and target embedding models do not match"))
	}
	return nil
}

func embeddingTenantID(kb *entity.Knowledgebase) string {
	if kb == nil || kb.TenantEmbdID == nil {
		return ""
	}
	return strings.TrimSpace(*kb.TenantEmbdID)
}

func (s *DocumentService) rejectActiveReuseTask(ctx context.Context, documentID string) error {
	task, err := s.ingestionTaskDAO.GetByDocumentID(ctx, dao.DB, documentID)
	if err != nil {
		return err
	}
	if task != nil && common.IsActiveTaskStatus(task.Status) {
		return reuseArgument(fmt.Errorf("target document has an active ingestion task (%s)", task.Status))
	}
	return nil
}

func (s *DocumentService) listDocumentChunks(ctx context.Context, kb *entity.Knowledgebase, documentID string) ([]map[string]interface{}, error) {
	indexName := chunkIndexName(kb.TenantID)
	chunks := make([]map[string]interface{}, 0)
	seen := make(map[string]struct{})
	// chunk_order_int alone is not unique. id is the stored keyword cursor, so a
	// repeated or missing order does not skip or repeat a row.
	order := (&enginetypes.OrderByExpr{}).Asc("chunk_order_int").Asc("id")
	for offset := 0; offset < reuseParsedMaxChunks; offset += reuseParsedPageSize {
		result, err := s.docEngine.Search(ctx, &enginetypes.SearchRequest{
			IndexNames:         []string{indexName},
			KbIDs:              []string{kb.ID},
			Offset:             offset,
			Limit:              reuseParsedPageSize,
			IncludeUnavailable: true,
			SelectFields:       reuseChunkSelectFields(),
			Filter:             map[string]interface{}{"doc_id": documentID},
			OrderBy:            order,
		})
		if err != nil {
			if errors.Is(err, enginetypes.ErrIndexNotFound) {
				return nil, nil
			}
			return nil, fmt.Errorf("list document chunks: %w", err)
		}
		if result == nil || len(result.Chunks) == 0 {
			return chunks, nil
		}
		added := 0
		for _, chunk := range result.Chunks {
			id := chunkID(chunk)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			chunks = append(chunks, chunk)
			added++
		}
		if added == 0 {
			return nil, errors.New("chunk page repeated; refusing to continue with a partial set")
		}
		if int64(len(seen)) >= result.Total || len(result.Chunks) < reuseParsedPageSize {
			return chunks, nil
		}
	}
	return nil, fmt.Errorf("document has more than %d chunks", reuseParsedMaxChunks)
}

func reuseChunkSelectFields() []string {
	fields := []string{
		"*",
		"content_sm_ltks",
		"compile_kwd",
		"raptor_kwd",
		"knowledge_graph_kwd",
	}
	for _, dim := range []int{256, 512, 768, 1024, 1536, 2560, 3072} {
		fields = append(fields, common.VectorColumnName(dim))
	}
	return fields
}

func (s *DocumentService) deleteTargetChunksExcept(ctx context.Context, kb *entity.Knowledgebase, documentID, indexName string, keep []string) error {
	existing, err := s.listDocumentChunks(ctx, kb, documentID)
	if err != nil {
		return err
	}
	stale := staleChunkIDs(existing, keep)
	for start := 0; start < len(stale); start += reuseParsedPageSize {
		end := start + reuseParsedPageSize
		if end > len(stale) {
			end = len(stale)
		}
		if _, err = s.docEngine.DeleteChunks(ctx, map[string]interface{}{"id": stale[start:end]}, indexName, kb.ID); err != nil && !errors.Is(err, enginetypes.ErrIndexNotFound) {
			return fmt.Errorf("delete previous target chunks: %w", err)
		}
	}
	return nil
}

func staleChunkIDs(chunks []map[string]interface{}, keep []string) []string {
	kept := make(map[string]struct{}, len(keep))
	for _, id := range keep {
		if id != "" {
			kept[id] = struct{}{}
		}
	}
	stale := make([]string, 0)
	seen := make(map[string]struct{})
	for _, chunk := range chunks {
		id := chunkID(chunk)
		if id == "" {
			continue
		}
		if _, ok := kept[id]; ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		stale = append(stale, id)
	}
	return stale
}

func (s *DocumentService) markReusedDocumentParsed(ctx context.Context, doc *entity.Document, kb *entity.Knowledgebase) error {
	if doc == nil || kb == nil {
		return errors.New("reuse parsed chunks: document is missing")
	}
	if dao.DB == nil {
		return errors.New("reuse parsed chunks: database is not configured")
	}
	// Progress, the COMPLETED task, and the bubble event commit together. A
	// failed event must not leave the file list showing a finished parse.
	return dao.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Model(&entity.Document{}).
			Where("id = ? AND kb_id = ?", doc.ID, kb.ID).
			Updates(map[string]interface{}{
				"progress":         1,
				"progress_msg":     reusedChunksProgressMsg,
				"process_begin_at": now,
			}).Error; err != nil {
			return err
		}
		task, err := s.completeReusedIngestionTask(ctx, tx, doc, kb.ID)
		if err != nil {
			return err
		}
		return s.recordReusedIngestionEvent(ctx, tx, doc, kb, task)
	})
}

func (s *DocumentService) completeReusedIngestionTask(ctx context.Context, tx *gorm.DB, doc *entity.Document, datasetID string) (*entity.IngestionTask, error) {
	task, err := s.ingestionTaskDAO.GetByDocumentID(ctx, tx, doc.ID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		created, createErr := s.ingestionTaskDAO.Create(ctx, tx, &entity.IngestionTask{
			UserID:     doc.UserID,
			DocumentID: doc.ID,
			DatasetID:  datasetID,
			Schema:     entity.JSONMap{},
			Status:     common.COMPLETED,
		})
		if createErr == nil {
			return created, nil
		}
		task, err = s.ingestionTaskDAO.GetByDocumentID(ctx, tx, doc.ID)
		if err != nil {
			return nil, err
		}
		if task == nil {
			return nil, createErr
		}
	}
	if common.IsActiveTaskStatus(task.Status) {
		return nil, reuseArgument(fmt.Errorf("target document has an active ingestion task (%s)", task.Status))
	}
	if !common.IsTerminalTaskStatus(task.Status) {
		return nil, fmt.Errorf("target document ingestion task %s has status %s", task.ID, task.Status)
	}
	// An identical COMPLETED row is already what the file list reads. Skipping
	// the write avoids treating a no-op update (zero rows changed) as failure.
	if task.Status != common.COMPLETED || task.DatasetID != datasetID || task.UserID != doc.UserID {
		updated := tx.WithContext(ctx).Model(&entity.IngestionTask{}).
			Where("id = ? AND status IN ?", task.ID, []string{common.COMPLETED, common.FAILED, common.STOPPED}).
			Updates(map[string]interface{}{
				"status":     common.COMPLETED,
				"dataset_id": datasetID,
				"user_id":    doc.UserID,
			})
		if updated.Error != nil {
			return nil, updated.Error
		}
		if updated.RowsAffected == 0 {
			return nil, fmt.Errorf("target document ingestion task %s could not be completed", task.ID)
		}
	}
	task.Status = common.COMPLETED
	task.DatasetID = datasetID
	task.UserID = doc.UserID
	return task, nil
}

func (s *DocumentService) recordReusedIngestionEvent(ctx context.Context, tx *gorm.DB, doc *entity.Document, kb *entity.Knowledgebase, task *entity.IngestionTask) error {
	if task == nil || task.ID == "" {
		return errors.New("reuse parsed chunks: ingestion task is missing")
	}
	logID := ""
	if task.PipelineLogID != nil {
		logID = strings.TrimSpace(*task.PipelineLogID)
	}
	if logID == "" {
		createdID, err := s.openReusedIngestionLog(ctx, tx, doc, kb)
		if err != nil {
			return err
		}
		logID = createdID
		if err = tx.WithContext(ctx).Model(&entity.IngestionTask{}).
			Where("id = ?", task.ID).
			Update("pipeline_log_id", logID).Error; err != nil {
			return err
		}
		task.PipelineLogID = &logID
	}
	if err := tx.WithContext(ctx).Model(&entity.PipelineOperationLog{}).
		Where("id = ?", logID).
		Updates(map[string]interface{}{
			"operation_status": string(entity.TaskStatusDone),
			"progress":         1,
			"progress_msg":     reusedChunksProgressMsg,
		}).Error; err != nil {
		return err
	}
	// A new message event, not a terminal event: an existing FAILED run already
	// has a terminal row, and the file-list bubble reads the latest event.
	now := time.Now().Local()
	return tx.WithContext(ctx).Model(&entity.IngestionTaskLog{}).Create(map[string]interface{}{
		"task_id":         task.ID,
		"pipeline_log_id": logID,
		"checkpoint":      entity.JSONMap{},
		"phase":           0,
		"event_type":      dao.EventTypeMessage,
		"component":       "",
		"message":         reusedChunksProgressMsg,
		"create_time":     now.UnixMilli(),
		"create_date":     now.Truncate(time.Second),
		"update_time":     now.UnixMilli(),
		"update_date":     now.Truncate(time.Second),
	}).Error
}

func (s *DocumentService) openReusedIngestionLog(ctx context.Context, tx *gorm.DB, doc *entity.Document, kb *entity.Knowledgebase) (string, error) {
	runCount, err := dao.NewPipelineOperationLogDAO().NextRunCount(ctx, tx, doc.ID)
	if err != nil {
		return "", err
	}
	sourceFrom := doc.SourceType
	if i := strings.Index(sourceFrom, "/"); i >= 0 {
		sourceFrom = sourceFrom[:i]
	}
	if strings.TrimSpace(sourceFrom) == "" {
		sourceFrom = "local"
	}
	log, err := dao.NewPipelineOperationLogDAO().CreateOpenLog(ctx, tx, dao.OpenLogInput{
		DocumentID:      doc.ID,
		KbID:            kb.ID,
		TenantID:        kb.TenantID,
		ParserID:        doc.ParserID,
		DocumentName:    documentName(doc),
		DocumentSuffix:  doc.Suffix,
		DocumentType:    doc.Type,
		SourceFrom:      sourceFrom,
		Avatar:          doc.Thumbnail,
		OperationStatus: string(entity.TaskStatusDone),
		RunCount:        runCount,
	})
	if err != nil {
		return "", err
	}
	return log.ID, nil
}

func (s *DocumentService) copyReusedChunkImages(ctx context.Context, sourceKBID string, chunks []map[string]interface{}) ([][2]string, error) {
	storageImpl := storage.GetStorageFactory().GetStorage()
	copied := make([][2]string, 0)
	for _, chunk := range chunks {
		imgID, _ := chunk["img_id"].(string)
		imgID = strings.TrimSpace(imgID)
		if imgID == "" {
			continue
		}
		newID, _ := chunk["id"].(string)
		bucket, key, ok := imageKeyFromSourceKB(sourceKBID, imgID)
		if !ok || newID == "" {
			return copied, fmt.Errorf("copy chunk image %s: invalid storage key", imgID)
		}
		if storageImpl == nil {
			return copied, fmt.Errorf("copy chunk image %s: storage is not configured", imgID)
		}
		targetBucket, _ := chunk["kb_id"].(string)
		blob, getErr := storageImpl.Get(ctx, bucket, key)
		if getErr != nil || len(blob) == 0 {
			if getErr == nil {
				getErr = errors.New("empty object")
			}
			return copied, fmt.Errorf("copy chunk image %s: %w", imgID, getErr)
		}
		if putErr := storageImpl.Put(ctx, targetBucket, newID, blob); putErr != nil {
			return copied, fmt.Errorf("copy chunk image %s: %w", imgID, putErr)
		}
		chunk["img_id"] = targetBucket + "-" + newID
		copied = append(copied, [2]string{targetBucket, newID})
	}
	return copied, nil
}

func (s *DocumentService) removeCopiedImages(ctx context.Context, images [][2]string) {
	storageImpl := storage.GetStorageFactory().GetStorage()
	if storageImpl == nil {
		return
	}
	for _, image := range images {
		if err := storageImpl.Remove(ctx, image[0], image[1]); err != nil {
			common.Warn(fmt.Sprintf("reuse parsed chunks: failed to remove copied image %s/%s: %v", image[0], image[1], err))
		}
	}
}

func prepareReusedChunks(chunks []map[string]interface{}, targetDatasetID, targetDocumentID, targetName string) []map[string]interface{} {
	idMap := make(map[string]string, len(chunks))
	prepared := make([]map[string]interface{}, 0, len(chunks))
	for _, chunk := range chunks {
		if isCompiledChunk(chunk) {
			continue
		}
		oldID := chunkID(chunk)
		if oldID == "" {
			continue
		}
		idMap[oldID] = common.GenerateToken()
	}
	for _, chunk := range chunks {
		if isCompiledChunk(chunk) {
			continue
		}
		oldID := chunkID(chunk)
		newID := idMap[oldID]
		if newID == "" {
			continue
		}
		cloned := cloneChunkFields(chunk)
		cloned["id"] = newID
		cloned["kb_id"] = targetDatasetID
		cloned["doc_id"] = targetDocumentID
		if targetName != "" {
			cloned["docnm_kwd"] = targetName
		}
		if momID, ok := cloned["mom_id"].(string); ok {
			if mapped := idMap[momID]; mapped != "" {
				cloned["mom_id"] = mapped
			} else {
				delete(cloned, "mom_id")
			}
		}
		prepared = append(prepared, cloned)
	}
	for _, chunk := range chunks {
		if !isMindMapChunk(chunk) || chunkID(chunk) == "" {
			continue
		}
		cloned := cloneChunkFields(chunk)
		cloned["id"] = common.GenerateToken()
		cloned["kb_id"] = targetDatasetID
		cloned["doc_id"] = targetDocumentID
		if targetName != "" {
			cloned["docnm_kwd"] = targetName
		}
		remapMindMapSources(cloned, idMap, targetDocumentID)
		prepared = append(prepared, cloned)
	}
	return prepared
}

func isMindMapChunk(chunk map[string]interface{}) bool {
	switch compileKeyword(chunk) {
	case "mind_map", "mindmap":
		return true
	default:
		return false
	}
}

func compileKeyword(chunk map[string]interface{}) string {
	switch typed := chunk["compile_kwd"].(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(typed))
	case []string:
		for _, item := range typed {
			if text := strings.ToLower(strings.TrimSpace(item)); text != "" {
				return text
			}
		}
	case []interface{}:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				if text = strings.ToLower(strings.TrimSpace(text)); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func remapMindMapSources(chunk map[string]interface{}, idMap map[string]string, targetDocumentID string) {
	if raw, ok := chunk["source_chunk_ids"]; ok {
		chunk["source_chunk_ids"] = remapChunkIDList(raw, idMap)
	}
	if _, ok := chunk["source_doc_ids"]; ok {
		chunk["source_doc_ids"] = []string{targetDocumentID}
	}
	payload, ok := mindMapPayload(chunk["content_with_weight"])
	if !ok {
		return
	}
	changed := false
	if raw, ok := payload["source_chunk_ids"]; ok {
		payload["source_chunk_ids"] = remapChunkIDList(raw, idMap)
		changed = true
	}
	if _, ok := payload["source_doc_ids"]; ok {
		payload["source_doc_ids"] = []string{targetDocumentID}
		changed = true
	}
	if !changed {
		return
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	chunk["content_with_weight"] = string(encoded)
}

func mindMapPayload(value interface{}) (map[string]interface{}, bool) {
	var raw []byte
	switch typed := value.(type) {
	case string:
		if !strings.Contains(typed, "source_chunk") && !strings.Contains(typed, "source_doc") {
			return nil, false
		}
		raw = []byte(typed)
	case []byte:
		raw = typed
	case map[string]interface{}:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return nil, false
		}
		raw = encoded
	default:
		return nil, false
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

func remapChunkIDList(raw interface{}, idMap map[string]string) []string {
	mapped := make([]string, 0)
	seen := map[string]struct{}{}
	add := func(id string) {
		next := idMap[id]
		if next == "" {
			return
		}
		if _, ok := seen[next]; ok {
			return
		}
		seen[next] = struct{}{}
		mapped = append(mapped, next)
	}
	switch typed := raw.(type) {
	case string:
		add(typed)
	case []string:
		for _, id := range typed {
			add(id)
		}
	case []interface{}:
		for _, item := range typed {
			if id, ok := item.(string); ok {
				add(id)
			}
		}
	}
	return mapped
}

func cloneChunkFields(src map[string]interface{}) map[string]interface{} {
	dst := make(map[string]interface{}, len(src))
	for key, value := range src {
		if strings.HasPrefix(key, "_") || key == "highlight" {
			continue
		}
		dst[key] = value
	}
	return dst
}

func isCompiledChunk(chunk map[string]interface{}) bool {
	return chunkFieldPresent(chunk, "knowledge_graph_kwd") ||
		chunkFieldPresent(chunk, "raptor_kwd") ||
		chunkFieldPresent(chunk, "compile_kwd")
}

func chunkFieldPresent(chunk map[string]interface{}, key string) bool {
	value, ok := chunk[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []interface{}:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	default:
		return true
	}
}

func chunkID(chunk map[string]interface{}) string {
	if id, ok := chunk["id"].(string); ok && strings.TrimSpace(id) != "" {
		return id
	}
	return ""
}

func reusedChunkIDs(chunks []map[string]interface{}) []string {
	ids := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if id := chunkID(chunk); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func reusedBodyCount(chunks []map[string]interface{}) int {
	count := 0
	for _, chunk := range chunks {
		if isCompiledChunk(chunk) || chunkID(chunk) == "" {
			continue
		}
		count++
	}
	return count
}

func reusedTokenCount(chunks []map[string]interface{}) int64 {
	var total int64
	for _, chunk := range chunks {
		if isCompiledChunk(chunk) {
			continue
		}
		total += chunkTokenCount(chunk)
	}
	if total < 0 {
		return 0
	}
	return total
}

func chunkTokenCount(chunk map[string]interface{}) int64 {
	for _, key := range []string{"token_num", "tk_nums"} {
		n, ok := common.GetInt(chunk[key])
		if ok && n > 0 {
			return int64(n)
		}
	}
	return 0
}

func chunksCarryVector(chunks []map[string]interface{}) bool {
	for _, chunk := range chunks {
		for key, value := range chunk {
			if common.IsVectorColumn(key) && value != nil {
				return true
			}
		}
	}
	return false
}

func imageKeyFromSourceKB(sourceKBID, imgID string) (bucket, key string, ok bool) {
	sourceKBID = strings.TrimSpace(sourceKBID)
	imgID = strings.TrimSpace(imgID)
	prefix := sourceKBID + "-"
	if sourceKBID == "" || !strings.HasPrefix(imgID, prefix) || len(imgID) == len(prefix) {
		return "", "", false
	}
	return sourceKBID, imgID[len(prefix):], true
}

func chunkIndexName(tenantID string) string {
	return "ragflow_" + tenantID
}

func documentName(doc *entity.Document) string {
	if doc == nil || doc.Name == nil {
		return ""
	}
	return *doc.Name
}
