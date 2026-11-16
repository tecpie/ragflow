package handler

import (
	"errors"
	"strings"

	"ragflow/internal/common"
	"ragflow/internal/dao"
	"ragflow/internal/permission"
	"ragflow/internal/service/document"

	"github.com/gin-gonic/gin"
)

type reuseParsedChunksBody struct {
	SourceDatasetID  string `json:"source_dataset_id"`
	SourceDocumentID string `json:"source_document_id"`
}

// ReuseParsedChunks copies parsed chunks from a source document onto a document
// that was already uploaded into the target dataset.
func (h *DocumentHandler) ReuseParsedChunks(c *gin.Context) {
	ctx := c.Request.Context()
	if permission.UserIDFromContext(ctx) == "" {
		common.RespondError(c, common.CodeUnauthorized, permission.ErrUnauthenticated.Error())
		return
	}
	targetDatasetID := strings.TrimSpace(c.Param("dataset_id"))
	targetDocumentID := strings.TrimSpace(c.Param("document_id"))
	if targetDatasetID == "" || targetDocumentID == "" {
		common.RespondError(c, common.CodeArgumentError, "dataset_id and document_id are required")
		return
	}
	var body reuseParsedChunksBody
	if err := c.ShouldBindJSON(&body); err != nil {
		common.RespondError(c, common.CodeArgumentError, err.Error())
		return
	}
	sourceDatasetID := strings.TrimSpace(body.SourceDatasetID)
	sourceDocumentID := strings.TrimSpace(body.SourceDocumentID)
	if sourceDatasetID == "" || sourceDocumentID == "" {
		common.RespondError(c, common.CodeArgumentError, "source_dataset_id and source_document_id are required")
		return
	}
	checker := permission.NewDatabaseChecker(dao.DB)
	if err := checker.CheckResource(ctx, permission.ResourceRef{Kind: permission.ResourceKindDataset, ID: targetDatasetID}, permission.OperationUpdate); err != nil {
		respondPermissionError(c, err, false)
		return
	}
	if err := checker.CheckResource(ctx, permission.ResourceRef{Kind: permission.ResourceKindDataset, ID: sourceDatasetID}, permission.OperationRead); err != nil {
		respondPermissionError(c, err, false)
		return
	}

	result, err := h.documentService.ReuseParsedChunks(ctx, targetDatasetID, targetDocumentID, sourceDatasetID, sourceDocumentID)
	if err != nil {
		common.RespondError(c, reuseParsedErrorCode(err), err.Error())
		return
	}
	common.RespondSuccess(c, result, "success")
}

func reuseParsedErrorCode(err error) common.ErrorCode {
	var reuseErr *document.ReuseParsedError
	if errors.As(err, &reuseErr) && reuseErr.Argument {
		return common.CodeArgumentError
	}
	return common.CodeDataError
}
