package server

import (
	"log"
	"net/http"
)

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

// NewRouter constructs a clean, high-performance HTTP router serving the native Responses API gateway.
func NewRouter(h *Handlers) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", h.HandleHealth)
	mux.HandleFunc("/v1/health", h.HandleHealth)
	mux.HandleFunc("/v1/auth/status", h.HandleAuthStatus)
	mux.HandleFunc("/v1/quota", h.HandleQuota)
	mux.HandleFunc("/models", h.HandleModels)
	mux.HandleFunc("/v1/models", h.HandleModels)
	mux.HandleFunc("/responses", h.HandleResponses)
	mux.HandleFunc("/v1/responses", h.HandleResponses)

	// Codex Desktop and app-server native quota/rate-limits endpoints
	mux.HandleFunc("/wham/usage", h.HandleWhamUsage)
	mux.HandleFunc("/api/codex/usage", h.HandleWhamUsage)
	mux.HandleFunc("/backend-api/wham/usage", h.HandleWhamUsage)
	mux.HandleFunc("/v1/wham/usage", h.HandleWhamUsage)
	mux.HandleFunc("/v1/api/codex/usage", h.HandleWhamUsage)

	mux.HandleFunc("/wham/rate-limit-reset-credits", h.HandleRateLimitResetCredits)
	mux.HandleFunc("/api/codex/rate-limit-reset-credits", h.HandleRateLimitResetCredits)
	mux.HandleFunc("/backend-api/wham/rate-limit-reset-credits", h.HandleRateLimitResetCredits)
	mux.HandleFunc("/v1/wham/rate-limit-reset-credits", h.HandleRateLimitResetCredits)
	mux.HandleFunc("/v1/api/codex/rate-limit-reset-credits", h.HandleRateLimitResetCredits)

	// Remote plugin service & telemetry
	mux.HandleFunc("/api/codex/ps/mcp", h.HandlePsMcp)
	mux.HandleFunc("/codex/analytics-events/events", h.HandleAnalyticsEvents)
	mux.HandleFunc("/api/codex/settings/user", h.HandleEmptyObject)
	mux.HandleFunc("/ps/plugins/", h.HandleEmptyArray)
	mux.HandleFunc("/plugins/featured", h.HandleEmptyArray)

	mux.HandleFunc("/", h.HandleFallbackJSON)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		defer func() {
			log.Printf("[HTTP] %s %s -> %d", r.Method, r.URL.RequestURI(), lrw.statusCode)
		}()

		mux.ServeHTTP(lrw, r)
	})
}
