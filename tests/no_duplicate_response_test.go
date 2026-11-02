package tests

import (
	"encoding/json"
	"testing"
	"time"

	"comini/internal/domain"
	"comini/internal/session"
)

// Regression: a completed Responses-API function_call must yield exactly one FunctionResponse
// (from its function_call_output), never an extra empty one.
func TestFunctionCallProducesSingleFunctionResponse(t *testing.T) {
	raw := `[
	 {"type":"message","role":"user","content":[{"type":"input_text","text":"run it"}]},
	 {"type":"function_call","status":"completed","id":"fc_1","name":"exec_command","arguments":"{\"cmd\":\"ls\"}","call_id":"call_1"},
	 {"type":"function_call_output","id":"fco_1","call_id":"call_1","output":"file.txt"}
	]`
	var items []domain.InputItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatal(err)
	}
	store := session.NewMemorySignatureStore(time.Hour)
	store.Set("call_1", "sig_test", "exec_command")
	req := domain.ResponsesRequest{Model: "gemini-3.8-flash", Input: json.RawMessage(raw)}
	wb := session.BuildGeminiWireBodyWithItems(&req, items, "s", nil, "high", store)

	calls, resps := 0, 0
	for _, c := range wb.Contents {
		for _, p := range c.Parts {
			if p.FunctionCall != nil {
				calls++
			}
			if p.FunctionResponse != nil {
				resps++
			}
		}
	}
	if calls != resps {
		t.Fatalf("functionCall parts=%d functionResponse parts=%d (must be equal)", calls, resps)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 call/response pair, got %d", calls)
	}
}
