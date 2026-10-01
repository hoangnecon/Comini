package tests

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"comini/internal/domain"
	"comini/internal/session"
	"comini/internal/streaming"
)

func TestMapGeminiStreamToResponses(t *testing.T) {
	mockGeminiSSE := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"Thinking step 1...","thought":true,"thoughtSignature":"sig_stream_777"}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"text":"Thinking step 2.","thought":true}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"text":"The answer is: "}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"write_file","args":{"path":"main.go"}}}]}}]}}
`

	store := session.NewSignatureStore(time.Hour)
	var out bytes.Buffer

	toolMeta := map[string]domain.ToolMeta{
		"write_file": {OriginalName: "write_file"},
	}

	err := streaming.MapGeminiStreamToResponses(strings.NewReader(mockGeminiSSE), &out, nil, store, "resp_test_123", "deepseek-flash", toolMeta, "test_sess_123")
	if err != nil {
		t.Fatalf("streaming map error: %v", err)
	}

	result := out.String()

	// 1. Check response.created
	if !strings.Contains(result, "event: response.created") {
		t.Fatalf("missing response.created event")
	}

	// 2. Check reasoning summary events (drives Codex UI accordion and thought drawer)
	if !strings.Contains(result, "event: response.reasoning_summary_part.added") {
		t.Fatalf("missing response.reasoning_summary_part.added")
	}
	if !strings.Contains(result, "event: response.reasoning_summary_text.delta") {
		t.Fatalf("missing response.reasoning_summary_text.delta")
	}
	if !strings.Contains(result, `"delta":"Thinking step 1..."`) {
		t.Fatalf("missing thinking delta content")
	}
	if !strings.Contains(result, "event: response.reasoning_summary_text.done") {
		t.Fatalf("missing reasoning_summary_text.done")
	}
	if !strings.Contains(result, "event: response.reasoning_summary_part.done") {
		t.Fatalf("missing reasoning_summary_part.done")
	}
	if !strings.Contains(result, "Thinking step 1...Thinking step 2.") {
		t.Fatalf("missing full accumulated reasoning content in output")
	}

	// 3. Check response.output_text.delta
	if !strings.Contains(result, "event: response.output_text.delta") {
		t.Fatalf("missing response.output_text.delta")
	}
	if !strings.Contains(result, `"delta":"The answer is: "`) {
		t.Fatalf("missing output text delta")
	}

	// 4. Check function call
	if !strings.Contains(result, "event: response.function_call_arguments.delta") {
		t.Fatalf("missing function_call_arguments.delta")
	}
	if !strings.Contains(result, `"name":"write_file"`) {
		t.Fatalf("missing write_file function call name")
	}

	// 5. Check response.completed
	if !strings.Contains(result, "event: response.completed") {
		t.Fatalf("missing response.completed")
	}
}

func TestMapGeminiStreamNamespacedTool(t *testing.T) {
	mockGeminiSSE := `data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"default_api:exec_command","args":{"cmd":"ls -la"}}}]}}]}}
`
	store := session.NewSignatureStore(time.Hour)
	var out bytes.Buffer

	toolMeta := map[string]domain.ToolMeta{
		"default_api:exec_command": {OriginalName: "exec_command", Namespace: "default_api"},
	}

	err := streaming.MapGeminiStreamToResponses(strings.NewReader(mockGeminiSSE), &out, nil, store, "resp_ns_123", "deepseek-flash", toolMeta, "test_sess_ns")
	if err != nil {
		t.Fatalf("streaming map error: %v", err)
	}

	result := out.String()

	// Verify function call output item has name "exec_command" and namespace "default_api"
	if !strings.Contains(result, `"name":"exec_command"`) {
		t.Fatalf("expected name to be exec_command, got output: %s", result)
	}
	if !strings.Contains(result, `"namespace":"default_api"`) {
		t.Fatalf("expected namespace to be default_api, got output: %s", result)
	}
	if strings.Contains(result, `"name":"default_api:exec_command"`) {
		t.Fatalf("name should NOT be double prefixed with default_api:exec_command, got output: %s", result)
	}
	if !strings.Contains(result, `\"cmd\":\"ls -la\"`) {
		t.Fatalf("missing command argument in output: %s", result)
	}
}
