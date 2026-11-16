package document

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ragflow/internal/common"
	"ragflow/internal/dao"
	enginetypes "ragflow/internal/engine/types"
	"ragflow/internal/entity"
	"ragflow/internal/storage"
)

const (
	reuseParsedPageSize  = 200
	reuseParsedMaxChunks = 20000
)

// ReuseParsedChunksResult is the outcome of copying a source document's parsed
// chunks, including their vectors, onto an already uploaded target document.
type ReuseParsedChunksResult struct {
	DocumentID string `json:"document_id"`
	ChunkCount int    `json:"chunk_count"`
	TokenCount int64  `json:"token_count"`
}

// ReuseParsedChunks copies the source document's file chunks into the target
// document without running the parser. Vectors are kept as stored. Knowledge
// compilation rows (graph, raptor) are skipped. The target ingestion task is
// marked COMPLETED so callers can continue with post-parse work.
func (s *DocumentService) ReuseParsedChunks(ctx context.Context, targetDatasetID, targetDocumentID, sourceDatasetID, sourceDocumentID string) (result *ReuseParsedChunksResult, err error) {
	if s.docEngine == nil {
		return nil, errors.New("doc engine not initialized")
	}
	targetKB, targetDoc, sourceKB, sourceDoc, err := s.loadReuseDocuments(ctx, targetDatasetID, targetDocumentID, sourceDatasetID, sourceDocumentID)
	if err != nil {
		return nil, err
	}

	rawChunks, err := s.listDocumentChunks(ctx, sourceKB, sourceDoc.ID)
	if err != nil {
		return nil, err
	}
	prepared := prepareReusedChunks(rawChunks, targetKB.ID, targetDoc.ID, documentName(targetDoc))
	if len(prepared) == 0 {
		return nil, errors.New("source document has no reusable parsed chunks")
	}

	copiedImages, err := s.copyReusedChunkImages(ctx, prepared)
	if err != nil {
		s.removeCopiedImages(ctx, copiedImages)
		return nil, err
	}

	targetIndex := chunkIndexName(targetKB.TenantID)
	inserted := false
	defer func() {
		if err == nil || !inserted {
			return
		}
		if _, delErr := s.docEngine.DeleteChunks(ctx, map[string]interface{}{"doc_id": targetDoc.ID}, targetIndex, targetKB.ID); delErr != nil {
			common.Warn(fmt.Sprintf("reuse parsed chunks: failed to roll back target chunks %s: %v", targetDoc.ID, delErr))
		}
		if countErr := s.ApplyDocCounts(ctx, targetDoc.ID, targetKB.ID, 0, 0, 0); countErr != nil {
			common.Warn(fmt.Sprintf("reuse parsed chunks: failed to roll back counts %s: %v", targetDoc.ID, countErr))
		}
		s.removeCopiedImages(ctx, copiedImages)
	}()

	language := ""
	if targetKB.Language != nil {
		language = *targetKB.Language
	}
	inserted = true
	if _, err = s.docEngine.InsertChunks(ctx, prepared, targetIndex, targetKB.ID, language); err != nil {
		return nil, fmt.Errorf("insert reused chunks: %w", err)
	}

	tokenNum := sourceDoc.TokenNum
	if tokenNum < 0 {
		tokenNum = 0
	}
	if err = s.ApplyDocCounts(ctx, targetDoc.ID, targetKB.ID, len(prepared), int(tokenNum), 0); err != nil {
		return nil, err
	}
	if err = s.markReusedDocumentParsed(ctx, targetDoc, targetKB.ID); err != nil {
		return nil, err
	}
	return &ReuseParsedChunksResult{
		DocumentID: targetDoc.ID,
		ChunkCount: len(prepared),
		TokenCount: tokenNum,
	}, nil
}

func (s *DocumentService) loadReuseDocuments(ctx context.Context, targetDatasetID, targetDocumentID, sourceDatasetID, sourceDocumentID string) (*entity.Knowledgebase, *entity.Document, *entity.Knowledgebase, *entity.Document, error) {
	targetKB, err := s.kbDAO.GetByID(ctx, dao.DB, targetDatasetID)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("target dataset: %w", err)
	}
	sourceKB, err := s.kbDAO.GetByID(ctx, dao.DB, sourceDatasetID)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("source dataset: %w", err)
	}
	targetDoc, err := s.documentDAO.GetByID(ctx, dao.DB, targetDocumentID)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("target document: %w", err)
	}
	if targetDoc.KbID != targetKB.ID {
		return nil, nil, nil, nil, errors.New("target document does not belong to the target dataset")
	}
	sourceDoc, err := s.documentDAO.GetByID(ctx, dao.DB, sourceDocumentID)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("source document: %w", err)
	}
	if sourceDoc.KbID != sourceKB.ID {
		return nil, nil, nil, nil, errors.New("source document does not belong to the source dataset")
	}
	return targetKB, targetDoc, sourceKB, sourceDoc, nil
}

func (s *DocumentService) listDocumentChunks(ctx context.Context, kb *entity.Knowledgebase, documentID string) ([]map[string]interface{}, error) {
	indexName := chunkIndexName(kb.TenantID)
	chunks := make([]map[string]interface{}, 0)
	for offset := 0; offset < reuseParsedMaxChunks; offset += reuseParsedPageSize {
		result, err := s.docEngine.Search(ctx, &enginetypes.SearchRequest{
			IndexNames:         []string{indexName},
			KbIDs:              []string{kb.ID},
			Offset:             offset,
			Limit:              reuseParsedPageSize,
			IncludeUnavailable: true,
			Filter:             map[string]interface{}{"doc_id": documentID},
			OrderBy:            (&enginetypes.OrderByExpr{}).Asc("chunk_order_int"),
		})
		if err != nil {
			if errors.Is(err, enginetypes.ErrIndexNotFound) {
				return nil, nil
			}
			return nil, fmt.Errorf("list source chunks: %w", err)
		}
		if result == nil || len(result.Chunks) == 0 {
			return chunks, nil
		}
		chunks = append(chunks, result.Chunks...)
		if int64(len(chunks)) >= result.Total || len(result.Chunks) < reuseParsedPageSize {
			return chunks, nil
		}
	}
	return nil, fmt.Errorf("source document has more than %d chunks", reuseParsedMaxChunks)
}

func (s *DocumentService) markReusedDocumentParsed(ctx context.Context, doc *entity.Document, datasetID string) error {
	msg := "Reused parsed chunks from the source document"
	now := time.Now()
	if err := dao.DB.WithContext(ctx).Model(&entity.Document{}).
		Where("id = ? AND kb_id = ?", doc.ID, datasetID).
		Updates(map[string]interface{}{
			"progress":         1,
			"progress_msg":     msg,
			"process_begin_at": now,
		}).Error; err != nil {
		return err
	}

	task := &entity.IngestionTask{
		ID:         common.GenerateToken(),
		UserID:     doc.UserID,
		DocumentID: doc.ID,
		DatasetID:  datasetID,
		Schema:     entity.JSONMap{},
		Status:     common.COMPLETED,
	}
	err := dao.DB.WithContext(ctx).Create(task).Error
	if err == nil {
		return nil
	}
	updated := dao.DB.WithContext(ctx).Model(&entity.IngestionTask{}).
		Where("document_id = ?", doc.ID).
		Updates(map[string]interface{}{
			"status":     common.COMPLETED,
			"dataset_id": datasetID,
			"user_id":    doc.UserID,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected == 0 {
		return err
	}
	return nil
}

func (s *DocumentService) copyReusedChunkImages(ctx context.Context, chunks []map[string]interface{}) ([][2]string, error) {
	storageImpl := storage.GetStorageFactory().GetStorage()
	copied := make([][2]string, 0)
	for _, chunk := range chunks {
		imgID, _ := chunk["img_id"].(string)
		newID, _ := chunk["id"].(string)
		bucket, key := splitImageID(imgID)
		if bucket == "" || key == "" || newID == "" {
			delete(chunk, "img_id")
			continue
		}
		if storageImpl == nil {
			delete(chunk, "img_id")
			continue
		}
		targetBucket, _ := chunk["kb_id"].(string)
		blob, getErr := storageImpl.Get(ctx, bucket, key)
		if getErr != nil || len(blob) == 0 {
			delete(chunk, "img_id")
			continue
		}
		if putErr := storageImpl.Put(ctx, targetBucket, newID, blob); putErr != nil {
			delete(chunk, "img_id")
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
	return prepared
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
	return chunkFieldPresent(chunk, "knowledge_graph_kwd") || chunkFieldPresent(chunk, "raptor_kwd")
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

func splitImageID(imgID string) (bucket, key string) {
	imgID = strings.TrimSpace(imgID)
	index := strings.Index(imgID, "-")
	if index <= 0 || index == len(imgID)-1 {
		return "", ""
	}
	return imgID[:index], imgID[index+1:]
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
