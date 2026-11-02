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

package knowledge_compile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	kccommon "ragflow/internal/ingestion/component/knowledge_compiler/common"

	"gopkg.in/yaml.v3"
)

func TestOntologyBuiltinYAML(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "templates", "ontology.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ontology.yaml: %v", err)
	}
	var doc struct {
		Kind        string         `yaml:"kind"`
		DisplayName string         `yaml:"display_name"`
		Config      map[string]any `yaml:"config"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	if doc.Kind != "ontology" {
		t.Fatalf("kind=%q", doc.Kind)
	}
	if doc.DisplayName == "" || doc.Config == nil {
		t.Fatal("missing display_name or config")
	}
	v, err := kccommon.KindToVariant(doc.Kind)
	if err != nil {
		t.Fatalf("KindToVariant: %v", err)
	}
	if v != kccommon.VariantOntology {
		t.Fatalf("variant=%q, want ontology", v)
	}
	if _, ok := doc.Config["ontology_name"]; !ok {
		t.Fatal("config.ontology_name required")
	}
}
