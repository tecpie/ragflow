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

package remote

import (
	"strings"

	"ragflow/internal/entity"
)

// ToEntity maps a remote ontology into the local entity shape used by bind/OAG.
func ToEntity(o *Ontology) *entity.Ontology {
	if o == nil {
		return nil
	}
	row := &entity.Ontology{
		ID:       strings.TrimSpace(o.ID),
		Name:     strings.TrimSpace(o.Name),
		BaseIRI:  strings.TrimSpace(o.BaseIRI),
		Turtle:   o.DraftTurtle,
		Document: o.Document,
		Source:   strings.TrimSpace(o.Source),
	}
	if d := strings.TrimSpace(o.Description); d != "" {
		row.Description = &d
	}
	return row
}
