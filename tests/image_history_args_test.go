package tests

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"comini/internal/domain"
	"comini/internal/session"
)

// Codex echoes image_generation_call without image_paths; the replayed history must still show
// the full arguments (recovered from the store) so the model keeps passing reference images.
func TestImageGenHistoryRestoresImagePaths(t *testing.T) {
	store := session.NewSignatureStore(time.Hour)
	store.SetImageCall(map[string]any{
		"prompt":       "edit",
		"image_name":   "dechiu_edit",
		"aspect_ratio": "9:16",
		"image_paths":  []any{"/work/dechiu_01.jpg"},
	}, "igc_hist_1", "call_hist_1")

	input := `[
		{"type":"message","role":"user","content":"edit this"},
		{"type":"image_generation_call","id":"igc_hist_1","status":"completed","revised_prompt":"edit","saved_path":"/out/a.jpg"}
	]`
	req := domain.ResponsesRequest{Model: "gemini-3.8-flash", Input: json.RawMessage(input)}
	body := session.BuildGeminiWireBody(&req, "sess_img_hist", nil, "high", store)

	var all strings.Builder
	for _, c := range body.Contents {
		for _, p := range c.Parts {
			all.WriteString(p.Text)
		}
	}
	got := all.String()
	if !strings.Contains(got, `"image_paths":["/work/dechiu_01.jpg"]`) {
		t.Fatalf("image_paths missing from replayed image_generation call:\n%s", got)
	}
	if !strings.Contains(got, `"aspect_ratio":"9:16"`) || !strings.Contains(got, `"image_name":"dechiu_edit"`) {
		t.Fatalf("aspect_ratio/image_name missing:\n%s", got)
	}

	// Unknown id without a stored record keeps the legacy prompt-only shape.
	req2 := domain.ResponsesRequest{Model: "gemini-3.8-flash", Input: json.RawMessage(strings.ReplaceAll(input, "igc_hist_1", "igc_unknown"))}
	body2 := session.BuildGeminiWireBody(&req2, "sess_img_hist2", nil, "high", store)
	b, _ := json.Marshal(body2.Contents)
	if strings.Contains(string(b), "image_paths") {
		t.Fatalf("unexpected image_paths for unknown item: %s", b)
	}
}
