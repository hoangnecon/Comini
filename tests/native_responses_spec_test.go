package tests

import (
	"bufio"
	"bytes"
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
	"comini/internal/streaming"
	"comini/internal/transport"
)

// TestNativeRateLimitHeadersOnResponsesEndpoint tests that Comini injects standard
// OpenAI and Codex Responses API rate-limit headers into the HTTP response.
func TestNativeRateLimitHeadersOnResponsesEndpoint(t *testing.T) {
	// Mock CCA server that streams a minimal SSE response
	mockCCA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello!\"}]}}]}\n\n"))
	}))
	defer mockCCA.Close()

	cfg := &config.Config{
		CCABaseURL: mockCCA.URL,
		UserAgent:  "test-agent",
	}
	ccaClient := transport.NewCCAClient(mockCCA.URL, "test-agent")
	sigStore := session.NewSignatureStore(time.Hour)
	handlers := server.NewHandlers(cfg, ccaClient, sigStore)

	reqPayload := domain.ResponsesRequest{
		Model: "google-antigravity/gemini-3.8-flash",
		Input: json.RawMessage(`[{"type":"user_message","role":"user","content":"Hi"}]`),
	}
	bodyBytes, _ := json.Marshal(reqPayload)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(bodyBytes))
	rec := httptest.NewRecorder()

	handlers.HandleResponses(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", res.StatusCode)
	}

	// Verify standard OpenAI rate-limit headers
	headers := []string{
		"x-ratelimit-limit-tokens",
		"x-ratelimit-remaining-tokens",
		"x-ratelimit-reset-tokens",
		"x-ratelimit-limit-requests",
		"x-ratelimit-remaining-requests",
		"x-ratelimit-reset-requests",
		"x-ratelimit-limit-tokens-usage-based",
		"x-ratelimit-remaining-tokens-usage-based",
		"x-codex-primary-used-percent",
		"x-codex-primary-window-minutes",
	}

	for _, h := range headers {
		val := res.Header.Get(h)
		if val == "" {
			t.Errorf("missing expected header %q", h)
		}
	}
}

// TestNativeTokenCountsInResponseCompletedEvent verifies that the terminal response.completed
// SSE event includes the exact flat token count fields required by Codex Rust app-server.
func TestNativeTokenCountsInResponseCompletedEvent(t *testing.T) {
	mockGeminiSSE := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello world\"}]}}],\"usageMetadata\":{\"promptTokenCount\":150000,\"candidatesTokenCount\":250,\"totalTokenCount\":150250,\"cachedContentTokenCount\":148000}}\n\n"

	var out bytes.Buffer
	store := session.NewSignatureStore(time.Hour)

	err := streaming.MapGeminiStreamToResponses(
		strings.NewReader(mockGeminiSSE),
		&out,
		nil,
		store,
		"resp_native_test",
		"gemini-3.8-flash",
		nil,
		"sess_native_test",
	)
	if err != nil {
		t.Fatalf("streaming error: %v", err)
	}

	body := out.String()
	scanner := bufio.NewScanner(strings.NewReader(body))
	var lastCompletedEvent map[string]any

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			var event map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err == nil {
				if event["type"] == "response.completed" {
					lastCompletedEvent = event
				}
			}
		}
	}

	if lastCompletedEvent == nil {
		t.Fatal("response.completed event not found in stream")
	}

	respObj, ok := lastCompletedEvent["response"].(map[string]any)
	if !ok {
		t.Fatal("missing response object in response.completed")
	}

	usage, ok := respObj["usage"].(map[string]any)
	if !ok {
		t.Fatal("missing usage object in response.completed")
	}

	// Verify both flat native fields and nested OpenAI fields
	expectedFields := []string{
		"input_token_count",
		"output_token_count",
		"cached_token_count",
		"reasoning_token_count",
		"total_token_count",
		"input_tokens",
		"output_tokens",
		"total_tokens",
	}

	for _, f := range expectedFields {
		if _, exists := usage[f]; !exists {
			t.Errorf("missing expected usage field %q", f)
		}
	}

	if usage["input_token_count"].(float64) != 150000 {
		t.Errorf("expected input_token_count 150000, got %v", usage["input_token_count"])
	}
	if usage["cached_token_count"].(float64) != 148000 {
		t.Errorf("expected cached_token_count 148000, got %v", usage["cached_token_count"])
	}
}
