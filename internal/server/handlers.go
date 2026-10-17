package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"comini/internal/config"
	"comini/internal/domain"
	"comini/internal/schema"
	"comini/internal/session"
	"comini/internal/streaming"
	"comini/internal/transport"
)

const (
	CodexAccountID = "879af1e1-05b4-4b27-a9f8-906c13946419"
	CodexUserID    = "user-x959s7qOqk3uV6Aic5pUId0n"
)

type cachedQuotaSnapshot struct {
	PrimaryUsedPercent   int
	PrimaryResetAt       int64
	PrimaryResetAfterSec int
	WeeklyUsedPercent    int
	WeeklyResetAt        int64
	WeeklyResetAfterSec  int
	Summary              *transport.UserQuotaSummary
	FetchedAt            time.Time
}

type Handlers struct {
	cfg       *config.Config
	ccaClient *transport.CCAClient
	store     *session.SignatureStore
	authMgr   *transport.AuthManager

	quotaMu    sync.Mutex
	quotaCache *cachedQuotaSnapshot
}

func NewHandlers(cfg *config.Config, ccaClient *transport.CCAClient, store *session.SignatureStore) *Handlers {
	return &Handlers{
		cfg:       cfg,
		ccaClient: ccaClient,
		store:     store,
		authMgr:   transport.DefaultAuthManager,
	}
}

// getOrFetchQuotaSnapshot returns a cached (30s TTL) or fresh quota snapshot mapped to Codex 5h & weekly rate-limit windows.
func (h *Handlers) getOrFetchQuotaSnapshot(ctx context.Context) *cachedQuotaSnapshot {
	h.quotaMu.Lock()
	if h.quotaCache != nil && time.Since(h.quotaCache.FetchedAt) < 30*time.Second {
		snap := *h.quotaCache
		h.quotaMu.Unlock()
		return &snap
	}
	h.quotaMu.Unlock()

	now := time.Now()
	fallback := &cachedQuotaSnapshot{
		PrimaryUsedPercent:   0,
		PrimaryResetAt:       now.Add(5 * time.Hour).Unix(),
		PrimaryResetAfterSec: 18000,
		WeeklyUsedPercent:    0,
		WeeklyResetAt:        now.Add(7 * 24 * time.Hour).Unix(),
		WeeklyResetAfterSec:  604800,
		FetchedAt:            now,
	}

	if h.authMgr == nil || h.ccaClient == nil {
		return fallback
	}

	token, proj, err := h.authMgr.GetCredentials()
	if err != nil || token == "" {
		return fallback
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	summary, err := h.ccaClient.FetchUserQuotaSummary(fetchCtx, "Bearer "+token, proj)
	if err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "UNAUTHENTICATED")) {
		if freshToken, freshProj, refErr := h.authMgr.ForceRefresh(); refErr == nil && freshToken != "" {
			summary, err = h.ccaClient.FetchUserQuotaSummary(fetchCtx, "Bearer "+freshToken, freshProj)
		}
	}

	snap := *fallback
	if err == nil && summary != nil {
		snap.Summary = summary
		for _, g := range summary.Groups {
			for _, b := range g.Buckets {
				if b.BucketID == "gemini-5h" {
					used := int(math.Round((1.0 - b.RemainingFraction) * 100.0))
					if used < 0 {
						used = 0
					}
					if used > 100 {
						used = 100
					}
					snap.PrimaryUsedPercent = used
					if t, parseErr := time.Parse(time.RFC3339, b.ResetTime); parseErr == nil {
						snap.PrimaryResetAt = t.Unix()
						after := int(time.Until(t).Seconds())
						if after < 0 {
							after = 0
						}
						snap.PrimaryResetAfterSec = after
					}
				} else if b.BucketID == "gemini-weekly" {
					used := int(math.Round((1.0 - b.RemainingFraction) * 100.0))
					if used < 0 {
						used = 0
					}
					if used > 100 {
						used = 100
					}
					snap.WeeklyUsedPercent = used
					if t, parseErr := time.Parse(time.RFC3339, b.ResetTime); parseErr == nil {
						snap.WeeklyResetAt = t.Unix()
						after := int(time.Until(t).Seconds())
						if after < 0 {
							after = 0
						}
						snap.WeeklyResetAfterSec = after
					}
				}
			}
		}
		snap.FetchedAt = time.Now()
		h.quotaMu.Lock()
		h.quotaCache = &snap
		h.quotaMu.Unlock()
	}

	return &snap
}

// HandleModels returns the model catalog. When queried by Codex ModelsManager (`?client_version=...`),
// it serves the full Codex ModelsResponse schema from ~/.codex/models.json; otherwise it returns the OpenAI list format.
func (h *Handlers) HandleModels(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("client_version") != "" {
		home, _ := os.UserHomeDir()
		modelsPath := filepath.Join(home, ".codex", "models.json")
		if raw, err := os.ReadFile(modelsPath); err == nil && len(raw) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(raw)
			return
		}
	}
	if r.URL.Path == "/models" {
		h.HandleTppModels(w, r)
		return
	}

	makeEntry := func(id string) domain.ModelCatalogEntry {
		ctxWin := 272000
		if strings.Contains(id, "claude") {
			ctxWin = 250000
		} else if strings.Contains(id, "gpt-oss") {
			ctxWin = 131072
		}
		return domain.ModelCatalogEntry{
			ID:                       id,
			Object:                   "model",
			Created:                  1770000000,
			OwnedBy:                  "google-antigravity",
			SupportedReasoningLevels: domain.DefaultReasoningLadder,
			ContextWindow:            ctxWin,
			MaxContextWindow:         ctxWin,
			InputModalities:          []string{"text", "image"},
		}
	}

	resp := domain.ModelCatalogResponse{
		Object: "list",
		Data: []domain.ModelCatalogEntry{
			makeEntry("google-antigravity/gemini-3.8-flash"),
			makeEntry("google-antigravity/gemini-4-argon"),
			makeEntry("google-antigravity/claude-sonnet-5-5"),
			makeEntry("google-antigravity/claude-opus-5-5-thinking"),
			makeEntry("google-antigravity/gpt-oss-120b-medium"),
			makeEntry("gemini-3.8-flash"),
			makeEntry("gemini-4-argon"),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleHealth returns service readiness and active authentication state.
func (h *Handlers) HandleHealth(w http.ResponseWriter, r *http.Request) {
	var authStatus transport.AuthStatus
	if h.authMgr != nil {
		authStatus = h.authMgr.Status()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":         "ok",
		"service":        "comini",
		"default_model":  h.cfg.DefaultModel,
		"cca_base_url":   h.cfg.CCABaseURL,
		"authenticated":  authStatus.Authenticated,
		"active_account": authStatus.Email,
		"project_id":     authStatus.ProjectID,
	})
}

// HandleAuthStatus returns detailed authentication and account store status.
func (h *Handlers) HandleAuthStatus(w http.ResponseWriter, r *http.Request) {
	var authStatus transport.AuthStatus
	if h.authMgr != nil {
		authStatus = h.authMgr.Status()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(authStatus)
}

// HandleQuota queries live Google CCA quota across available models and user quota buckets.
func (h *Handlers) HandleQuota(w http.ResponseWriter, r *http.Request) {
	if h.authMgr == nil || h.ccaClient == nil {
		http.Error(w, `{"error":{"message":"Auth manager or CCA client not initialized"}}`, http.StatusServiceUnavailable)
		return
	}
	token, proj, err := h.authMgr.GetCredentials()
	if err != nil || token == "" {
		http.Error(w, `{"error":{"message":"Not authenticated with Google Antigravity"}}`, http.StatusUnauthorized)
		return
	}
	models, err := h.ccaClient.FetchQuotaAndModels(r.Context(), "Bearer "+token, proj)
	if err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "UNAUTHENTICATED")) {
		if freshToken, freshProj, refErr := h.authMgr.ForceRefresh(); refErr == nil && freshToken != "" {
			token = freshToken
			proj = freshProj
			models, err = h.ccaClient.FetchQuotaAndModels(r.Context(), "Bearer "+freshToken, freshProj)
		}
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": err.Error()},
		})
		return
	}
	summary, _ := h.ccaClient.FetchUserQuotaSummary(r.Context(), "Bearer "+token, proj)
	st := h.authMgr.Status()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"email":      st.Email,
		"project_id": proj,
		"summary":    summary,
		"models":     models,
	})
}

// HandleWhamUsage serves /wham/usage and /backend-api/wham/usage for Codex Desktop & codex app-server native Quota UI.

// HandleResponses processes incoming Responses API requests from Codex Desktop.
func (h *Handlers) HandleResponses(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024*1024)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[Comini] ❌ Failed to read request body: %v (len: %d)", err, len(bodyBytes))
		http.Error(w, `{"error":{"message":"Failed to read request body or payload exceeds limit"}}`, http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req domain.ResponsesRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, `{"error":{"message":"Invalid JSON payload"}}`, http.StatusBadRequest)
		return
	}

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

	// Ensure image_generation tool is declared so Gemini can generate images
	hasImageGen := false
	for _, t := range allToolDefs {
		if t.Type == "image_generation" || t.Name == "image_generation" {
			hasImageGen = true
			break
		}
	}
	if !hasImageGen {
		allToolDefs = append(allToolDefs, domain.ToolDefinition{
			Type: "image_generation",
			Name: "image_generation",
		})
	}

	// 2. Compile Tool Declarations
	funcDecls, toolMetaMap := schema.ExtractAndCompileTools(allToolDefs)

	// 3. Resolve Model and Effort
	var parsedInputItems []domain.InputItem
	if len(req.Input) > 0 {
		_ = json.Unmarshal(req.Input, &parsedInputItems)
	}

	effort := ""
	if req.Reasoning != nil {
		effort = req.Reasoning.Effort
	}
	if effort == "" && req.ReasoningEffort != "" {
		effort = req.ReasoningEffort
	}
	if effort == "" {
		for _, it := range parsedInputItems {
			if it.Type == "reasoning" && it.Effort != "" {
				effort = it.Effort
				break
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
	for _, item := range parsedInputItems {
		if item.Role == "user" {
			if item.Text != "" {
				firstUserText = item.Text
				break
			}
			if len(item.Content) > 0 {
				var contentParts []domain.ContentPart
				if err := json.Unmarshal(item.Content, &contentParts); err == nil {
					for _, cp := range contentParts {
						if cp.Text != "" {
							firstUserText = cp.Text
							break
						}
					}
				}
				if firstUserText != "" {
					break
				}
			}
		}
	}
	if firstUserText == "" && len(parsedInputItems) > 0 {
		firstUserText = parsedInputItems[0].ID
	}
	sessionID := session.DeriveSessionID(parentThreadID, ownThreadID, firstUserText)


	// 5. Build Wire Body using parsedInputItems (single-pass, zero redundant allocations)
	wireBody := session.BuildGeminiWireBodyWithItems(
		&req,
		parsedInputItems,
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

	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "RESOURCE_EXHAUSTED") {
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": "Google Cloud Code Assist upstream rate limit reached (HTTP 429). Please retry after a brief moment.",
					"type":    "rate_limit_error",
					"code":    "rate_limit_exceeded",
				},
			})
			return
		}

		if strings.Contains(err.Error(), "400") || strings.Contains(err.Error(), "exceeds the maximum number of tokens") {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": err.Error(),
					"type":    "invalid_request_error",
					"code":    "context_length_exceeded",
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

	// 8. Stream Responses API SSE (with live x-codex rate-limit headers for Codex session token_count updates)
	snap := h.getOrFetchQuotaSnapshot(r.Context())
	w.Header().Set("x-codex-primary-used-percent", fmt.Sprintf("%d", snap.PrimaryUsedPercent))
	w.Header().Set("x-codex-primary-window-minutes", "300")
	w.Header().Set("x-codex-primary-reset-at", fmt.Sprintf("%d", snap.PrimaryResetAt))
	w.Header().Set("x-codex-secondary-used-percent", fmt.Sprintf("%d", snap.WeeklyUsedPercent))
	w.Header().Set("x-codex-secondary-window-minutes", "10080")
	w.Header().Set("x-codex-secondary-reset-at", fmt.Sprintf("%d", snap.WeeklyResetAt))

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

	var imageGenFn func(ctx context.Context, prompt string, aspectRatio string) (string, string, error)
	if h.ccaClient != nil {
		imageGenFn = func(ctx context.Context, prompt, ar string) (string, string, error) {
			curAuth := authHeader
			curProj := projectID
			if h.authMgr != nil {
				if token, proj, err := h.authMgr.GetCredentials(); err == nil && token != "" {
					curAuth = "Bearer " + token
					if proj != "" {
						curProj = proj
					}
				}
			}
			savedPath, dataURL, err := h.ccaClient.GenerateImage(ctx, prompt, ar, curAuth, curProj)
			if err != nil && h.authMgr != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "UNAUTHENTICATED")) {
				freshToken, freshProj, refreshErr := h.authMgr.ForceRefresh()
				if refreshErr == nil && freshToken != "" {
					if freshProj != "" {
						curProj = freshProj
					}
					curAuth = "Bearer " + freshToken
					savedPath, dataURL, err = h.ccaClient.GenerateImage(ctx, prompt, ar, curAuth, curProj)
				}
			}
			return savedPath, dataURL, err
		}
	}

	var continueFn func(ctx context.Context, modelPart domain.GeminiPart, obsText string) (io.ReadCloser, error)
	if h.ccaClient != nil {
		wireBodyCopy := wireBody
		wireBodyCopy.Contents = append([]domain.GeminiContent(nil), wireBody.Contents...)

		continueFn = func(ctx context.Context, modelPart domain.GeminiPart, obsText string) (io.ReadCloser, error) {
			wireBodyCopy.Contents = append(wireBodyCopy.Contents, domain.GeminiContent{
				Role:  "model",
				Parts: []domain.GeminiPart{modelPart},
			})
			wireBodyCopy.Contents = append(wireBodyCopy.Contents, domain.GeminiContent{
				Role:  "user",
				Parts: []domain.GeminiPart{{Text: obsText}},
			})

			curAuth := authHeader
			curProj := projectID
			if h.authMgr != nil {
				if token, proj, err := h.authMgr.GetCredentials(); err == nil && token != "" {
					curAuth = "Bearer " + token
					if proj != "" {
						curProj = proj
					}
				}
			}

			log.Printf("[Comini Autonomous Loop] 🔁 Continuing stream for next turn (contents: %d, model: %s)...",
				len(wireBodyCopy.Contents), resolved.WireModelID)
			nextResp, err := h.ccaClient.StreamGenerateContent(ctx, resolved.WireModelID, wireBodyCopy, curAuth, curProj)
			if err != nil && h.authMgr != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "UNAUTHENTICATED")) {
				freshToken, freshProj, refreshErr := h.authMgr.ForceRefresh()
				if refreshErr == nil && freshToken != "" {
					if freshProj != "" {
						curProj = freshProj
					}
					curAuth = "Bearer " + freshToken
					nextResp, err = h.ccaClient.StreamGenerateContent(ctx, resolved.WireModelID, wireBodyCopy, curAuth, curProj)
				}
			}



			if err != nil {
				return nil, err
			}
			return nextResp.Body, nil
		}
	}

	_ = streaming.MapGeminiStreamToResponses(
		resp.Body, w, flushFn, h.store, "", req.Model, toolMetaMap, sessionID,
		streaming.StreamOptions{
			Context:    r.Context(),
			ImageGenFn: imageGenFn,
			ContinueFn: continueFn,
		},
	)
}
