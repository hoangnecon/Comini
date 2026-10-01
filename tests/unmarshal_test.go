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
