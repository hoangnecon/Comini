package tests

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"comini/internal/domain"
	"comini/internal/schema"
	"comini/internal/session"
	"comini/internal/streaming"
)

func TestMCPToolFullLifecycle(t *testing.T) {
	// 1. Tool Compilation for MCP tools (namespaced and standalone)
	mcpTools := []domain.ToolDefinition{
		{
			Type: "namespace",
			Name: "scholar_mcp",
			Tools: []domain.ToolDefinition{
				{
					Type:        "function",
					Name:        "search_google_scholar",
					Description: "Search Google Scholar for academic papers",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"query": map[string]any{
								"type":        "string",
								"description": "Search keyword",
							},
						},
						"required": []any{"query"},
					},
				},
			},
		},
		{
			Type:        "tool_search",
			Description: "Search deferred MCP tools",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
				},
				"required": []any{"query"},
			},
		},
	}

	decls, metaMap := schema.ExtractAndCompileTools(mcpTools)
	if len(decls) != 2 {
		t.Fatalf("expected 2 compiled function declarations, got %d", len(decls))
	}

	// Verify scholar_mcp:search_google_scholar
	foundScholar := false
	for _, d := range decls {
		if d["name"] == "scholar_mcp:search_google_scholar" {
			foundScholar = true
			break
		}
	}
	if !foundScholar {
		t.Fatalf("missing compiled scholar_mcp:search_google_scholar declaration")
	}

	// 2. Stream Mapping: When Gemini emits an MCP function call
	mockGeminiSSE := `data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"scholar_mcp:search_google_scholar","args":{"query":"quantum computing"}}}]}}]}}
`
	store := session.NewSignatureStore(time.Hour)
	var out bytes.Buffer

	err := streaming.MapGeminiStreamToResponses(
		strings.NewReader(mockGeminiSSE),
		&out,
		nil,
		store,
		"resp_mcp_test",
		"google-antigravity/gemini-3.7-flash",
		metaMap,
		"sess_mcp_test",
	)
	if err != nil {
		t.Fatalf("streaming error: %v", err)
	}

	respStr := out.String()
	if !strings.Contains(respStr, `"name":"search_google_scholar"`) {
		t.Fatalf("expected name to be search_google_scholar, got: %s", respStr)
	}
	if !strings.Contains(respStr, `"namespace":"scholar_mcp"`) {
		t.Fatalf("expected namespace to be scholar_mcp, got: %s", respStr)
	}
	if !strings.Contains(respStr, `\"query\":\"quantum computing\"`) {
		t.Fatalf("missing tool argument query, got: %s", respStr)
	}

	// 3. Turn Builder: When Codex returns an MCP tool output
	mcpHistoryJSON := `[
		{"type":"message","role":"user","content":"Search for quantum papers"},
		{"type":"mcptoolcall","server":"scholar_mcp","tool":"search_google_scholar","arguments":"{\"query\":\"quantum computing\"}","call_id":"call_mcp_1"},
		{"type":"mcptoolcalloutput","call_id":"call_mcp_1","output":"{\"papers\":[{\"title\":\"Quantum Supremacy\",\"year\":2024}]}"}
	]`

	req := domain.ResponsesRequest{
		Model: "google-antigravity/gemini-3.7-flash",
		Tools: mcpTools,
		Input: json.RawMessage(mcpHistoryJSON),
	}

	wireBody := session.BuildGeminiWireBody(&req, "sess_mcp_test", decls, "high", store)
	if len(wireBody.Contents) == 0 {
		t.Fatalf("expected non-empty contents")
	}

	lastContent := wireBody.Contents[len(wireBody.Contents)-1]
	foundObservation := false
	for _, p := range lastContent.Parts {
		if strings.Contains(p.Text, "scholar_mcp:search_google_scholar") && strings.Contains(p.Text, "Quantum Supremacy") {
			foundObservation = true
			break
		}
	}
	if !foundObservation {
		t.Fatalf("MCP tool observation missing from wire body: %+v", lastContent)
	}
}

func TestDeferredMCPToolSearchAndDynamicInjection(t *testing.T) {
	// Turn 1: Initial request with tool_search
	initialTools := []domain.ToolDefinition{
		{
			Type:        "tool_search",
			Description: "Search deferred MCP tools",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
				},
				"required": []any{"query"},
			},
		},
	}

	decls1, _ := schema.ExtractAndCompileTools(initialTools)
	if len(decls1) != 1 || decls1[0]["name"] != "tool_search" {
		t.Fatalf("expected tool_search compiled, got: %+v", decls1)
	}

	// Turn 2: Codex returns tool_search output and injects additional_tools item
	deferredToolItem := domain.InputItem{
		Type: "additional_tools",
		Tools: []domain.ToolDefinition{
			{
				Type: "namespace",
				Name: "golab",
				Tools: []domain.ToolDefinition{
					{
						Type:        "function",
						Name:        "add_code_cell",
						Description: "Add a new Python code cell to the active notebook",
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"code": map[string]any{"type": "string"},
							},
							"required": []any{"code"},
						},
					},
				},
			},
		},
	}

	allTools := append(initialTools, deferredToolItem.Tools...)
	decls2, metaMap2 := schema.ExtractAndCompileTools(allTools)
	if len(decls2) != 2 {
		t.Fatalf("expected 2 declarations after additional_tools, got %d", len(decls2))
	}

	meta, ok := metaMap2["golab:add_code_cell"]
	if !ok || meta.Namespace != "golab" || meta.OriginalName != "add_code_cell" {
		t.Fatalf("expected golab metadata, got: %+v", meta)
	}
}

