package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

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
	makeEntry := func(id string) domain.ModelCatalogEntry {
		return domain.ModelCatalogEntry{
			ID:                       id,
			Object:                   "model",
			Created:                  1770000000,
			OwnedBy:                  "google-antigravity",
			SupportedReasoningLevels: domain.DeepSeekReasoningLadder,
			ContextWindow:            1048576,
			MaxContextWindow:         1048576,
			InputModalities:          []string{"text", "image"},
		}
	}

	resp := domain.ModelCatalogResponse{
		Object: "list",
		Data: []domain.ModelCatalogEntry{
			makeEntry("deepseek-v4-pro"),
			makeEntry("deepseek-flash"),
			makeEntry("google-antigravity/gemini-3.7-flash"),
			makeEntry("google-antigravity/gemini-3.8-flash"),
			makeEntry("google-antigravity/gemini-3.1-pro"),
			makeEntry("google-antigravity/gemini-3.1-flash-image"),
			makeEntry("google-antigravity/claude-sonnet-4-6"),
			makeEntry("google-antigravity/claude-opus-4-6-thinking"),
			makeEntry("google-antigravity/gpt-oss-120b-medium"),
			makeEntry("gemini-3.7-flash"),
			makeEntry("gemini-3.8-flash"),
			makeEntry("gemini-3.1-pro"),
			makeEntry("gemini-3.1-flash-image"),
			makeEntry("gemini-pro-agent"),
			makeEntry("gemini-3.1-pro-low"),
			makeEntry("claude-sonnet-4-6"),
			makeEntry("claude-opus-4-6-thinking"),
			makeEntry("gpt-oss-120b-medium"),
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

	// 1. Collect all tools from top-level req.Tools and dynamic tool items (additional_tools, tool_search_output).
	// ONLY collect dynamic tools that belong to the active turn (after the latest user prompt).
	// Stale dynamic tools from historical turns are not loaded in Codex Desktop's turn router,
	// so exposing them causes model invocation of unmounted tools resulting in "unsupported call".
	allToolDefs := make([]domain.ToolDefinition, 0, len(req.Tools))
	allToolDefs = append(allToolDefs, req.Tools...)

	if len(req.Input) > 0 {
		var inputItems []domain.InputItem
		if err := json.Unmarshal(req.Input, &inputItems); err == nil {
			lastUserIdx := -1
			for i, item := range inputItems {
				norm := strings.ToLower(strings.TrimSpace(item.Type))
				role := strings.ToLower(strings.TrimSpace(item.Role))
				if (norm == "message" || norm == "usermessage" || norm == "userinput") && (role == "user" || role == "") {
					lastUserIdx = i
				}
			}

			for i, item := range inputItems {
				if i <= lastUserIdx {
					continue
				}

				norm := strings.ToLower(strings.TrimSpace(item.Type))
				norm = strings.ReplaceAll(norm, "_", "")
				norm = strings.ReplaceAll(norm, "-", "")

				if norm == "additionaltools" || norm == "toolsearchoutput" || norm == "toolsearch" {
					if len(item.Tools) > 0 {
						allToolDefs = append(allToolDefs, item.Tools...)
					}
					// Also check if tools are inside item.Output or item.Payload
					if len(item.Tools) == 0 {
						if len(item.Output) > 0 {
							var toolContainer struct {
								Tools []domain.ToolDefinition `json:"tools"`
							}
							if err := json.Unmarshal(item.Output, &toolContainer); err == nil && len(toolContainer.Tools) > 0 {
								allToolDefs = append(allToolDefs, toolContainer.Tools...)
							} else {
								var toolList []domain.ToolDefinition
								if err := json.Unmarshal(item.Output, &toolList); err == nil && len(toolList) > 0 {
									allToolDefs = append(allToolDefs, toolList...)
								}
							}
						}
						if len(item.Payload) > 0 {
							var toolContainer struct {
								Tools []domain.ToolDefinition `json:"tools"`
							}
							if err := json.Unmarshal(item.Payload, &toolContainer); err == nil && len(toolContainer.Tools) > 0 {
								allToolDefs = append(allToolDefs, toolContainer.Tools...)
							} else {
								var toolList []domain.ToolDefinition
								if err := json.Unmarshal(item.Payload, &toolList); err == nil && len(toolList) > 0 {
									allToolDefs = append(allToolDefs, toolList...)
								}
							}
						}
					}
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
	parentThreadID := r.Header.Get("x-codex-parent-thread-id")
	ownThreadID := r.Header.Get("x-codex-thread-id")
	if ownThreadID == "" {
		ownThreadID = r.Header.Get("x-codex-own-thread-id")
	}
	var firstUserText string
	if len(req.Input) > 0 {
		var inputItems []domain.InputItem
		if err := json.Unmarshal(req.Input, &inputItems); err == nil {
			for _, item := range inputItems {
				if item.Role == "user" && item.Text != "" {
					firstUserText = item.Text
					break
				}
			}
		}
	}
	sessionID := session.DeriveSessionID(parentThreadID, ownThreadID, firstUserText)

	// 5. Build Wire Body
	wireBody := session.BuildGeminiWireBody(
		&req,
		sessionID,
		funcDecls,
		resolved.ThinkingLevel,
		h.store,
	)

	// 6. Authentication resolution (Auto AuthManager -> Static Token -> Header)
	var authHeader string
	projectID := "aicode-consumers"

	if h.authMgr != nil {
		token, proj, err := h.authMgr.GetCredentials()
		if proj != "" {
			projectID = proj
		}
		if err == nil && token != "" {
			authHeader = "Bearer " + token
		}
	}

	if authHeader == "" {
		if clientAuth := r.Header.Get("Authorization"); clientAuth != "" && (strings.HasPrefix(clientAuth, "Bearer ya29.") || strings.HasPrefix(clientAuth, "ya29.")) {
			authHeader = clientAuth
		} else if h.cfg.StaticToken != "" {
			authHeader = "Bearer " + h.cfg.StaticToken
		}
	}

	// 7. Send to CCA with auto-refresh on 401 Unauthenticated and fallback on 429 Resource Exhausted
	resp, err := h.ccaClient.StreamGenerateContent(r.Context(), resolved.WireModelID, wireBody, authHeader, projectID)
	if err != nil && h.authMgr != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "UNAUTHENTICATED")) {
		log.Printf("[Comini Auth] 🔄 Upstream returned 401. Triggering ForceRefresh and retrying...")
		freshToken, freshProj, refreshErr := h.authMgr.ForceRefresh()
		if refreshErr == nil && freshToken != "" {
			if freshProj != "" {
				projectID = freshProj
			}
			authHeader = "Bearer " + freshToken
			resp, err = h.ccaClient.StreamGenerateContent(r.Context(), resolved.WireModelID, wireBody, authHeader, projectID)
		}
	}

	// If 429 Resource Exhausted on 3.7 or Pro, fallback to healthy Gemini 3.8 Flash
	if err != nil && (strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "RESOURCE_EXHAUSTED")) {
		fallbackModel := "gemini-3.8-flash-high"
		if resolved.WireModelID != fallbackModel {
			log.Printf("[Comini Quota] ⚡ Upstream 429 on %s. Seamlessly falling back to %s...", resolved.WireModelID, fallbackModel)
			resp, err = h.ccaClient.StreamGenerateContent(r.Context(), fallbackModel, wireBody, authHeader, projectID)
		}
	}

	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "RESOURCE_EXHAUSTED") {
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": "Google Cloud Code Assist upstream quota exhausted (HTTP 429). Please wait a moment for quota window reset.",
					"type":    "insufficient_quota",
					"code":    "rate_limit_exceeded",
				},
			})
			return
		}

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
