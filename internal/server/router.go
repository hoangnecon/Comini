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

	// Native Plugin Service & Asset Routes
	mux.HandleFunc("/plugins/assets/", h.HandlePluginAssets)
	mux.HandleFunc("/ps/plugins/home", h.HandlePsPluginsHome)
	mux.HandleFunc("/ps/plugins/installed", h.HandlePsPluginsInstalled)
	mux.HandleFunc("/ps/plugin-categories/", h.HandlePsPluginCategories)
	mux.HandleFunc("/ps/plugins/search", h.HandlePsPluginsSearch)
	mux.HandleFunc("/ps/plugins/list", h.HandlePsPluginsList)
	mux.HandleFunc("/ps/plugins/batch", h.HandlePsPluginsBatch)
	mux.HandleFunc("/ps/apps/batch", h.HandlePsAppsBatch)
	mux.HandleFunc("/ps/plugins/canonical-app/", h.HandlePsCanonicalApp)
	mux.HandleFunc("/ps/plugins/suggested/codex", h.HandlePsPluginsSuggested)
	mux.HandleFunc("/ps/plugins/workspace/created", h.HandlePsWorkspacePlugins)
	mux.HandleFunc("/ps/plugins/workspace/shared", h.HandlePsWorkspacePlugins)
	mux.HandleFunc("/ps/event-subscriptions/batch", h.HandlePsEventSubscriptions)
	mux.HandleFunc("/ps/plugins/", h.HandlePsPluginSubroutes)
	mux.HandleFunc("/ps/mcp", h.HandlePsMcp)
	mux.HandleFunc("/api/codex/ps/mcp", h.HandlePsMcp)

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
