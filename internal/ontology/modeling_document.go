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

// ModelingFunction is a BFO-aligned function (capability) type.
type ModelingFunction struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	Description    string             `json:"description,omitempty"`
	BearerConcepts []string           `json:"bearerConcepts,omitempty"`
	Inputs         []ModelingProperty `json:"inputs,omitempty"`
	Outputs        []ModelingProperty `json:"outputs,omitempty"`
}

// ModelingAction is a BFO-aligned process/action type.
type ModelingAction struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	Description    string             `json:"description,omitempty"`
	AgentConcepts  []string           `json:"agentConcepts,omitempty"`
	ObjectConcepts []string           `json:"objectConcepts,omitempty"`
	ResultConcepts []string           `json:"resultConcepts,omitempty"`
	Properties     []ModelingProperty `json:"properties,omitempty"`
}

// ModelingDocument is the TBox subset used for ontology-operator extraction.
type ModelingDocument struct {
	Name      string             `json:"name,omitempty"`
	Concepts  []ModelingConcept  `json:"conceptTypes"`
	Relations []ModelingRelation `json:"relationTypes,omitempty"`
	Functions []ModelingFunction `json:"functionTypes,omitempty"`
	Actions   []ModelingAction   `json:"actionTypes,omitempty"`
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

// CountModelingDocument counts concept types, property definitions, relation
// types, function types and action types in a workbench OntologyDocument.
func CountModelingDocument(raw string) (concepts, properties, relations int, present bool, err error) {
	body, present, err := ParseModelingDocument(raw)
	if err != nil || !present {
		return 0, 0, 0, present, err
	}
	concepts = len(body.Concepts)
	for _, c := range body.Concepts {
		properties += len(c.Properties)
	}
	for _, r := range body.Relations {
		properties += len(r.Properties)
	}
	for _, f := range body.Functions {
		properties += len(f.Inputs) + len(f.Outputs)
	}
	for _, a := range body.Actions {
		properties += len(a.Properties)
	}
	relations = len(body.Relations)
	return concepts, properties, relations, true, nil
}

// CountFunctionAction returns function and action type counts.
func CountFunctionAction(raw string) (functions, actions int, present bool, err error) {
	body, present, err := ParseModelingDocument(raw)
	if err != nil || !present {
		return 0, 0, present, err
	}
	return len(body.Functions), len(body.Actions), true, nil
}

// TurtleFromModelingJSON builds a BFO-aligned TBox Turtle document from
// workbench JSON so Playground and knowledge compilation can keep reading the
// ontology row.
func TurtleFromModelingJSON(baseIRI, raw string) (string, error) {
	body, present, err := ParseModelingDocument(raw)
	if err != nil {
		return "", err
	}
	if !present {
		return "", fmt.Errorf("document is empty")
	}
	text := buildModelingTurtle(baseIRI, body)
	if _, err := ParseTurtle(text, "Ontology", "", baseIRI); err != nil {
		return "", err
	}
	return text, nil
}

// PlaygroundFromModeling projects the modeling document into Playground JSON
// with function/action as entity types and BFO/RO role edges.
func PlaygroundFromModeling(name, description string, body ModelingDocument) PlaygroundOntology {
	out := PlaygroundOntology{
		Name:          name,
		Description:   description,
		EntityTypes:   []PlaygroundEntityType{},
		Relationships: []PlaygroundRelationship{},
	}
	if strings.TrimSpace(out.Name) == "" {
		out.Name = strings.TrimSpace(body.Name)
	}
	if out.Name == "" {
		out.Name = "Ontology"
	}
	seen := map[string]struct{}{}
	addEntity := func(id, label, desc, icon, color string, props []ModelingProperty) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(label) == "" {
			label = id
		}
		et := PlaygroundEntityType{
			ID:          id,
			Name:        label,
			Description: strings.TrimSpace(desc),
			Properties:  playgroundProps(props),
			Icon:        icon,
			Color:       color,
		}
		out.EntityTypes = append(out.EntityTypes, et)
	}
	for i, c := range body.Concepts {
		addEntity(c.ID, c.Name, c.Description, IconConcept, entityColors[i%len(entityColors)], c.Properties)
	}
	for _, f := range body.Functions {
		addEntity(f.ID, f.Name, f.Description, IconFunction, ColorFunction, append(f.Inputs, f.Outputs...))
	}
	for _, a := range body.Actions {
		addEntity(a.ID, a.Name, a.Description, IconAction, ColorAction, a.Properties)
	}
	for _, r := range body.Relations {
		for _, from := range r.Source {
			for _, to := range r.Target {
				if _, ok := seen[from]; !ok {
					continue
				}
				if _, ok := seen[to]; !ok {
					continue
				}
				out.Relationships = append(out.Relationships, PlaygroundRelationship{
					ID:          r.ID + "__" + from + "__" + to,
					Name:        firstNonEmpty(r.Name, r.ID),
					From:        from,
					To:          to,
					Cardinality: "many-to-many",
					Kind:        KindAssociation,
					Description: r.Description,
				})
			}
		}
	}
	for _, f := range body.Functions {
		for _, bearer := range f.BearerConcepts {
			if _, ok := seen[bearer]; !ok {
				continue
			}
			out.Relationships = append(out.Relationships, PlaygroundRelationship{
				ID:          f.ID + "__" + RoleInheresIn + "__" + bearer,
				Name:        RoleLabel(RoleInheresIn),
				From:        f.ID,
				To:          bearer,
				Cardinality: "many-to-one",
				Kind:        KindInheresIn,
				Description: "BFO: function inheres in bearer",
			})
		}
	}
	for _, a := range body.Actions {
		addRoleEdges := func(role string, concepts []string) {
			for _, c := range concepts {
				if _, ok := seen[c]; !ok {
					continue
				}
				out.Relationships = append(out.Relationships, PlaygroundRelationship{
					ID:          a.ID + "__" + role + "__" + c,
					Name:        RoleLabel(role),
					From:        a.ID,
					To:          c,
					Cardinality: "many-to-many",
					Kind:        role,
					Description: "RO-style participation role",
				})
			}
		}
		addRoleEdges(KindHasAgent, a.AgentConcepts)
		addRoleEdges(KindHasPatient, a.ObjectConcepts)
		addRoleEdges(KindHasResult, a.ResultConcepts)
	}
	return out
}

func playgroundProps(props []ModelingProperty) []PlaygroundProperty {
	var out []PlaygroundProperty
	for _, p := range props {
		key := strings.TrimSpace(p.Key)
		if key == "" {
			continue
		}
		name := strings.TrimSpace(p.Label)
		if name == "" {
			name = key
		}
		typ := strings.TrimSpace(p.Type)
		if typ == "" {
			typ = "string"
		}
		out = append(out, PlaygroundProperty{
			Name:        name,
			Type:        typ,
			Description: strings.TrimSpace(p.Description),
		})
	}
	if out == nil {
		out = []PlaygroundProperty{}
	}
	return out
}

func buildModelingTurtle(baseIRI string, body ModelingDocument) string {
	base := strings.TrimSpace(baseIRI)
	if base == "" {
		base = "http://ragflow.local/ontology#"
	}
	if !strings.HasSuffix(base, "#") && !strings.HasSuffix(base, "/") {
		base += "#"
	}
	localByID := map[string]string{}
	resolve := func(id, name string) string {
		local := SafeLocalName(id)
		if local == "" {
			local = SafeLocalName(name)
		}
		return local
	}
	var b strings.Builder
	b.WriteString("@prefix : <")
	b.WriteString(base)
	b.WriteString("> .\n")
	b.WriteString("@prefix owl: <http://www.w3.org/2002/07/owl#> .\n")
	b.WriteString("@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .\n")
	b.WriteString("@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .\n")
	b.WriteString("@prefix bfo: <")
	b.WriteString(BFOPURL)
	b.WriteString("> .\n\n")

	writeClass := func(id, label, comment, parentIRI string) {
		if id == "" {
			return
		}
		b.WriteString(":")
		b.WriteString(id)
		b.WriteString(" a owl:Class")
		parts := make([]string, 0, 3)
		if label != "" && label != id {
			parts = append(parts, "  rdfs:label "+turtleString(label))
		}
		if comment != "" {
			parts = append(parts, "  rdfs:comment "+turtleString(comment))
		}
		if parentIRI != "" {
			parts = append(parts, "  rdfs:subClassOf <"+parentIRI+">")
		}
		if len(parts) > 0 {
			b.WriteString(" ;\n")
			b.WriteString(strings.Join(parts, " ;\n"))
		}
		b.WriteString(" .\n\n")
	}

	for _, c := range body.Concepts {
		local := resolve(c.ID, c.Name)
		if local == "" {
			continue
		}
		if c.ID != "" {
			localByID[c.ID] = local
		}
		localByID[local] = local
		writeClass(local, strings.TrimSpace(c.Name), strings.TrimSpace(c.Description), "")
		for _, p := range c.Properties {
			writeDatatypeProperty(&b, local, p)
		}
	}
	for _, f := range body.Functions {
		local := resolve(f.ID, f.Name)
		if local == "" {
			continue
		}
		if f.ID != "" {
			localByID[f.ID] = local
		}
		localByID[local] = local
		writeClass(local, strings.TrimSpace(f.Name), strings.TrimSpace(f.Description), BFOFunction)
		for _, bearer := range f.BearerConcepts {
			dom := localByID[bearer]
			if dom == "" {
				dom = SafeLocalName(bearer)
			}
			if dom == "" {
				continue
			}
			writeObjectProperty(&b, local+"_"+RoleInheresIn+"_"+dom, RoleLabel(RoleInheresIn), local, dom)
		}
		for _, p := range f.Inputs {
			writeDatatypeProperty(&b, local, p)
		}
		for _, p := range f.Outputs {
			writeDatatypeProperty(&b, local, p)
		}
	}
	for _, a := range body.Actions {
		local := resolve(a.ID, a.Name)
		if local == "" {
			continue
		}
		if a.ID != "" {
			localByID[a.ID] = local
		}
		localByID[local] = local
		writeClass(local, strings.TrimSpace(a.Name), strings.TrimSpace(a.Description), BFOProcess)
		writeRoles := func(role string, concepts []string) {
			for _, c := range concepts {
				rng := localByID[c]
				if rng == "" {
					rng = SafeLocalName(c)
				}
				if rng == "" {
					continue
				}
				writeObjectProperty(&b, local+"_"+role+"_"+rng, RoleLabel(role), local, rng)
			}
		}
		writeRoles(RoleHasAgent, a.AgentConcepts)
		writeRoles(RoleHasPatient, a.ObjectConcepts)
		writeRoles(RoleHasResult, a.ResultConcepts)
		for _, p := range a.Properties {
			writeDatatypeProperty(&b, local, p)
		}
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
				propID := SafeLocalName(baseID + "_" + from + "_" + to)
				if propID == "" {
					continue
				}
				writeObjectProperty(&b, propID, label, domain, rng)
			}
		}
		for _, p := range r.Properties {
			owner := SafeLocalName(baseID)
			if owner == "" {
				continue
			}
			writeDatatypeProperty(&b, owner, p)
		}
	}
	return b.String()
}

func writeObjectProperty(b *strings.Builder, id, label, domain, rng string) {
	if id == "" {
		return
	}
	b.WriteString(":")
	b.WriteString(id)
	b.WriteString(" a owl:ObjectProperty")
	parts := make([]string, 0, 3)
	if label != "" && label != id {
		parts = append(parts, "  rdfs:label "+turtleString(label))
	}
	if domain != "" {
		parts = append(parts, "  rdfs:domain :"+domain)
	}
	if rng != "" {
		parts = append(parts, "  rdfs:range :"+rng)
	}
	if len(parts) > 0 {
		b.WriteString(" ;\n")
		b.WriteString(strings.Join(parts, " ;\n"))
	}
	b.WriteString(" .\n\n")
}

func writeDatatypeProperty(b *strings.Builder, domain string, p ModelingProperty) {
	key := SafeLocalName(p.Key)
	if key == "" || domain == "" {
		return
	}
	id := domain + "_" + key
	label := strings.TrimSpace(p.Label)
	if label == "" {
		label = strings.TrimSpace(p.Key)
	}
	b.WriteString(":")
	b.WriteString(id)
	b.WriteString(" a owl:DatatypeProperty")
	parts := []string{"  rdfs:domain :" + domain}
	if label != "" && label != id {
		parts = append([]string{"  rdfs:label " + turtleString(label)}, parts...)
	}
	if d := strings.TrimSpace(p.Description); d != "" {
		parts = append(parts, "  rdfs:comment "+turtleString(d))
	}
	b.WriteString(" ;\n")
	b.WriteString(strings.Join(parts, " ;\n"))
	b.WriteString(" .\n\n")
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
	if rawFns, ok := body["functionTypes"]; ok && string(rawFns) != "null" {
		var fns []ModelingFunction
		if err := json.Unmarshal(rawFns, &fns); err != nil {
			return ModelingDocument{}, false, fmt.Errorf("document functionTypes: %w", err)
		}
		out.Functions = fns
	}
	if rawActs, ok := body["actionTypes"]; ok && string(rawActs) != "null" {
		var acts []ModelingAction
		if err := json.Unmarshal(rawActs, &acts); err != nil {
			return ModelingDocument{}, false, fmt.Errorf("document actionTypes: %w", err)
		}
		out.Actions = acts
	}
	return out, true, nil
}
