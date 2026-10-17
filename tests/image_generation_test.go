package tests

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"comini/internal/domain"
	"comini/internal/schema"
	"comini/internal/session"
	"comini/internal/streaming"
)

func TestImageGenerationCompilation(t *testing.T) {
	tools := []domain.ToolDefinition{
		{
			Type: "image_generation",
			Name: "image_generation",
		},
	}

	decls, metaMap := schema.ExtractAndCompileTools(tools)
	if len(decls) != 1 {
		t.Fatalf("expected 1 declaration, got %d", len(decls))
	}

	decl := decls[0]
	if decl["name"] != "image_generation" {
		t.Errorf("expected name 'image_generation', got %v", decl["name"])
	}

	meta, ok := metaMap["image_generation"]
	if !ok || !meta.IsImageGen {
		t.Fatalf("expected meta.IsImageGen to be true, got %v", meta)
	}

	params, ok := decl["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters missing or invalid")
	}
	props, ok := params["properties"].(map[string]any)
	if !ok || props["prompt"] == nil {
		t.Errorf("expected 'prompt' property in image_generation schema")
	}
}

func TestImageGenerationStreamingMapping(t *testing.T) {
	mockGeminiSSE := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"image_generation","args":{"prompt":"A fluffy cat sitting in sunlight","aspect_ratio":"1:1"}}}]}}]}
data: [DONE]
`

	var out strings.Builder
	store := session.NewSignatureStore(time.Hour)
	toolMeta := map[string]domain.ToolMeta{
		"image_generation": {
			OriginalName:  "image_generation",
			CanonicalName: "image_generation",
			IsImageGen:    true,
		},
	}

	mockGenCalled := false
	mockGenFn := func(ctx context.Context, prompt, ar string) (string, string, error) {
		mockGenCalled = true
		if prompt != "A fluffy cat sitting in sunlight" {
			t.Errorf("unexpected prompt: %s", prompt)
		}
		if ar != "1:1" {
			t.Errorf("unexpected aspect ratio: %s", ar)
		}
		return "/tmp/fake_image.jpg", "data:image/jpeg;base64,dGVzdA==", nil
	}

	err := streaming.MapGeminiStreamToResponses(
		strings.NewReader(mockGeminiSSE),
		&out,
		nil,
		store,
		"resp_img_test",
		"gemini-3.8-flash",
		toolMeta,
		"sess_img_test",
		streaming.StreamOptions{
			Context:    context.Background(),
			ImageGenFn: mockGenFn,
		},
	)
	if err != nil {
		t.Fatalf("MapGeminiStreamToResponses returned error: %v", err)
	}

	if !mockGenCalled {
		t.Fatal("expected ImageGenFn to be invoked")
	}

	res := out.String()
	if !strings.Contains(res, `"type":"image_generation_call"`) {
		t.Errorf("missing image_generation_call in output: %s", res)
	}
	if !strings.Contains(res, `"status":"in_progress"`) {
		t.Errorf("missing in_progress event: %s", res)
	}
	if !strings.Contains(res, `"status":"completed"`) {
		t.Errorf("missing completed event: %s", res)
	}
	if !strings.Contains(res, `"result":"data:image/jpeg;base64,dGVzdA=="`) {
		t.Errorf("missing result dataURL: %s", res)
	}
	if !strings.Contains(res, `"saved_path":"/tmp/fake_image.jpg"`) {
		t.Errorf("missing saved_path: %s", res)
	}

	// Verify synthesized assistant message
	if !strings.Contains(res, `"type":"message"`) {
		t.Errorf("missing assistant message item in output: %s", res)
	}
	if !strings.Contains(res, `![A fluffy cat sitting in sunlight](/tmp/fake_image.jpg)`) {
		t.Errorf("missing markdown image tag in assistant message: %s", res)
	}
}

func TestImageGenerationAutonomousLoop(t *testing.T) {
	stream1 := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"image_generation","args":{"prompt":"Frame 01: Hero enters cave","aspect_ratio":"16:9"}}}]}}]}
`
	stream2 := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"image_generation","args":{"prompt":"Frame 02: Glowing crystal discovered","aspect_ratio":"16:9"}}}]}}]}
`
	stream3 := `data: {"candidates":[{"content":{"parts":[{"text":"Here are the storyboard frames:\n\n![Frame 01](/tmp/frame_01.jpg)\n\n![Frame 02](/tmp/frame_02.jpg)"}]}}]}
data: [DONE]
`

	var generatedPrompts []string
	mockGenFn := func(ctx context.Context, prompt, ar string) (string, string, error) {
		generatedPrompts = append(generatedPrompts, prompt)
		if prompt == "Frame 01: Hero enters cave" {
			return "/tmp/frame_01.jpg", "data:image/jpeg;base64,frame1", nil
		}
		return "/tmp/frame_02.jpg", "data:image/jpeg;base64,frame2", nil
	}

	continueCount := 0
	continueFn := func(ctx context.Context, modelPart domain.GeminiPart, obsText string) (io.ReadCloser, error) {
		continueCount++
		if continueCount == 1 {
			if !strings.Contains(obsText, "/tmp/frame_01.jpg") {
				t.Errorf("expected frame_01 in obsText, got: %s", obsText)
			}
			return io.NopCloser(strings.NewReader(stream2)), nil
		} else if continueCount == 2 {
			if !strings.Contains(obsText, "/tmp/frame_02.jpg") {
				t.Errorf("expected frame_02 in obsText, got: %s", obsText)
			}
			return io.NopCloser(strings.NewReader(stream3)), nil
		}
		return nil, io.EOF
	}

	var out strings.Builder
	store := session.NewSignatureStore(time.Hour)
	toolMeta := map[string]domain.ToolMeta{
		"image_generation": {
			OriginalName:  "image_generation",
			CanonicalName: "image_generation",
			IsImageGen:    true,
		},
	}

	err := streaming.MapGeminiStreamToResponses(
		strings.NewReader(stream1),
		&out,
		nil,
		store,
		"resp_loop_test",
		"gemini-3.8-flash",
		toolMeta,
		"sess_loop_test",
		streaming.StreamOptions{
			Context:    context.Background(),
			ImageGenFn: mockGenFn,
			ContinueFn: continueFn,
		},
	)
	if err != nil {
		t.Fatalf("MapGeminiStreamToResponses failed: %v", err)
	}

	if continueCount != 2 {
		t.Fatalf("expected ContinueFn to be called twice, got %d", continueCount)
	}
	if len(generatedPrompts) != 2 {
		t.Fatalf("expected 2 images generated, got %d", len(generatedPrompts))
	}

	res := out.String()

	// Both image generations should be completed (added + done + inside response.completed = 6)
	if strings.Count(res, `"type":"image_generation_call"`) != 6 {
		t.Errorf("expected 6 occurrences of image_generation_call, got %d", strings.Count(res, `"type":"image_generation_call"`))
	}
	if !strings.Contains(res, `"/tmp/frame_01.jpg"`) {
		t.Errorf("missing frame 1 path in output")
	}
	if !strings.Contains(res, `"/tmp/frame_02.jpg"`) {
		t.Errorf("missing frame 2 path in output")
	}

	// Final message should contain Gemini's summary text
	if !strings.Contains(res, "Here are the storyboard frames:") {
		t.Errorf("missing Gemini final message in output: %s", res)
	}
	if !strings.Contains(res, "event: response.completed") {
		t.Errorf("missing response.completed event in output")
	}

	// Verify only 1 assistant message item was added (added + done + response.completed = 3)
	if strings.Count(res, `"type":"message"`) != 3 {
		t.Errorf("expected exactly 1 assistant message created (3 occurrences), got: %d", strings.Count(res, `"type":"message"`))
	}
}

func TestImageGenerationAutonomousLoop_TerminalSynthesizedMessage(t *testing.T) {
	stream1 := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"image_generation","args":{"prompt":"Apple","aspect_ratio":"1:1"}}}]}}]}
`
	stream2 := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"image_generation","args":{"prompt":"Banana","aspect_ratio":"1:1"}}}]}}]}
`
	stream3 := `data: [DONE]
`

	mockGenFn := func(ctx context.Context, prompt, ar string) (string, string, error) {
		if prompt == "Apple" {
			return "/tmp/apple.jpg", "data:image/jpeg;base64,apple", nil
		}
		return "/tmp/banana.jpg", "data:image/jpeg;base64,banana", nil
	}

	continueCount := 0
	continueFn := func(ctx context.Context, modelPart domain.GeminiPart, obsText string) (io.ReadCloser, error) {
		continueCount++
		if continueCount == 1 {
			return io.NopCloser(strings.NewReader(stream2)), nil
		} else if continueCount == 2 {
			return io.NopCloser(strings.NewReader(stream3)), nil
		}
		return nil, io.EOF
	}

	var out strings.Builder
	store := session.NewSignatureStore(time.Hour)
	toolMeta := map[string]domain.ToolMeta{
		"image_generation": {
			OriginalName:  "image_generation",
			CanonicalName: "image_generation",
			IsImageGen:    true,
		},
	}

	err := streaming.MapGeminiStreamToResponses(
		strings.NewReader(stream1),
		&out,
		nil,
		store,
		"resp_term_test",
		"gemini-3.8-flash",
		toolMeta,
		"sess_term_test",
		streaming.StreamOptions{
			Context:    context.Background(),
			ImageGenFn: mockGenFn,
			ContinueFn: continueFn,
		},
	)
	if err != nil {
		t.Fatalf("MapGeminiStreamToResponses failed: %v", err)
	}

	res := out.String()
	// Terminal synthesis should have created a message containing both images
	if !strings.Contains(res, `![Apple](/tmp/apple.jpg)`) {
		t.Errorf("missing Apple image markdown in terminal synthesized message: %s", res)
	}
	if !strings.Contains(res, `![Banana](/tmp/banana.jpg)`) {
		t.Errorf("missing Banana image markdown in terminal synthesized message: %s", res)
	}
}

