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
	"time"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"

	"ragflow/internal/common"
	"ragflow/internal/entity/models"
)

// chatModelContextKey carries the turn's chat model so graph tools can ask it
// which subgraph nodes/edges answer the question (UI focus), without threading
// the model through every tool constructor.
type chatModelContextKey struct{}

func withChatModel(ctx context.Context, m *models.EinoChatModel) context.Context {
	if m == nil {
		return ctx
	}
	return context.WithValue(ctx, chatModelContextKey{}, m)
}

func chatModelFrom(ctx context.Context) *models.EinoChatModel {
	m, _ := ctx.Value(chatModelContextKey{}).(*models.EinoChatModel)
	return m
}

const kgFocusJudgeTimeout = 20 * time.Second

const kgFocusSystemPrompt = `You select the knowledge-graph focus for a question.
You are given a walked subgraph (entities + relations). Return ONLY the entities and relations needed to answer the question — the answer path, not distractors of the same type.
Reply with JSON only, no markdown:
{"entities":["name",...],"relations":[{"from":"...","to":"...","type":"..."},...]}
Rules:
- Use exact names from the provided lists.
- Prefer the shortest path that answers the question.
- If nothing in the subgraph answers the question, return {"entities":[],"relations":[]}.`

// kgFocus is the model-chosen highlight set for a walked subgraph.
type kgFocus struct {
	Entities  []string       `json:"entities"`
	Relations []kgFocusRel   `json:"relations"`
}

type kgFocusRel struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
}

// applyKgModelFocus asks the turn's chat model which entities/relations answer
// the question and stamps Relevant on those rows. Clears prior marks first so
// retrieval Seed cannot leak into the UI. A missing model, empty input, or
// judge failure leaves the subgraph unmarked.
func applyKgModelFocus(ctx context.Context, query string, entities []KgEntity, relations []KgRelation) {
	clearKgRelevant(entities, relations)
	query = strings.TrimSpace(query)
	m := chatModelFrom(ctx)
	if m == nil || query == "" || len(entities) == 0 {
		return
	}
	focus, err := kgJudgeFocus(ctx, m, query, entities, relations)
	if err != nil {
		common.WarnCtx(ctx, "agentic_rag: kg focus judge failed; leaving subgraph unmarked",
			zap.String("query", truncateForLog(query, 120)), zap.Error(err))
		return
	}
	applyKgFocusMarks(entities, relations, focus)
}

func clearKgRelevant(entities []KgEntity, relations []KgRelation) {
	for i := range entities {
		entities[i].Seed = false
		entities[i].Relevant = false
	}
	for i := range relations {
		relations[i].Relevant = false
	}
}

func kgJudgeFocus(ctx context.Context, m *models.EinoChatModel, query string, entities []KgEntity, relations []KgRelation) (kgFocus, error) {
	ctx, cancel := context.WithTimeout(ctx, kgFocusJudgeTimeout)
	defer cancel()
	resp, err := m.Generate(ctx, []*schema.Message{
		schema.SystemMessage(kgFocusSystemPrompt),
		schema.UserMessage(kgFocusUserPrompt(query, entities, relations)),
	})
	if err != nil {
		return kgFocus{}, err
	}
	if resp == nil {
		return kgFocus{}, fmt.Errorf("empty model response")
	}
	focus, err := parseKgFocusJSON(resp.Content)
	if err != nil {
		return kgFocus{}, err
	}
	return focus, nil
}

func kgFocusUserPrompt(query string, entities []KgEntity, relations []KgRelation) string {
	var b strings.Builder
	b.WriteString("Question:\n")
	b.WriteString(query)
	b.WriteString("\n\nEntities:\n")
	for _, e := range entities {
		b.WriteString("- ")
		b.WriteString(e.Name)
		if e.Type != "" {
			b.WriteString(" (")
			b.WriteString(e.Type)
			b.WriteString(")")
		}
		b.WriteByte('\n')
	}
	b.WriteString("\nRelations:\n")
	if len(relations) == 0 {
		b.WriteString("(none)\n")
	}
	for _, r := range relations {
		b.WriteString("- ")
		b.WriteString(r.From)
		b.WriteString(" -[")
		b.WriteString(r.Type)
		b.WriteString("]-> ")
		b.WriteString(r.To)
		b.WriteByte('\n')
	}
	b.WriteString("\nJSON:")
	return b.String()
}

func parseKgFocusJSON(raw string) (kgFocus, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return kgFocus{}, fmt.Errorf("empty focus JSON")
	}
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j > i {
			raw = raw[i : j+1]
		}
	}
	var focus kgFocus
	if err := json.Unmarshal([]byte(raw), &focus); err != nil {
		return kgFocus{}, fmt.Errorf("parse focus JSON: %w", err)
	}
	return focus, nil
}

func applyKgFocusMarks(entities []KgEntity, relations []KgRelation, focus kgFocus) {
	entWant := map[string]bool{}
	for _, name := range focus.Entities {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			entWant[name] = true
		}
	}
	relWant := map[string]bool{}
	for _, r := range focus.Relations {
		from := strings.ToLower(strings.TrimSpace(r.From))
		to := strings.ToLower(strings.TrimSpace(r.To))
		typ := strings.ToLower(strings.TrimSpace(r.Type))
		if from == "" || to == "" {
			continue
		}
		relWant[from+"|"+to+"|"+typ] = true
		entWant[from] = true
		entWant[to] = true
	}
	for i := range entities {
		if entWant[strings.ToLower(entities[i].Name)] {
			entities[i].Relevant = true
		}
	}
	for i := range relations {
		key := strings.ToLower(strings.TrimSpace(relations[i].From)) + "|" +
			strings.ToLower(strings.TrimSpace(relations[i].To)) + "|" +
			strings.ToLower(strings.TrimSpace(relations[i].Type))
		if relWant[key] {
			relations[i].Relevant = true
		}
	}
}
