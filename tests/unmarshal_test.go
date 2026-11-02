package tests

import (
	"encoding/json"
	"fmt"
	"testing"

	"comini/internal/domain"
)

func TestUnmarshalCCAResponse(t *testing.T) {
	raw := `{"response": {"candidates": [{"content": {"role": "model","parts": [{"text": "red"}]}}],"usageMetadata": {"promptTokenCount": 1110,"candidatesTokenCount": 1,"totalTokenCount": 1195,"thoughtsTokenCount": 84},"modelVersion": "gemini-3.7-flash-tiered","responseId": "Y-K8aqiCC-Deg8UPu6jX6Qo"},"traceId": "99cceaf88a1d0177","metadata": {}}`

	var root domain.CCARootChunk
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if root.Response == nil {
		t.Fatalf("root.Response is nil")
	}
	if len(root.Response.Candidates) == 0 {
		t.Fatalf("no candidates")
	}
	parts := root.Response.Candidates[0].Content.Parts
	if len(parts) == 0 {
		t.Fatalf("no parts")
	}
	fmt.Printf("Parsed part 0: text=%q thought=%v sig=%q\n", parts[0].Text, parts[0].Thought, parts[0].ThoughtSignature)
	if parts[0].Text != "red" {
		t.Fatalf("expected text=red, got %q", parts[0].Text)
	}
}

func TestUnmarshalInputItemMCPResult(t *testing.T) {
	raw := `[
		{"type":"userMessage","text":"Hello 1"},
		{"type":"mcpToolCall","id":"call_eac13629efc8","server":"golab","tool":"run_code_cell","status":"completed","arguments":{"cellId":"jmIUm3Qhr3MR"},"result":{"content":[{"type":"text","text":"started"}]}},
		{"type":"userMessage","text":"Hello 2"}
	]`
	var items []domain.InputItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
}
