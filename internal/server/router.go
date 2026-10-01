package server

import (
	"net/http"
)

func NewRouter(h *Handlers) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/models", h.HandleModels)
	mux.HandleFunc("/v1/models", h.HandleModels)
	mux.HandleFunc("/responses", h.HandleResponses)
	mux.HandleFunc("/v1/responses", h.HandleResponses)
	mux.HandleFunc("/v1/chat/completions", h.HandleResponses)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		mux.ServeHTTP(w, r)
	})
}
