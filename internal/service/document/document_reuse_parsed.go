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
	// reusedChunksProgressMsg is the document progress text written after a
	// successful copy. Callers that continue with post-parse work recognize it
	// because this path does not create an ingestion task.
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
// document without running the parser. Vectors are kept as stored. Compiled
// rows (graph, raptor, wiki, mind map, page index) are skipped. An active
// ingestion task is rejected, and this path does not write a COMPLETED task.
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
	if len(prepared) == 0 {
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
	if _, err = s.docEngine.DeleteChunks(ctx, map[string]interface{}{"doc_id": targetDoc.ID}, targetIndex, targetKB.ID); err != nil && !errors.Is(err, enginetypes.ErrIndexNotFound) {
		s.removeCopiedImages(ctx, copiedImages)
		return nil, fmt.Errorf("clear target chunks: %w", err)
	}

	insertedIDs := reusedChunkIDs(prepared)
	defer func() {
		if err == nil {
			return
		}
		if len(insertedIDs) > 0 {
			if _, delErr := s.docEngine.DeleteChunks(ctx, map[string]interface{}{"id": insertedIDs}, targetIndex, targetKB.ID); delErr != nil && !errors.Is(delErr, enginetypes.ErrIndexNotFound) {
				common.Warn(fmt.Sprintf("reuse parsed chunks: failed to roll back inserted chunks %s: %v", targetDoc.ID, delErr))
			}
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
	if _, err = s.docEngine.InsertChunks(ctx, prepared, targetIndex, targetKB.ID, language); err != nil {
		return nil, fmt.Errorf("insert reused chunks: %w", err)
	}

	tokenNum := reusedTokenCount(prepared)
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
			return nil, fmt.Errorf("list source chunks: %w", err)
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
			return nil, errors.New("source chunk page repeated; refusing to copy a partial set")
		}
		if int64(len(seen)) >= result.Total || len(result.Chunks) < reuseParsedPageSize {
			return chunks, nil
		}
	}
	return nil, fmt.Errorf("source document has more than %d chunks", reuseParsedMaxChunks)
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

func (s *DocumentService) markReusedDocumentParsed(ctx context.Context, doc *entity.Document, datasetID string) error {
	now := time.Now()
	return dao.DB.WithContext(ctx).Model(&entity.Document{}).
		Where("id = ? AND kb_id = ?", doc.ID, datasetID).
		Updates(map[string]interface{}{
			"progress":         1,
			"progress_msg":     reusedChunksProgressMsg,
			"process_begin_at": now,
		}).Error
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

func reusedTokenCount(chunks []map[string]interface{}) int64 {
	var total int64
	for _, chunk := range chunks {
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
