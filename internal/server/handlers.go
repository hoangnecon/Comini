package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"

	"comini/internal/config"
	"comini/internal/domain"
	"comini/internal/schema"
	"comini/internal/session"
	"comini/internal/streaming"
	"comini/internal/transport"
)

type Handlers struct {
	cfg       *config.Config
	ccaClient *transport.CCAClient
	store     *session.SignatureStore
	authMgr   *transport.AuthManager
}

func NewHandlers(cfg *config.Config, ccaClient *transport.CCAClient, store *session.SignatureStore) *Handlers {
	return &Handlers{
		cfg:       cfg,
		ccaClient: ccaClient,
		store:     store,
		authMgr:   transport.DefaultAuthManager,
	}
}

// HandleModels returns the model catalog matching DeepSeek 2026 custom responses standard.
func (h *Handlers) HandleModels(w http.ResponseWriter, r *http.Request) {
	resp := domain.ModelCatalogResponse{
		Object: "list",
		Data: []domain.ModelCatalogEntry{
			{
				ID:                       "deepseek-v4-pro",
				Object:                   "model",
				Created:                  1770000000,
				OwnedBy:                  "google-antigravity",
				SupportedReasoningLevels: domain.DeepSeekReasoningLadder,
			},
			{
				ID:                       "deepseek-flash",
				Object:                   "model",
				Created:                  1770000000,
				OwnedBy:                  "google-antigravity",
				SupportedReasoningLevels: domain.DeepSeekReasoningLadder,
			},
			{
				ID:                       "google-antigravity/gemini-3.7-flash",
				Object:                   "model",
				Created:                  1770000000,
				OwnedBy:                  "google-antigravity",
				SupportedReasoningLevels: domain.DeepSeekReasoningLadder,
			},
			{
				ID:                       "google-antigravity/gemini-3.8-flash",
				Object:                   "model",
				Created:                  1770000000,
				OwnedBy:                  "google-antigravity",
				SupportedReasoningLevels: domain.DeepSeekReasoningLadder,
			},
			{
				ID:                       "google-antigravity/gemini-3.1-pro",
				Object:                   "model",
				Created:                  1770000000,
				OwnedBy:                  "google-antigravity",
				SupportedReasoningLevels: domain.DeepSeekReasoningLadder,
			},
			{
				ID:                       "google-antigravity/claude-sonnet-4-6",
				Object:                   "model",
				Created:                  1770000000,
				OwnedBy:                  "google-antigravity",
				SupportedReasoningLevels: domain.DeepSeekReasoningLadder,
			},
			{
				ID:                       "google-antigravity/claude-opus-4-6-thinking",
				Object:                   "model",
				Created:                  1770000000,
				OwnedBy:                  "google-antigravity",
				SupportedReasoningLevels: domain.DeepSeekReasoningLadder,
			},
			{
				ID:                       "gemini-3.7-flash",
				Object:                   "model",
				Created:                  1770000000,
				OwnedBy:                  "google-antigravity",
				SupportedReasoningLevels: domain.DeepSeekReasoningLadder,
			},
			{
				ID:                       "gemini-3.8-flash",
				Object:                   "model",
				Created:                  1770000000,
				OwnedBy:                  "google-antigravity",
				SupportedReasoningLevels: domain.DeepSeekReasoningLadder,
			},
			{
				ID:                       "gemini-3.1-pro",
				Object:                   "model",
				Created:                  1770000000,
				OwnedBy:                  "google-antigravity",
				SupportedReasoningLevels: domain.DeepSeekReasoningLadder,
			},
			{
				ID:                       "claude-sonnet-4-6",
				Object:                   "model",
				Created:                  1770000000,
				OwnedBy:                  "google-antigravity",
				SupportedReasoningLevels: domain.DeepSeekReasoningLadder,
			},
			{
				ID:                       "claude-opus-4-6-thinking",
				Object:                   "model",
				Created:                  1770000000,
				OwnedBy:                  "google-antigravity",
				SupportedReasoningLevels: domain.DeepSeekReasoningLadder,
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleResponses processes incoming Responses API requests from Codex Desktop.
func (h *Handlers) HandleResponses(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 50*1024*1024)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":{"message":"Failed to read request body or payload exceeds limit"}}`, http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req domain.ResponsesRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, `{"error":{"message":"Invalid JSON payload"}}`, http.StatusBadRequest)
		return
	}
	_ = os.WriteFile("/tmp/comini_debug_req.json", bodyBytes, 0644)

	if req.Model == "" {
		http.Error(w, `{"error":{"message":"model field is required"}}`, http.StatusBadRequest)
		return
	}

	// 1. Collect all tools from top-level req.Tools and input additional_tools items
	allToolDefs := make([]domain.ToolDefinition, 0, len(req.Tools))
	allToolDefs = append(allToolDefs, req.Tools...)

	if len(req.Input) > 0 {
		var inputItems []domain.InputItem
		if err := json.Unmarshal(req.Input, &inputItems); err == nil {
			for _, item := range inputItems {
				if item.Type == "additional_tools" && len(item.Tools) > 0 {
					allToolDefs = append(allToolDefs, item.Tools...)
				}
			}
		}
	}

	// 2. Compile Tool Declarations
	funcDecls, toolMetaMap := schema.ExtractAndCompileTools(allToolDefs)

	// 3. Resolve Model and Effort
	effort := ""
	if req.Reasoning != nil {
		effort = req.Reasoning.Effort
	}
	if effort == "" && req.ReasoningEffort != "" {
		effort = req.ReasoningEffort
	}
	if effort == "" && len(req.Input) > 0 {
		var inputItems []struct {
			Type   string `json:"type"`
			Effort string `json:"effort"`
		}
		if err := json.Unmarshal(req.Input, &inputItems); err == nil {
			for _, it := range inputItems {
				if it.Type == "reasoning" && it.Effort != "" {
					effort = it.Effort
					break
				}
			}
		}
	}
	resolved := transport.ResolveModelAndEffort(req.Model, effort)
	log.Printf("[Comini] 🎯 Request received: model=%s effort=%s -> Gemini wireModel=%s thinkingLevel=%s (tools=%d)",
		req.Model, effort, resolved.WireModelID, resolved.ThinkingLevel, len(funcDecls))

	// 4. Derive Session ID
	threadID := r.Header.Get("x-codex-parent-thread-id")
	sessionID := session.DeriveSessionID(threadID, "")

	// 5. Build Wire Body
	wireBody := session.BuildGeminiWireBody(
		&req,
		sessionID,
		funcDecls,
		resolved.ThinkingLevel,
		h.store,
	)

	// 6. Authentication resolution (Header -> Static Token -> Auto AuthManager)
	authHeader := r.Header.Get("Authorization")
	projectID := "aicode-consumers"

	if h.authMgr != nil {
		token, proj, err := h.authMgr.GetCredentials()
		if proj != "" {
			projectID = proj
		}
		if authHeader == "" && err == nil && token != "" {
			authHeader = "Bearer " + token
		}
	}

	if authHeader == "" && h.cfg.StaticToken != "" {
		authHeader = "Bearer " + h.cfg.StaticToken
	}

	// 7. Send to CCA
	resp, err := h.ccaClient.StreamGenerateContent(r.Context(), resolved.WireModelID, wireBody, authHeader, projectID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": err.Error(),
			},
		})
		return
	}
	defer resp.Body.Close()

	// 8. Stream Responses API SSE
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	flushFn := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	_ = streaming.MapGeminiStreamToResponses(resp.Body, w, flushFn, h.store, "", req.Model, toolMetaMap, sessionID)
}
