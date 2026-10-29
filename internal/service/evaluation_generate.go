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
	"math/rand"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"ragflow/internal/common"
	"ragflow/internal/dao"
	"ragflow/internal/engine"
	"ragflow/internal/engine/types"
	"ragflow/internal/entity"
	modelModule "ragflow/internal/entity/models"
	"ragflow/internal/permission"
)

const (
	evalGenConcurrency   = 4
	evalGenMaxChunkChars = 6000
	evalGenChunkPageSize = 12
)

// EvalStyleSample is a few-shot example for dataset generation prompts.
type EvalStyleSample struct {
	Question    string `json:"question"`
	GroundTruth string `json:"ground_truth"`
}

// GenerateDatasetParams controls GenerateDatasetFromKBs.
type GenerateDatasetParams struct {
	Name           string
	Description    string
	KBIDs          []string
	LLMID          string
	PerDocMin      int
	PerDocMax      int
	MultiHopRatio  float64
	Prompt         string // single-doc template; supports {n}/{content}/{samples}
	MultiHopPrompt string // multi-hop template; supports {n}/{content}/{samples}
	StyleSamples   []EvalStyleSample
}

type evalGenDoc struct {
	ID   string
	KbID string
	Name string
}

var defaultEvalStyleSamples = []EvalStyleSample{
	{
		Question:    "《电力系统安全稳定导则》（GB 38755—2019）适用于哪个电压等级的电力系统？",
		GroundTruth: "适用于电压等级为220kV及以上的电力系统；220kV以下的电力系统（含分布式电源）可参照执行。",
	},
	{
		Question:    "《电能质量 供电电压偏差》（GB/T 12325—2008）对20kV及以下三相供电电压和220V单相供电电压的偏差限值分别是多少？",
		GroundTruth: "20kV及以下三相供电电压偏差为标称电压的±7%；220V单相供电电压偏差为标称电压的+7%、－10%。",
	},
	{
		Question:    "《发电厂继电保护和安全自动装置现场工作安全措施规范》（DL/T 2533—2022）规定，二次安全措施执行和恢复时压板应按什么顺序操作？",
		GroundTruth: "执行时先退出出口压板，后退出功能压板，最后投入检修压板；恢复时操作顺序与执行时相反。",
	},
	{
		Question:    "《燃煤发电企业安全生产标准化实施规范》（DL/T 3006—2025）对主要负责人和安全管理人员的初次安全培训及每年再培训时间有何要求？",
		GroundTruth: "初次安全培训时间不得少于32学时；每年再培训时间不得少于12学时。",
	},
	{
		Question:    "《电力系统大面积停电恢复技术导则》（GB/T 40613—2021）适用于哪个电压等级电力系统的大面积停电恢复？",
		GroundTruth: "适用于220kV及以上电力系统的大面积停电恢复，涉及其他电压等级的电力系统参照执行。",
	},
	{
		Question:    "《电力安全工作规程 发电厂和变电站电气部分》（GB 26860—2011）规定，高压设备发生接地故障时，室内和室外人员进入接地点多大范围内应穿绝缘靴？",
		GroundTruth: "室内人员进入接地点4m以内、室外人员进入接地点8m以内，均应穿绝缘靴；接触设备的外壳和构架时，还应戴绝缘手套。",
	},
	{
		Question:    "《电力系统安全稳定导则》（GB 38755—2019）中的N－1原则要求，正常运行方式下任一元件断开后系统应满足什么条件？",
		GroundTruth: "正常运行方式下任一元件无故障或因故障断开时，电力系统应能保持稳定运行和正常供电，其他元件不过负荷，电压和频率均在允许范围内。",
	},
	{
		Question:    "《电力系统自动低频减负荷技术规定》（GB/T 40596—2021）对因负荷过切引起恢复时的系统频率过调最大值有何规定？",
		GroundTruth: "因负荷过切引起恢复时的系统频率过调，其最大值不应超过51.0 Hz，并应与运行中机组过频率保护相协调，且留有一定裕度。",
	},
}

// GenerateDatasetFromKBs creates a dataset and asynchronously generates cases
// from completed documents in the selected knowledge bases.
func (s *EvaluationService) GenerateDatasetFromKBs(ctx context.Context, userID string, params GenerateDatasetParams) (*entity.EvaluationDataset, error) {
	kbIDs := normalizeEvalKBIDs(params.KBIDs)
	if len(kbIDs) == 0 {
		return nil, common.NewCodedError(common.CodeArgumentError, "kb_ids is required")
	}
	llmID := strings.TrimSpace(params.LLMID)
	if llmID == "" {
		return nil, common.NewCodedError(common.CodeArgumentError, "llm_id is required")
	}
	perMin := params.PerDocMin
	perMax := params.PerDocMax
	if perMin <= 0 {
		perMin = 2
	}
	if perMax < perMin {
		perMax = perMin
	}
	if perMax > 5 {
		perMax = 5
	}
	ratio := params.MultiHopRatio
	if ratio <= 0 {
		ratio = 0.2
	}
	if ratio > 0.5 {
		ratio = 0.5
	}

	checker := permission.NewDatabaseChecker(dao.DB)
	for _, kbID := range kbIDs {
		if err := checker.CheckResource(
			ctx,
			permission.Subject{UserID: userID},
			permission.ResourceRef{Kind: permission.ResourceKindDataset, ID: kbID},
			permission.OperationRead,
		); err != nil {
			return nil, err
		}
	}

	desc := strings.TrimSpace(params.Description)
	if desc == "" {
		desc = "从知识库文档自动生成（后台生成中）"
	} else if !strings.Contains(desc, "生成中") {
		desc = desc + "（后台生成中）"
	}
	ds, err := s.CreateDataset(ctx, userID, params.Name, desc, kbIDs)
	if err != nil {
		return nil, err
	}

	samples := params.StyleSamples
	if len(samples) == 0 {
		samples = append([]EvalStyleSample(nil), defaultEvalStyleSamples...)
	}

	tenantID, err := requireCurrentTenantID(ctx, userID)
	if err != nil {
		return nil, err
	}
	bg := context.WithoutCancel(ctx)
	if _, loaded := s.activeGens.LoadOrStore(ds.ID, struct{}{}); loaded {
		return ds, nil
	}
	prompt := strings.TrimSpace(params.Prompt)
	if prompt == "" {
		prompt = defaultEvalSinglePromptTemplate
	}
	multiPrompt := strings.TrimSpace(params.MultiHopPrompt)
	if multiPrompt == "" {
		multiPrompt = defaultEvalMultiHopPromptTemplate
	}

	go func() {
		defer s.activeGens.Delete(ds.ID)
		s.runGenerateWorker(bg, userID, tenantID, ds.ID, llmID, kbIDs, perMin, perMax, ratio, prompt, multiPrompt, samples)
	}()
	return ds, nil
}

func (s *EvaluationService) runGenerateWorker(
	ctx context.Context,
	userID, tenantID, datasetID, llmID string,
	kbIDs []string,
	perMin, perMax int,
	multiHopRatio float64,
	promptTemplate, multiHopPromptTemplate string,
	samples []EvalStyleSample,
) {
	fail := func(msg string) {
		common.Error(fmt.Sprintf("evaluation dataset generate %s failed", datasetID), fmt.Errorf("%s", msg))
		desc := "生成失败: " + msg
		now := time.Now().UnixMilli()
		_ = s.db.WithContext(ctx).Model(&entity.EvaluationDataset{}).
			Where("id = ?", datasetID).
			Updates(map[string]any{"description": desc, "update_time": now}).Error
	}

	docs, err := s.listCompletedEvalDocs(ctx, kbIDs)
	if err != nil {
		fail(err.Error())
		return
	}
	if len(docs) == 0 {
		fail("selected knowledge bases have no completed documents")
		return
	}
	common.Info("evaluation dataset generate start",
		zap.String("dataset_id", datasetID),
		zap.Int("docs", len(docs)),
		zap.Int("kb_count", len(kbIDs)),
	)

	var (
		mu           sync.Mutex
		caseCount    int
		docDone      int
		importFailed bool
	)
	totalDocs := len(docs)
	persist := func(batch []ImportCaseItem, doneDocs int) {
		mu.Lock()
		defer mu.Unlock()
		if len(batch) > 0 {
			if _, err := s.ImportCases(ctx, userID, datasetID, batch); err != nil {
				importFailed = true
				common.Error(fmt.Sprintf("evaluation dataset generate %s: import batch", datasetID), err)
			} else {
				caseCount += len(batch)
			}
		}
		if doneDocs > 0 {
			docDone += doneDocs
		}
		desc := fmt.Sprintf("生成中：已完成 %d/%d 文档，已落库 %d 题", docDone, totalDocs, caseCount)
		now := time.Now().UnixMilli()
		_ = s.db.WithContext(ctx).Model(&entity.EvaluationDataset{}).
			Where("id = ?", datasetID).
			Updates(map[string]any{"description": desc, "update_time": now}).Error
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(evalGenConcurrency)
	for _, doc := range docs {
		doc := doc
		g.Go(func() error {
			n := perMin
			if perMax > perMin {
				n = perMin + rand.Intn(perMax-perMin+1)
			}
			chunkText, err := s.fetchDocChunkText(gctx, tenantID, doc.KbID, doc.ID)
			if err != nil {
				common.Error(fmt.Sprintf("eval gen fetch chunks %s", doc.ID), err)
				persist(nil, 1)
				return nil
			}
			if strings.TrimSpace(chunkText) == "" {
				persist(nil, 1)
				return nil
			}
			userPrompt := renderEvalGenPrompt(promptTemplate, n, chunkText, samples)
			generated, err := s.llmGenerateQA(gctx, tenantID, llmID, userPrompt)
			if err != nil {
				common.Error(fmt.Sprintf("eval gen single doc %s", doc.ID), err)
				persist(nil, 1)
				return nil
			}
			valid := filterGeneratedCases(generated, []string{doc.ID}, "single", []string{doc.Name})
			persist(valid, 1)
			return nil
		})
	}
	_ = g.Wait()

	multiHopTarget := int(float64(len(docs))*multiHopRatio + 0.5)
	if multiHopTarget < 1 && len(docs) >= 2 {
		multiHopTarget = 1
	}
	if len(docs) >= 2 && multiHopTarget > 0 {
		pairs := buildMultiHopGroups(docs, multiHopTarget)
		g2, gctx2 := errgroup.WithContext(ctx)
		g2.SetLimit(evalGenConcurrency)
		for _, group := range pairs {
			group := group
			g2.Go(func() error {
				parts := make([]string, 0, len(group))
				ids := make([]string, 0, len(group))
				names := make([]string, 0, len(group))
				for i, d := range group {
					text, err := s.fetchDocChunkText(gctx2, tenantID, d.KbID, d.ID)
					if err != nil || strings.TrimSpace(text) == "" {
						continue
					}
					parts = append(parts, fmt.Sprintf("【资料%d】\n%s", i+1, text))
					ids = append(ids, d.ID)
					names = append(names, d.Name)
				}
				if len(parts) < 2 {
					return nil
				}
				userPrompt := renderEvalGenPrompt(multiHopPromptTemplate, 1, strings.Join(parts, "\n\n---\n\n"), samples)
				generated, err := s.llmGenerateQA(gctx2, tenantID, llmID, userPrompt)
				if err != nil {
					common.Error("eval gen multi-hop", err)
					return nil
				}
				valid := filterGeneratedCases(generated, ids, "multi_hop", names)
				if len(valid) == 0 {
					return nil
				}
				// Keep at most one multi-hop case per group.
				if len(valid) > 1 {
					valid = valid[:1]
				}
				persist(valid, 0)
				return nil
			})
		}
		_ = g2.Wait()
	}

	if caseCount == 0 {
		if importFailed {
			fail("import cases failed")
			return
		}
		fail("no valid cases generated")
		return
	}
	desc := fmt.Sprintf("从知识库自动生成完成：%d 题（文档 %d）", caseCount, totalDocs)
	if importFailed {
		desc = fmt.Sprintf("从知识库自动生成部分完成：%d 题（文档 %d，部分批次落库失败）", caseCount, totalDocs)
	}
	now := time.Now().UnixMilli()
	_ = s.db.WithContext(ctx).Model(&entity.EvaluationDataset{}).
		Where("id = ?", datasetID).
		Updates(map[string]any{"description": desc, "update_time": now}).Error
	common.Info("evaluation dataset generate done",
		zap.String("dataset_id", datasetID),
		zap.Int("cases", caseCount),
	)
}

func (s *EvaluationService) listCompletedEvalDocs(ctx context.Context, kbIDs []string) ([]evalGenDoc, error) {
	docDAO := dao.NewDocumentDAO()
	out := make([]evalGenDoc, 0)
	for _, kbID := range kbIDs {
		page := 1
		for {
			rows, total, err := docDAO.ListByKBIDWithOptions(ctx, s.db, dao.DocumentListOptions{
				KbID:        kbID,
				RunStatuses: []string{common.COMPLETED},
				OrderBy:     "create_time",
				Desc:        true,
				Offset:      (page - 1) * 100,
				Limit:       100,
			})
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				if row == nil || row.ChunkNum <= 0 {
					continue
				}
				name := ""
				if row.Name != nil {
					name = strings.TrimSpace(*row.Name)
				}
				if name == "" {
					name = row.ID
				}
				out = append(out, evalGenDoc{ID: row.ID, KbID: row.KbID, Name: name})
			}
			if int64(page*100) >= total || len(rows) == 0 {
				break
			}
			page++
		}
	}
	return out, nil
}

func (s *EvaluationService) fetchDocChunkText(ctx context.Context, tenantID, kbID, docID string) (string, error) {
	docEngine := engine.Get()
	if docEngine == nil {
		return "", fmt.Errorf("document engine is not initialized")
	}
	indexName := fmt.Sprintf("ragflow_%s", tenantID)
	orderBy := (&types.OrderByExpr{}).
		Asc("chunk_order_int").
		Asc("page_num_int").
		Asc("top_int")
	resp, err := docEngine.Search(ctx, &types.SearchRequest{
		IndexNames:   []string{indexName},
		KbIDs:        []string{kbID},
		Offset:       0,
		Limit:        evalGenChunkPageSize,
		SelectFields: []string{"id", "content_with_weight", "content"},
		OrderBy:      orderBy,
		Filter: map[string]interface{}{
			"doc_id": docID,
		},
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, chunk := range resp.Chunks {
		text := ""
		if v, ok := chunk["content_with_weight"].(string); ok {
			text = v
		} else if v, ok := chunk["content"].(string); ok {
			text = v
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(text)
		if utf8.RuneCountInString(b.String()) >= evalGenMaxChunkChars {
			break
		}
	}
	sText := b.String()
	if utf8.RuneCountInString(sText) > evalGenMaxChunkChars {
		runes := []rune(sText)
		sText = string(runes[:evalGenMaxChunkChars])
	}
	return sText, nil
}

func (s *EvaluationService) llmGenerateQA(ctx context.Context, tenantID, llmID, userPrompt string) ([]ImportCaseItem, error) {
	if s.factory == nil {
		return nil, fmt.Errorf("model factory not initialized")
	}
	chatModel, err := s.factory.NewChatModel(ctx, ModelAccess{TenantID: tenantID}, llmID)
	if err != nil {
		return nil, err
	}
	temp := 0.2
	maxTokens := 2048
	config := &modelModule.ChatConfig{Temperature: &temp, MaxTokens: &maxTokens}
	system := "你是电网/电力标准知识评测出题专家。只依据给定原文出题，禁止编造原文没有的数值与条款。输出严格 JSON。"
	resp, err := chatModel.ChatWithMessages(ctx, []modelModule.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: userPrompt},
	}, config, nil)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Answer == nil {
		return nil, fmt.Errorf("empty llm response")
	}
	return parseGeneratedQAJSON(*resp.Answer)
}

const defaultEvalSinglePromptTemplate = `根据下列原文，生成 {n} 道中文问答。题型可参考：适用对象、定义、限值、操作顺序、适用范围等。

要求：
1. 问题要像真实用户口头/业务提问，直接问知识点或规定本身。
2. 禁止在问题中出现文件名、文档名、附件名、资料编号（如「根据某某文档」「资料1」「xxx.pdf」）；也不要以「该文档规定」起句。
3. 若原文中有正式标准/规程名称或标准号，可自然写入问题；没有则不要硬凑名称。
4. ground_truth 必须能由原文直接支撑，写完整可核对答案。
5. 禁止输出「未找到」「无法回答」类内容。
6. 只输出 JSON：{"cases":[{"question":"...","ground_truth":"..."},...]}

样例：
{samples}

原文：
{content}
`

const defaultEvalMultiHopPromptTemplate = `根据下列多段原文，生成 {n} 道需要综合 2 段及以上内容才能完整回答的中文问答题（对比、条件组合、多规定协调等）。

要求：
1. 问题要像真实用户提问，直接问业务/技术点；禁止出现文件名、文档名、附件名、资料编号。
2. ground_truth 必须综合多段原文，禁止只用单段就能答完的浅题。
3. 禁止编造；禁止「未找到」。
4. 只输出 JSON：{"cases":[{"question":"...","ground_truth":"..."}]}

样例风格（供文风参考）：
{samples}

原文：
{content}
`

// renderEvalGenPrompt fills {n}/{content}/{samples}. Missing placeholders are appended.
func renderEvalGenPrompt(tmpl string, n int, content string, samples []EvalStyleSample) string {
	tmpl = strings.TrimSpace(tmpl)
	if tmpl == "" {
		tmpl = defaultEvalSinglePromptTemplate
	}
	sampleText := formatStyleSamples(samples, 5)
	out := tmpl
	out = strings.ReplaceAll(out, "{n}", fmt.Sprintf("%d", n))
	hasContent := strings.Contains(out, "{content}")
	hasSamples := strings.Contains(out, "{samples}")
	out = strings.ReplaceAll(out, "{content}", content)
	out = strings.ReplaceAll(out, "{samples}", sampleText)
	if !hasSamples && strings.TrimSpace(sampleText) != "" {
		out += "\n\n样例：\n" + sampleText
	}
	if !hasContent {
		out += "\n\n原文：\n" + content
	}
	return out
}

func formatStyleSamples(samples []EvalStyleSample, limit int) string {
	if limit <= 0 || limit > len(samples) {
		limit = len(samples)
	}
	var b strings.Builder
	for i := 0; i < limit; i++ {
		s := samples[i]
		b.WriteString(fmt.Sprintf("- Q: %s\n  A: %s\n", strings.TrimSpace(s.Question), strings.TrimSpace(s.GroundTruth)))
	}
	return b.String()
}

func buildMultiHopGroups(docs []evalGenDoc, target int) [][]evalGenDoc {
	if len(docs) < 2 || target <= 0 {
		return nil
	}
	perm := append([]evalGenDoc(nil), docs...)
	rand.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
	out := make([][]evalGenDoc, 0, target)
	i := 0
	for len(out) < target && i < len(perm) {
		size := 2
		if len(perm)-i >= 3 && rand.Intn(2) == 0 {
			size = 3
		}
		if i+size > len(perm) {
			size = len(perm) - i
		}
		if size < 2 {
			break
		}
		out = append(out, append([]evalGenDoc(nil), perm[i:i+size]...))
		i += size
	}
	return out
}

func parseGeneratedQAJSON(raw string) ([]ImportCaseItem, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty answer")
	}
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j > i {
			raw = raw[i : j+1]
		}
	}
	var payload struct {
		Cases []struct {
			Question    string `json:"question"`
			GroundTruth string `json:"ground_truth"`
			Answer      string `json:"answer"`
		} `json:"cases"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, err
	}
	out := make([]ImportCaseItem, 0, len(payload.Cases))
	for _, c := range payload.Cases {
		q := strings.TrimSpace(c.Question)
		gt := strings.TrimSpace(c.GroundTruth)
		if gt == "" {
			gt = strings.TrimSpace(c.Answer)
		}
		if q == "" || gt == "" {
			continue
		}
		out = append(out, ImportCaseItem{Question: q, GroundTruth: gt})
	}
	return out, nil
}

func filterGeneratedCases(items []ImportCaseItem, docIDs []string, kind string, bannedNames []string) []ImportCaseItem {
	out := make([]ImportCaseItem, 0, len(items))
	docAny := make([]any, 0, len(docIDs))
	for _, id := range docIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			docAny = append(docAny, id)
		}
	}
	for _, item := range items {
		q := strings.TrimSpace(item.Question)
		gt := strings.TrimSpace(item.GroundTruth)
		if q == "" || utf8.RuneCountInString(gt) < 8 {
			continue
		}
		lowGT := strings.ToLower(gt)
		if strings.Contains(gt, "未找到") || strings.Contains(gt, "无法提供") ||
			strings.Contains(gt, "无法给出") || strings.Contains(gt, "知识库中未") ||
			strings.Contains(lowGT, "i don't know") {
			continue
		}
		if questionLooksLikeDocNameCue(q, bannedNames) {
			continue
		}
		out = append(out, ImportCaseItem{
			Question:       q,
			GroundTruth:    gt,
			RelevantDocIDs: append([]any(nil), docAny...),
			Metadata: map[string]any{
				"kind": kind,
			},
		})
	}
	return out
}

func questionLooksLikeDocNameCue(question string, bannedNames []string) bool {
	q := strings.TrimSpace(question)
	if q == "" {
		return false
	}
	low := strings.ToLower(q)
	for _, ext := range []string{".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".md"} {
		if strings.Contains(low, ext) {
			return true
		}
	}
	for _, cue := range []string{"该文档", "本文档", "上述文档", "以下文档", "根据文档", "参考文档", "资料1", "资料2", "资料3", "文件名"} {
		if strings.Contains(q, cue) {
			return true
		}
	}
	for _, name := range bannedNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		// Only reject explicit filename echoes (with extension), not standard titles
		// that happen to equal a document's display name.
		lowName := strings.ToLower(name)
		hasExt := false
		for _, ext := range []string{".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".md"} {
			if strings.HasSuffix(lowName, ext) {
				hasExt = true
				break
			}
		}
		if hasExt && strings.Contains(low, lowName) {
			return true
		}
		if hasExt {
			base := name[:strings.LastIndex(name, ".")]
			if len([]rune(base)) >= 6 && strings.Contains(q, base) &&
				(strings.Contains(q, "文档") || strings.Contains(q, "文件") || strings.Contains(q, "附件")) {
				return true
			}
		}
	}
	return false
}
