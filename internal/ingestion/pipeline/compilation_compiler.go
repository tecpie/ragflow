package pipeline

import (
	"encoding/json"
	"strings"
)

const (
	compilationGroupKey     = "compilation_template_group_id"
	compilationCompilerID   = "Compiler:KnowledgeCompile"
	compilationCompilerName = "Compiler"
)

// IsCompilationGroupKey reports whether key is the dataset-level compilation
// template group selector. Builtin pipelines have no Compiler node; the parse
// run reads this key and attaches one.
func IsCompilationGroupKey(key string) bool {
	return key == compilationGroupKey || key == "compilation_template_group_ids"
}

// AttachCompilationCompiler inserts a Compiler node into dsl when parser
// configs select compilation template groups and the pipeline has no Compiler
// yet. configs are checked in order; the first map that contains the group key
// wins, including an explicit empty selection. The returned DSL keeps the
// original canvas envelope.
func AttachCompilationCompiler(dsl string, configs ...map[string]any) (string, error) {
	raw, found := firstCompilationGroups(configs...)
	if !found {
		return dsl, nil
	}
	groupIDs := compilationGroupIDs(raw)
	if len(groupIDs) == 0 {
		return dsl, nil
	}

	var top map[string]any
	if err := json.Unmarshal([]byte(dsl), &top); err != nil {
		return "", err
	}
	inner := top
	enveloped := false
	if env, ok := top["dsl"].(map[string]any); ok && len(env) > 0 {
		inner = env
		enveloped = true
	}
	components, _ := inner["components"].(map[string]any)
	if len(components) == 0 {
		return dsl, nil
	}
	exists, changed := ensureExistingCompiler(components, groupIDs, llmIDFrom(configs...))
	if exists {
		if !changed {
			return dsl, nil
		}
		return marshalDSL(top, inner, enveloped)
	}
	if !insertCompilationCompiler(components, inner, groupIDs, llmIDFrom(configs...)) {
		return dsl, nil
	}
	return marshalDSL(top, inner, enveloped)
}

func firstCompilationGroups(configs ...map[string]any) (any, bool) {
	for _, cfg := range configs {
		if cfg == nil {
			continue
		}
		if raw, ok := cfg[compilationGroupKey]; ok {
			return raw, true
		}
		if raw, ok := cfg["compilation_template_group_ids"]; ok {
			return raw, true
		}
	}
	return nil, false
}

func compilationGroupIDs(raw any) []string {
	switch v := raw.(type) {
	case string:
		if s := strings.TrimSpace(v); s != "" {
			return []string{s}
		}
	case []string:
		return nonEmpty(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func llmIDFrom(configs ...map[string]any) string {
	for _, cfg := range configs {
		if cfg == nil {
			continue
		}
		if s, ok := cfg["llm_id"].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func compilerParams(groupIDs []string, llmID string) map[string]any {
	params := map[string]any{}
	if len(groupIDs) == 1 {
		params[compilationGroupKey] = groupIDs[0]
	} else {
		params[compilationGroupKey] = groupIDs
	}
	if llmID != "" {
		params["llm_id"] = llmID
	}
	return params
}

func isCompilerName(name string) bool {
	return strings.EqualFold(name, compilationCompilerName) || strings.EqualFold(name, "KnowledgeCompiler")
}

// ensureExistingCompiler fills an empty Compiler node's group selection.
// exists reports that a Compiler node is already in the pipeline.
func ensureExistingCompiler(components map[string]any, groupIDs []string, llmID string) (exists, changed bool) {
	for _, raw := range components {
		comp, _ := raw.(map[string]any)
		if comp == nil || !isCompilerName(componentName(comp)) {
			continue
		}
		obj, _ := comp["obj"].(map[string]any)
		if obj == nil {
			return true, false
		}
		params, _ := obj["params"].(map[string]any)
		if params == nil {
			params = map[string]any{}
			obj["params"] = params
		}
		if len(compilationGroupIDs(params[compilationGroupKey])) == 0 &&
			len(compilationGroupIDs(params["compilation_template_group_ids"])) == 0 {
			if id, _ := params["compilation_template_id"].(string); strings.TrimSpace(id) == "" {
				for k, v := range compilerParams(groupIDs, llmID) {
					params[k] = v
				}
				changed = true
			}
		}
		return true, changed
	}
	return false, false
}

func insertCompilationCompiler(components map[string]any, inner map[string]any, groupIDs []string, llmID string) bool {
	tokenizerID := ""
	for id, raw := range components {
		comp, _ := raw.(map[string]any)
		if comp != nil && strings.EqualFold(componentName(comp), "Tokenizer") {
			tokenizerID = id
			break
		}
	}
	upstream := make([]string, 0, 1)
	if tokenizerID != "" {
		for id, raw := range components {
			comp, _ := raw.(map[string]any)
			if comp == nil || id == tokenizerID {
				continue
			}
			down := stringList(comp["downstream"])
			rewired := false
			for i, next := range down {
				if next == tokenizerID {
					down[i] = compilationCompilerID
					rewired = true
				}
			}
			if rewired {
				comp["downstream"] = toAnySlice(down)
				upstream = append(upstream, id)
			}
		}
		if len(upstream) == 0 {
			return false
		}
		tok, _ := components[tokenizerID].(map[string]any)
		if tok != nil {
			tok["upstream"] = []any{compilationCompilerID}
		}
		rewireGraphEdge(inner, upstream, tokenizerID)
	} else {
		for id, raw := range components {
			comp, _ := raw.(map[string]any)
			if comp == nil || id == "File" {
				continue
			}
			if len(stringList(comp["downstream"])) == 0 {
				upstream = append(upstream, id)
			}
		}
		if len(upstream) != 1 {
			return false
		}
		comp := components[upstream[0]].(map[string]any)
		comp["downstream"] = []any{compilationCompilerID}
		rewireGraphEdge(inner, upstream, "")
	}

	components[compilationCompilerID] = map[string]any{
		"obj": map[string]any{
			"component_name": compilationCompilerName,
			"params":         compilerParams(groupIDs, llmID),
		},
		"upstream":   toAnySlice(upstream),
		"downstream": tokenizerDownstream(tokenizerID),
	}
	return true
}

func tokenizerDownstream(tokenizerID string) []any {
	if tokenizerID == "" {
		return []any{}
	}
	return []any{tokenizerID}
}

func rewireGraphEdge(inner map[string]any, upstream []string, tokenizerID string) {
	graph, _ := inner["graph"].(map[string]any)
	if graph == nil {
		return
	}
	edges, _ := graph["edges"].([]any)
	if edges == nil {
		return
	}
	from := map[string]bool{}
	for _, id := range upstream {
		from[id] = true
	}
	for _, raw := range edges {
		edge, _ := raw.(map[string]any)
		if edge == nil || !from[stringValue(edge["source"])] {
			continue
		}
		target := stringValue(edge["target"])
		if tokenizerID != "" && target != tokenizerID {
			continue
		}
		edge["target"] = compilationCompilerID
	}
	if tokenizerID == "" {
		return
	}
	graph["edges"] = append(edges, map[string]any{
		"source": compilationCompilerID,
		"target": tokenizerID,
	})
}

func stringList(v any) []string {
	switch t := v.(type) {
	case []string:
		return append([]string(nil), t...)
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func toAnySlice(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func marshalDSL(top, inner map[string]any, enveloped bool) (string, error) {
	var encoded []byte
	var err error
	if enveloped {
		top["dsl"] = inner
		encoded, err = json.Marshal(top)
	} else {
		encoded, err = json.Marshal(inner)
	}
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
