package tests

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"comini/internal/domain"
	"comini/internal/session"
	"comini/internal/streaming"
)

var imageToolMeta = map[string]domain.ToolMeta{
	"image_generation": {OriginalName: "image_generation", CanonicalName: "image_generation", IsImageGen: true},
}

const imageGenStream = `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"image_generation","args":{"prompt":"A red fox","aspect_ratio":"1:1"}}}]}}]}
data: [DONE]
`

// When Codex declares view_image, the generated image must be handed back as a native
// view_image function_call (the only path Codex 0.163 renders), without looping inside the
// HTTP response and without shipping the multi-MB base64 payload again.
func TestImageGenerationHandsOffToViewImage(t *testing.T) {
	var out strings.Builder
	var continued int32
	err := streaming.MapGeminiStreamToResponses(
		strings.NewReader(imageGenStream), &out, nil,
		session.NewSignatureStore(time.Hour), "resp_handoff", "gemini-3.8-flash",
		imageToolMeta, "sess_handoff",
		streaming.StreamOptions{
			Context: context.Background(),
			ImageGenFn: func(ctx context.Context, a domain.ImageGenArgs) (string, string, error) {
				prompt, ar := a.Prompt, a.AspectRatio
				_, _ = prompt, ar
				return "/tmp/fox.jpg", "data:image/jpeg;base64,ZmFrZWltYWdlYmFzZTY0", nil
			},
			ContinueFn: func(ctx context.Context, p domain.GeminiPart, obs string) (io.ReadCloser, error) {
				atomic.AddInt32(&continued, 1)
				return nil, fmt.Errorf("must not be called")
			},
			CanViewImage: true,
		},
	)
	if err != nil {
		t.Fatalf("map error: %v", err)
	}
	res := out.String()

	if atomic.LoadInt32(&continued) != 0 {
		t.Fatal("ContinueFn must not run when the image is handed to Codex via view_image")
	}
	if !strings.Contains(res, `"name":"view_image"`) || !strings.Contains(res, `"arguments":"{\"path\":\"/tmp/fox.jpg\"}"`) {
		t.Fatalf("missing view_image function_call with saved path:\n%s", res)
	}
	if strings.Contains(res, "ZmFrZWltYWdlYmFzZTY0") {
		t.Fatal("base64 image payload must not be re-sent when view_image renders the image")
	}
	if strings.Contains(res, "![A red fox]") {
		t.Fatal("markdown fallback must not be emitted when view_image handles display")
	}
	if !strings.Contains(res, "Generating image") {
		t.Fatalf("expected a live status thought while the image renders:\n%s", res)
	}
	if !strings.Contains(res, "event: response.completed") || !strings.Contains(res, "data: [DONE]") {
		t.Fatal("stream must terminate normally so Codex can run view_image")
	}
	// The completed response must list the function_call so Codex dispatches it.
	completed := res[strings.Index(res, "event: response.completed"):]
	if !strings.Contains(completed, `"name":"view_image"`) {
		t.Fatalf("response.completed output must contain the view_image call:\n%s", completed)
	}
}

// Without view_image the legacy in-stream loop and markdown fallback still apply.
func TestImageGenerationLegacyPathWithoutViewImage(t *testing.T) {
	var out strings.Builder
	err := streaming.MapGeminiStreamToResponses(
		strings.NewReader(imageGenStream), &out, nil,
		session.NewSignatureStore(time.Hour), "resp_legacy", "gemini-3.8-flash",
		imageToolMeta, "sess_legacy",
		streaming.StreamOptions{
			Context: context.Background(),
			ImageGenFn: func(ctx context.Context, a domain.ImageGenArgs) (string, string, error) {
				prompt, ar := a.Prompt, a.AspectRatio
				_, _ = prompt, ar
				return "/tmp/fox.jpg", "data:image/jpeg;base64,dGVzdA==", nil
			},
		},
	)
	if err != nil {
		t.Fatalf("map error: %v", err)
	}
	res := out.String()
	if strings.Contains(res, `"name":"view_image"`) {
		t.Fatal("view_image must not be emitted when the client did not declare it")
	}
	if !strings.Contains(res, "![A red fox](/tmp/fox.jpg)") {
		t.Fatalf("legacy markdown fallback missing:\n%s", res)
	}
}

// A silent upstream (e.g. a very large tool call being generated) must not leave the client
// connection idle: the mapper emits SSE comment frames.
func TestStreamEmitsKeepaliveWhileUpstreamIsSilent(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		time.Sleep(1500 * time.Millisecond)
		_, _ = io.WriteString(pw, `data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`+"\n")
		_ = pw.Close()
	}()

	var out strings.Builder
	err := streaming.MapGeminiStreamToResponses(
		pr, &out, nil, session.NewSignatureStore(time.Hour), "resp_ka", "gemini-3.8-flash",
		nil, "sess_ka",
		streaming.StreamOptions{KeepaliveInterval: 100 * time.Millisecond},
	)
	if err != nil {
		t.Fatalf("map error: %v", err)
	}
	res := out.String()
	// Generous bounds: this must hold even when the machine is loaded (e.g. under -race).
	if n := strings.Count(res, ": keepalive\n\n"); n < 1 {
		t.Fatalf("expected keepalive frames during 1.5s of upstream silence, got %d", n)
	}
	if !strings.Contains(res, `"delta":"hello"`) {
		t.Fatal("text delta must still be delivered after the silent period")
	}
}

// Evicting oldest images one at a time rewrote early history on every new image and dropped the
// KV prefix cache each turn. Eviction must now move in batches: between boundaries the set of
// evicted images is identical, so the prompt prefix is byte-stable.
func TestEvictStaleImagesIsBatched(t *testing.T) {
	build := func(n int) []int {
		store := session.NewSignatureStore(time.Hour)
		items := []domain.InputItem{{Type: "message", Role: "user", Text: "inspect"}}
		img := strings.Repeat("A", 30000)
		for i := 1; i <= n; i++ {
			id := fmt.Sprintf("call_%d", i)
			store.Set(id, "sig", "view_image")
			items = append(items,
				domain.InputItem{Type: "function_call", Name: "view_image", CallID: id, Arguments: fmt.Sprintf(`{"step":%d}`, i)},
				domain.InputItem{Type: "function_call_output", CallID: id,
					Output: []byte(fmt.Sprintf(`[{"type":"input_image","image_url":"data:image/jpeg;base64,%s"}]`, img))},
			)
		}
		body := session.BuildGeminiWireBodyWithItems(&domain.ResponsesRequest{Model: "gemini-3.8-flash"}, items, "sess_evict", nil, "high", store)
		var evicted []int
		idx := 0
		for _, c := range body.Contents {
			for _, p := range c.Parts {
				if p.InlineData != nil {
					idx++
				} else if strings.Contains(p.Text, "previously inspected and processed") {
					idx++
					evicted = append(evicted, idx)
				}
			}
		}
		return evicted
	}

	base := build(17)
	if len(base) != 8 {
		t.Fatalf("17 images: expected one batch of 8 evicted, got %v", base)
	}
	for n := 18; n <= 24; n++ {
		got := build(n)
		if fmt.Sprint(got) != fmt.Sprint(base) {
			t.Fatalf("%d images: evicted set changed inside a batch (%v vs %v) -> prefix cache would break", n, got, base)
		}
	}
	if got := build(25); len(got) != 16 {
		t.Fatalf("25 images: expected next batch (16 evicted), got %v", got)
	}
}

// Codex replays the hand-off as image_generation_call + function_call(view_image) +
// function_call_output(input_image). The rebuilt Gemini prompt must stay valid (alternating roles,
// ending on a user turn) and the model must actually receive the pixels.
func TestViewImageHandoffReplayBuildsValidGeminiPrompt(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	b64 := base64.StdEncoding.EncodeToString(png)
	items := []domain.InputItem{
		{Type: "message", Role: "user", Text: "draw a red fox"},
		{Type: "image_generation_call", ID: "igc_1", Status: "completed", RevisedPrompt: "A red fox"},
		{Type: "function_call", Name: "view_image", CallID: "call_v1", Arguments: `{"path":"/tmp/fox.png"}`},
		{Type: "function_call_output", CallID: "call_v1",
			Output: []byte(fmt.Sprintf(`[{"type":"input_image","image_url":"data:image/png;base64,%s"}]`, b64))},
	}
	body := session.BuildGeminiWireBodyWithItems(&domain.ResponsesRequest{Model: "gemini-3.8-flash"}, items, "sess_replay", nil, "high", session.NewSignatureStore(time.Hour))

	if len(body.Contents) == 0 || body.Contents[0].Role != "user" || body.Contents[len(body.Contents)-1].Role != "user" {
		t.Fatalf("prompt must start and end on a user turn: %+v", body.Contents)
	}
	for i := 1; i < len(body.Contents); i++ {
		if body.Contents[i].Role == body.Contents[i-1].Role {
			t.Fatalf("consecutive %q turns at %d", body.Contents[i].Role, i)
		}
	}
	hasImage, hasPrompt := false, false
	for _, c := range body.Contents {
		for _, p := range c.Parts {
			if p.InlineData != nil && len(p.InlineData.Data) > 0 {
				hasImage = true
			}
			if strings.Contains(p.Text, "A red fox") {
				hasPrompt = true
			}
		}
	}
	if !hasImage {
		t.Fatal("the generated image must reach the model as inlineData")
	}
	if !hasPrompt {
		t.Fatal("the model must see its own image prompt in history")
	}
}
