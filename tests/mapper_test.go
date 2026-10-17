package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

	err := streaming.MapGeminiStreamToResponses(strings.NewReader(mockGeminiSSE), &out, nil, store, "resp_test_123", "gemini-3.8-flash", toolMeta, "test_sess_123")
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
	mockGeminiSSE := `data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"mcp__default_api__exec_command","args":{"cmd":"ls -la"}}}]}}]}}
`
	store := session.NewSignatureStore(time.Hour)
	var out bytes.Buffer

	toolMeta := map[string]domain.ToolMeta{
		"mcp__default_api__exec_command": {OriginalName: "exec_command", Namespace: "default_api", CanonicalName: "mcp__default_api__exec_command"},
	}

	err := streaming.MapGeminiStreamToResponses(strings.NewReader(mockGeminiSSE), &out, nil, store, "resp_ns_123", "gemini-3.8-flash", toolMeta, "test_sess_ns")
	if err != nil {
		t.Fatalf("streaming map error: %v", err)
	}

	result := out.String()

	// Verify function call output item has original name "exec_command" and namespace "default_api"
	if !strings.Contains(result, `"name":"exec_command"`) {
		t.Fatalf("expected name to be exec_command, got output: %s", result)
	}
	if !strings.Contains(result, `"namespace":"default_api"`) {
		t.Fatalf("expected namespace to be default_api, got output: %s", result)
	}
	if !strings.Contains(result, `\"cmd\":\"ls -la\"`) {
		t.Fatalf("missing command argument in output: %s", result)
	}
}

func TestStripLeakedProtocolObservations(t *testing.T) {
	// Scenario 1: Exact reproduction of the user's issue with multiple leaked protocol blocks
	mockGeminiSSE1 := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"[Image omitted from historical context]"}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"text":"[Observation: tool view_image returned visual image (image/png)]"}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"text":"[Observation: tool exec_command executed with {\"cmd\":\"cat << 'EOF' > generate_de_cuong_final.py\nimport docx\"} -> returned: {\"output\":\"\"}]"}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"text":"[Observation: tool exec_command executed with {\"cmd\":\"ls -la captures/\"} -> returned: {\"output\":\"proposal_page-1.png\n\"}]"}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"text":"Toàn bộ kế hoạch sản xuất video kịch bản lịch sử đã hoàn thành."}]}}]}}
`
	store := session.NewSignatureStore(time.Hour)
	var out1 bytes.Buffer
	err := streaming.MapGeminiStreamToResponses(strings.NewReader(mockGeminiSSE1), &out1, nil, store, "resp_strip_1", "gemini-3.8-flash", nil, "sess_strip_1")
	if err != nil {
		t.Fatalf("streaming map error: %v", err)
	}

	res1 := out1.String()
	if strings.Contains(res1, "[Observation:") {
		t.Fatalf("leaked [Observation: found in output: %s", res1)
	}
	if strings.Contains(res1, "[Image omitted") {
		t.Fatalf("leaked [Image omitted found in output: %s", res1)
	}
	if strings.Contains(res1, "generate_de_cuong_final.py") {
		t.Fatalf("leaked command found in output: %s", res1)
	}
	if strings.Contains(res1, "proposal_page-1.png") {
		t.Fatalf("leaked file capture found in output: %s", res1)
	}
	if !strings.Contains(res1, "Toàn bộ kế hoạch sản xuất video kịch bản lịch sử đã hoàn thành.") {
		t.Fatalf("expected legitimate assistant text not found in output: %s", res1)
	}

	// Scenario 2: Protocol prefix split across streaming chunks
	mockGeminiSSE2 := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"[Observ"}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"text":"ation: tool get_goal executed with {} -> returned: {\"objective\":\"test\"}]"}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"text":"Kế hoạch chi tiết bước 1."}]}}]}}
`
	var out2 bytes.Buffer
	err = streaming.MapGeminiStreamToResponses(strings.NewReader(mockGeminiSSE2), &out2, nil, store, "resp_strip_2", "gemini-3.8-flash", nil, "sess_strip_2")
	if err != nil {
		t.Fatalf("streaming map error: %v", err)
	}
	res2 := out2.String()
	if strings.Contains(res2, "[Observation:") || strings.Contains(res2, "get_goal") {
		t.Fatalf("leaked split observation found in output: %s", res2)
	}
	if !strings.Contains(res2, "Kế hoạch chi tiết bước 1.") {
		t.Fatalf("expected assistant text not found in output: %s", res2)
	}

	// Scenario 3: Mid-stream observation echo
	mockGeminiSSE3 := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"Bước 1: hoàn tất.\n"}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"text":"[Observation: tool exec_command executed with {\"cmd\":\"test\"} -> returned: {\"output\":\"ok\"}]\n"}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"text":"Bước 2: tiếp tục."}]}}]}}
`
	var out3 bytes.Buffer
	err = streaming.MapGeminiStreamToResponses(strings.NewReader(mockGeminiSSE3), &out3, nil, store, "resp_strip_3", "gemini-3.8-flash", nil, "sess_strip_3")
	if err != nil {
		t.Fatalf("streaming map error: %v", err)
	}
	res3 := out3.String()
	if strings.Contains(res3, "[Observation:") || strings.Contains(res3, "exec_command") {
		t.Fatalf("leaked mid-stream observation found in output: %s", res3)
	}
	if !strings.Contains(res3, "Bước 1: hoàn tất.") || !strings.Contains(res3, "Bước 2: tiếp tục.") {
		t.Fatalf("expected text parts not found in output: %s", res3)
	}

	// Scenario 4: Legitimate markdown brackets must NOT be stripped
	mockGeminiSSE4 := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"Tham khảo tại [Đề cương đồ án](https://example.com) và [1] ghi chú [x] hoàn thành."}]}}]}}
`
	var out4 bytes.Buffer
	err = streaming.MapGeminiStreamToResponses(strings.NewReader(mockGeminiSSE4), &out4, nil, store, "resp_strip_4", "gemini-3.8-flash", nil, "sess_strip_4")
	if err != nil {
		t.Fatalf("streaming map error: %v", err)
	}
	res4 := out4.String()
	if !strings.Contains(res4, "[Đề cương đồ án](https://example.com)") {
		t.Fatalf("markdown link was incorrectly stripped: %s", res4)
	}
	if !strings.Contains(res4, "[1]") || !strings.Contains(res4, "[x]") {
		t.Fatalf("markdown brackets were incorrectly stripped: %s", res4)
	}
}

func TestStreamingActionCallExtraction(t *testing.T) {
	// Scenario A: Exact reproduction of streamed action call split across multiple tokens/chunks
	mockGeminiSSE := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"[Action"}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"text":": called write_stdin with {\"chars\":\"\",\"max_output_tokens\":10000,"}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"text":"\"session_id\":69235,\"yield_time_ms\":20000}]"}]}}]}}
`
	store := session.NewSignatureStore(time.Hour)
	toolMeta := map[string]domain.ToolMeta{
		"write_stdin": {OriginalName: "write_stdin"},
	}
	var out bytes.Buffer
	err := streaming.MapGeminiStreamToResponses(strings.NewReader(mockGeminiSSE), &out, nil, store, "resp_act_1", "gemini-3.8-flash", toolMeta, "sess_act_1")
	if err != nil {
		t.Fatalf("streaming map error: %v", err)
	}

	res := out.String()

	// 1. Must NOT emit [Action: to output_text.delta
	if strings.Contains(res, "event: response.output_text.delta") {
		t.Fatalf("unexpected output_text.delta emitted for action call: %s", res)
	}
	if strings.Contains(res, "[Action:") {
		t.Fatalf("leaked [Action: found in output: %s", res)
	}

	// 2. Must emit structured function call
	if !strings.Contains(res, "event: response.output_item.added") || !strings.Contains(res, `"type":"function_call"`) {
		t.Fatalf("expected function_call output item not found in: %s", res)
	}
	if !strings.Contains(res, `"name":"write_stdin"`) {
		t.Fatalf("expected write_stdin function call name in: %s", res)
	}
	if !strings.Contains(res, `\"session_id\":69235`) {
		t.Fatalf("expected session_id argument in function call: %s", res)
	}

	// Scenario B: Text followed by action call in the same stream
	mockGeminiSSE2 := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"Đang kiểm tra tiến trình...\n"}]}}]}}

data: {"response":{"candidates":[{"content":{"parts":[{"text":"[Action: called write_stdin with {\"session_id\":69235}]"}]}}]}}
`
	var out2 bytes.Buffer
	err = streaming.MapGeminiStreamToResponses(strings.NewReader(mockGeminiSSE2), &out2, nil, store, "resp_act_2", "gemini-3.8-flash", toolMeta, "sess_act_2")
	if err != nil {
		t.Fatalf("streaming map error: %v", err)
	}
	res2 := out2.String()
	if !strings.Contains(res2, `"delta":"Đang kiểm tra tiến trình...\n"`) {
		t.Fatalf("expected message delta not found in: %s", res2)
	}
	if strings.Contains(res2, "[Action:") {
		t.Fatalf("leaked [Action: in output text: %s", res2)
	}
	if !strings.Contains(res2, `"name":"write_stdin"`) {
		t.Fatalf("expected write_stdin tool call in: %s", res2)
	}
}

func TestGeneratedImageTagExtraction(t *testing.T) {
	// Scenario: Gemini emits [Generated Image based on prompt: "..."] instead of structured call
	mockGeminiSSE := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"[Generated Image based on prompt: \"Cinematic 9:16 interior shot of Prince Dinh Lien in 10th-century royal attire sitting before an ancient altar.\"]"}]}}]}}
`
	store := session.NewSignatureStore(time.Hour)
	var out bytes.Buffer

	capturedPrompt := ""
	capturedAR := ""
	streamOpts := &streaming.StreamOptions{
		ImageGenFn: func(ctx context.Context, prompt string, aspectRatio string) (string, string, error) {
			capturedPrompt = prompt
			capturedAR = aspectRatio
			return "/tmp/scene_13.jpg", "data:image/jpeg;base64,mockdata", nil
		},
	}

	err := streaming.MapGeminiStreamToResponses(strings.NewReader(mockGeminiSSE), &out, nil, store, "resp_img_ext", "gemini-3.8-flash", nil, "sess_img_ext", *streamOpts)
	if err != nil {
		t.Fatalf("streaming map error: %v", err)
	}

	res := out.String()

	// 1. Must NOT leak raw [Generated Image to chat output_text.delta
	if strings.Contains(res, "[Generated Image based on prompt:") {
		t.Fatalf("leaked [Generated Image tag found in output: %s", res)
	}

	// 2. Must emit image_generation_call SSE item
	if !strings.Contains(res, `"type":"image_generation_call"`) {
		t.Fatalf("expected image_generation_call output item not found in: %s", res)
	}
	if !strings.Contains(res, `\"saved_path\":\"/tmp/scene_13.jpg\"`) && !strings.Contains(res, `"saved_path":"/tmp/scene_13.jpg"`) {
		t.Fatalf("expected saved_path in output: %s", res)
	}

	// 3. Verify ImageGenFn received correct prompt and detected 9:16 aspect ratio
	if !strings.Contains(capturedPrompt, "Prince Dinh Lien") {
		t.Fatalf("expected captured prompt to contain 'Prince Dinh Lien', got: %s", capturedPrompt)
	}
	if capturedAR != "9:16" {
		t.Fatalf("expected aspect ratio '9:16', got: %s", capturedAR)
	}
}

func TestNativeApplyPatchAndZeroDrop(t *testing.T) {
	patchContent := "*** Begin Patch\n*** Update File: query/gamification.sql\n@@\n-foo\n+bar\n*** End Patch"
	mockPartText := fmt.Sprintf("[Action: called apply_patch with %s]", patchContent)
	rootObj := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{"text": mockPartText},
						},
					},
				},
			},
		},
	}
	rootBytes, _ := json.Marshal(rootObj)
	mockGeminiSSE := fmt.Sprintf("data: %s\n\n", string(rootBytes))

	store := session.NewSignatureStore(time.Hour)
	toolMeta := map[string]domain.ToolMeta{
		"apply_patch": {OriginalName: "apply_patch", IsCustom: true},
	}
	var out bytes.Buffer
	err := streaming.MapGeminiStreamToResponses(strings.NewReader(mockGeminiSSE), &out, nil, store, "resp_patch_1", "gemini-3.8-flash", toolMeta, "sess_patch_1")
	if err != nil {
		t.Fatalf("streaming map error: %v", err)
	}

	res := out.String()
	if !strings.Contains(res, `"type":"custom_tool_call"`) {
		t.Fatalf("expected custom_tool_call for apply_patch, got: %s", res)
	}
	if !strings.Contains(res, `"name":"apply_patch"`) {
		t.Fatalf("expected apply_patch name, got: %s", res)
	}
	if !strings.Contains(res, "Update File: query/gamification.sql") {
		t.Fatalf("expected patch content in custom tool call delta, got: %s", res)
	}

	// Scenario 2: Unparsable action block must NOT be dropped, must be emitted as text
	mockGeminiSSE2 := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"[Action: called unknown_tool with broken non json syntax]"}]}}]}}
`
	var out2 bytes.Buffer
	err = streaming.MapGeminiStreamToResponses(strings.NewReader(mockGeminiSSE2), &out2, nil, store, "resp_patch_2", "gemini-3.8-flash", toolMeta, "sess_patch_2")
	if err != nil {
		t.Fatalf("streaming map error: %v", err)
	}
	res2 := out2.String()
	if !strings.Contains(res2, "broken non json syntax") {
		t.Fatalf("expected unparsable action block to be emitted as text instead of dropped, got: %s", res2)
	}
	if !strings.Contains(res2, "event: response.output_item.added") || !strings.Contains(res2, `"type":"message"`) {
		t.Fatalf("expected message item emitted for fallback text, got: %s", res2)
	}

	// Scenario 3: Stream with zero content parts must trigger safety net message
	mockGeminiSSE3 := `data: {"response":{"candidates":[{"content":{"parts":[{"thought":true,"text":"Thinking only..."}]}}]}}
`
	var out3 bytes.Buffer
	err = streaming.MapGeminiStreamToResponses(strings.NewReader(mockGeminiSSE3), &out3, nil, store, "resp_patch_3", "gemini-3.8-flash", toolMeta, "sess_patch_3")
	if err != nil {
		t.Fatalf("streaming map error: %v", err)
	}
	res3 := out3.String()
	if !strings.Contains(res3, `"type":"message"`) {
		t.Fatalf("expected fallback safety net message when zero output items, got: %s", res3)
	}
}

