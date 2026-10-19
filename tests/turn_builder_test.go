package tests

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"comini/internal/domain"
	"comini/internal/harness"
	"comini/internal/session"
	"comini/internal/transport"
)

func TestBuildGeminiWireBody(t *testing.T) {
	store := session.NewSignatureStore(time.Hour)
	store.Set("call_100", "sig_cached_999", "exec_linter")

	inputJSON := `[
		{"type":"message","role":"user","content":"Run linter"},
		{"type":"function_call","name":"exec_linter","arguments":"{\"fix\":true}","call_id":"call_100"},
		{"type":"function_call_output","call_id":"call_100","output":"{\"errors\":0}"},
		{"type":"function_call_output","call_id":"call_101","output":"{\"ok\":true}"}
	]`

	sysPrompt := "You are an expert coder."

	req := domain.ResponsesRequest{
		Model:        "gemini-3.8-flash",
		Instructions: &sysPrompt,
		Input:        json.RawMessage(inputJSON),
	}

	body := session.BuildGeminiWireBody(&req, "sess_abc", nil, "high", store)

	if body.SystemInstruction == nil || len(body.SystemInstruction.Parts) == 0 {
		t.Fatalf("expected system instruction")
	}
	if !strings.Contains(body.SystemInstruction.Parts[0].Text, sysPrompt) {
		t.Fatalf("expected to contain %s, got %s", sysPrompt, body.SystemInstruction.Parts[0].Text)
	}

	if len(body.Contents) == 0 {
		t.Fatalf("expected non-empty contents")
	}

	lastTurn := body.Contents[len(body.Contents)-1]
	if lastTurn.Role != "user" {
		t.Fatalf("last turn must be user")
	}

	hasLinterObs := false
	for _, p := range lastTurn.Parts {
		if strings.Contains(p.Text, "exec_linter") || (p.FunctionResponse != nil && p.FunctionResponse.Name == "exec_linter") {
			hasLinterObs = true
			break
		}
	}
	if !hasLinterObs {
		t.Fatalf("expected observation for exec_linter")
	}
}

func TestSignatureStoreTieredLookup(t *testing.T) {
	store := session.NewSignatureStore(time.Hour)
	store.SetSessionCall("sess_xyz", "check_status", map[string]any{"task_id": "t1"}, "sig_sess_999")

	inputJSON := `[
		{"type":"message","role":"user","content":"status?"},
		{"type":"function_call","name":"default_api:check_status","arguments":"{\"task_id\":\"t1\"}","id":"fc_unknown"}
	]`

	req := domain.ResponsesRequest{
		Model: "gemini-3.7-flash",
		Input: json.RawMessage(inputJSON),
	}

	body := session.BuildGeminiWireBody(&req, "sess_xyz", nil, "high", store)
	if len(body.Contents) == 0 {
		t.Fatalf("expected non-empty contents")
	}
}

func TestGeminiPartUnmarshalSnakeCase(t *testing.T) {
	snakeJSON := `{"text":"hello","thought_signature":"sig_snake_123","thought":true}`
	var part domain.GeminiPart
	if err := json.Unmarshal([]byte(snakeJSON), &part); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if part.ThoughtSignature != "sig_snake_123" {
		t.Fatalf("expected sig_snake_123, got %s", part.ThoughtSignature)
	}
	if part.Text != "hello" {
		t.Fatalf("expected hello, got %s", part.Text)
	}
	if !part.Thought {
		t.Fatalf("expected thought=true")
	}
}

func TestExtractImageFromOutput(t *testing.T) {
	store := session.NewSignatureStore(time.Hour)
	store.Set("call_img", "sig_img_123", "view_file")

	inputJSON := `[
		{"type":"message","role":"user","content":"view screenshot"},
		{"type":"function_call","name":"view_file","arguments":"{\"path\":\"vault/screen.png\"}","call_id":"call_img"},
		{"type":"function_call_output","call_id":"call_img","output":"{\"image_url\":\"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==\"}"}
	]`

	req := domain.ResponsesRequest{
		Model: "gemini-3.7-flash",
		Input: json.RawMessage(inputJSON),
	}

	body := session.BuildGeminiWireBody(&req, "sess_img", nil, "high", store)
	if len(body.Contents) == 0 {
		t.Fatalf("expected non-empty contents")
	}

	lastTurn := body.Contents[len(body.Contents)-1]
	hasInline := false
	hasObs := false
	for _, p := range lastTurn.Parts {
		if p.InlineData != nil && p.InlineData.MimeType == "image/png" {
			hasInline = true
		}
		if strings.Contains(p.Text, "view_file") || (p.FunctionResponse != nil && p.FunctionResponse.Name == "view_file") {
			hasObs = true
		}

	}
	if !hasInline {
		t.Fatalf("expected InlineData image part")
	}
	if !hasObs {
		t.Fatalf("expected observation part for view_file")
	}
}

func TestUniversalImageExtractionAnthropicAndMCP(t *testing.T) {
	// 1. Anthropic source format
	anthropicInput := `[
		{"type":"message","role":"user","content":[
			{"type":"text","text":"Analyze this image:"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}}
		]}
	]`

	req1 := domain.ResponsesRequest{
		Model: "gemini-3.7-flash",
		Input: json.RawMessage(anthropicInput),
	}
	body1 := session.BuildGeminiWireBody(&req1, "sess_anthropic", nil, "high", nil)
	if len(body1.Contents) == 0 || len(body1.Contents[0].Parts) < 2 {
		t.Fatalf("expected at least 2 parts for Anthropic image input")
	}
	hasInline := false
	for _, p := range body1.Contents[0].Parts {
		if p.InlineData != nil && p.InlineData.MimeType == "image/png" {
			hasInline = true
			break
		}
	}
	if !hasInline {
		t.Fatalf("expected at least 1 part to be image/png InlineData")
	}

	// 2. MCP tool result format
	store := session.NewSignatureStore(time.Hour)
	store.Set("call_mcp", "sig_mcp_123", "mcp_tool")

	mcpInput := `[
		{"type":"function_call","name":"mcp_tool","arguments":"{}","call_id":"call_mcp"},
		{"type":"function_call_output","call_id":"call_mcp","output":"{\"content\":[{\"type\":\"image\",\"data\":\"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==\",\"mimeType\":\"image/png\"}]}"}
	]`
	req2 := domain.ResponsesRequest{
		Model: "gemini-3.7-flash",
		Input: json.RawMessage(mcpInput),
	}
	body2 := session.BuildGeminiWireBody(&req2, "sess_mcp", nil, "high", store)
	if len(body2.Contents) == 0 {
		t.Fatalf("expected non-empty contents for MCP tool call")
	}
	userTurn := body2.Contents[len(body2.Contents)-1]
	hasMCPInline := false
	for _, p := range userTurn.Parts {
		if p.InlineData != nil && p.InlineData.MimeType == "image/png" {
			hasMCPInline = true
			break
		}
	}
	if !hasMCPInline {
		t.Fatalf("expected part to be InlineData image in MCP user turn")
	}
}

func TestEmbeddedDataURLAndSanitizedArgs(t *testing.T) {
	// Embedded data URL in text prompt
	prompt := `[
		{"type":"message","role":"user","content":"Look at this: data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg== and explain what it is."}
	]`
	req := domain.ResponsesRequest{
		Model: "gemini-3.7-flash",
		Input: json.RawMessage(prompt),
	}
	body := session.BuildGeminiWireBody(&req, "sess_emb", nil, "high", nil)
	if len(body.Contents) == 0 || len(body.Contents[0].Parts) < 2 {
		t.Fatalf("expected InlineData + Text parts")
	}
	if body.Contents[0].Parts[0].InlineData == nil {
		t.Fatalf("expected inline data")
	}
	if strings.Contains(body.Contents[0].Parts[1].Text, "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJ") {
		t.Fatalf("text part still contains raw base64 string!")
	}
}

func TestEmbeddedLocalFilePathExtraction(t *testing.T) {
	// Create temporary 1x1 png file
	tmpFile, err := os.CreateTemp("", "test_img_*.png")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	redPngBytes := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE, 0x00, 0x00, 0x00,
		0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xDD, 0x8D, 0xB0, 0x00, 0x00, 0x00,
		0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
	}
	if _, err := tmpFile.Write(redPngBytes); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	_ = tmpFile.Close()

	// Text prompt referencing the local image path inside a sentence
	prompt := fmt.Sprintf(`[
		{"type":"message","role":"user","content":"Please check the screenshot at %s and tell me what color it is."}
	]`, tmpFile.Name())

	req := domain.ResponsesRequest{
		Model: "gemini-3.7-flash",
		Input: json.RawMessage(prompt),
	}

	body := session.BuildGeminiWireBody(&req, "sess_local_img", nil, "high", nil)
	if len(body.Contents) == 0 || len(body.Contents[0].Parts) < 2 {
		t.Fatalf("expected at least 2 parts (InlineData + Text)")
	}

	hasInline := false
	for _, p := range body.Contents[0].Parts {
		if p.InlineData != nil && p.InlineData.MimeType == "image/png" {
			hasInline = true
			break
		}
	}
	if !hasInline {
		t.Fatalf("failed to extract local image into InlineData")
	}
}

func TestContextBudgetPreservesUserPrompts(t *testing.T) {
	// Build a simulated long session with 250 tool execution cycles and a critical user prompt in the middle
	store := session.NewSignatureStore(time.Hour)
	var items []map[string]any

	// 1. Initial user turn
	items = append(items, map[string]any{
		"type": "message", "role": "user", "content": "Initialize project repo CASHAA",
	})

	// 2. 50 tool executions
	for i := 1; i <= 50; i++ {
		callID := fmt.Sprintf("call_%d", i)
		store.Set(callID, fmt.Sprintf("sig_%d", i), "exec_cmd")
		items = append(items, map[string]any{
			"type": "function_call", "name": "exec_cmd", "arguments": `{"cmd":"ls"}`, "call_id": callID,
		})
		items = append(items, map[string]any{
			"type": "function_call_output", "call_id": callID, "output": strings.Repeat("a", 3000),
		})
	}

	// 3. User steering prompt in the middle
	const criticalUserPrompt = "Sửa lỗi lưu đơn hàng CASHAA và tích hợp Tab giao diện mới"
	items = append(items, map[string]any{
		"type": "message", "role": "user", "content": criticalUserPrompt,
	})

	// 4. 200 more tool executions
	for i := 51; i <= 250; i++ {
		callID := fmt.Sprintf("call_%d", i)
		store.Set(callID, fmt.Sprintf("sig_%d", i), "exec_cmd")
		items = append(items, map[string]any{
			"type": "function_call", "name": "exec_cmd", "arguments": `{"cmd":"read"}`, "call_id": callID,
		})
		items = append(items, map[string]any{
			"type": "function_call_output", "call_id": callID, "output": strings.Repeat("b", 3000),
		})
	}

	inputBytes, _ := json.Marshal(items)
	req := domain.ResponsesRequest{
		Model: "gemini-3.7-flash",
		Input: inputBytes,
	}

	body := session.BuildGeminiWireBody(&req, "sess_long", nil, "high", store)

	// Check that the critical prompt is 100% preserved in the contents!
	foundCriticalPrompt := false
	for _, c := range body.Contents {
		for _, p := range c.Parts {
			if strings.Contains(p.Text, criticalUserPrompt) {
				foundCriticalPrompt = true
				break
			}
		}
		if foundCriticalPrompt {
			break
		}
	}

	if !foundCriticalPrompt {
		t.Fatalf("CRITICAL ERROR: User instruction '%s' was pruned from context!", criticalUserPrompt)
	}
}

func TestCodexPascalCaseAndNovelItemTypes(t *testing.T) {
	inputJSON := `[
		{"type":"retained_context","payload":{"type":"verified_answer","questions":[{"question":"Which database to use?","answer":"PostgreSQL 16"}]}},
		{"type":"Plan","text":"1. Set up schemas\n2. Implement API\n3. Run migrations"},
		{"type":"FileChange","payload":{"stdout":"M internal/db/schema.sql\nA internal/db/queries.sql"}},
		{"type":"thread_goal_updated","goal":"Build order management service for CASHAA"},
		{"type":"world_state","payload":{"state":{"collaboration_mode":{"mode":"pair_programming"}}}},
		{"type":"CommandExecution","command":"go test ./...","aggregated_output":"PASS\nok comini/db 0.05s"},
		{"type":"AgentMessage","text":"I have verified all database migrations and the tests pass."}
	]`

	req := domain.ResponsesRequest{
		Model: "gemini-3.7-flash",
		Input: json.RawMessage(inputJSON),
	}

	body := session.BuildGeminiWireBody(&req, "sess_pascal_novel", nil, "high", nil)

	var allTexts []string
	for _, c := range body.Contents {
		for _, p := range c.Parts {
			if p.Text != "" {
				allTexts = append(allTexts, p.Text)
			}
		}
	}
	combinedText := strings.Join(allTexts, "\n---\n")

	// 1. Check retained_context / verified_answer
	if !strings.Contains(combinedText, "Which database to use?") || !strings.Contains(combinedText, "PostgreSQL 16") {
		t.Fatalf("retained_context questions/answers missing from context: %s", combinedText)
	}

	// 2. Check Plan
	if !strings.Contains(combinedText, "[Active Technical Plan]") || !strings.Contains(combinedText, "1. Set up schemas") {
		t.Fatalf("Plan missing from context: %s", combinedText)
	}

	// 3. Check FileChange
	if !strings.Contains(combinedText, "[Workspace File Changes]") || !strings.Contains(combinedText, "internal/db/schema.sql") {
		t.Fatalf("FileChange missing from context: %s", combinedText)
	}

	// 4. Check thread_goal_updated
	if !strings.Contains(combinedText, "[Updated Thread Goal]") || !strings.Contains(combinedText, "Build order management service for CASHAA") {
		t.Fatalf("thread_goal_updated missing from context: %s", combinedText)
	}

	// 5. Check world_state
	if !strings.Contains(combinedText, "[Active Collaboration Mode: pair_programming]") {
		t.Fatalf("world_state missing from context: %s", combinedText)
	}

	// 6. Check CommandExecution
	if !strings.Contains(combinedText, "[Observation: command go test ./... executed -> output: PASS\nok comini/db 0.05s]") {
		t.Fatalf("CommandExecution missing or corrupted: %s", combinedText)
	}

	// 7. Check AgentMessage
	if !strings.Contains(combinedText, "I have verified all database migrations and the tests pass.") {
		t.Fatalf("AgentMessage missing: %s", combinedText)
	}
}

func TestHarmoneySkillAndPlanPreservation(t *testing.T) {
	sysPrompt := `# Agent operating contract
1. Always state plan and announce skill usage.
2. Read before you act.`

	inputJSON := `[
		{"type":"message","role":"user","content":"Implement auth module in Harmoney"},
		{"type":"Plan","text":"### Plan\n1. Analyze JWT spec\n2. Write unit tests\n3. Implement handler"},
		{"type":"CommandExecution","command":"ls -la ./auth","stdout":"auth.go\nauth_test.go"}
	]`

	req := domain.ResponsesRequest{
		Model:        "gemini-3.7-flash",
		Instructions: &sysPrompt,
		Input:        json.RawMessage(inputJSON),
	}

	body := session.BuildGeminiWireBody(&req, "sess_harmoney", nil, "high", nil)

	if body.SystemInstruction == nil || len(body.SystemInstruction.Parts) == 0 {
		t.Fatalf("expected system instruction")
	}
	sysText := body.SystemInstruction.Parts[0].Text
	if !strings.Contains(sysText, "Always state plan and announce skill usage.") {
		t.Fatalf("harmoney system instruction missing original text: %s", sysText)
	}
	if strings.Contains(sysText, "Be extremely concise") || strings.Contains(sysText, "Do not announce skills") {
		t.Fatalf("found unwanted brevity override in system prompt")
	}

	var foundPlan bool
	for _, c := range body.Contents {
		for _, p := range c.Parts {
			if strings.Contains(p.Text, "### Plan\n1. Analyze JWT spec") {
				foundPlan = true
			}
		}
	}
	if !foundPlan {
		t.Fatalf("Harmoney technical plan was not preserved in contents!")
	}
}

func TestSanitizeSystemInstructionCodexAndClaude(t *testing.T) {
	rawPrompt := `x-anthropic-billing-header: test-header
You are Codex, a coding agent based on GPT-5.
You are a Claude agent, built on Anthropic's Claude Agent SDK.

Follow these coding guidelines:
1. Always write unit tests.
2. Maintain clean architecture.`

	req := domain.ResponsesRequest{
		Model:        "gemini-3.7-flash",
		Instructions: &rawPrompt,
		Input:        json.RawMessage(`[{"type":"message","role":"user","content":"hi"}]`),
	}

	body := session.BuildGeminiWireBody(&req, "sess_clean", nil, "high", nil)
	if body.SystemInstruction == nil || len(body.SystemInstruction.Parts) == 0 {
		t.Fatalf("expected non-nil system instruction")
	}

	res := body.SystemInstruction.Parts[0].Text
	if strings.Contains(res, "x-anthropic-billing-header") {
		t.Errorf("billing header was not removed")
	}
	if strings.Contains(res, "Claude Agent SDK") {
		t.Errorf("Claude Agent SDK text was not removed")
	}
	if !strings.Contains(res, "You are an expert AI software engineering assistant.") {
		t.Errorf("Codex system instruction was not properly sanitized to neutral identity")
	}
	if !strings.Contains(res, "Follow these coding guidelines:") {
		t.Errorf("User guidelines were accidentally stripped")
	}
}

func TestPolymorphicSummaryAndInputDecoding(t *testing.T) {
	rawInput := `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"Turn 1"}]},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"Reasoning summary"}],"content":[{"type":"reasoning_text","text":"Reasoning detail"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Answer 1"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"Turn 2"}]}
	]`

	var req domain.ResponsesRequest
	req.Model = "gemini-3.7-flash"
	req.Input = json.RawMessage(rawInput)

	var items []domain.InputItem
	if err := json.Unmarshal(req.Input, &items); err != nil {
		t.Fatalf("failed to unmarshal input with array summary: %v", err)
	}

	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}

	body := session.BuildGeminiWireBody(&req, "sess_poly", nil, "high", nil)
	if len(body.Contents) != 3 {
		t.Fatalf("expected 3 turns (user, model, user), got %d", len(body.Contents))
	}

	if body.Contents[0].Role != "user" || body.Contents[0].Parts[0].Text != "Turn 1" {
		t.Errorf("turn 0 mismatch: %+v", body.Contents[0])
	}
	if body.Contents[1].Role != "model" || body.Contents[1].Parts[0].Text != "Answer 1" {
		t.Errorf("turn 1 mismatch: %+v", body.Contents[1])
	}
	if body.Contents[2].Role != "user" || body.Contents[2].Parts[0].Text != "Turn 2" {
		t.Errorf("turn 2 mismatch: %+v", body.Contents[2])
	}
}

func TestToolChoiceMapping(t *testing.T) {
	funcDecls := []map[string]any{
		{"name": "exec_cmd", "description": "run cmd"},
	}

	// 1. Tool choice "required" -> mode "ANY"
	req1 := domain.ResponsesRequest{
		Model:      "gemini-3.7-flash",
		ToolChoice: "required",
		Input:      json.RawMessage(`[{"type":"message","role":"user","content":"hi"}]`),
	}
	body1 := session.BuildGeminiWireBody(&req1, "sess_tc1", funcDecls, "high", nil)
	if body1.ToolConfig == nil || body1.ToolConfig.FunctionCallingConfig == nil {
		t.Fatalf("expected toolConfig for required tool choice")
	}
	if body1.ToolConfig.FunctionCallingConfig.Mode != "ANY" {
		t.Errorf("expected ANY, got %s", body1.ToolConfig.FunctionCallingConfig.Mode)
	}

	// 2. Specific function choice -> mode "ANY" with allowedFunctionNames
	req2 := domain.ResponsesRequest{
		Model: "gemini-3.7-flash",
		ToolChoice: map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "default_api:exec_cmd",
			},
		},
		Input: json.RawMessage(`[{"type":"message","role":"user","content":"hi"}]`),
	}
	body2 := session.BuildGeminiWireBody(&req2, "sess_tc2", funcDecls, "high", nil)
	if body2.ToolConfig == nil || body2.ToolConfig.FunctionCallingConfig == nil {
		t.Fatalf("expected toolConfig for specific function choice")
	}
	cfg := body2.ToolConfig.FunctionCallingConfig
	if cfg.Mode != "ANY" {
		t.Errorf("expected ANY, got %s", cfg.Mode)
	}
	if len(cfg.AllowedFunctionNames) == 0 || cfg.AllowedFunctionNames[0] != "default_api:exec_cmd" {
		t.Errorf("expected default_api:exec_cmd in allowedFunctionNames, got %+v", cfg.AllowedFunctionNames)
	}
}

func TestMemoryCitationPreservedUnderBudgetPruning(t *testing.T) {
	store := session.NewSignatureStore(time.Hour)
	var items []map[string]any

	// 1. Turn 0: User setup with Memory Citation
	const memCitation = `<oai-mem-citation>
<citation_entries>
MEMORY.md:12-15|note=[project arch guidelines]
</citation_entries>
<rollout_ids>
019c6e27-e55b-73d1-87d8-4e01f1f75043
</rollout_ids>
</oai-mem-citation>`

	items = append(items, map[string]any{
		"type": "message", "role": "user", "content": "Start project setup\n\n" + memCitation,
	})

	// 2. 60 tool executions
	for i := 1; i <= 60; i++ {
		callID := fmt.Sprintf("call_%d", i)
		store.Set(callID, fmt.Sprintf("sig_%d", i), "exec_cmd")
		items = append(items, map[string]any{
			"type": "function_call", "name": "exec_cmd", "arguments": `{"cmd":"ls"}`, "call_id": callID,
		})
		items = append(items, map[string]any{
			"type": "function_call_output", "call_id": callID, "output": strings.Repeat("x", 4000),
		})
	}

	// 3. User middle message with memory citation
	items = append(items, map[string]any{
		"type": "message", "role": "user", "content": "Please check memory citation:\n" + memCitation,
	})

	// 4. 200 more tool executions
	for i := 61; i <= 260; i++ {
		callID := fmt.Sprintf("call_%d", i)
		store.Set(callID, fmt.Sprintf("sig_%d", i), "exec_cmd")
		items = append(items, map[string]any{
			"type": "function_call", "name": "exec_cmd", "arguments": `{"cmd":"git diff"}`, "call_id": callID,
		})
		items = append(items, map[string]any{
			"type": "function_call_output", "call_id": callID, "output": strings.Repeat("y", 4000),
		})
	}

	inputBytes, _ := json.Marshal(items)
	req := domain.ResponsesRequest{
		Model: "gemini-3.7-flash",
		Input: inputBytes,
	}

	body := session.BuildGeminiWireBody(&req, "sess_mem_pruning", nil, "high", store)

	foundCitationCount := 0
	for _, c := range body.Contents {
		for _, p := range c.Parts {
			if strings.Contains(p.Text, "019c6e27-e55b-73d1-87d8-4e01f1f75043") {
				foundCitationCount++
			}
		}
	}

	if foundCitationCount < 2 {
		t.Fatalf("expected memory citation in both user turns to be preserved, found in %d turns", foundCitationCount)
	}
}

func TestLiveCCAPromptRejection(t *testing.T) {
	if os.Getenv("COMINI_LIVE_TESTS") != "1" {
		t.Skip("Skipping live CCA network test (set COMINI_LIVE_TESTS=1 to run)")
	}
	token, proj, err := transport.DefaultAuthManager.GetCredentials()
	if err != nil || token == "" {
		t.Skip("no credentials")
	}

	client := transport.NewCCAClient("https://cloudcode-pa.googleapis.com", "antigravity")

	// 1. With GPT-5
	bodyWithGPT5 := domain.GeminiWireBody{
		SystemInstruction: &domain.GeminiSystemInstruction{
			Parts: []domain.GeminiPart{{Text: "You are Codex, an agent based on GPT-5. You and the user share one workspace, and your job is to collaborate with them until their goal is genuinely handled."}},
		},
		Contents: []domain.GeminiContent{{Role: "user", Parts: []domain.GeminiPart{{Text: "Hi"}}}},
	}
	resp1, err1 := client.StreamGenerateContent(context.Background(), "gemini-3.8-flash-high", bodyWithGPT5, token, proj)
	if err1 != nil {
		t.Logf("Prompt with GPT-5 -> REJECTED: %v", err1)
	} else {
		resp1.Body.Close()
		t.Logf("Prompt with GPT-5 -> ACCEPTED")
	}

	// 2. With neutral identity
	bodyNeutral := domain.GeminiWireBody{
		SystemInstruction: &domain.GeminiSystemInstruction{
			Parts: []domain.GeminiPart{{Text: "You are an expert AI software engineering assistant. You and the user share one workspace, and your job is to collaborate with them until their goal is genuinely handled."}},
		},
		Contents: []domain.GeminiContent{{Role: "user", Parts: []domain.GeminiPart{{Text: "Hi"}}}},
	}
	resp2, err2 := client.StreamGenerateContent(context.Background(), "gemini-3.8-flash-high", bodyNeutral, token, proj)
	if err2 != nil {
		t.Logf("Prompt with neutral identity -> REJECTED: %v", err2)
	} else if resp2 != nil {
		resp2.Body.Close()
		t.Logf("Prompt with neutral identity -> ACCEPTED")
	}
}

func TestLiveAuthManager(t *testing.T) {
	if os.Getenv("COMINI_LIVE_TESTS") != "1" {
		t.Skip("Skipping live auth test (set COMINI_LIVE_TESTS=1 to run)")
	}
	mgr := transport.NewAuthManager()
	token, proj, err := mgr.GetCredentials()
	st := mgr.Status()
	t.Logf("GetCredentials: tokenLen=%d proj=%s email=%s accounts=%d err=%v", len(token), proj, st.Email, st.TotalAccounts, err)
	if err != nil {
		t.Fatalf("GetCredentials failed: %v", err)
	}
	if token == "" {
		t.Fatalf("empty token returned")
	}
}

func TestAuthManagerMultiAccountPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	authFile := tmpDir + "/comini_auth.json"
	mgr := transport.NewAuthManagerWithPath(authFile)

	exp := time.Now().Add(time.Hour)
	acc1, err := mgr.SaveAccount("dev1@example.com", "ya29.token1", "1//refresh1", "proj-1", "oauth", exp)
	if err != nil {
		t.Fatalf("SaveAccount 1 failed: %v", err)
	}
	acc2, err := mgr.SaveAccount("dev2@example.com", "ya29.token2", "1//refresh2", "proj-2", "oauth", exp)
	if err != nil {
		t.Fatalf("SaveAccount 2 failed: %v", err)
	}

	// Verify active account is acc2
	tok, proj, err := mgr.GetCredentials()
	if err != nil || tok != "ya29.token2" || proj != "proj-2" {
		t.Fatalf("expected acc2 credentials, got tok=%s proj=%s err=%v", tok, proj, err)
	}

	// Switch back to acc1 by email
	switched, err := mgr.SwitchAccount("dev1@example.com")
	if err != nil || switched.ID != acc1.ID {
		t.Fatalf("SwitchAccount failed: %v", err)
	}

	// Reload from disk in a new AuthManager instance to verify persistence
	mgr2 := transport.NewAuthManagerWithPath(authFile)
	tok2, proj2, err := mgr2.GetCredentials()
	if err != nil || tok2 != "ya29.token1" || proj2 != "proj-1" {
		t.Fatalf("expected persisted acc1 credentials after reload, got tok=%s proj=%s err=%v", tok2, proj2, err)
	}

	// Remove acc1 and verify fallback to acc2
	if err := mgr2.RemoveAccount(acc1.ID); err != nil {
		t.Fatalf("RemoveAccount failed: %v", err)
	}
	st := mgr2.Status()
	if st.ActiveAccountID != acc2.ID || st.TotalAccounts != 1 {
		t.Fatalf("expected fallback to acc2 with 1 total account, got %+v", st)
	}
}

func TestSanitizeMessageTextStripsLeakedTags(t *testing.T) {
	raw := `[Generated Image based on prompt: "Cinematic 9:16 interior shot of Prince Dinh Lien in 10th-century royal attire..."][Generated Image based on prompt: "Scene 2". Saved to: /tmp/img.jpg][Observation: tool exec_command executed with {"cmd":"ls"} -> returned: ok][Image omitted from historical context]Tôi đã tạo xong các ảnh theo yêu cầu.`
	cleaned := harness.SanitizeMessageText(raw)

	if strings.Contains(cleaned, "[Generated Image") {
		t.Fatalf("leaked [Generated Image tag was not stripped: %s", cleaned)
	}
	if strings.Contains(cleaned, "[Observation:") {
		t.Fatalf("leaked [Observation tag was not stripped: %s", cleaned)
	}
	if strings.Contains(cleaned, "[Image omitted") {
		t.Fatalf("leaked [Image omitted tag was not stripped: %s", cleaned)
	}
	if !strings.Contains(cleaned, "Tôi đã tạo xong các ảnh theo yêu cầu.") {
		t.Fatalf("legitimate assistant text was lost: %s", cleaned)
	}
}

func TestParseSessionRolloutToGeminiContents_ImageGen(t *testing.T) {
	inputJSON := `[
		{"type":"image_generation_call","id":"igc_123","status":"completed","revised_prompt":"Cinematic 9:16 scene of ancient king","aspect_ratio":"9:16","saved_path":"/tmp/king.jpg"},
		{"type":"message","id":"msg_456","role":"assistant","content":[{"type":"output_text","text":"Ảnh hoàng đế đã hoàn thành."}]}
	]`

	req := domain.ResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: json.RawMessage(inputJSON),
	}

	body := session.BuildGeminiWireBody(&req, "sess_img_test", nil, "high", nil)

	// Verify alternating model Action and user Observation turns
	foundAction := false
	foundObs := false
	for _, c := range body.Contents {
		for _, p := range c.Parts {
			if strings.Contains(p.Text, "[Generated Image based on prompt:") {
				t.Fatalf("prohibited raw prompt dump found in history: %s", p.Text)
			}
			if c.Role == "model" && strings.Contains(p.Text, "[Action: called image_generation with") {
				foundAction = true
				if !strings.Contains(p.Text, "Cinematic 9:16 scene of ancient king") {
					t.Errorf("expected prompt in action args, got: %s", p.Text)
				}
			}
			if c.Role == "user" && strings.Contains(p.Text, "[Observation: tool image_generation succeeded -> saved to: /tmp/king.jpg") {
				foundObs = true
			}
		}
	}

	if !foundAction {
		t.Fatalf("expected structured image_generation action call in model turn")
	}
	if !foundObs {
		t.Fatalf("expected image_generation observation in user turn")
	}
}

func TestPayloadOptimizationAndContextConsistency(t *testing.T) {
	// 1. Verify UnmarshalJSON strips large image data from Result
	largeBase64 := "data:image/jpeg;base64," + strings.Repeat("A", 1000000)
	rawItemJSON := fmt.Sprintf(`{
		"type": "image_generation_call",
		"id": "ig_test_123",
		"status": "completed",
		"revised_prompt": "Cinematic portrait of King Dinh Tien Hoang in golden dragon armor",
		"saved_path": "/path/to/scene_01.png",
		"aspect_ratio": "9:16",
		"result": %q
	}`, largeBase64)

	var item domain.InputItem
	if err := json.Unmarshal([]byte(rawItemJSON), &item); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if item.Result != "" {
		t.Fatalf("expected item.Result to be stripped to empty string, got len=%d", len(item.Result))
	}
	if item.RevisedPrompt != "Cinematic portrait of King Dinh Tien Hoang in golden dragon armor" {
		t.Fatalf("expected revised prompt preserved, got: %s", item.RevisedPrompt)
	}
	if item.SavedPath != "/path/to/scene_01.png" {
		t.Fatalf("expected saved path preserved, got: %s", item.SavedPath)
	}

	// 2. Verify deterministic capping on massive command execution
	hugeOutput := strings.Repeat("ffmpeg processing frame 001...\n", 1000) // ~32,000 chars
	inputWithCmd := fmt.Sprintf(`[
		{"type": "user_message", "role": "user", "text": "Build documentary scenes"},
		{"type": "command_execution", "command": "ffmpeg -i input.mp4", "aggregated_output": %q},
		{"type": "image_generation_call", "prompt": "Scene 01: Ancient Hoa Lu citadel under lightning", "saved_path": "/out/01.png", "aspect_ratio": "9:16"}
	]`, hugeOutput)

	req := domain.ResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: json.RawMessage(inputWithCmd),
	}

	body := session.BuildGeminiWireBody(&req, "sess_perf_test", nil, "high", nil)

	foundCappedCmd := false
	foundPromptUncut := false
	for _, c := range body.Contents {
		for _, p := range c.Parts {
			if strings.Contains(p.Text, "ffmpeg -i input.mp4") {
				if !strings.Contains(p.Text, "terminal stdout truncated") {
					t.Fatalf("expected huge command output to be deterministically capped, got len=%d", len(p.Text))
				}
				foundCappedCmd = true
			}
			if strings.Contains(p.Text, "Scene 01: Ancient Hoa Lu citadel under lightning") {
				foundPromptUncut = true
			}
		}
	}

	if !foundCappedCmd {
		t.Fatalf("expected capped command execution in turns")
	}
	if !foundPromptUncut {
		t.Fatalf("expected visual image prompt to remain 100%% intact")
	}
}

func TestNativeApplyPatchInTurnBuilder(t *testing.T) {
	store := session.NewSignatureStore(time.Hour)
	store.Set("call_patch_1", "sig_patch_valid_777", "apply_patch")

	patchRaw := "*** Begin Patch\n*** Update File: core-backend/query/gamification.sql\n@@\n-foo\n+bar\n*** End Patch"
	inputJSON := fmt.Sprintf(`[
		{"type":"message","role":"user","content":"Fix SQL"},
		{"type":"custom_tool_call","name":"apply_patch","input":%q,"call_id":"call_patch_1"},
		{"type":"custom_tool_call_output","call_id":"call_patch_1","output":"patch applied successfully"}
	]`, patchRaw)

	req := domain.ResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: json.RawMessage(inputJSON),
	}

	body := session.BuildGeminiWireBody(&req, "sess_native_patch", nil, "high", store)

	// Verify that the model turn contains a NATIVE FunctionCall with thoughtSignature
	foundNativeCall := false
	foundNativeResp := false

	for _, c := range body.Contents {
		if c.Role == "model" {
			for _, p := range c.Parts {
				if p.FunctionCall != nil && p.FunctionCall.Name == "apply_patch" {
					if p.ThoughtSignature != "sig_patch_valid_777" {
						t.Fatalf("expected ThoughtSignature preserved, got: %s", p.ThoughtSignature)
					}
					if inputVal, ok := p.FunctionCall.Args["input"].(string); !ok || !strings.Contains(inputVal, "gamification.sql") {
						t.Fatalf("expected patch content in Args['input'], got: %v", p.FunctionCall.Args)
					}
					foundNativeCall = true
				}
			}
		} else if c.Role == "user" {
			for _, p := range c.Parts {
				if p.FunctionResponse != nil && p.FunctionResponse.Name == "apply_patch" {
					foundNativeResp = true
				}
			}
		}
	}

	if !foundNativeCall {
		t.Fatalf("expected native FunctionCall for apply_patch with signature")
	}
	if !foundNativeResp {
		t.Fatalf("expected native FunctionResponse for apply_patch")
	}
}

func TestFindAnyCallDeterminism(t *testing.T) {
	store := session.NewSignatureStore(time.Hour)
	// Populate multiple sessions with the same call but different signatures
	store.SetSessionCall("sess_z", "deterministic_test_tool", map[string]any{"cmd": "test"}, "sig_z_last")
	store.SetSessionCall("sess_a", "deterministic_test_tool", map[string]any{"cmd": "test"}, "sig_a_first")
	store.SetSessionCall("sess_m", "deterministic_test_tool", map[string]any{"cmd": "test"}, "sig_m_middle")

	// Call FindAnyCall 50 times - must ALWAYS return the exact same signature
	var firstSig string
	for i := 0; i < 50; i++ {
		sig, ok := store.FindAnyCall("deterministic_test_tool", map[string]any{"cmd": "test"})
		if !ok || sig == "" {
			t.Fatalf("iteration %d: expected signature to be found", i)
		}
		if i == 0 {
			firstSig = sig
		} else if sig != firstSig {
			t.Fatalf("iteration %d: non-deterministic FindAnyCall! got %s, expected %s", i, sig, firstSig)
		}
	}
	// Verify that candidates were sorted (lexicographically smallest key: sess_a)
	if firstSig != "sig_a_first" {
		t.Fatalf("expected deterministic first candidate sig_a_first, got %s", firstSig)
	}
}

func TestVisionToolOutputHasFunctionResponseAndInlineData(t *testing.T) {
	store := session.NewSignatureStore(time.Hour)
	store.Set("call_img_1", "sig_img_123", "view_image")

	// 1x1 transparent PNG base64
	tinyPNG := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	inputJSON := fmt.Sprintf(`[
		{"type":"message","role":"user","content":"Look at this image"},
		{"type":"function_call","name":"view_image","arguments":"{\"path\":\"test.png\"}","call_id":"call_img_1"},
		{"type":"function_call_output","call_id":"call_img_1","output":"[{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64,%s\"}]"}
	]`, tinyPNG)

	req := domain.ResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: json.RawMessage(inputJSON),
	}

	body := session.BuildGeminiWireBody(&req, "sess_vision_test", nil, "high", store)

	var foundCall bool
	var foundResp bool
	var foundInlineData bool

	for _, c := range body.Contents {
		if c.Role == "model" {
			for _, p := range c.Parts {
				if p.FunctionCall != nil && p.FunctionCall.Name == "view_image" {
					foundCall = true
				}
			}
		} else if c.Role == "user" {
			for _, p := range c.Parts {
				if p.FunctionResponse != nil && p.FunctionResponse.Name == "view_image" {
					foundResp = true
					if p.FunctionResponse.Response == nil {
						t.Fatalf("expected non-nil response in FunctionResponse")
					}
				}
				if p.InlineData != nil {
					foundInlineData = true
					if p.InlineData.MimeType != "image/png" {
						t.Fatalf("expected image/png, got %s", p.InlineData.MimeType)
					}
				}
			}
		}
	}

	if !foundCall {
		t.Fatalf("expected FunctionCall for view_image in model turn")
	}
	if !foundResp {
		t.Fatalf("expected FunctionResponse for view_image in user turn to satisfy turn symmetry")
	}
	if !foundInlineData {
		t.Fatalf("expected InlineData for view_image in user turn")
	}
}

func TestPrefixCachingStabilityAcrossTurns(t *testing.T) {
	store := session.NewSignatureStore(time.Hour)

	var items []domain.InputItem
	items = append(items, domain.InputItem{
		Type: "message",
		Role: "user",
		Text: "Hello, start task",
	})

	// Simulate 600 turns (exceeding old 500 maxTurnsBudget limit), including 25KB tool outputs
	// to verify that capAllPartsText does NOT mutate prefix turns when new turns are appended!
	largeOut := strings.Repeat("Z", 25000)
	for i := 0; i < 300; i++ {
		callID := fmt.Sprintf("call_%d", i)
		sig := fmt.Sprintf("sig_%d", i)
		store.Set(callID, sig, "exec_command")
		items = append(items, domain.InputItem{
			Type:      "function_call",
			Name:      "exec_command",
			CallID:    callID,
			Arguments: fmt.Sprintf(`{"cmd":"echo %d"}`, i),
		})
		outPayload := fmt.Sprintf(`{"stdout":"out_%d"}`, i)
		if i >= 290 {
			outPayload = fmt.Sprintf(`{"stdout":"out_%d_%s"}`, i, largeOut)
		}
		items = append(items, domain.InputItem{
			Type:   "function_call_output",
			CallID: callID,
			Output: json.RawMessage(outPayload),
		})
	}

	req := domain.ResponsesRequest{Model: "gemini-3.8-flash"}
	body1 := session.BuildGeminiWireBodyWithItems(&req, items, "sess_stable", nil, "high", store)

	// Now add 1 more tool call & output
	nextCallID := "call_300"
	nextSig := "sig_300"
	store.Set(nextCallID, nextSig, "exec_command")
	items = append(items, domain.InputItem{
		Type:      "function_call",
		Name:      "exec_command",
		CallID:    nextCallID,
		Arguments: `{"cmd":"echo 300"}`,
	})
	items = append(items, domain.InputItem{
		Type:   "function_call_output",
		CallID: nextCallID,
		Output: json.RawMessage(`{"stdout":"out_300"}`),
	})

	body2 := session.BuildGeminiWireBodyWithItems(&req, items, "sess_stable", nil, "high", store)

	if len(body2.Contents) <= len(body1.Contents) {
		t.Fatalf("expected body2 to have more turns than body1, got body1=%d, body2=%d", len(body1.Contents), len(body2.Contents))
	}

	for i := 0; i < len(body1.Contents)-1; i++ {
		b1JSON, _ := json.Marshal(body1.Contents[i])
		b2JSON, _ := json.Marshal(body2.Contents[i])
		if string(b1JSON) != string(b2JSON) {
			t.Fatalf("turn %d diverged between consecutive requests! Old prefix was mutated!\nT1: %s\nT2: %s", i, string(b1JSON), string(b2JSON))
		}
	}
}

func TestDeriveSessionIDWithContentParts(t *testing.T) {
	userText := "Project architecture refactoring"
	sid1 := session.DeriveSessionID("", "", userText)
	sid2 := session.DeriveSessionID("", "", userText)

	if sid1 != sid2 {
		t.Fatalf("expected deterministic session ID, got %s vs %s", sid1, sid2)
	}
	if strings.HasPrefix(sid1, "-0") {
		t.Fatalf("invalid session ID format: %s", sid1)
	}
}

func TestEvictStaleImages(t *testing.T) {
	store := session.NewSignatureStore(time.Hour)
	var items []domain.InputItem
	items = append(items, domain.InputItem{
		Type: "message",
		Role: "user",
		Text: "Perform visual inspections",
	})

	dummyBase64 := strings.Repeat("A", 100000) // 100KB base64 image data
	for i := 1; i <= 8; i++ {
		callID := fmt.Sprintf("call_img_%d", i)
		store.Set(callID, fmt.Sprintf("sig_img_%d", i), "view_image")
		items = append(items, domain.InputItem{
			Type:      "function_call",
			Name:      "view_image",
			CallID:    callID,
			Arguments: fmt.Sprintf(`{"step":%d}`, i),
		})
		items = append(items, domain.InputItem{
			Type:   "function_call_output",
			CallID: callID,
			Output: json.RawMessage(fmt.Sprintf(`[{"type":"input_image","image_url":"data:image/jpeg;base64,%s"}]`, dummyBase64)),
		})
	}

	req := domain.ResponsesRequest{
		Model: "gemini-3.8-flash",
	}

	body := session.BuildGeminiWireBodyWithItems(&req, items, "sess_img_test", nil, "high", store)

	imageCount := 0
	placeholderCount := 0
	for _, c := range body.Contents {
		for _, p := range c.Parts {
			if p.InlineData != nil && len(p.InlineData.Data) > 0 {
				imageCount++
			}
			if strings.Contains(p.Text, "[Image: screenshot") || strings.Contains(p.Text, "inspected and processed") {
				placeholderCount++
			}
		}
	}

	// When 8 images exist, only the latest 2 should retain InlineData.
	// The older 6 should be evicted to placeholders.
	if imageCount != 2 {
		t.Fatalf("expected exactly 2 recent images with InlineData, got %d", imageCount)
	}
	if placeholderCount != 6 {
		t.Fatalf("expected 6 evicted image placeholders, got %d", placeholderCount)
	}
}

func TestGiantSessionPruningUnder1MTokenCeiling(t *testing.T) {
	store := session.NewSignatureStore(time.Hour)
	var items []domain.InputItem

	const rootGoal = "Critical Mission: Redesign UI architecture and optimize rendering pipeline"
	items = append(items, domain.InputItem{
		Type: "message",
		Role: "user",
		Text: rootGoal,
	})

	dummyBase64 := strings.Repeat("B", 150000) // 150KB base64 per image
	const intermediateDecision = "[User Confirmed: Deploy v2 to production]"

	for i := 1; i <= 60; i++ {
		callID := fmt.Sprintf("call_step_%d", i)
		store.Set(callID, fmt.Sprintf("sig_step_%d", i), "exec_action")
		items = append(items, domain.InputItem{
			Type:      "function_call",
			Name:      "exec_action",
			CallID:    callID,
			Arguments: fmt.Sprintf(`{"step":%d}`, i),
		})

		// Tool returns image and text
		items = append(items, domain.InputItem{
			Type:   "function_call_output",
			CallID: callID,
			Output: json.RawMessage(fmt.Sprintf(`[{"type":"input_image","image_url":"data:image/png;base64,%s"},{"type":"text","text":"Step %d executed successfully with logs %s"}]`, dummyBase64, i, strings.Repeat("x", 20000))),
		})

		if i == 30 {
			items = append(items, domain.InputItem{
				Type: "message",
				Role: "user",
				Text: intermediateDecision,
			})
		}
	}

	req := domain.ResponsesRequest{
		Model: "gemini-3.8-flash",
	}

	body := session.BuildGeminiWireBodyWithItems(&req, items, "sess_giant_test", nil, "high", store)

	// Measure total wire character size
	totalSize := 0
	foundRootGoal := false
	foundDecision := false

	for _, c := range body.Contents {
		for _, p := range c.Parts {
			totalSize += len(p.Text)
			if p.FunctionCall != nil {
				b, _ := json.Marshal(p.FunctionCall.Args)
				totalSize += len(b)
			}
			if p.FunctionResponse != nil {
				b, _ := json.Marshal(p.FunctionResponse.Response)
				totalSize += len(b)
			}
			if p.InlineData != nil {
				totalSize += len(p.InlineData.Data)
			}
			if strings.Contains(p.Text, rootGoal) {
				foundRootGoal = true
			}
			if strings.Contains(p.Text, intermediateDecision) {
				foundDecision = true
			}
		}
	}

	// 1. Total wire size MUST NOT exceed 3,000,000 characters (~750,000 tokens)
	const maxSafeChars = 3000000
	if totalSize > maxSafeChars {
		t.Fatalf("Wire payload size %d exceeds safe budget %d chars!", totalSize, maxSafeChars)
	}

	// 2. Root goal MUST be preserved
	if !foundRootGoal {
		t.Fatalf("Root goal was pruned from context!")
	}

	// 3. User decision MUST be preserved
	if !foundDecision {
		t.Fatalf("User intermediate decision was pruned from context!")
	}

	// 4. Alternating turns and valid endings
	if len(body.Contents) == 0 {
		t.Fatalf("empty contents")
	}
	if body.Contents[0].Role != "user" {
		t.Fatalf("first turn must be user")
	}
	if body.Contents[len(body.Contents)-1].Role != "user" {
		t.Fatalf("last turn must be user")
	}
}

func TestFullResolutionPaperImageDoesNotDropRecentContext(t *testing.T) {
	store := session.NewSignatureStore(time.Hour)
	var items []domain.InputItem

	const rootGoal = "Initial Mission: Research 3D Gaussian Splatting"
	items = append(items, domain.InputItem{
		Type: "message",
		Role: "user",
		Text: rootGoal,
	})

	// Add 30 normal conversation turns
	for i := 1; i <= 30; i++ {
		callID := fmt.Sprintf("call_step_%d", i)
		store.Set(callID, fmt.Sprintf("sig_step_%d", i), "exec_cmd")
		items = append(items, domain.InputItem{
			Type:      "function_call",
			Name:      "exec_cmd",
			CallID:    callID,
			Arguments: fmt.Sprintf(`{"cmd":"echo %d"}`, i),
		})
		items = append(items, domain.InputItem{
			Type:   "function_call_output",
			CallID: callID,
			Output: json.RawMessage(fmt.Sprintf(`{"stdout":"executed step %d with detailed logs"}`, i)),
		})
	}

	// Recent crucial instruction right before viewing the image
	const recentCrucialInstruction = "CRITICAL: Check equation (4) on Gaussian density control and adaptive splitting"
	items = append(items, domain.InputItem{
		Type: "message",
		Role: "user",
		Text: recentCrucialInstruction,
	})

	// Load real 4MB PNG base64 from page_007.png if exists, otherwise generate 4MB synthetic PNG base64
	realPagePath := "/Users/hoangnguyen142/Work/NCKH/CVPR2027/00_Foundational_Milestones/SIGGRAPH2023_Kerbl_3D_Gaussian_Splatting_for_Real-Time_Radiance_Field_Rendering/pages/page_007.png"
	var imgB64 string
	if b, err := os.ReadFile(realPagePath); err == nil {
		imgB64 = base64.StdEncoding.EncodeToString(b)
	} else {
		imgB64 = strings.Repeat("A", 3900000)
	}

	store.Set("call_view_img_4mb", "sig_view_img_valid", "view_image")
	items = append(items, domain.InputItem{
		Type:      "function_call",
		Name:      "view_image",
		CallID:    "call_view_img_4mb",
		Arguments: `{"path":"page_007.png"}`,
	})
	items = append(items, domain.InputItem{
		Type:   "function_call_output",
		CallID: "call_view_img_4mb",
		Output: json.RawMessage(fmt.Sprintf(`[{"type":"input_image","image_url":"data:image/png;base64,%s"}]`, imgB64)),
	})

	req := domain.ResponsesRequest{
		Model: "gemini-3.8-flash",
	}

	body := session.BuildGeminiWireBodyWithItems(&req, items, "sess_paper_img", nil, "high", store)

	// Check that the wire body is well within budget
	totalSize := 0
	foundRecentInstruction := false
	foundRootGoal := false

	for _, c := range body.Contents {
		for _, p := range c.Parts {
			totalSize += len(p.Text)
			if p.FunctionCall != nil {
				b, _ := json.Marshal(p.FunctionCall.Args)
				totalSize += len(b)
			}
			if p.FunctionResponse != nil {
				b, _ := json.Marshal(p.FunctionResponse.Response)
				totalSize += len(b)
			}
			if p.InlineData != nil {
				totalSize += len(p.InlineData.Data)
			}
			if strings.Contains(p.Text, recentCrucialInstruction) {
				foundRecentInstruction = true
			}
			if strings.Contains(p.Text, rootGoal) {
				foundRootGoal = true
			}
		}
	}

	if totalSize > 3000000 {
		t.Fatalf("total wire size %d exceeds 3MB safe budget", totalSize)
	}
	if !foundRootGoal {
		t.Fatalf("expected root goal preserved")
	}
	if !foundRecentInstruction {
		t.Fatalf("CRITICAL BUG REPRODUCED: Recent instruction was dropped due to image size!")
	}
}

func TestHysteresisCompactionLocksPrefixAcross30Turns(t *testing.T) {
	store := session.NewSignatureStore(time.Hour)
	var items []domain.InputItem

	items = append(items, domain.InputItem{
		Type: "message",
		Role: "user",
		Text: "Build full-stack platform with 100% prefix cache stability",
	})

	// Build 140 turns (~4,200,000 chars total) so the session exceeds the 3,800,000-char budget
	// and triggers hysteresis compaction down to ~2,090,000 chars.
	chunk30k := strings.Repeat("K", 30000)
	for i := 1; i <= 140; i++ {
		callID := fmt.Sprintf("call_hist_%d", i)
		store.Set(callID, fmt.Sprintf("sig_hist_%d", i), "exec_command")
		items = append(items, domain.InputItem{
			Type:      "function_call",
			Name:      "exec_command",
			CallID:    callID,
			Arguments: fmt.Sprintf(`{"cmd":"step %d"}`, i),
		})
		items = append(items, domain.InputItem{
			Type:   "function_call_output",
			CallID: callID,
			Output: json.RawMessage(fmt.Sprintf(`{"stdout":"step_%d_%s"}`, i, chunk30k)),
		})
	}

	req := domain.ResponsesRequest{Model: "gemini-4-argon"}
	sessionID := "sess_hysteresis_lock_test"

	prevBody := session.BuildGeminiWireBodyWithItems(&req, items, sessionID, nil, "LOW", store)

	// Now simulate 25 consecutive subsequent turns (each ~8KB), crossing multiple 16-turn boundaries.
	// Every single turn MUST keep the exact same prefix (0 .. len(prevBody.Contents)-1) 100% byte-identical!
	chunk8k := strings.Repeat("M", 8000)
	for step := 101; step <= 125; step++ {
		callID := fmt.Sprintf("call_hist_%d", step)
		store.Set(callID, fmt.Sprintf("sig_hist_%d", step), "exec_command")
		items = append(items, domain.InputItem{
			Type:      "function_call",
			Name:      "exec_command",
			CallID:    callID,
			Arguments: fmt.Sprintf(`{"cmd":"step %d"}`, step),
		})
		items = append(items, domain.InputItem{
			Type:   "function_call_output",
			CallID: callID,
			Output: json.RawMessage(fmt.Sprintf(`{"stdout":"step_%d_%s"}`, step, chunk8k)),
		})

		nextBody := session.BuildGeminiWireBodyWithItems(&req, items, sessionID, nil, "LOW", store)
		if len(nextBody.Contents) != len(prevBody.Contents)+2 {
			t.Fatalf("step %d: expected contents length to grow by +2 (from %d to %d), got %d (cutoffIndex shifted!)",
				step, len(prevBody.Contents), len(prevBody.Contents)+2, len(nextBody.Contents))
		}

		for idx := 0; idx < len(prevBody.Contents); idx++ {
			b1, _ := json.Marshal(prevBody.Contents[idx])
			b2, _ := json.Marshal(nextBody.Contents[idx])
			if string(b1) != string(b2) {
				t.Fatalf("step %d: prefix turn %d mutated between consecutive turns!\nPrev: %s\nNext: %s",
					step, idx, string(b1), string(b2))
			}
		}
		prevBody = nextBody
	}
}


