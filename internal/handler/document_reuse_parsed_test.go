package handler

import (
	"errors"
	"testing"

	"ragflow/internal/common"
	"ragflow/internal/service/document"
)

func TestReuseParsedErrorCode(t *testing.T) {
	argument := reuseParsedErrorCode(&document.ReuseParsedError{
		Argument: true,
		Err:      errors.New("target document does not belong to the target dataset"),
	})
	if argument != common.CodeArgumentError {
		t.Fatalf("code = %v, want argument error", argument)
	}
	if got := reuseParsedErrorCode(errors.New("target document: record not found")); got != common.CodeDataError {
		t.Fatalf("code = %v, want data error", got)
	}
}
