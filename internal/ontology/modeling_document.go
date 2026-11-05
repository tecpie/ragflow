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
	"encoding/json"
	"fmt"
	"strings"
)

// CountModelingDocument counts concept types, property definitions and relation
// types in a workbench OntologyDocument or CaseBundle JSON.
// present is false when raw is empty. A non-empty payload without conceptTypes
// returns an error.
func CountModelingDocument(raw string) (concepts, properties, relations int, present bool, err error) {
	body, present, err := modelingBody(raw)
	if err != nil || !present {
		return 0, 0, 0, present, err
	}
	concepts = len(body.concepts)
	for _, c := range body.concepts {
		properties += len(c.Properties)
	}
	relations = len(body.relations)
	return concepts, properties, relations, true, nil
}

// TurtleFromModelingJSON builds a TBox Turtle document from workbench JSON so
// Playground and knowledge compilation can keep reading the ontology row.
func TurtleFromModelingJSON(baseIRI, raw string) (string, error) {
	body, present, err := modelingBody(raw)
	if err != nil {
		return "", err
	}
	if !present {
		return "", fmt.Errorf("document is empty")
	}
	schema := InducedSchema{
		Classes:    make([]InducedClass, 0, len(body.concepts)),
		Properties: make([]InducedProperty, 0),
	}
	localByID := map[string]string{}
	for _, c := range body.concepts {
		local := SafeLocalName(c.ID)
		if local == "" {
			local = SafeLocalName(c.Name)
		}
		if local == "" {
			continue
		}
		if c.ID != "" {
			localByID[c.ID] = local
		}
		localByID[local] = local
		schema.Classes = append(schema.Classes, InducedClass{ID: local, Label: strings.TrimSpace(c.Name)})
	}
	for _, r := range body.relations {
		label := strings.TrimSpace(r.Name)
		baseID := r.ID
		if baseID == "" {
			baseID = label
		}
		for _, from := range r.Source {
			for _, to := range r.Target {
				domain := localByID[from]
				if domain == "" {
					domain = SafeLocalName(from)
				}
				rng := localByID[to]
				if rng == "" {
					rng = SafeLocalName(to)
				}
				schema.Properties = append(schema.Properties, InducedProperty{
					ID:     baseID + "_" + from + "_" + to,
					Label:  label,
					Domain: domain,
					Range:  rng,
				})
			}
		}
	}
	text := BuildTurtle(baseIRI, schema)
	if _, err := ParseTurtle(text, "Ontology", "", baseIRI); err != nil {
		return "", err
	}
	return text, nil
}

type modelingConcept struct {
	ID         string
	Name       string
	Properties []struct{}
}

type modelingRelation struct {
	ID     string
	Name   string
	Source []string
	Target []string
}

type modelingDoc struct {
	concepts  []modelingConcept
	relations []modelingRelation
}

func modelingBody(raw string) (modelingDoc, bool, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return modelingDoc{}, false, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &root); err != nil {
		return modelingDoc{}, false, fmt.Errorf("document is not json: %w", err)
	}
	body := root
	if ont, ok := root["ontology"]; ok {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(ont, &nested); err != nil {
			return modelingDoc{}, false, fmt.Errorf("document ontology is not an object: %w", err)
		}
		body = nested
	}
	rawConcepts, ok := body["conceptTypes"]
	if !ok {
		return modelingDoc{}, false, fmt.Errorf("document is not an ontology model")
	}
	var concepts []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Properties []struct {
			Key string `json:"key"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(rawConcepts, &concepts); err != nil {
		return modelingDoc{}, false, fmt.Errorf("document conceptTypes: %w", err)
	}
	out := modelingDoc{concepts: make([]modelingConcept, 0, len(concepts))}
	for _, c := range concepts {
		out.concepts = append(out.concepts, modelingConcept{
			ID:         c.ID,
			Name:       c.Name,
			Properties: make([]struct{}, len(c.Properties)),
		})
	}
	if rawRels, ok := body["relationTypes"]; ok && string(rawRels) != "null" {
		var rels []struct {
			ID     string   `json:"id"`
			Name   string   `json:"name"`
			Source []string `json:"sourceConcepts"`
			Target []string `json:"targetConcepts"`
		}
		if err := json.Unmarshal(rawRels, &rels); err != nil {
			return modelingDoc{}, false, fmt.Errorf("document relationTypes: %w", err)
		}
		for _, r := range rels {
			out.relations = append(out.relations, modelingRelation{
				ID: r.ID, Name: r.Name, Source: r.Source, Target: r.Target,
			})
		}
	}
	return out, true, nil
}
