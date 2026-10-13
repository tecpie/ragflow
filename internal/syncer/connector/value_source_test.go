//
// Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package connector

import (
	"context"
	"errors"
	"testing"

	"ragflow/internal/common"
)

func TestIsQueryableSource(t *testing.T) {
	for _, src := range []string{"mysql", "postgresql", "rest_api", "MySQL"} {
		if !IsQueryableSource(src) {
			t.Errorf("%q should be queryable", src)
		}
	}
	if IsQueryableSource("s3") {
		t.Error("s3 must not be queryable")
	}
}

func TestPrefetchMetadataValueSources(t *testing.T) {
	fields := []common.MetadataFieldDef{
		{Key: "manual", Enum: []string{"a"}},
		{
			Key: "from_conn",
			ValueSource: &common.MetadataValueSource{
				ConnectorID:    "c1",
				EnumValueField: "code",
			},
		},
	}
	err := PrefetchMetadataValueSources(context.Background(), fields, func(_ context.Context, id string) (ConnectorRecord, error) {
		if id != "c1" {
			t.Fatalf("unexpected id %q", id)
		}
		return ConnectorRecord{}, errors.New("not found")
	})
	if err == nil {
		t.Fatal("want error when connector loader fails")
	}

	// No value_source → loader unused, no error.
	plain := []common.MetadataFieldDef{{Key: "x", Type: "string"}}
	if err := PrefetchMetadataValueSources(context.Background(), plain, nil); err != nil {
		t.Fatalf("plain fields: %v", err)
	}
}

func TestResolveResultColumnName(t *testing.T) {
	cols := []string{"Code", "Name"}
	if got := resolveResultColumnName(cols, "code"); got != "Code" {
		t.Fatalf("got %q, want Code", got)
	}
	if got := resolveResultColumnName(cols, "missing"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
