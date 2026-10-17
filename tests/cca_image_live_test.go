package tests

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"comini/internal/domain"
	"comini/internal/transport"
)

func TestLiveCCAImageAnalysis(t *testing.T) {
	if os.Getenv("COMINI_LIVE_TESTS") != "1" {
		t.Skip("Skipping live CCA network test (set COMINI_LIVE_TESTS=1 to run)")
	}
	authMgr := transport.DefaultAuthManager
	token, proj, err := authMgr.GetCredentials()
	if err != nil || token == "" {
		t.Skipf("Skipping live CCA test: no auth token available (%v)", err)
	}

	client := transport.NewCCAClient("", "antigravity")

	// 1x1 red PNG
	redPngBytes := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE, 0x00, 0x00, 0x00,
		0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xDD, 0x8D, 0xB0, 0x00, 0x00, 0x00,
		0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
	}
	b64Img := base64.StdEncoding.EncodeToString(redPngBytes)

	// Test 1: with current domain struct (inline_data / mime_type)
	body := domain.GeminiWireBody{
		Contents: []domain.GeminiContent{
			{
				Role: "user",
				Parts: []domain.GeminiPart{
					{
						InlineData: &domain.GeminiInlineData{
							MimeType: "image/png",
							Data:     b64Img,
						},
					},
					{
						Text: "What color is this 1x1 pixel image? Reply with just the color name in lowercase.",
					},
				},
			},
		},
	}

	resp, err := client.StreamGenerateContent(context.Background(), "gemini-2.5-flash", body, token, proj)
	if err != nil {
		t.Fatalf("CCA request failed: %v", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	respStr := string(respBytes)
	fmt.Printf("Live CCA Response: %s\n", respStr)

	// Test 2: gemini-3.8-flash-high
	body38 := domain.GeminiWireBody{
		Contents: body.Contents,
	}
	resp38, err := client.StreamGenerateContent(context.Background(), "gemini-3.8-flash-high", body38, token, proj)
	if err != nil {
		if strings.Contains(err.Error(), "429") {
			t.Skipf("Live quota temporarily exhausted for 3.8: %v", err)
		}
		t.Fatalf("CCA request 3.8 failed: %v", err)
	}
	defer resp38.Body.Close()

	resp38Bytes, _ := io.ReadAll(resp38.Body)
	fmt.Printf("Live CCA 3.8 Response: %s\n", string(resp38Bytes))
}

func TestProbeAllCCAModelIDs(t *testing.T) {
	if os.Getenv("COMINI_LIVE_TESTS") != "1" {
		t.Skip("Skipping live CCA network test (set COMINI_LIVE_TESTS=1 to run)")
	}
	authMgr := transport.DefaultAuthManager
	token, proj, err := authMgr.GetCredentials()
	if err != nil || token == "" {
		t.Skipf("Skipping live CCA test: no auth token available (%v)", err)
	}

	client := transport.NewCCAClient("", "antigravity")

	candidateModels := []string{
		"gemini-3.8-flash-high",
		"gemini-3.8-flash-medium",
		"gemini-3.8-flash-low",
		"gemini-4-argon-high",
		"gemini-4-argon-medium",
		"gemini-4-argon-low",
		"claude-sonnet-5-5-high",
		"claude-opus-5-5-high",
		"gpt-oss-120b-medium",
	}

	body := domain.GeminiWireBody{
		Contents: []domain.GeminiContent{
			{
				Role:  "user",
				Parts: []domain.GeminiPart{{Text: "Hi"}},
			},
		},
	}

	for _, model := range candidateModels {
		resp, err := client.StreamGenerateContent(context.Background(), model, body, token, proj)
		if err != nil {
			fmt.Printf("❌ Model %s -> ERROR: %v\n", model, err)
		} else {
			respBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			fmt.Printf("✅ Model %s -> SUCCESS (%d bytes)\n", model, len(respBytes))
		}
	}
}

