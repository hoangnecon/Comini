package main

import (
	"fmt"
	"log"
	"net/http"

	"comini/internal/config"
	"comini/internal/server"
	"comini/internal/session"
	"comini/internal/transport"
)

func main() {
	cfg := config.Load()

	ccaClient := transport.NewCCAClient(cfg.CCABaseURL, cfg.UserAgent)
	store := session.DefaultSignatureStore
	handlers := server.NewHandlers(cfg, ccaClient, store)
	router := server.NewRouter(handlers)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	log.Printf("[Comini Go] Server running on http://%s", addr)
	log.Printf("[Comini Go] Forwarding Responses API -> Google CCA (%s)", cfg.CCABaseURL)

	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
