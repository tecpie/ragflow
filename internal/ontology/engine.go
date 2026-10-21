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

// Package ontology wraps goRDFlib for Turtle parse, TBox projection, and
// Ontology Playground JSON export used by the product UI.
package ontology

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/rdfxml"
	"github.com/tggo/goRDFlib/turtle"
)

var entityColors = []string{
	"#0078D4", "#107C10", "#5C2D91", "#FFB900", "#D83B01", "#00A9E0", "#8764B8", "#498205",
}

// Document is a loaded TBox graph with optional display name.
type Document struct {
	Name        string
	Description string
	BaseIRI     string
	g           *rdflibgo.Graph
}

// ParseTurtle loads Turtle into an in-memory graph.
func ParseTurtle(turtleText, name, description, baseIRI string) (*Document, error) {
	text := strings.TrimSpace(turtleText)
	if text == "" {
		return nil, fmt.Errorf("turtle is empty")
	}
	g := rdflibgo.NewGraph()
	if baseIRI != "" {
		g.Bind("", rdflibgo.NewURIRefUnsafe(baseIRI))
	}
	if err := turtle.Parse(g, strings.NewReader(text)); err != nil {
		return nil, fmt.Errorf("parse turtle: %w", err)
	}
	if name == "" {
		name = "Ontology"
	}
	return &Document{Name: name, Description: description, BaseIRI: baseIRI, g: g}, nil
}

// ExportRDFXML serializes the graph as RDF/XML for Playground URL embeds.
func (d *Document) ExportRDFXML() ([]byte, error) {
	if d == nil || d.g == nil {
		return nil, fmt.Errorf("nil ontology document")
	}
	var buf bytes.Buffer
	if err := rdfxml.Serialize(d.g, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// PlaygroundProperty is a datatype-style property on an entity type.
type PlaygroundProperty struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// PlaygroundEntityType is Microsoft Ontology Playground entityTypes[] item.
type PlaygroundEntityType struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Properties  []PlaygroundProperty `json:"properties"`
	Icon        string               `json:"icon"`
	Color       string               `json:"color"`
}

// PlaygroundRelationship is Microsoft Ontology Playground relationships[] item.
type PlaygroundRelationship struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	From        string `json:"from"`
	To          string `json:"to"`
	Cardinality string `json:"cardinality"`
	Kind        string `json:"kind,omitempty"`
	Description string `json:"description,omitempty"`
}

// PlaygroundOntology is the JSON shape for data-ontology-inline embeds.
type PlaygroundOntology struct {
	Name          string                   `json:"name"`
	Description   string                   `json:"description"`
	EntityTypes   []PlaygroundEntityType   `json:"entityTypes"`
	Relationships []PlaygroundRelationship `json:"relationships"`
}

// ToPlayground projects owl/rdfs classes and object properties into Playground JSON.
func (d *Document) ToPlayground() PlaygroundOntology {
	out := PlaygroundOntology{
		Name:          d.Name,
		Description:   d.Description,
		EntityTypes:   []PlaygroundEntityType{},
		Relationships: []PlaygroundRelationship{},
	}
	if d == nil || d.g == nil {
		return out
	}

	classIRIs := d.classIRIs()
	sort.Strings(classIRIs)
	idByIRI := map[string]string{}
	usedIDs := map[string]struct{}{}
	for i, iri := range classIRIs {
		id := localName(iri)
		if id == "" {
			id = fmt.Sprintf("class_%d", i)
		}
		base := id
		for n := 2; ; n++ {
			if _, taken := usedIDs[id]; !taken {
				break
			}
			id = fmt.Sprintf("%s_%d", base, n)
		}
		usedIDs[id] = struct{}{}
		idByIRI[iri] = id
		out.EntityTypes = append(out.EntityTypes, PlaygroundEntityType{
			ID:          id,
			Name:        d.labelOf(iri, id),
			Description: d.commentOf(iri),
			Properties:  []PlaygroundProperty{},
			Icon:        "◇",
			Color:       entityColors[i%len(entityColors)],
		})
	}

	// subclass edges
	subP := rdflibgo.RDFS.SubClassOf
	for _, iri := range classIRIs {
		for parent := range d.g.Objects(rdflibgo.NewURIRefUnsafe(iri), &subP) {
			parentIRI := termIRI(parent)
			if parentIRI == "" || idByIRI[parentIRI] == "" {
				continue
			}
			fromID := idByIRI[iri]
			toID := idByIRI[parentIRI]
			out.Relationships = append(out.Relationships, PlaygroundRelationship{
				ID:          fromID + "__subclass__" + toID,
				Name:        "子类于",
				From:        fromID,
				To:          toID,
				Cardinality: "many-to-one",
				Kind:        "subclass",
				Description: "子类关系",
			})
		}
	}

	// object properties → association edges (domain → range)
	for _, propIRI := range d.objectPropertyIRIs() {
		domains := d.objectsOf(propIRI, rdflibgo.RDFS.Domain)
		ranges := d.objectsOf(propIRI, rdflibgo.RDFS.Range)
		if len(domains) == 0 || len(ranges) == 0 {
			continue
		}
		propID := localName(propIRI)
		for _, dom := range domains {
			for _, rng := range ranges {
				fromID, okFrom := idByIRI[dom]
				toID, okTo := idByIRI[rng]
				if !okFrom || !okTo {
					continue
				}
				out.Relationships = append(out.Relationships, PlaygroundRelationship{
					ID:          propID + "__" + fromID + "__" + toID,
					Name:        d.labelOf(propIRI, propID),
					From:        fromID,
					To:          toID,
					Cardinality: "many-to-many",
					Kind:        "association",
					Description: d.commentOf(propIRI),
				})
			}
		}
	}

	return out
}

func (d *Document) classIRIs() []string {
	seen := map[string]struct{}{}
	typeP := rdflibgo.RDF.Type
	for s := range d.g.Subjects(&typeP, rdflibgo.OWL.Class) {
		if iri := termIRI(s); iri != "" {
			seen[iri] = struct{}{}
		}
	}
	for s := range d.g.Subjects(&typeP, rdflibgo.RDFS.Class) {
		if iri := termIRI(s); iri != "" {
			seen[iri] = struct{}{}
		}
	}
	subP := rdflibgo.RDFS.SubClassOf
	for s, o := range d.g.SubjectObjects(&subP) {
		if iri := termIRI(s); iri != "" {
			seen[iri] = struct{}{}
		}
		if iri := termIRI(o); iri != "" {
			seen[iri] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for iri := range seen {
		out = append(out, iri)
	}
	return out
}

func (d *Document) objectPropertyIRIs() []string {
	seen := map[string]struct{}{}
	typeP := rdflibgo.RDF.Type
	for s := range d.g.Subjects(&typeP, rdflibgo.OWL.ObjectProperty) {
		if iri := termIRI(s); iri != "" {
			seen[iri] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for iri := range seen {
		out = append(out, iri)
	}
	sort.Strings(out)
	return out
}

func (d *Document) objectsOf(subjectIRI string, pred rdflibgo.URIRef) []string {
	var out []string
	for o := range d.g.Objects(rdflibgo.NewURIRefUnsafe(subjectIRI), &pred) {
		if iri := termIRI(o); iri != "" {
			out = append(out, iri)
		}
	}
	return out
}

func (d *Document) labelOf(iri, fallback string) string {
	labelP := rdflibgo.RDFS.Label
	for o := range d.g.Objects(rdflibgo.NewURIRefUnsafe(iri), &labelP) {
		if lit, ok := o.(rdflibgo.Literal); ok {
			v := strings.TrimSpace(lit.Lexical())
			if v != "" {
				return v
			}
		}
	}
	return fallback
}

func (d *Document) commentOf(iri string) string {
	commentP := rdflibgo.RDFS.Comment
	for o := range d.g.Objects(rdflibgo.NewURIRefUnsafe(iri), &commentP) {
		if lit, ok := o.(rdflibgo.Literal); ok {
			return strings.TrimSpace(lit.Lexical())
		}
	}
	return ""
}

func termIRI(t rdflibgo.Term) string {
	u, ok := t.(rdflibgo.URIRef)
	if !ok {
		return ""
	}
	return u.Value()
}

func localName(iri string) string {
	for i := len(iri) - 1; i >= 0; i-- {
		if iri[i] == '#' || iri[i] == '/' {
			if i+1 < len(iri) {
				return iri[i+1:]
			}
			return iri
		}
	}
	return iri
}
