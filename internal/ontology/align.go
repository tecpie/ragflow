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

package ontology

import (
	"strings"
	"unicode"
)

// Grammatical particles that may follow a class or relation name without
// starting a new content word. 变电站食堂 is one content word; 电源侧厂站是 is
// the relation name plus a copula.
const alignParticles = "是的了吗呢吧啊呀着过在把被给对向从到由为和与或及并而就都也还又再"

// AlignedClass is one ontology class named by a question.
type AlignedClass struct {
	ID    string
	Label string
}

// AlignedProperty is one object property named by a question.
// Domain and Range are class local ids from the playground projection.
type AlignedProperty struct {
	ID     string
	Label  string
	Domain string
	Range  string
}

// Alignment is the set of classes and object properties a question names.
type Alignment struct {
	Classes    []AlignedClass
	Properties []AlignedProperty
}

// Empty reports whether the question named neither a class nor a relation.
func (a Alignment) Empty() bool {
	return len(a.Classes) == 0 && len(a.Properties) == 0
}

// AlignLabels returns classes and association properties whose label or local
// id occurs in query. Subclass edges are skipped. A label glued to the front
// of a longer content word (变电站食堂) does not match.
func AlignLabels(query string, pg PlaygroundOntology) Alignment {
	var out Alignment
	query = strings.TrimSpace(query)
	if query == "" {
		return out
	}
	seenClass := map[string]bool{}
	for _, et := range pg.EntityTypes {
		label := firstNonEmpty(et.Name, et.ID)
		if !labelHit(query, label) && !labelHit(query, et.ID) {
			continue
		}
		id := firstNonEmpty(et.ID, SafeLocalName(label))
		if id == "" || seenClass[id] {
			continue
		}
		seenClass[id] = true
		out.Classes = append(out.Classes, AlignedClass{ID: id, Label: label})
	}
	seenProp := map[string]bool{}
	for _, rel := range pg.Relationships {
		if rel.Kind == "subclass" {
			continue
		}
		label := strings.TrimSpace(rel.Name)
		if label == "" || !labelHit(query, label) {
			continue
		}
		id := SafeLocalName(label)
		if id == "" {
			id = label
		}
		key := id + "\x00" + rel.From + "\x00" + rel.To
		if seenProp[key] {
			continue
		}
		seenProp[key] = true
		out.Properties = append(out.Properties, AlignedProperty{
			ID:     id,
			Label:  label,
			Domain: rel.From,
			Range:  rel.To,
		})
	}
	return out
}

func labelHit(query, label string) bool {
	label = strings.TrimSpace(label)
	if label == "" || strings.Contains(label, "__") {
		return false
	}
	if occurrenceOK(query, label) {
		return true
	}
	safeLabel := SafeLocalName(label)
	safeQuery := SafeLocalName(query)
	if safeLabel == "" || safeQuery == "" || strings.EqualFold(safeLabel, label) {
		return false
	}
	return occurrenceOK(safeQuery, safeLabel)
}

// occurrenceOK reports whether label occurs in query as its own mention.
// A match that begins a letter run and is immediately followed by another
// content letter is a prefix of a longer word, not a mention.
func occurrenceOK(query, label string) bool {
	q := []rune(strings.ToLower(query))
	l := []rune(strings.ToLower(strings.TrimSpace(label)))
	if len(l) == 0 || len(q) < len(l) {
		return false
	}
	for i := 0; i+len(l) <= len(q); i++ {
		if string(q[i:i+len(l)]) != string(l) {
			continue
		}
		startsRun := i == 0 || !unicode.IsLetter(q[i-1])
		if !startsRun {
			return true
		}
		end := i + len(l)
		if end >= len(q) || !unicode.IsLetter(q[end]) || strings.ContainsRune(alignParticles, q[end]) {
			return true
		}
	}
	return false
}
