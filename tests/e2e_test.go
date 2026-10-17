package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"comini/internal/config"
	"comini/internal/domain"
	"comini/internal/server"
	"comini/internal/session"
	"comini/internal/transport"
)

func TestModelCatalogWithReasoningLadder(t *testing.T) {
	cfg := config.Load()
	ccaClient := transport.NewCCAClient("http://dummy", "antigravity")
	store := session.DefaultSignatureStore
	handlers := server.NewHandlers(cfg, ccaClient, store)
	router := server.NewRouter(handlers)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", rec.Code)
	}

	var cat domain.ModelCatalogResponse
	if err := json.NewDecoder(rec.Body).Decode(&cat); err != nil {
		t.Fatalf("decode catalog error: %v", err)
	}

	if len(cat.Data) == 0 {
		t.Fatalf("catalog must not be empty")
	}

	pro := cat.Data[0]
	if pro.ID != "google-antigravity/gemini-3.8-flash" {
		t.Fatalf("expected google-antigravity/gemini-3.8-flash, got %s", pro.ID)
	}

	// Verify supported_reasoning_levels contains 3-rung ladder (low, medium, high)
	if len(pro.SupportedReasoningLevels) != 3 {
		t.Fatalf("expected 3 reasoning rungs, got %d", len(pro.SupportedReasoningLevels))
	}

	rungs := make(map[string]bool)
	for _, l := range pro.SupportedReasoningLevels {
		rungs[l.Effort] = true
	}

	expectedRungs := []string{"low", "medium", "high"}
	for _, exp := range expectedRungs {
		if !rungs[exp] {
			t.Fatalf("missing expected rung: %s", exp)
		}
	}
}

func TestE2EResponsesImageAndToolLoop(t *testing.T) {
	// Mock CCA server that responds with SSE
	mockCCA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Analyzing image...\",\"thought\":true,\"thoughtSignature\":\"sig_img_e2e\"}]}}]}}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"The image has contrast issue.\"}]}}]}}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer mockCCA.Close()

	cfg := config.Load()
	ccaClient := transport.NewCCAClient(mockCCA.URL, "antigravity")
	store := session.NewSignatureStore(time.Hour)
	handlers := server.NewHandlers(cfg, ccaClient, store)
	router := server.NewRouter(handlers)

	inputJSON := `{
		"model": "google-antigravity/gemini-3.7-flash",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": "Analyze this screenshot: data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
			}
		]
	}`

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(inputJSON))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: response.reasoning_summary_text.delta") {
		t.Fatalf("missing reasoning summary text delta")
	}
	if !strings.Contains(body, "event: response.output_text.delta") {
		t.Fatalf("missing output text delta")
	}
	if !strings.Contains(body, "The image has contrast issue.") {
		t.Fatalf("missing answer text")
	}
}

func TestResolveModelAndEffortLadder(t *testing.T) {
	cases := []struct {
		model         string
		effort        string
		expectedWire  string
		expectedLevel string
	}{
		// 3.8 Flash (wire suffix encodes effort)
		{"gemini-3.8-flash", "low", "gemini-3.8-flash-low", ""},
		{"gemini-3.8-flash", "medium", "gemini-3.8-flash-medium", ""},
		{"gemini-3.8-flash", "high", "gemini-3.8-flash-high", ""},
		{"google-antigravity/gemini-3.8-flash", "high", "gemini-3.8-flash-high", ""},
		{"gemini-3.8-flash-low", "", "gemini-3.8-flash-low", ""},
		{"gemini-3.8-flash-medium", "", "gemini-3.8-flash-medium", ""},

		// 3.7 Flash (thinkingLevel encodes effort)
		{"gemini-3.7-flash", "low", "gemini-3.7-flash-low", "low"},
		{"gemini-3.7-flash", "medium", "gemini-3.7-flash-medium", "medium"},
		{"gemini-3.7-flash", "high", "gemini-3.7-flash-high", "high"},
		{"google-antigravity/gemini-3.7-flash", "", "gemini-3.7-flash-high", "high"},

		// 3.1 Pro (low -> gemini-3.1-pro-low, high/medium -> gemini-pro-agent)
		{"gemini-3.1-pro", "low", "gemini-3.1-pro-low", "low"},
		{"gemini-3.1-pro", "medium", "gemini-pro-agent", "medium"},
		{"gemini-3.1-pro", "high", "gemini-pro-agent", "high"},

		// Claude models (4.6 legacy + 5.5 active)
		{"claude-sonnet-4-6", "low", "claude-sonnet-4-6", "low"},
		{"claude-sonnet-4-6", "high", "claude-sonnet-4-6", "high"},
		{"claude-opus-4-6-thinking", "high", "claude-opus-4-6-thinking", "high"},
		{"google-antigravity/claude-sonnet-5-5", "low", "claude-sonnet-5-5-low", ""},
		{"google-antigravity/claude-sonnet-5-5", "medium", "claude-sonnet-5-5-medium", ""},
		{"google-antigravity/claude-sonnet-5-5", "high", "claude-sonnet-5-5-high", ""},
		{"google-antigravity/claude-opus-5-5-thinking", "low", "claude-opus-5-5-low", ""},
		{"google-antigravity/claude-opus-5-5-thinking", "high", "claude-opus-5-5-high", ""},

		// Gemini 4 Argon (1:1 3-rung ladder matching Antigravity)
		{"google-antigravity/gemini-4-argon", "low", "gemini-4-argon-low", ""},
		{"google-antigravity/gemini-4-argon", "medium", "gemini-4-argon-medium", ""},
		{"google-antigravity/gemini-4-argon", "high", "gemini-4-argon-high", ""},
	}

	for _, tc := range cases {
		res := transport.ResolveModelAndEffort(tc.model, tc.effort)
		if res.WireModelID != tc.expectedWire {
			t.Errorf("model=%q effort=%q: expected wireModel=%q, got %q", tc.model, tc.effort, tc.expectedWire, res.WireModelID)
		}
		if res.ThinkingLevel != tc.expectedLevel {
			t.Errorf("model=%q effort=%q: expected thinkingLevel=%q, got %q", tc.model, tc.effort, tc.expectedLevel, res.ThinkingLevel)
		}
	}
}
