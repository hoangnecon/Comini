package tests

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"comini/internal/config"
	"comini/internal/server"
	"comini/internal/session"
)

// Commit d7ce41d removed the mock usage/credits endpoints; they must stay unsupported (404).
func TestWhamUsageAndRateLimitsEndpointsRemoved(t *testing.T) {
	cfg := &config.Config{
		DefaultModel: "gemini-3.8-flash",
		CCABaseURL:   "https://daily-cloudcode-pa.googleapis.com",
	}
	store := session.NewSignatureStore(24 * time.Hour)
	h := server.NewHandlers(cfg, nil, store)
	router := server.NewRouter(h)

	for _, path := range []string{"/api/codex/usage", "/wham/usage", "/api/codex/rate-limit-reset-credits"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s: expected 404, got %d: %s", path, w.Code, w.Body.String())
		}
	}
}
