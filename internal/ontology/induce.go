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
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// InducedClass is an owl:Class candidate from knowledge compilation.
type InducedClass struct {
	ID    string
	Label string
}

// InducedProperty is an owl:ObjectProperty candidate with optional domain/range.
type InducedProperty struct {
	ID     string
	Label  string
	Domain string // class local id; empty → omit rdfs:domain
	Range  string // class local id; empty → omit rdfs:range
}

// InducedSchema is the TBox slice induced from structure merge buckets.
type InducedSchema struct {
	Classes    []InducedClass
	Properties []InducedProperty
}

// BuildTurtle serializes an induced schema as Turtle under baseIRI (default namespace).
func BuildTurtle(baseIRI string, schema InducedSchema) string {
	base := strings.TrimSpace(baseIRI)
	if base == "" {
		base = "http://ragflow.local/ontology#"
	}
	if !strings.HasSuffix(base, "#") && !strings.HasSuffix(base, "/") {
		base += "#"
	}

	classes := dedupeClasses(schema.Classes)
	props := dedupeProperties(schema.Properties)

	var b strings.Builder
	b.WriteString("@prefix : <")
	b.WriteString(base)
	b.WriteString("> .\n")
	b.WriteString("@prefix owl: <http://www.w3.org/2002/07/owl#> .\n")
	b.WriteString("@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .\n")
	b.WriteString("\n")

	for _, c := range classes {
		b.WriteString(":")
		b.WriteString(c.ID)
		b.WriteString(" a owl:Class")
		if c.Label != "" && c.Label != c.ID {
			b.WriteString(" ;\n  rdfs:label ")
			b.WriteString(turtleString(c.Label))
		}
		b.WriteString(" .\n\n")
	}
	for _, p := range props {
		b.WriteString(":")
		b.WriteString(p.ID)
		b.WriteString(" a owl:ObjectProperty")
		parts := make([]string, 0, 3)
		if p.Label != "" && p.Label != p.ID {
			parts = append(parts, "  rdfs:label "+turtleString(p.Label))
		}
		if p.Domain != "" {
			parts = append(parts, "  rdfs:domain :"+p.Domain)
		}
		if p.Range != "" {
			parts = append(parts, "  rdfs:range :"+p.Range)
		}
		if len(parts) > 0 {
			b.WriteString(" ;\n")
			b.WriteString(strings.Join(parts, " ;\n"))
		}
		b.WriteString(" .\n\n")
	}
	return b.String()
}

// MergeTurtleSchemas unions existing Turtle classes/properties with induced ones
// and re-serializes. Existing labels win when IDs collide.
func MergeTurtleSchemas(existingTurtle, baseIRI, name, description string, induced InducedSchema) (string, error) {
	merged := induced
	existing := strings.TrimSpace(existingTurtle)
	if existing != "" {
		doc, err := ParseTurtle(existing, name, description, baseIRI)
		if err != nil {
			return "", fmt.Errorf("parse existing turtle: %w", err)
		}
		pg := doc.ToPlayground()
		byClass := map[string]InducedClass{}
		for _, c := range induced.Classes {
			byClass[c.ID] = c
		}
		for _, et := range pg.EntityTypes {
			id := SafeLocalName(et.ID)
			if id == "" {
				continue
			}
			if _, ok := byClass[id]; !ok {
				byClass[id] = InducedClass{ID: id, Label: firstNonEmpty(et.Name, id)}
			} else if et.Name != "" {
				cur := byClass[id]
				if cur.Label == "" || cur.Label == cur.ID {
					cur.Label = et.Name
					byClass[id] = cur
				}
			}
		}
		byProp := map[string]InducedProperty{}
		for _, p := range induced.Properties {
			byProp[p.ID] = p
		}
		for _, r := range pg.Relationships {
			if r.Kind == "subclass" {
				continue
			}
			// Prefer relationship name (property local name); playground IDs are
			// compound (prop__from__to) and must not become class-local ids.
			id := SafeLocalName(r.Name)
			if id == "" {
				id = SafeLocalName(r.ID)
			}
			if id == "" {
				continue
			}
			from := SafeLocalName(r.From)
			to := SafeLocalName(r.To)
			if cur, ok := byProp[id]; ok {
				if cur.Domain == "" {
					cur.Domain = from
				}
				if cur.Range == "" {
					cur.Range = to
				}
				if cur.Label == "" || cur.Label == cur.ID {
					cur.Label = firstNonEmpty(r.Name, id)
				}
				byProp[id] = cur
			} else {
				byProp[id] = InducedProperty{
					ID:     id,
					Label:  firstNonEmpty(r.Name, id),
					Domain: from,
					Range:  to,
				}
			}
			if from != "" {
				if _, ok := byClass[from]; !ok {
					byClass[from] = InducedClass{ID: from, Label: from}
				}
			}
			if to != "" {
				if _, ok := byClass[to]; !ok {
					byClass[to] = InducedClass{ID: to, Label: to}
				}
			}
		}
		merged.Classes = merged.Classes[:0]
		for _, c := range byClass {
			merged.Classes = append(merged.Classes, c)
		}
		merged.Properties = merged.Properties[:0]
		for _, p := range byProp {
			merged.Properties = append(merged.Properties, p)
		}
	}
	out := BuildTurtle(baseIRI, merged)
	if _, err := ParseTurtle(out, name, description, baseIRI); err != nil {
		return "", err
	}
	return out, nil
}

// SafeLocalName turns an arbitrary label into a Turtle local name fragment.
func SafeLocalName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if i == 0 || b.Len() == 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
		case unicode.IsLetter(r):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	if out == "" {
		return ""
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	return out
}

func dedupeClasses(in []InducedClass) []InducedClass {
	by := map[string]InducedClass{}
	for _, c := range in {
		id := SafeLocalName(c.ID)
		if id == "" {
			id = SafeLocalName(c.Label)
		}
		if id == "" {
			continue
		}
		label := strings.TrimSpace(c.Label)
		if label == "" {
			label = id
		}
		if prev, ok := by[id]; ok {
			if prev.Label == prev.ID && label != id {
				prev.Label = label
				by[id] = prev
			}
			continue
		}
		by[id] = InducedClass{ID: id, Label: label}
	}
	out := make([]InducedClass, 0, len(by))
	for _, c := range by {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func dedupeProperties(in []InducedProperty) []InducedProperty {
	by := map[string]InducedProperty{}
	for _, p := range in {
		id := SafeLocalName(p.ID)
		if id == "" {
			id = SafeLocalName(p.Label)
		}
		if id == "" {
			continue
		}
		label := strings.TrimSpace(p.Label)
		if label == "" {
			label = id
		}
		domain := SafeLocalName(p.Domain)
		rng := SafeLocalName(p.Range)
		if prev, ok := by[id]; ok {
			if prev.Domain == "" {
				prev.Domain = domain
			}
			if prev.Range == "" {
				prev.Range = rng
			}
			if prev.Label == prev.ID && label != id {
				prev.Label = label
			}
			by[id] = prev
			continue
		}
		by[id] = InducedProperty{ID: id, Label: label, Domain: domain, Range: rng}
	}
	out := make([]InducedProperty, 0, len(by))
	for _, p := range by {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func turtleString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return `"` + s + `"`
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
