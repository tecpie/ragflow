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

package agentic_rag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"go.uber.org/zap"

	"ragflow/internal/common"
	"ragflow/internal/ontology"
)

const ontologyExploreToolName = "ontology_explore"

const ontologyExploreToolDescription = `Walk the COMPILED KNOWLEDGE GRAPH under the dataset ONTOLOGY. The question is aligned to ontology classes and relations first; the walk then keeps only those classes and relations (including domain and range). It returns the subgraph plus the source passages behind the relevant nodes.
USE IT for a question over a dataset that has a compiled ontology. When the question names ontology classes or relations, the walk keeps only those. When it names none, the compiled graph is still walked, without that filter.
Returns an XML <kg_explore mode="ontology"> document: an <ontology> element listing the classes and properties that constrained the walk, a <subgraph> of <entity> and <relation> children, then one <passage chunk_id doc_id doc_name ref> element per source chunk. The ref attribute is the [ID:n] handle to cite.
An empty result means the walk found no entity row. Read the note. error="no compiled knowledge graph" is a DATASET-level absence (do not retry this tool). A note without that attribute is a QUERY-level miss.`

// OntologyExploreTool walks the compiled graph constrained by the tenant ontology
// bound through the dataset's compilation template group.
type OntologyExploreTool struct {
	tenantID   string
	datasetIDs []string
}

// NewOntologyExploreTool returns an OntologyExploreTool scoped to the tenant and datasets.
func NewOntologyExploreTool(tenantID string, datasetIDs []string) *OntologyExploreTool {
	return &OntologyExploreTool{tenantID: tenantID, datasetIDs: datasetIDs}
}

// Info returns the tool's metadata for the chat model.
func (o *OntologyExploreTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	schemaJSON := `{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "REQUIRED: the question to align to the ontology and walk the graph for. Use the ontology's class and relation names when you know them."
    },
    "dataset_ids": {
      "type": "array",
      "description": "Optional dataset ids to restrict the walk to (at most 10). When omitted, the current conversation's bound datasets are used. Datasets without an ontology are skipped.",
      "items": { "type": "string" },
      "maxItems": 10
    },
    "doc_scope": {
      "type": "array",
      "description": "Optional document ids to restrict the walk to (at most 10).",
      "items": { "type": "string" },
      "maxItems": 10
    }
  },
  "required": ["query"]
}`
	s := &jsonschema.Schema{}
	if err := json.Unmarshal([]byte(schemaJSON), s); err != nil {
		return nil, fmt.Errorf("ontology_explore: parse schema: %w", err)
	}
	return &schema.ToolInfo{
		Name:        ontologyExploreToolName,
		Desc:        ontologyExploreToolDescription,
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(s),
	}, nil
}

// InvokableRun aligns the question to the ontology and walks the graph.
func (o *OntologyExploreTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...einotool.Option) (string, error) {
	return guardedToolRun(ctx, ontologyExploreToolName, o.invokableRun, argumentsInJSON)
}

func (o *OntologyExploreTool) invokableRun(ctx context.Context, argumentsInJSON string) (string, error) {
	var args graphExploreArgs
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("ontology_explore: parse arguments: %w", err)
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "", fmt.Errorf("ontology_explore: query is required and must be a non-empty string")
	}
	common.InfoCtx(ctx, "agentic_rag: ontology_explore called",
		zap.String("query", query), zap.Int("doc_scope", len(args.DocScope)))
	if !navToolEnabled(ctx, ontologyExploreToolName) {
		common.DebugCtx(ctx, "ontology_explore: marked unavailable; skipping backend (no compiled knowledge graph)")
		return renderKgExplore(ctx, query, KgExploreResult{GraphAbsent: true}, &ontology.Alignment{}), nil
	}
	datasetIDs, err := resolveDatasetScope(o.datasetIDs, args.DatasetIDs)
	if err != nil {
		return "", fmt.Errorf("ontology_explore: %w", err)
	}
	docScope, err := resolveDocumentScope(ctx, args.DocScope)
	if err != nil {
		return "", fmt.Errorf("ontology_explore: %w", err)
	}
	if docScope != nil && len(docScope) == 0 {
		return renderKgExplore(ctx, query, KgExploreResult{}, nil), nil
	}
	pg, covered := playgroundForDatasets(ctx, o.tenantID, datasetIDs)
	if len(covered) == 0 {
		return renderKgExplore(ctx, query, KgExploreResult{}, &ontology.Alignment{}), nil
	}
	aligned := alignQuery(ctx, o.tenantID, query, pg)
	req := KgExploreRequest{
		TenantID:   o.tenantID,
		DatasetIDs: covered,
		DocScope:   docScope,
		Query:      query,
	}
	var alignedOut *ontology.Alignment
	if !aligned.Empty() {
		req.Constraint = &aligned
		alignedOut = &aligned
	}
	result, err := GetKgService().Explore(ctx, req)
	if err != nil {
		return "", fmt.Errorf("ontology_explore: %w", err)
	}
	if result.GraphAbsent {
		markNavToolUnavailable(ctx, ontologyExploreToolName)
		common.DebugCtx(ctx, "ontology_explore: marking unavailable for conversation (no compiled knowledge graph)")
	}
	common.InfoCtx(ctx, "agentic_rag: ontology_explore",
		zap.String("query", query),
		zap.Int("datasets", len(covered)),
		zap.Int("classes", len(aligned.Classes)),
		zap.Int("properties", len(aligned.Properties)),
		zap.Int("entities", len(result.Entities)),
		zap.Int("relations", len(result.Relations)),
		zap.Int("passages", len(result.Passages)))
	return renderKgExplore(ctx, query, result, alignedOut), nil
}
