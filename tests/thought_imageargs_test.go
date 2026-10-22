package tests

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"comini/internal/domain"
	"comini/internal/schema"
	"comini/internal/session"
	"comini/internal/streaming"
	"comini/internal/transport"
)

// Each Gemini thought block must produce a clean summary headline for the 1-line shimmer title,
// carry the raw reasoning text in reasoning_text, and emit a commentary message so that Codex
// Desktop renders the thought block visibly and permanently in the conversation timeline.
func TestThoughtStreamsAsNativeSummaryPartsWithCommentary(t *testing.T) {
	in := `data: {"candidates":[{"content":{"parts":[{"thought":true,"text":"**Planning**\nI will draw a fox."}]}}]}
data: {"candidates":[{"content":{"parts":[{"thought":true,"text":"**Checking**\nThe tools are ready."}]}}]}
data: {"candidates":[{"content":{"parts":[{"text":"Done."}]}}]}
`
	var out strings.Builder
	if err := streaming.MapGeminiStreamToResponses(strings.NewReader(in), &out, nil,
		session.NewSignatureStore(time.Hour), "resp_th", "gemini-4-argon", nil, "s"); err != nil {
		t.Fatal(err)
	}
	res := out.String()

	if !strings.Contains(res, `"phase":"commentary"`) {
		t.Fatalf("thought must be emitted as a commentary message for timeline visibility:\n%s", res)
	}
	if strings.Count(res, "event: response.reasoning_summary_part.added") != 2 {
		t.Fatalf("expected one summary part per thought block (2):\n%s", res)
	}
	if !strings.Contains(res, `"summary_index":1`) {
		t.Fatal("second thought block must use summary_index 1")
	}
	if !strings.Contains(res, `"delta":"**Planning**"`) {
		t.Fatalf("heading delta must be concise bold headline:\n%s", res)
	}
	completed := res[strings.Index(res, "event: response.completed"):]
	if !strings.Contains(completed, `"summary":[{"text":"**Planning**","type":"summary_text"},{"text":"**Checking**","type":"summary_text"}]`) {
		t.Fatalf("completed reasoning item must carry concise summary headlines:\n%s", completed)
	}
}

// When Gemini streams raw natural-language reasoning (without markdown bold headings), Comini must
// automatically synthesize a concise bold heading (e.g. "**Initial verification is needed**")
// so that Codex Desktop's title span bar renders a clean 1-line title, and emits commentary for the timeline.
func TestRawGeminiThoughtGeneratesConciseBoldHeading(t *testing.T) {
	in := `data: {"candidates":[{"content":{"parts":[{"thought":true,"text":"Initial verification is needed regarding the precise Kakeya conjecture status in R^3. Requires careful review of literature."}]}}]}
data: {"candidates":[{"content":{"parts":[{"text":"Done."}]}}]}
`
	var out strings.Builder
	if err := streaming.MapGeminiStreamToResponses(strings.NewReader(in), &out, nil,
		session.NewSignatureStore(time.Hour), "resp_raw_th", "gemini-3.8-flash", nil, "s"); err != nil {
		t.Fatal(err)
	}
	res := out.String()

	if !strings.Contains(res, `"delta":"**Initial verification is needed**"`) {
		t.Fatalf("summary delta must be concise bold headline:\n%s", res)
	}
	// Content part should carry raw reasoning text
	if !strings.Contains(res, `"delta":"Initial verification is needed regarding the precise Kakeya conjecture status in R^3. Requires careful review of literature."`) {
		t.Fatalf("reasoning_text.delta must carry raw reasoning text:\n%s", res)
	}
	// Commentary message must be emitted with full text
	if !strings.Contains(res, `"phase":"commentary"`) {
		t.Fatalf("commentary message must be emitted for timeline:\n%s", res)
	}
}

// In an agentic multi-step turn, a reasoning block before a tool call must close cleanly with its
// own summary heading, followed by the tool call, followed by subsequent reasoning blocks and the
// final answer, each maintaining separate reasoning IDs.
func TestMultiStepReasoningWithToolCallSequence(t *testing.T) {
	in := `data: {"candidates":[{"content":{"parts":[{"thought":true,"text":"Searching the repository files to locate main server entry point."}]}}]}
data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"run_command","args":{"command":"ls -la"}}}]}}]}
`
	var out strings.Builder
	if err := streaming.MapGeminiStreamToResponses(strings.NewReader(in), &out, nil,
		session.NewSignatureStore(time.Hour), "resp_multi", "gemini-3.8-flash", map[string]domain.ToolMeta{
			"run_command": {OriginalName: "run_command"},
		}, "s"); err != nil {
		t.Fatal(err)
	}
	res := out.String()

	// Must contain reasoning item done before function_call added
	rsDoneIdx := strings.Index(res, `event: response.output_item.done` + "\n" + `data: {"item":{"content":[{"text":"Searching the repository files to locate main server entry point.","type":"reasoning_text"}],"id":"rs_`)
	if rsDoneIdx == -1 {
		t.Fatalf("reasoning item must complete before tool call:\n%s", res)
	}
	fcAddedIdx := strings.Index(res, `event: response.output_item.added` + "\n" + `data: {"item":{"arguments":"","call_id":"call_`)
	if fcAddedIdx == -1 {
		t.Fatalf("function call must be emitted:\n%s", res)
	}
	if rsDoneIdx > fcAddedIdx {
		t.Fatalf("reasoning done (%d) must precede function call added (%d)", rsDoneIdx, fcAddedIdx)
	}
}

func TestParseImageGenArgsMatchesAGYSchema(t *testing.T) {
	got := domain.ParseImageGenArgs(map[string]any{
		"Prompt":      "edit this",
		"ImageName":   "login_page_mockup",
		"AspectRatio": "16:9",
		"ImagePaths":  []any{"/a.png", "/b.png", "/c.png", "/d.png"},
	})
	if got.Prompt != "edit this" || got.ImageName != "login_page_mockup" || got.AspectRatio != "16:9" {
		t.Fatalf("PascalCase args not parsed: %+v", got)
	}
	if len(got.ImagePaths) != domain.MaxImageReferencePaths {
		t.Fatalf("ImagePaths must be capped at %d (AGY maxItems=3), got %d", domain.MaxImageReferencePaths, len(got.ImagePaths))
	}

	snake := domain.ParseImageGenArgs(map[string]any{"prompt": "p", "aspect_ratio": "21:9", "image_paths": "/only.png"})
	if snake.AspectRatio != "" {
		t.Fatalf("unsupported aspect ratio must fall back to default, got %q", snake.AspectRatio)
	}
	if len(snake.ImagePaths) != 1 || snake.ImagePaths[0] != "/only.png" {
		t.Fatalf("single string image_paths not accepted: %+v", snake.ImagePaths)
	}
	if legacy := domain.ParseImageGenArgs(map[string]any{"description": "old style"}); legacy.Prompt != "old style" {
		t.Fatal("legacy description alias must still work")
	}
}

func TestImageToolSchemaExposesAGYParameters(t *testing.T) {
	decls, _ := schema.ExtractAndCompileTools([]domain.ToolDefinition{{Type: "image_generation", Name: "image_generation"}})
	props := decls[0]["parameters"].(map[string]any)["properties"].(map[string]any)
	for _, k := range []string{"prompt", "image_name", "aspect_ratio", "image_paths"} {
		if props[k] == nil {
			t.Fatalf("image tool schema missing %q (AGY generate_image parameter)", k)
		}
	}
	if props["image_paths"].(map[string]any)["type"] != "ARRAY" {
		t.Fatal("image_paths must be an array")
	}
	if enum, _ := props["aspect_ratio"].(map[string]any)["enum"].([]string); len(enum) != len(domain.SupportedImageAspectRatios) {
		t.Fatalf("aspect_ratio enum must list AGY's supported values, got %v", enum)
	}
}

// The image request must carry reference images before the prompt and send the aspect ratio as
// generationConfig.imageConfig (verified live: 16:9 -> 1376x768, 9:16 -> 768x1376).
func TestGenerateImageSendsReferencesAndImageConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ref := filepath.Join(t.TempDir(), "ref.png")
	pngBytes := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	if err := os.WriteFile(ref, pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		out := base64.StdEncoding.EncodeToString([]byte("fakejpegbytes"))
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/jpeg","data":"` + out + `"}}]}}]}}`))
	}))
	defer srv.Close()

	c := transport.NewCCAClient(srv.URL, "antigravity")
	path, _, err := c.GenerateImage(context.Background(), domain.ImageGenArgs{
		Prompt: "make it night", ImageName: "Fox Night!", AspectRatio: "9:16", ImagePaths: []string{ref},
	}, "Bearer x", "proj")
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(path), "fox_night_") {
		t.Fatalf("saved file should be named from image_name, got %s", path)
	}

	req := got["request"].(map[string]any)
	parts := req["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	if len(parts) != 2 {
		t.Fatalf("expected [reference image, prompt], got %d parts", len(parts))
	}
	if parts[0].(map[string]any)["inlineData"] == nil || parts[1].(map[string]any)["text"] != "make it night" {
		t.Fatalf("reference image must precede the prompt: %v", parts)
	}
	if mt := parts[0].(map[string]any)["inlineData"].(map[string]any)["mimeType"]; mt != "image/png" {
		t.Fatalf("reference mime type must be detected, got %v", mt)
	}
	cfg := req["generationConfig"].(map[string]any)
	if cfg["imageConfig"].(map[string]any)["aspectRatio"] != "9:16" {
		t.Fatalf("aspect ratio must be sent as imageConfig, got %v", cfg)
	}
	if got["model"] != "gemini-3.1-flash-image" {
		t.Fatalf("unexpected image model %v", got["model"])
	}

	// A missing reference must fail clearly instead of silently generating without it.
	if _, _, err := c.GenerateImage(context.Background(), domain.ImageGenArgs{Prompt: "x", ImagePaths: []string{"/nonexistent/zzz.png"}}, "Bearer x", "proj"); err == nil ||
		!strings.Contains(err.Error(), "reference image not found") {
		t.Fatalf("expected a clear missing-reference error, got %v", err)
	}
}
