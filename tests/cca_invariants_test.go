package tests

import (
	"context"
	"fmt"
	"io"
	"os"
	"testing"

	"comini/internal/domain"
	"comini/internal/transport"
)

func TestLiveCCAInvariants(t *testing.T) {
	if os.Getenv("COMINI_LIVE_TESTS") != "1" {
		t.Skip("Skipping live CCA network test (set COMINI_LIVE_TESTS=1 to run)")
	}
	authMgr := transport.DefaultAuthManager
	token, proj, err := authMgr.GetCredentials()
	if err != nil || token == "" {
		t.Skipf("Skipping live CCA test: no auth token available (%v)", err)
	}

	client := transport.NewCCAClient("", "antigravity")

	testCases := []struct {
		name     string
		contents []domain.GeminiContent
		tools    []domain.GeminiTool
	}{
		{
			name: "Standard function call with response",
			contents: []domain.GeminiContent{
				{
					Role:  "user",
					Parts: []domain.GeminiPart{{Text: "What is the weather?"}},
				},
				{
					Role: "model",
					Parts: []domain.GeminiPart{
						{
							FunctionCall: &domain.GeminiFunctionCall{
								Name: "get_weather",
								Args: map[string]any{"location": "Tokyo"},
							},
						},
					},
				},
				{
					Role: "user",
					Parts: []domain.GeminiPart{
						{
							FunctionResponse: &domain.GeminiFunctionResponse{
								Name:     "get_weather",
								Response: map[string]any{"temp": "22C"},
							},
						},
					},
				},
			},
			tools: []domain.GeminiTool{
				{
					FunctionDeclarations: []map[string]any{
						{
							"name":        "get_weather",
							"description": "Get weather info",
							"parameters": map[string]any{
								"type": "OBJECT",
								"properties": map[string]any{
									"location": map[string]any{"type": "STRING"},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "Model FunctionCall followed by User text instead of FunctionResponse",
			contents: []domain.GeminiContent{
				{
					Role:  "user",
					Parts: []domain.GeminiPart{{Text: "What is the weather?"}},
				},
				{
					Role: "model",
					Parts: []domain.GeminiPart{
						{
							FunctionCall: &domain.GeminiFunctionCall{
								Name: "get_weather",
								Args: map[string]any{"location": "Tokyo"},
							},
						},
					},
				},
				{
					Role:  "user",
					Parts: []domain.GeminiPart{{Text: "Nevermind, what is 2+2?"}},
				},
			},
			tools: []domain.GeminiTool{
				{
					FunctionDeclarations: []map[string]any{
						{
							"name":        "get_weather",
							"description": "Get weather info",
						},
					},
				},
			},
		},
		{
			name: "User FunctionResponse without preceding Model FunctionCall",
			contents: []domain.GeminiContent{
				{
					Role:  "user",
					Parts: []domain.GeminiPart{{Text: "What is the weather?"}},
				},
				{
					Role:  "model",
					Parts: []domain.GeminiPart{{Text: "Checking..."}},
				},
				{
					Role: "user",
					Parts: []domain.GeminiPart{
						{
							FunctionResponse: &domain.GeminiFunctionResponse{
								Name:     "get_weather",
								Response: map[string]any{"temp": "22C"},
							},
						},
					},
				},
			},
		},
		{
			name: "Model FunctionCall with valid thoughtSignature and matching User FunctionResponse",
			contents: []domain.GeminiContent{
				{
					Role:  "user",
					Parts: []domain.GeminiPart{{Text: "What is 2+2?"}},
				},
				{
					Role: "model",
					Parts: []domain.GeminiPart{
						{
							FunctionCall: &domain.GeminiFunctionCall{
								Name: "exec_command",
								Args: map[string]any{"cmd": "echo 4"},
							},
							ThoughtSignature: "ErQCCrECAWkUfRP871OL8t0hdb1kk9n/ETC7k7Gji19N9uFNU27ZasyJ5ne2B/xV/pS90kTrHJLsEDFiD5rCcCJk2mCf5+AAAGH3AqxdpaBZU4eXd/LW4JEDbqyBKpbzb62fdLPZNoqlnJsSqBfUkUvX5VgKWLGmF5KlbeJX3Nok3ihO+zxZE70C/gHoAa+0F5VcoTTgR59oClJ+oymsJqPPKsXl5Xwn/YpbSPJSTXMzuIWjDZbFQ9FMruHukIiWyzbzJFixL7wPByUHSD7f/sExoy7MrySRmsD7xGFiRCcSryvv5Dh6TjeLdwhl/uBaTwMNM/gf+glyc3RhSFMfG9UKusHx8rUSmAGpJuwuLKLGrBBlolTRJ993coTasGD7UA3jzuwF+ag2reY5MmGx3zAqWBBzVlo=",
						},
					},
				},
				{
					Role: "user",
					Parts: []domain.GeminiPart{
						{
							FunctionResponse: &domain.GeminiFunctionResponse{
								Name:     "exec_command",
								Response: map[string]any{"output": "4\n"},
							},
						},
					},
				},
			},
			tools: []domain.GeminiTool{
				{
					FunctionDeclarations: []map[string]any{
						{
							"name":        "exec_command",
							"description": "run cmd",
						},
					},
				},
			},
		},
		{
			name: "Model Action Text and User Observation Text fallback when no signature",
			contents: []domain.GeminiContent{
				{
					Role:  "user",
					Parts: []domain.GeminiPart{{Text: "What is 2+2?"}},
				},
				{
					Role:  "model",
					Parts: []domain.GeminiPart{{Text: "[Action: called exec_command with {\"cmd\":\"echo 4\"}]"}},
				},
				{
					Role:  "user",
					Parts: []domain.GeminiPart{{Text: "[Observation: exec_command returned {\"output\":\"4\\n\"}]"}},
				},
			},
			tools: []domain.GeminiTool{
				{
					FunctionDeclarations: []map[string]any{
						{
							"name":        "exec_command",
							"description": "run cmd",
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			body := domain.GeminiWireBody{
				Contents: tc.contents,
				Tools:    tc.tools,
				GenerationConfig: &domain.GeminiGenerationConfig{
					ThinkingConfig: &domain.GeminiThinkingConfig{
						ThinkingLevel:   "high",
						IncludeThoughts: true,
					},
				},
			}
			resp, err := client.StreamGenerateContent(context.Background(), "gemini-3.8-flash-high", body, token, proj)
			if err != nil {
				fmt.Printf("❌ [%s] Failed: %v\n", tc.name, err)
			} else {
				defer resp.Body.Close()
				b, _ := io.ReadAll(resp.Body)
				fmt.Printf("✅ [%s] Succeeded (%d bytes output)\n", tc.name, len(b))
			}
		})
	}
}
