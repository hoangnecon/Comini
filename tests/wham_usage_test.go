package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"comini/internal/config"
	"comini/internal/server"
	"comini/internal/session"
)

func TestWhamUsageAndRateLimitsEndpoints(t *testing.T) {
	cfg := &config.Config{
		DefaultModel: "gemini-3.8-flash",
		CCABaseURL:   "https://daily-cloudcode-pa.googleapis.com",
	}
	store := session.NewSignatureStore(24 * time.Hour)
	h := server.NewHandlers(cfg, nil, store)
	router := server.NewRouter(h)

	// 1. Test GET /api/codex/usage
	req := httptest.NewRequest(http.MethodGet, "/api/codex/usage", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/codex/usage, got %d: %s", w.Code, w.Body.String())
	}

	var usageResp server.WhamUsageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &usageResp); err != nil {
		t.Fatalf("failed to decode WhamUsageResponse: %v", err)
	}

	if usageResp.AccountID == "" {
		t.Errorf("expected non-empty AccountID")
	}
	if usageResp.PlanType != "pro" {
		t.Errorf("expected PlanType pro, got %s", usageResp.PlanType)
	}
	if usageResp.RateLimit == nil || usageResp.RateLimit.PrimaryWindow == nil {
		t.Fatalf("expected non-nil PrimaryWindow in RateLimit")
	}
	if usageResp.RateLimit.PrimaryWindow.LimitWindowSeconds != 18000 {
		t.Errorf("expected 18000s limit window (5h), got %d", usageResp.RateLimit.PrimaryWindow.LimitWindowSeconds)
	}
	if usageResp.RateLimit.SecondaryWindow == nil || usageResp.RateLimit.SecondaryWindow.LimitWindowSeconds != 604800 {
		t.Errorf("expected 604800s weekly window")
	}
	if len(usageResp.AdditionalRateLimits) == 0 {
		t.Errorf("expected non-empty AdditionalRateLimits")
	}
	for _, a := range usageResp.AdditionalRateLimits {
		if a.MeteredFeature != "codex" {
			t.Errorf("expected metered_feature 'codex', got '%s'", a.MeteredFeature)
		}
	}

	// 2. Test GET /wham/usage
	reqWham := httptest.NewRequest(http.MethodGet, "/wham/usage", nil)
	wWham := httptest.NewRecorder()
	router.ServeHTTP(wWham, reqWham)
	if wWham.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /wham/usage, got %d", wWham.Code)
	}

	// 3. Test GET /api/codex/rate-limit-reset-credits
	reqCredits := httptest.NewRequest(http.MethodGet, "/api/codex/rate-limit-reset-credits", nil)
	wCredits := httptest.NewRecorder()
	router.ServeHTTP(wCredits, reqCredits)
	if wCredits.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/codex/rate-limit-reset-credits, got %d", wCredits.Code)
	}
	var creditsResp struct {
		AvailableCount int `json:"available_count"`
	}
	if err := json.Unmarshal(wCredits.Body.Bytes(), &creditsResp); err != nil {
		t.Fatalf("failed to decode rate-limit-reset-credits: %v", err)
	}
	if creditsResp.AvailableCount != 0 {
		t.Errorf("expected available_count 0, got %d", creditsResp.AvailableCount)
	}
}
