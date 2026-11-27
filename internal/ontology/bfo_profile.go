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

// BFO / RO IRIs used when projecting workbench function/action types into Turtle
// and Playground. Remote owl:imports of the full BFO is optional; these IRIs
// keep exports Protégé-mergeable and Ontosphere-comparable.
const (
	BFOPURL = "http://purl.obolibrary.org/obo/"

	// BFO_0000034 function ⊂ disposition
	BFOFunction = BFOPURL + "BFO_0000034"
	// BFO_0000015 process (occurrent)
	BFOProcess = BFOPURL + "BFO_0000015"
	// BFO_0000052 inheres in
	BFOInheresIn = BFOPURL + "BFO_0000052"
	// BFO_0000054 realized in
	BFORealizedIn = BFOPURL + "BFO_0000054"

	// RO-style participation roles (stable local aliases under the ontology base
	// when exporting; documented equivalents below).
	RoleInheresIn  = "inheres_in"
	RoleHasAgent   = "has_agent"
	RoleHasPatient = "has_patient"
	RoleHasResult  = "has_result"

	// Playground / workbench edge kinds
	KindInheresIn   = "inheres_in"
	KindHasAgent    = "has_agent"
	KindHasPatient  = "has_patient"
	KindHasResult   = "has_result"
	KindSubclass    = "subclass"
	KindAssociation = "association"

	// Playground icon/color markers for projected function/action types
	IconFunction  = "⚙"
	IconAction    = "▶"
	IconConcept   = "◇"
	ColorFunction = "#5C2D91"
	ColorAction   = "#D83B01"
)

// RoleLabel returns a Chinese display label for a BFO/RO role local name.
func RoleLabel(role string) string {
	switch role {
	case RoleInheresIn:
		return "依存于"
	case RoleHasAgent:
		return "执行者"
	case RoleHasPatient:
		return "作用对象"
	case RoleHasResult:
		return "结果"
	default:
		return role
	}
}
