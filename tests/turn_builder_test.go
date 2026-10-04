package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"comini/internal/domain"
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

	if len(body.Contents) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(body.Contents))
	}

	// Turn 0: user
	if body.Contents[0].Role != "user" || body.Contents[0].Parts[0].Text != "Run linter" {
		t.Fatalf("turn 0 mismatch")
	}

	// Turn 1: model with replayed signature
	if body.Contents[1].Role != "model" {
		t.Fatalf("turn 1 must be model")
	}
	modelPart := body.Contents[1].Parts[0]
	if modelPart.FunctionCall.Name != "exec_linter" {
		t.Fatalf("expected exec_linter")
	}
	if modelPart.ThoughtSignature != "sig_cached_999" {
		t.Fatalf("expected sig_cached_999, got %s", modelPart.ThoughtSignature)
	}

	// Turn 2: user batch of 2 functionResponses
	if body.Contents[2].Role != "user" {
		t.Fatalf("turn 2 must be user")
	}
	if len(body.Contents[2].Parts) != 2 {
		t.Fatalf("turn 2 must have 2 functionResponse parts, got %d", len(body.Contents[2].Parts))
	}
	resp0 := body.Contents[2].Parts[0].FunctionResponse
	if resp0.Name != "exec_linter" {
		t.Fatalf("expected exec_linter, got %s", resp0.Name)
	}
}

func TestSignatureStoreTieredLookup(t *testing.T) {
	store := session.NewSignatureStore(time.Hour)
	// Store only by session and args
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
	if len(body.Contents) < 2 {
		t.Fatalf("expected 2 turns, got %d", len(body.Contents))
	}

	modelPart := body.Contents[1].Parts[0]
	if modelPart.FunctionCall == nil {
		t.Fatalf("expected functionCall part")
	}
	if modelPart.ThoughtSignature != "sig_sess_999" {
		t.Fatalf("expected tiered signature sig_sess_999, got %s", modelPart.ThoughtSignature)
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
	if len(body.Contents) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(body.Contents))
	}

	// Turn 2 is user turn with tool response
	userTurn := body.Contents[2]
	if len(userTurn.Parts) < 2 {
		t.Fatalf("expected at least 2 parts (inline_data + functionResponse), got %d", len(userTurn.Parts))
	}

	// Check inline data
	if userTurn.Parts[0].InlineData == nil {
		t.Fatalf("expected part 0 to be InlineData image")
	}
	if userTurn.Parts[0].InlineData.MimeType != "image/png" {
		t.Fatalf("expected image/png, got %s", userTurn.Parts[0].InlineData.MimeType)
	}

	// Check function response
	if userTurn.Parts[1].FunctionResponse == nil {
		t.Fatalf("expected part 1 to be FunctionResponse")
	}
	if userTurn.Parts[1].FunctionResponse.Name != "view_file" {
		t.Fatalf("expected view_file, got %s", userTurn.Parts[1].FunctionResponse.Name)
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
	if len(body2.Contents) < 2 {
		t.Fatalf("expected at least 2 turns for MCP tool call, got %d", len(body2.Contents))
	}
	userTurn := body2.Contents[len(body2.Contents)-1]
	if len(userTurn.Parts) < 2 {
		t.Fatalf("expected inlineData + functionResponse in MCP user turn")
	}
	if userTurn.Parts[0].InlineData == nil {
		t.Fatalf("expected part 0 to be InlineData")
	}
	// Check that function response has NO residual base64
	respBytes, _ := json.Marshal(userTurn.Parts[1].FunctionResponse.Response)
	if strings.Contains(string(respBytes), "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJ") {
		t.Fatalf("function response still contains raw base64 bloat!")
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
	if !strings.Contains(combinedText, "[Action: executed command go test ./...]") || !strings.Contains(combinedText, "[Observation: command output: PASS\nok comini/db 0.05s]") {
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
	if !strings.Contains(res, "You are Codex, a coding agent.") {
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
	token, proj, err := transport.DefaultAuthManager.GetCredentials()
	if err != nil || token == "" {
		t.Skip("no credentials")
	}

	client := transport.NewCCAClient("https://daily-cloudcode-pa.googleapis.com", "antigravity")

	// 1. With GPT-5
	bodyWithGPT5 := domain.GeminiWireBody{
		SystemInstruction: &domain.GeminiSystemInstruction{
			Parts: []domain.GeminiPart{{Text: "You are Codex, an agent based on GPT-5. You and the user share one workspace, and your job is to collaborate with them until their goal is genuinely handled."}},
		},
		Contents: []domain.GeminiContent{{Role: "user", Parts: []domain.GeminiPart{{Text: "Hi"}}}},
	}
	resp1, err1 := client.StreamGenerateContent(context.Background(), "gemini-3.7-flash-tiered", bodyWithGPT5, token, proj)
	if err1 != nil {
		t.Logf("Prompt with GPT-5 -> REJECTED: %v", err1)
	} else {
		resp1.Body.Close()
		t.Logf("Prompt with GPT-5 -> ACCEPTED")
	}

	// 2. With Codex coding agent
	bodyCodex := domain.GeminiWireBody{
		SystemInstruction: &domain.GeminiSystemInstruction{
			Parts: []domain.GeminiPart{{Text: "You are Codex, a coding agent. You and the user share one workspace, and your job is to collaborate with them until their goal is genuinely handled."}},
		},
		Contents: []domain.GeminiContent{{Role: "user", Parts: []domain.GeminiPart{{Text: "Hi"}}}},
	}
	resp2, err2 := client.StreamGenerateContent(context.Background(), "gemini-3.7-flash-tiered", bodyCodex, token, proj)
	if err2 != nil {
		t.Logf("Prompt with Codex coding agent -> REJECTED: %v", err2)
	} else {
		resp2.Body.Close()
		t.Logf("Prompt with Codex coding agent -> ACCEPTED")
	}
}
