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
	"encoding/json"
	"fmt"
	"math"
	"strings"

	modelModule "ragflow/internal/entity/models"
)

const (
	promptStatementSplit = `Given a question and an answer, analyze the complexity of each sentence in the answer. Break down each sentence into one or more fully understandable statements. Ensure that no pronouns are used in any statement. Format the outputs in JSON with key "statements" as a list of strings.
Input JSON:
{"question":%q,"answer":%q}`

	promptFaithfulnessNLI = `Your task is to judge the faithfulness of a series of statements based on a given context. For each statement you must return verdict as 1 if the statement can be directly inferred based on the context or 0 if the statement can not be directly inferred based on the context. Output JSON: {"statements":[{"statement":"...","reason":"...","verdict":0|1},...]}.
Context:
%s
Statements JSON:
%s`

	promptContextPrecision = `Given question, answer and context verify if the context was useful in arriving at the given answer. Give verdict as "1" if useful and "0" if not with json output {"reason":"...","verdict":0|1}.
Question: %s
Answer: %s
Context: %s`

	promptContextRecall = `Given a context, and an answer, analyze each sentence in the answer and classify if the sentence can be attributed to the given context or not. Use only 'Yes' (1) or 'No' (0) as a binary classification. Output json with reason: {"classifications":[{"statement":"...","reason":"...","attributed":0|1},...]}.
Question: %s
Context:
%s
Answer: %s`

	promptCorrectnessClassify = `Given a ground truth and an answer statements, analyze each statement and classify them in one of the following categories: TP (true positive): statements that are present in answer that are also directly supported by the one or more statements in ground truth, FP (false positive): statements present in the answer but not directly supported by any statement in ground truth, FN (false negative): statements found in the ground truth but not present in answer. Each statement can only belong to one of the categories. Provide a reason for each classification. Output JSON: {"TP":[{"statement":"...","reason":"..."}],"FP":[...],"FN":[...]}.
Question: %s
Answer statements: %s
Ground truth statements: %s`

	promptAnswerRelevancy = `Generate a question for the given answer and Identify if answer is noncommittal. Give noncommittal as 1 if the answer is noncommittal and 0 if the answer is committal. A noncommittal answer is one that is evasive, vague, or ambiguous. For example, "I don't know" or "I'm not sure" are noncommittal answers. Output JSON: {"question":"...","noncommittal":0|1}.
Answer: %s`
)

var defaultEvalMetrics = []string{
	"faithfulness",
	"context_precision",
	"context_recall",
	"answer_correctness",
	"answer_relevancy",
}

type ragasScorer struct {
	factory     *ModelFactory
	tenantID    string
	judgeLLMID  string
	judgeEmbdID string
}

func newRagasScorer(factory *ModelFactory, tenantID, judgeLLMID, judgeEmbdID string) *ragasScorer {
	return &ragasScorer{
		factory:     factory,
		tenantID:    tenantID,
		judgeLLMID:  judgeLLMID,
		judgeEmbdID: judgeEmbdID,
	}
}

func (s *ragasScorer) judgeChat(ctx context.Context, system, user string) (string, error) {
	if s.factory == nil {
		return "", fmt.Errorf("model factory not initialized")
	}
	access := ModelAccess{TenantID: s.tenantID}
	var (
		chatModel *modelModule.ChatModel
		err       error
	)
	if strings.TrimSpace(s.judgeLLMID) == "" {
		chatModel, err = s.factory.NewDefaultChatModel(ctx, access)
	} else {
		chatModel, err = s.factory.NewChatModel(ctx, access, s.judgeLLMID)
	}
	if err != nil {
		return "", err
	}
	temp := 0.0
	maxTokens := 1024
	config := &modelModule.ChatConfig{Temperature: &temp, MaxTokens: &maxTokens}
	msgs := []modelModule.Message{}
	if strings.TrimSpace(system) != "" {
		msgs = append(msgs, modelModule.Message{Role: "system", Content: system})
	}
	msgs = append(msgs, modelModule.Message{Role: "user", Content: user})
	resp, err := chatModel.ChatWithMessages(ctx, msgs, config, nil)
	if err != nil {
		return "", err
	}
	if resp == nil || resp.Answer == nil {
		return "", nil
	}
	return strings.TrimSpace(*resp.Answer), nil
}

func (s *ragasScorer) embedTexts(ctx context.Context, texts []string) ([][]float64, error) {
	if s.factory == nil {
		return nil, fmt.Errorf("model factory not initialized")
	}
	if strings.TrimSpace(s.judgeEmbdID) == "" {
		return nil, fmt.Errorf("embedding model not configured")
	}
	embModel, err := s.factory.NewEmbeddingModel(ctx, ModelAccess{TenantID: s.tenantID}, s.judgeEmbdID)
	if err != nil {
		return nil, err
	}
	data, err := embModel.Embed(ctx, modelModule.EmbedRequest{Texts: texts}, &modelModule.EmbeddingConfig{}, nil)
	if err != nil {
		return nil, err
	}
	out := make([][]float64, len(data))
	for i, d := range data {
		out[i] = d.Embedding
	}
	return out, nil
}

func extractJSONObject(text string) string {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return ""
	}
	return text[start : end+1]
}

func extractJSONArray(text string) string {
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start < 0 || end <= start {
		return ""
	}
	return text[start : end+1]
}

func parseJSONMap(text string) (map[string]any, error) {
	raw := extractJSONObject(text)
	if raw == "" {
		return nil, fmt.Errorf("no JSON object in LLM response")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func parseStringList(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			} else if m, ok := item.(map[string]any); ok {
				if s, ok := m["statement"].(string); ok && strings.TrimSpace(s) != "" {
					out = append(out, strings.TrimSpace(s))
				}
			}
		}
		return out
	case []string:
		return x
	default:
		return nil
	}
}

func asInt01(v any) int {
	switch x := v.(type) {
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		if x >= 0.5 {
			return 1
		}
		return 0
	case int:
		if x != 0 {
			return 1
		}
		return 0
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		if s == "1" || s == "yes" || s == "true" {
			return 1
		}
		return 0
	default:
		return 0
	}
}

func averagePrecision(verdicts []int) float64 {
	if len(verdicts) == 0 {
		return 0
	}
	denom := 0.0
	for _, v := range verdicts {
		denom += float64(v)
	}
	denom += 1e-10
	num := 0.0
	cum := 0.0
	for i, v := range verdicts {
		cum += float64(v)
		num += (cum / float64(i+1)) * float64(v)
	}
	return num / denom
}

func cosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	den := math.Sqrt(na) * math.Sqrt(nb)
	if den == 0 {
		return 0
	}
	return dot / den
}

func answerCorrectnessF1(tp, fp, fn int) float64 {
	denom := 2*tp + fp + fn
	if denom == 0 {
		return 1.0
	}
	return float64(2*tp) / float64(denom)
}

func (s *ragasScorer) splitStatements(ctx context.Context, question, answer string) ([]string, error) {
	user := fmt.Sprintf(promptStatementSplit, question, answer)
	raw, err := s.judgeChat(ctx, "", user)
	if err != nil {
		return nil, err
	}
	obj, err := parseJSONMap(raw)
	if err != nil {
		// Fallback: try bare array.
		arrRaw := extractJSONArray(raw)
		if arrRaw == "" {
			return nil, err
		}
		var arr []any
		if uerr := json.Unmarshal([]byte(arrRaw), &arr); uerr != nil {
			return nil, err
		}
		return parseStringList(arr), nil
	}
	if stmts := parseStringList(obj["statements"]); len(stmts) > 0 {
		return stmts, nil
	}
	return nil, fmt.Errorf("empty statements from judge")
}

func (s *ragasScorer) scoreFaithfulness(ctx context.Context, question, answer string, contexts []string) (float64, error) {
	if len(contexts) == 0 {
		return 0, nil
	}
	stmts, err := s.splitStatements(ctx, question, answer)
	if err != nil {
		return 0, err
	}
	if len(stmts) == 0 {
		return 0, nil
	}
	stmtsJSON, _ := json.Marshal(stmts)
	user := fmt.Sprintf(promptFaithfulnessNLI, strings.Join(contexts, "\n"), string(stmtsJSON))
	raw, err := s.judgeChat(ctx, "", user)
	if err != nil {
		return 0, err
	}
	obj, err := parseJSONMap(raw)
	if err != nil {
		return 0, err
	}
	items, _ := obj["statements"].([]any)
	if len(items) == 0 {
		return 0, fmt.Errorf("empty faithfulness verdicts")
	}
	ok := 0
	for _, item := range items {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		ok += asInt01(m["verdict"])
	}
	return float64(ok) / float64(len(items)), nil
}

func (s *ragasScorer) scoreContextPrecision(ctx context.Context, question, reference string, contexts []string) (float64, error) {
	if len(contexts) == 0 {
		return 0, nil
	}
	verdicts := make([]int, 0, len(contexts))
	for _, c := range contexts {
		user := fmt.Sprintf(promptContextPrecision, question, reference, c)
		raw, err := s.judgeChat(ctx, "", user)
		if err != nil {
			return 0, err
		}
		obj, err := parseJSONMap(raw)
		if err != nil {
			return 0, err
		}
		verdicts = append(verdicts, asInt01(obj["verdict"]))
	}
	return averagePrecision(verdicts), nil
}

func (s *ragasScorer) scoreContextRecall(ctx context.Context, question, reference string, contexts []string) (float64, error) {
	if len(contexts) == 0 {
		return 0, nil
	}
	user := fmt.Sprintf(promptContextRecall, question, strings.Join(contexts, "\n"), reference)
	raw, err := s.judgeChat(ctx, "", user)
	if err != nil {
		return 0, err
	}
	obj, err := parseJSONMap(raw)
	if err != nil {
		return 0, err
	}
	items, _ := obj["classifications"].([]any)
	if len(items) == 0 {
		return 0, fmt.Errorf("empty context_recall classifications")
	}
	ok := 0
	for _, item := range items {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		ok += asInt01(m["attributed"])
	}
	return float64(ok) / float64(len(items)), nil
}

func (s *ragasScorer) scoreAnswerCorrectness(ctx context.Context, question, answer, reference string) (float64, error) {
	ansStmts, err := s.splitStatements(ctx, question, answer)
	if err != nil {
		return 0, err
	}
	gtStmts, err := s.splitStatements(ctx, question, reference)
	if err != nil {
		return 0, err
	}
	if len(ansStmts) == 0 && len(gtStmts) == 0 {
		return 1, nil
	}
	ansJSON, _ := json.Marshal(ansStmts)
	gtJSON, _ := json.Marshal(gtStmts)
	user := fmt.Sprintf(promptCorrectnessClassify, question, string(ansJSON), string(gtJSON))
	raw, err := s.judgeChat(ctx, "", user)
	if err != nil {
		return 0, err
	}
	obj, err := parseJSONMap(raw)
	if err != nil {
		return 0, err
	}
	tp := len(parseStringList(obj["TP"]))
	fp := len(parseStringList(obj["FP"]))
	fn := len(parseStringList(obj["FN"]))
	return answerCorrectnessF1(tp, fp, fn), nil
}

func (s *ragasScorer) scoreAnswerRelevancy(ctx context.Context, question, answer string) (float64, error) {
	user := fmt.Sprintf(promptAnswerRelevancy, answer)
	raw, err := s.judgeChat(ctx, "", user)
	if err != nil {
		return 0, err
	}
	obj, err := parseJSONMap(raw)
	if err != nil {
		return 0, err
	}
	genQ, _ := obj["question"].(string)
	noncommittal := asInt01(obj["noncommittal"])
	if strings.TrimSpace(genQ) == "" {
		return 0, fmt.Errorf("empty generated question")
	}
	vecs, err := s.embedTexts(ctx, []string{question, genQ})
	if err != nil {
		return 0, err
	}
	if len(vecs) < 2 {
		return 0, fmt.Errorf("embedding returned insufficient vectors")
	}
	sim := cosineSimilarity(vecs[0], vecs[1])
	if noncommittal != 0 {
		return 0, nil
	}
	return sim, nil
}

func (s *ragasScorer) scoreAll(ctx context.Context, payload scoringPayload, metrics []string) map[string]any {
	if len(metrics) == 0 {
		metrics = defaultEvalMetrics
	}
	out := map[string]any{}
	errorsMap := map[string]string{}
	hasEmbd := strings.TrimSpace(s.judgeEmbdID) != ""

	setScore := func(name string, score float64, err error) {
		if err != nil {
			out[name] = nil
			errorsMap[name] = err.Error()
			return
		}
		out[name] = score
	}

	for _, name := range metrics {
		switch name {
		case "faithfulness":
			sc, err := s.scoreFaithfulness(ctx, payload.Question, payload.Answer, payload.Contexts)
			setScore(name, sc, err)
		case "context_precision":
			sc, err := s.scoreContextPrecision(ctx, payload.Question, payload.Reference, payload.Contexts)
			setScore(name, sc, err)
		case "context_recall":
			sc, err := s.scoreContextRecall(ctx, payload.Question, payload.Reference, payload.Contexts)
			setScore(name, sc, err)
		case "answer_correctness":
			sc, err := s.scoreAnswerCorrectness(ctx, payload.Question, payload.Answer, payload.Reference)
			setScore(name, sc, err)
		case "answer_relevancy":
			if !hasEmbd {
				out[name] = nil
				errorsMap[name] = "skipped: no embedding model"
				continue
			}
			sc, err := s.scoreAnswerRelevancy(ctx, payload.Question, payload.Answer)
			setScore(name, sc, err)
		default:
			out[name] = nil
			errorsMap[name] = "unknown metric"
		}
	}
	if len(errorsMap) > 0 {
		out["errors"] = errorsMap
	}
	if payload.Meta != nil {
		out["_scoring"] = payload.Meta
	}
	return out
}
