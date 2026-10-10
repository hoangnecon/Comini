package tests

import (
	"testing"
	"comini/internal/domain"
	"comini/internal/schema"
)

func TestCompileGeminiFunctionDeclaration(t *testing.T) {
	tool := domain.ToolDefinition{
		Type:        "function",
		Name:        "edit_code",
		Description: "Edits code in file",
		Parameters: map[string]any{
			"type": "object",
			"required": []any{"path", "content"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "pattern": "^/.*"},
				"content": map[string]any{"type": "string"},
				"mode": map[string]any{
					"anyOf": []any{
						map[string]any{"type": "string", "enum": []any{"replace", "insert"}},
						map[string]any{"type": "null"},
					},
				},
			},
			"additionalProperties": false,
		},
	}

	compiled := schema.CompileGeminiFunctionDeclaration(tool, "edit_code")
	if compiled["name"] != "edit_code" {
		t.Fatalf("expected name edit_code, got %v", compiled["name"])
	}

	params, ok := compiled["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("expected parameters map, got %T", compiled["parameters"])
	}

	if params["type"] != "OBJECT" {
		t.Fatalf("expected type OBJECT, got %v", params["type"])
	}

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", params["properties"])
	}

	pathProp, _ := props["path"].(map[string]any)
	if pathProp["type"] != "STRING" {
		t.Fatalf("expected path type STRING, got %v", pathProp["type"])
	}
	if _, hasPattern := pathProp["pattern"]; hasPattern {
		t.Fatalf("pattern should be stripped")
	}

	modeProp, _ := props["mode"].(map[string]any)
	if modeProp["type"] != "STRING" {
		t.Fatalf("expected mode type STRING, got %v", modeProp["type"])
	}
}

func TestExtractAndCompileTools(t *testing.T) {
	tools := []domain.ToolDefinition{
		{
			Type: "namespace",
			Name: "documents@openai-primary-runtime",
			Tools: []domain.ToolDefinition{
				{
					Type:        "function",
					Name:        "read_doc",
					Description: "Reads document",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"doc_id": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
		{
			Type:        "custom",
			Name:        "apply_patch",
			Description: "Apply unified diff patch",
		},
	}

	decls, metaMap := schema.ExtractAndCompileTools(tools)
	if len(decls) != 2 {
		t.Fatalf("expected 2 declarations, got %d", len(decls))
	}

	// Check apply_patch
	patchMeta, hasPatch := metaMap["apply_patch"]
	if !hasPatch || !patchMeta.IsCustom {
		t.Fatalf("apply_patch should be in metaMap with IsCustom=true")
	}

	// Check namespaced tool
	nsName := schema.SanitizeFunctionName("documents@openai-primary-runtime:read_doc")
	nsMeta, hasNS := metaMap[nsName]
	if !hasNS || nsMeta.OriginalName != "read_doc" {
		t.Fatalf("namespaced tool should be sanitized and mapped, got %+v", nsMeta)
	}

	// Check deduplication when duplicate tools are passed
	dupTools := append(tools, tools...)
	declsDup, _ := schema.ExtractAndCompileTools(dupTools)
	if len(declsDup) != 2 {
		t.Fatalf("expected 2 unique declarations after passing duplicates, got %d", len(declsDup))
	}
}

func TestSanitizeFunctionName(t *testing.T) {
	cases := map[string]string{
		"documents@openai-primary-runtime":   "documents_openai-primary-runtime",
		"scholar_mcp:search_google_scholar":  "scholar_mcp:search_google_scholar",
		"-bad-start":                         "_bad-start",
		"123numeric":                         "_123numeric",
		"plugins.documents/pdf@runtime":      "plugins.documents_pdf_runtime",
	}

	for input, expected := range cases {
		got := schema.SanitizeFunctionName(input)
		if got != expected {
			t.Errorf("SanitizeFunctionName(%q) = %q, expected %q", input, got, expected)
		}
	}
}
