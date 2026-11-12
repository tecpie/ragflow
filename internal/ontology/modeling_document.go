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

// ModelingProperty is one datatype/ref property on a concept or relation type.
type ModelingProperty struct {
	Key              string   `json:"key"`
	Label            string   `json:"label,omitempty"`
	Type             string   `json:"type,omitempty"`
	Required         bool     `json:"required,omitempty"`
	Description      string   `json:"description,omitempty"`
	EnumValues       []string `json:"enumValues,omitempty"`
	RefConceptTypeID string   `json:"refConceptTypeId,omitempty"`
}

// ModelingConcept is one concept type from a workbench OntologyDocument.
type ModelingConcept struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Properties  []ModelingProperty `json:"properties,omitempty"`
}

// ModelingRelation is one relation type from a workbench OntologyDocument.
type ModelingRelation struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Source      []string           `json:"sourceConcepts,omitempty"`
	Target      []string           `json:"targetConcepts,omitempty"`
	Directed    *bool              `json:"directed,omitempty"`
	Properties  []ModelingProperty `json:"properties,omitempty"`
}

// ModelingDocument is the TBox subset used for ontology-operator extraction.
type ModelingDocument struct {
	Name      string             `json:"name,omitempty"`
	Concepts  []ModelingConcept  `json:"conceptTypes"`
	Relations []ModelingRelation `json:"relationTypes,omitempty"`
}

// ParseModelingDocument parses workbench OntologyDocument or CaseBundle JSON.
// present is false when raw is empty. A non-empty payload without conceptTypes
// returns an error.
func ParseModelingDocument(raw string) (ModelingDocument, bool, error) {
	body, present, err := modelingBody(raw)
	if err != nil || !present {
		return ModelingDocument{}, present, err
	}
	return body, true, nil
}

// CountModelingDocument counts concept types, property definitions and relation
// types in a workbench OntologyDocument or CaseBundle JSON.
func CountModelingDocument(raw string) (concepts, properties, relations int, present bool, err error) {
	body, present, err := ParseModelingDocument(raw)
	if err != nil || !present {
		return 0, 0, 0, present, err
	}
	concepts = len(body.Concepts)
	for _, c := range body.Concepts {
		properties += len(c.Properties)
	}
	relations = len(body.Relations)
	return concepts, properties, relations, true, nil
}

// TurtleFromModelingJSON builds a TBox Turtle document from workbench JSON so
// Playground and knowledge compilation can keep reading the ontology row.
func TurtleFromModelingJSON(baseIRI, raw string) (string, error) {
	body, present, err := ParseModelingDocument(raw)
	if err != nil {
		return "", err
	}
	if !present {
		return "", fmt.Errorf("document is empty")
	}
	schema := InducedSchema{
		Classes:    make([]InducedClass, 0, len(body.Concepts)),
		Properties: make([]InducedProperty, 0),
	}
	localByID := map[string]string{}
	for _, c := range body.Concepts {
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
	for _, r := range body.Relations {
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

func modelingBody(raw string) (ModelingDocument, bool, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ModelingDocument{}, false, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &root); err != nil {
		return ModelingDocument{}, false, fmt.Errorf("document is not json: %w", err)
	}
	body := root
	if ont, ok := root["ontology"]; ok {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(ont, &nested); err != nil {
			return ModelingDocument{}, false, fmt.Errorf("document ontology is not an object: %w", err)
		}
		body = nested
	}
	rawConcepts, ok := body["conceptTypes"]
	if !ok {
		return ModelingDocument{}, false, fmt.Errorf("document is not an ontology model")
	}
	var concepts []ModelingConcept
	if err := json.Unmarshal(rawConcepts, &concepts); err != nil {
		return ModelingDocument{}, false, fmt.Errorf("document conceptTypes: %w", err)
	}
	out := ModelingDocument{Concepts: concepts}
	if rawName, ok := body["name"]; ok {
		_ = json.Unmarshal(rawName, &out.Name)
	}
	if rawRels, ok := body["relationTypes"]; ok && string(rawRels) != "null" {
		var rels []ModelingRelation
		if err := json.Unmarshal(rawRels, &rels); err != nil {
			return ModelingDocument{}, false, fmt.Errorf("document relationTypes: %w", err)
		}
		out.Relations = rels
	}
	return out, true, nil
}
