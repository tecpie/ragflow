package component

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"ragflow/internal/ingestion/component/schema"
	"ragflow/internal/parser/parser"
	"ragflow/internal/utility"
)

// AgentPDFSetup maps a layout_recognize value onto the PDF setup
// the ingestion dispatcher already understands.
func AgentPDFSetup(layoutRecognize string) schema.ParserSetup {
	trimmed := strings.TrimSpace(layoutRecognize)
	if trimmed == "" || parser.IsPDFParseMethod(trimmed) {
		method := trimmed
		if method == "" {
			method = "deepdoc"
		}
		return schema.ParserSetup{"parse_method": method}
	}
	return schema.ParserSetup{"layout_recognizer": trimmed}
}

// DispatchAgentPDF runs the same PDF engine selection as knowledge-base
// ingestion. A false handled result means the local PDF parser owns the file
// (DeepDOC, plain text, and the setup-driven engines).
func DispatchAgentPDF(
	ctx context.Context,
	db *gorm.DB,
	tenantID, filename string,
	data []byte,
	layoutRecognize string,
) (parser.ParseResult, bool, error) {
	return maybeDispatchPDFVision(
		ctx,
		db,
		utility.FileTypePDF,
		filename,
		data,
		map[string]any{"tenant_id": tenantID},
		map[string]schema.ParserSetup{"pdf": AgentPDFSetup(layoutRecognize)},
	)
}
