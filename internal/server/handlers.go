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
	hasGemini := false
	if err == nil && summary != nil {
		snap.Summary = summary
		for _, g := range summary.Groups {
			for _, b := range g.Buckets {
				if b.BucketID == "gemini-5h" {
					hasGemini = true
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
	}

	// Live fallback matching AGY 2.0: if retrieveUserQuotaSummary omitted gemini-5h,
	// query fetchAvailableModels directly to extract exact Gemini model quotaInfo.
	if !hasGemini && h.ccaClient != nil {
		if models, merr := h.ccaClient.FetchQuotaAndModels(fetchCtx, "Bearer "+token, proj); merr == nil {
			for _, m := range models {
				if strings.Contains(m.ModelID, "gemini") && m.RemainingFraction > 0 && m.ResetTime != "" {
					used := int(math.Round((1.0 - m.RemainingFraction) * 100.0))
					if used < 0 {
						used = 0
					}
					if used > 100 {
						used = 100
					}
					snap.PrimaryUsedPercent = used
					if t, parseErr := time.Parse(time.RFC3339, m.ResetTime); parseErr == nil {
						snap.PrimaryResetAt = t.Unix()
						after := int(time.Until(t).Seconds())
						if after < 0 {
							after = 0
						}
						snap.PrimaryResetAfterSec = after
					}
					break
				}
			}
		}
	}

	snap.FetchedAt = time.Now()
	h.quotaMu.Lock()
	h.quotaCache = &snap
	h.quotaMu.Unlock()

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

	makeEntry := func(id string) domain.ModelCatalogEntry {
		ctxWin := 1048576
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

// WhamWindow represents a single sliding window rate-limit bucket in /wham/usage.
type WhamWindow struct {
	UsedPercent        int   `json:"used_percent"`
	LimitWindowSeconds int   `json:"limit_window_seconds"`
	ResetAfterSeconds  int   `json:"reset_after_seconds"`
	ResetAt            int64 `json:"reset_at"`
}

// WhamRateLimit represents the rate limit block in /wham/usage.
type WhamRateLimit struct {
	Allowed         bool        `json:"allowed"`
	LimitReached    bool        `json:"limit_reached"`
	PrimaryWindow   *WhamWindow `json:"primary_window"`
	SecondaryWindow *WhamWindow `json:"secondary_window"`
}

// WhamAdditionalLimit represents an additional per-model rate limit item.
type WhamAdditionalLimit struct {
	LimitName      string         `json:"limit_name"`
	MeteredFeature string         `json:"metered_feature"`
	RateLimit      *WhamRateLimit `json:"rate_limit"`
}

// WhamCredits represents the credits block.
type WhamCredits struct {
	HasCredits bool    `json:"has_credits"`
	Unlimited  bool    `json:"unlimited"`
	Balance    *string `json:"balance"`
}

// WhamUsageResponse represents the exact JSON schema required by Codex Desktop & app-server for /wham/usage and /api/codex/usage.
type WhamUsageResponse struct {
	AccountID            string                `json:"account_id"`
	UserID               string                `json:"user_id"`
	PlanType             string                `json:"plan_type"`
	RateLimitName        *string               `json:"rate_limit_name"`
	RateLimit            *WhamRateLimit        `json:"rate_limit"`
	AdditionalRateLimits []WhamAdditionalLimit `json:"additional_rate_limits"`
	Credits              WhamCredits           `json:"credits"`
}

// HandleWhamUsage serves /wham/usage and /api/codex/usage for Codex Desktop & codex app-server native Quota UI.
func (h *Handlers) HandleWhamUsage(w http.ResponseWriter, r *http.Request) {
	snap := h.getOrFetchQuotaSnapshot(r.Context())

	// Read active ChatGPT account and user identifiers from ~/.codex/auth.json if present
	accountID := "879af1e1-05b4-4b27-a9f8-906c13946419"
	userID := "user-x959s7qOqk3uV6Aic5pUId0n"

	home, _ := os.UserHomeDir()
	authPath := filepath.Join(home, ".codex", "auth.json")
	if raw, err := os.ReadFile(authPath); err == nil && len(raw) > 0 {
		var authData struct {
			Tokens struct {
				AccountID string `json:"account_id"`
			} `json:"tokens"`
		}
		if json.Unmarshal(raw, &authData) == nil && authData.Tokens.AccountID != "" {
			accountID = authData.Tokens.AccountID
		}
	}

	primaryWin := &WhamWindow{
		UsedPercent:        snap.PrimaryUsedPercent,
		LimitWindowSeconds: 18000,
		ResetAfterSeconds:  snap.PrimaryResetAfterSec,
		ResetAt:            snap.PrimaryResetAt,
	}

	secondaryWin := &WhamWindow{
		UsedPercent:        snap.WeeklyUsedPercent,
		LimitWindowSeconds: 604800,
		ResetAfterSeconds:  snap.WeeklyResetAfterSec,
		ResetAt:            snap.WeeklyResetAt,
	}

	rl := &WhamRateLimit{
		Allowed:         snap.PrimaryUsedPercent < 100,
		LimitReached:    snap.PrimaryUsedPercent >= 100,
		PrimaryWindow:   primaryWin,
		SecondaryWindow: secondaryWin,
	}

	knownModels := []string{
		"google-antigravity/gemini-3.8-flash",
		"google-antigravity/gemini-4-argon",
		"google-antigravity/claude-sonnet-5-5",
		"google-antigravity/claude-opus-5-5-thinking",
		"google-antigravity/gpt-oss-120b-medium",
		"gemini-3.8-flash",
		"gemini-4-argon",
	}

	additional := make([]WhamAdditionalLimit, 0, len(knownModels))
	for _, m := range knownModels {
		additional = append(additional, WhamAdditionalLimit{
			LimitName:      m,
			MeteredFeature: "codex",
			RateLimit:      rl,
		})
	}

	resp := WhamUsageResponse{
		AccountID:            accountID,
		UserID:               userID,
		PlanType:             "pro",
		RateLimitName:        nil,
		RateLimit:            rl,
		AdditionalRateLimits: additional,
		Credits: WhamCredits{
			HasCredits: false,
			Unlimited:  false,
			Balance:    nil,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleRateLimitResetCredits serves available reset credit status for /api/codex/rate-limit-reset-credits.
func (h *Handlers) HandleRateLimitResetCredits(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"available_count": 0,
		"credits":         []any{},
	})
}

// HandlePsMcp handles MCP JSON-RPC requests for the remote plugin service.
func (h *Handlers) HandlePsMcp(w http.ResponseWriter, r *http.Request) {
	var rpcReq struct {
		JSONRPC string `json:"jsonrpc"`
		ID      any    `json:"id"`
		Method  string `json:"method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&rpcReq)

	// JSON-RPC notifications do not have an ID and must not receive a JSON-RPC response message
	if rpcReq.ID == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	switch rpcReq.Method {
	case "initialize":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      rpcReq.ID,
			"result": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "comini-ps", "version": "1.0"},
			},
		})
	case "tools/list":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      rpcReq.ID,
			"result": map[string]any{
				"tools": []any{},
			},
		})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      rpcReq.ID,
			"result":  map[string]any{},
		})
	}
}

// HandleAnalyticsEvents absorbs telemetry quietly.
func (h *Handlers) HandleAnalyticsEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// HandleEmptyArray returns an empty JSON array.
func (h *Handlers) HandleEmptyArray(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`[]`))
}

// HandleEmptyObject returns an empty JSON object.
func (h *Handlers) HandleEmptyObject(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{}`))
}



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

	var parsedInputItems []domain.InputItem
	if len(req.Input) > 0 {
		_ = json.Unmarshal(req.Input, &parsedInputItems)
	}

	// 1. Derive Session ID first so session-sticky discovered tools can be loaded/persisted
	parentThreadID := r.Header.Get("x-codex-parent-thread-id")
	if parentThreadID == "" && len(req.ClientMetadata) > 0 {
		parentThreadID = req.ClientMetadata["x-codex-parent-thread-id"]
	}
	ownThreadID := r.Header.Get("session_id")
	if ownThreadID == "" {
		ownThreadID = r.Header.Get("x-session-id")
	}
	if ownThreadID == "" {
		ownThreadID = r.Header.Get("conversation_id")
	}
	if ownThreadID == "" {
		ownThreadID = r.Header.Get("x-codex-window-id")
	}
	if ownThreadID == "" {
		ownThreadID = r.Header.Get("x-codex-thread-id")
	}
	if ownThreadID == "" {
		ownThreadID = r.Header.Get("x-codex-own-thread-id")
	}
	if ownThreadID == "" && req.PromptCacheKey != "" {
		ownThreadID = strings.TrimSpace(req.PromptCacheKey)
	}
	if ownThreadID == "" && len(req.ClientMetadata) > 0 {
		for _, k := range []string{"session_id", "thread_id", "conversation_id", "x-codex-thread-id", "x-codex-own-thread-id", "x-codex-window-id"} {
			if v := strings.TrimSpace(req.ClientMetadata[k]); v != "" {
				ownThreadID = v
				break
			}
		}
		if ownThreadID == "" {
			if tm := req.ClientMetadata["x-codex-turn-metadata"]; tm != "" {
				var metaObj struct {
					SessionID string `json:"session_id"`
					ThreadID  string `json:"thread_id"`
				}
				if err := json.Unmarshal([]byte(tm), &metaObj); err == nil {
					if metaObj.SessionID != "" {
						ownThreadID = metaObj.SessionID
					} else if metaObj.ThreadID != "" {
						ownThreadID = metaObj.ThreadID
					}
				}
			}
		}
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

	// 2. Collect all tools from top-level req.Tools and dynamic tool items (additional_tools, tool_search_output).
	// Persist discovered dynamic tools across all turns in the session so wireBody.Tools never shrinks or mutates
	// when tool_search_output items are compacted away (preserving prefix KV cache stability).
	allToolDefs := make([]domain.ToolDefinition, 0, len(req.Tools))
	allToolDefs = append(allToolDefs, req.Tools...)

	var discoveredTools []domain.ToolDefinition
	for _, item := range parsedInputItems {
		norm := strings.ToLower(strings.TrimSpace(item.Type))
		norm = strings.ReplaceAll(norm, "_", "")
		norm = strings.ReplaceAll(norm, "-", "")

		if norm == "additionaltools" || norm == "toolsearchoutput" || norm == "toolsearch" {
			if len(item.Tools) > 0 {
				discoveredTools = append(discoveredTools, item.Tools...)
			}
			// Also check if tools are inside item.Output or item.Payload
			if len(item.Tools) == 0 {
				if len(item.Output) > 0 {
					var toolContainer struct {
						Tools []domain.ToolDefinition `json:"tools"`
					}
					if err := json.Unmarshal(item.Output, &toolContainer); err == nil && len(toolContainer.Tools) > 0 {
						discoveredTools = append(discoveredTools, toolContainer.Tools...)
					} else {
						var toolList []domain.ToolDefinition
						if err := json.Unmarshal(item.Output, &toolList); err == nil && len(toolList) > 0 {
							discoveredTools = append(discoveredTools, toolList...)
						}
					}
				}
				if len(item.Payload) > 0 {
					var toolContainer struct {
						Tools []domain.ToolDefinition `json:"tools"`
					}
					if err := json.Unmarshal(item.Payload, &toolContainer); err == nil && len(toolContainer.Tools) > 0 {
						discoveredTools = append(discoveredTools, toolContainer.Tools...)
					} else {
						var toolList []domain.ToolDefinition
						if err := json.Unmarshal(item.Payload, &toolList); err == nil && len(toolList) > 0 {
							discoveredTools = append(discoveredTools, toolList...)
						}
					}
				}
			}
		}
	}

	if len(req.Tools) > 0 && h.store != nil && sessionID != "" {
		h.store.RecordSessionTools(sessionID, req.Tools)
	}
	if len(discoveredTools) > 0 && h.store != nil && sessionID != "" {
		h.store.RecordSessionTools(sessionID, discoveredTools)
	}

	hasToolHistory := false
	for _, it := range parsedInputItems {
		norm := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(it.Type), "_", ""), "-", ""))
		if norm == "functioncall" || norm == "customtoolcall" || norm == "imagegenerationcall" || norm == "toolsearchcall" || norm == "functioncalloutput" || norm == "customtoolcalloutput" {
			hasToolHistory = true
			break
		}
	}
	isCompaction := req.Instructions != nil && (strings.Contains(strings.ToLower(*req.Instructions), "compact") || strings.Contains(strings.ToLower(*req.Instructions), "summariz"))

	if (len(req.Tools) > 0 || len(discoveredTools) > 0 || hasToolHistory) && !isCompaction {
		if h.store != nil && sessionID != "" {
			if stickyTools := h.store.GetSessionTools(sessionID); len(stickyTools) > 0 {
				allToolDefs = append(allToolDefs, stickyTools...)
			}
		}
		if len(allToolDefs) == 0 && hasToolHistory {
			allToolDefs = append(allToolDefs, schema.DefaultCodexDesktopTools()...)
		}
		if len(discoveredTools) > 0 {
			allToolDefs = append(allToolDefs, discoveredTools...)
		}

		// Ensure image_generation tool is declared on normal tool-enabled turns so Gemini can generate images
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
	}

	// 3. Compile Tool Declarations
	funcDecls, toolMetaMap := schema.ExtractAndCompileTools(allToolDefs)

	// Codex only renders generated images natively through its own `view_image` tool,
	// so the proxy hands images back through it when the client declared that tool.
	canViewImage := false
	for _, d := range funcDecls {
		if name, _ := d["name"].(string); name == "view_image" {
			canViewImage = true
			break
		}
	}

	// 4. Resolve Model and Effort
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


	// 5. Build Wire Body using parsedInputItems (single-pass, zero redundant allocations)
	wireBody := session.BuildGeminiWireBodyWithItems(
		&req,
		parsedInputItems,
		sessionID,
		funcDecls,
		resolved.ThinkingLevel,
		h.store,
	)
	if wireBody.GenerationConfig == nil {
		wireBody.GenerationConfig = &domain.GeminiGenerationConfig{}
	}
	if wireBody.GenerationConfig.ThinkingConfig == nil {
		if resolved.ThinkingLevel != "" || resolved.ThinkingBudget != nil {
			wireBody.GenerationConfig.ThinkingConfig = &domain.GeminiThinkingConfig{
				IncludeThoughts: true,
				ThinkingLevel:   resolved.ThinkingLevel,
				ThinkingBudget:  resolved.ThinkingBudget,
			}
		}
	} else if resolved.ThinkingBudget != nil && wireBody.GenerationConfig.ThinkingConfig.ThinkingLevel == "" {
		wireBody.GenerationConfig.ThinkingConfig.ThinkingBudget = resolved.ThinkingBudget
	}
	if wireBody.GenerationConfig.MaxOutputTokens == nil && resolved.MaxOutputTokens > 0 {
		maxOut := resolved.MaxOutputTokens
		wireBody.GenerationConfig.MaxOutputTokens = &maxOut
	}

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

	// 8. Stream Responses API SSE (with live x-codex & standard x-ratelimit headers for Codex session token_count & /status UI)
	snap := h.getOrFetchQuotaSnapshot(r.Context())
	w.Header().Set("x-codex-primary-used-percent", fmt.Sprintf("%d", snap.PrimaryUsedPercent))
	w.Header().Set("x-codex-primary-window-minutes", "300")
	w.Header().Set("x-codex-primary-reset-at", fmt.Sprintf("%d", snap.PrimaryResetAt))
	w.Header().Set("x-codex-secondary-used-percent", fmt.Sprintf("%d", snap.WeeklyUsedPercent))
	w.Header().Set("x-codex-secondary-window-minutes", "10080")
	w.Header().Set("x-codex-secondary-reset-at", fmt.Sprintf("%d", snap.WeeklyResetAt))

	// Standard OpenAI & Codex Responses API rate-limit headers (consumed by account/rateLimits/read for /status UI)
	const baseTokenLimit = 1000000
	remTokens := int(float64(baseTokenLimit) * (1.0 - float64(snap.PrimaryUsedPercent)/100.0))
	if remTokens < 0 {
		remTokens = 0
	}
	resetSec := snap.PrimaryResetAfterSec
	if resetSec < 1 {
		resetSec = 1
	}
	resetTokensStr := fmt.Sprintf("%ds", resetSec)

	w.Header().Set("x-ratelimit-limit-tokens", fmt.Sprintf("%d", baseTokenLimit))
	w.Header().Set("x-ratelimit-remaining-tokens", fmt.Sprintf("%d", remTokens))
	w.Header().Set("x-ratelimit-reset-tokens", resetTokensStr)
	w.Header().Set("x-ratelimit-limit-requests", "10000")
	remReq := 10000 - snap.PrimaryUsedPercent*100
	if remReq < 0 {
		remReq = 0
	}
	w.Header().Set("x-ratelimit-remaining-requests", fmt.Sprintf("%d", remReq))
	w.Header().Set("x-ratelimit-reset-requests", resetTokensStr)
	w.Header().Set("x-ratelimit-limit-tokens-usage-based", fmt.Sprintf("%d", baseTokenLimit))
	w.Header().Set("x-ratelimit-remaining-tokens-usage-based", fmt.Sprintf("%d", remTokens))

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

	clientCWD := extractClientCWD(parsedInputItems)
	var imageGenFn func(ctx context.Context, args domain.ImageGenArgs) (string, string, error)
	if h.ccaClient != nil {
		imageGenFn = func(ctx context.Context, args domain.ImageGenArgs) (string, string, error) {
			if args.BaseDir == "" {
				args.BaseDir = clientCWD
			}
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
			savedPath, dataURL, err := h.ccaClient.GenerateImage(ctx, args, curAuth, curProj)
			if err != nil && h.authMgr != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "UNAUTHENTICATED")) {
				freshToken, freshProj, refreshErr := h.authMgr.ForceRefresh()
				if refreshErr == nil && freshToken != "" {
					if freshProj != "" {
						curProj = freshProj
					}
					curAuth = "Bearer " + freshToken
					savedPath, dataURL, err = h.ccaClient.GenerateImage(ctx, args, curAuth, curProj)
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
			Context:      r.Context(),
			ImageGenFn:   imageGenFn,
			ContinueFn:   continueFn,
			CanViewImage: canViewImage,
		},
	)
}

// extractClientCWD finds the client's working directory in the `<cwd>...</cwd>` block Codex
// puts in its environment context message. It is used to resolve relative reference-image paths.
func extractClientCWD(items []domain.InputItem) string {
	find := func(txt string) string {
		if i := strings.Index(txt, "<cwd>"); i >= 0 {
			rest := txt[i+len("<cwd>"):]
			if j := strings.Index(rest, "</cwd>"); j >= 0 {
				return strings.TrimSpace(rest[:j])
			}
		}
		return ""
	}
	// Newest context wins: Codex re-emits the block when the directory changes.
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Role == "assistant" {
			continue
		}
		if c := find(it.Text); c != "" {
			return c
		}
		if len(it.Content) > 0 {
			var parts []domain.ContentPart
			if err := json.Unmarshal(it.Content, &parts); err == nil {
				for _, p := range parts {
					if c := find(p.Text); c != "" {
						return c
					}
				}
			}
		}
	}
	return ""
}

// HandleFallbackJSON returns standard 404 JSON for unhandled endpoints on the Responses API gateway.
func (h *Handlers) HandleFallbackJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": fmt.Sprintf("Endpoint %s not supported on Comini Responses API gateway", r.URL.Path),
			"type":    "invalid_request_error",
			"code":    "not_found",
		},
	})
}
