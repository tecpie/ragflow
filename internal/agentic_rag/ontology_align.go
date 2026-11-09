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
	"math"
	"strings"

	"ragflow/internal/ontology"
)

// oagAlignSim is the cosine floor for a question vector against a class or
// relation label. It is below the entity-seed floor (0.8) because a short
// label is compared with a full question.
const oagAlignSim = 0.55

// alignQuery names ontology classes and relations in the question. Lexical
// hits come first. When the graph walk has a seed encoder, labels the question
// does not contain are added if their vector is close enough. No encoder means
// lexical hits only.
func alignQuery(ctx context.Context, tenantID, query string, pg ontology.PlaygroundOntology) ontology.Alignment {
	aligned := ontology.AlignLabels(query, pg)
	encode := kgSeedEncoder()
	if encode == nil {
		return aligned
	}
	qv := encode(ctx, tenantID, query)
	if len(qv) == 0 {
		return aligned
	}
	cache := map[string][]float64{}
	labelVec := func(label string) []float64 {
		label = strings.TrimSpace(label)
		if label == "" {
			return nil
		}
		if vec, ok := cache[label]; ok {
			return vec
		}
		vec := encode(ctx, tenantID, label)
		cache[label] = vec
		return vec
	}
	closeEnough := func(label string) bool {
		return cosineSim(qv, labelVec(label)) >= oagAlignSim
	}

	seenClass := map[string]bool{}
	for _, class := range aligned.Classes {
		seenClass[class.ID] = true
	}
	for _, et := range pg.EntityTypes {
		if et.ID == "" || seenClass[et.ID] {
			continue
		}
		label := et.Name
		if strings.TrimSpace(label) == "" {
			label = et.ID
		}
		if !closeEnough(label) {
			continue
		}
		seenClass[et.ID] = true
		aligned.Classes = append(aligned.Classes, ontology.AlignedClass{ID: et.ID, Label: label})
	}

	seenProp := map[string]bool{}
	for _, prop := range aligned.Properties {
		seenProp[prop.ID+"\x00"+prop.Domain+"\x00"+prop.Range] = true
	}
	for _, rel := range pg.Relationships {
		if rel.Kind == "subclass" {
			continue
		}
		label := strings.TrimSpace(rel.Name)
		if label == "" {
			continue
		}
		id := ontology.SafeLocalName(label)
		if id == "" {
			id = label
		}
		key := id + "\x00" + rel.From + "\x00" + rel.To
		if seenProp[key] || !closeEnough(label) {
			continue
		}
		seenProp[key] = true
		aligned.Properties = append(aligned.Properties, ontology.AlignedProperty{
			ID:     id,
			Label:  label,
			Domain: rel.From,
			Range:  rel.To,
		})
	}
	return aligned
}

func kgSeedEncoder() func(context.Context, string, string) []float64 {
	adapter, ok := GetKgService().(*KgAdapter)
	if !ok || adapter == nil || adapter.seedEncoder == nil {
		return nil
	}
	return func(ctx context.Context, tenantID, text string) []float64 {
		return adapter.seedVector(ctx, tenantID, text)
	}
}

func cosineSim(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
