package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func (h *Handlers) HandleWhamUsage(w http.ResponseWriter, r *http.Request) {
	snap := h.getOrFetchQuotaSnapshot(r.Context())
	email := "comini@antigravity.local"
	if h.authMgr != nil {
		if st := h.authMgr.Status(); st.Email != "" {
			email = st.Email
		}
	}

	limitReached := snap.PrimaryUsedPercent >= 100 || snap.WeeklyUsedPercent >= 100

	payload := map[string]any{
		"user_id":    CodexUserID,
		"account_id": CodexAccountID,
		"email":      email,
		"plan_type":  "pro",
		"rate_limit": map[string]any{
			"allowed":       !limitReached,
			"limit_reached": limitReached,
			"primary_window": map[string]any{
				"used_percent":         snap.PrimaryUsedPercent,
				"limit_window_seconds": 18000,
				"reset_after_seconds":  snap.PrimaryResetAfterSec,
				"reset_at":             snap.PrimaryResetAt,
			},
			"secondary_window": map[string]any{
				"used_percent":         snap.WeeklyUsedPercent,
				"limit_window_seconds": 604800,
				"reset_after_seconds":  snap.WeeklyResetAfterSec,
				"reset_at":             snap.WeeklyResetAt,
			},
		},
		"code_review_rate_limit": nil,
		"additional_rate_limits": []any{},
		"credits": map[string]any{
			"has_credits":          false,
			"unlimited":            false,
			"balance":              "0",
			"approx_local_messages": []int{0, 0},
			"approx_cloud_messages": []int{0, 0},
		},
		"promo": nil,
		"ambient_usage": map[string]any{
			"window": map[string]any{
				"used_percent":         snap.PrimaryUsedPercent,
				"limit_window_seconds": 18000,
				"reset_after_seconds":  snap.PrimaryResetAfterSec,
				"reset_at":             snap.PrimaryResetAt,
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(payload)
}

// HandleWhamAccountsCheck serves /wham/accounts/check and /backend-api/wham/accounts/check for Codex Desktop.
func (h *Handlers) HandleWhamAccountsCheck(w http.ResponseWriter, r *http.Request) {
	payload := map[string]any{
		"accounts": []map[string]any{
			{
				"id":                       CodexAccountID,
				"account_id":               CodexAccountID,
				"account_user_id":          CodexUserID,
				"account_user_role":        "account-owner",
				"name":                     "Comini",
				"plan_type":                "pro",
				"structure":                "personal",
				"is_zdr":                   false,
				"is_openai_internal":       false,
				"can_access_with_session":  true,
				"is_deactivated":           false,
				"workspace_backend_origin": "https://chatgpt.com",
				"account_routing_override": "NO_CONSTRAINT",
			},
		},
		"account_ordering":   []string{CodexAccountID},
		"default_account_id": CodexAccountID,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(payload)
}

// HandleWhamResetCredits serves /wham/rate-limit-reset-credits for Codex Desktop.
func (h *Handlers) HandleWhamResetCredits(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"credits": []any{},
		"summary": map[string]any{
			"available_count": 0,
		},
	})
}

// HandleAccountsFallback serves /accounts/* and /backend-api/accounts/* so Codex Desktop background checks succeed.
// IMPORTANT:
// - /wham/accounts/check expects an ARRAY of accounts (`Gde` schema in main-CqEoQDOU.js)
// - /accounts/check/v4-2023-04-27 expects a RECORD/MAP of accounts keyed by accountId (`iq` / `EAn` schema in app-shared.js)!
func (h *Handlers) HandleAccountsFallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(r.URL.Path, "optimized/check") {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"account": map[string]any{
				"account_id":                     CodexAccountID,
				"account_user_id":                CodexUserID,
				"account_user_role":              "account-owner",
				"name":                           "Comini",
				"plan_type":                      "pro",
				"structure":                      "personal",
				"is_zdr":                         false,
				"is_openai_internal":             false,
				"is_deactivated":                 false,
				"is_fedramp_compliant_workspace": false,
				"tbo_config": map[string]any{
					"tbo_available": false,
				},
			},
			"account_user": map[string]any{
				"is_trial":                     false,
				"seat_type":                    "default",
				"trial_state":                  nil,
				"trial_expires_at":             nil,
				"pending_seat_upgrade_request": false,
			},
			"features": []string{"plugins", "apps", "search_mcp", "tool_suggest"},
		})
		return
	}
	if strings.Contains(r.URL.Path, "check") {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accounts": map[string]any{
				CodexAccountID: map[string]any{
					"account": map[string]any{
						"account_id":                     CodexAccountID,
						"account_user_id":                CodexUserID,
						"account_user_role":              "account-owner",
						"name":                           "Comini",
						"plan_type":                      "pro",
						"structure":                      "personal",
						"is_zdr":                         false,
						"is_openai_internal":             false,
						"is_deactivated":                 false,
						"is_fedramp_compliant_workspace": false,
						"has_previously_paid_subscription": true,
						"tbo_config": map[string]any{
							"tbo_available": false,
						},
					},
					"entitlement": map[string]any{
						"subscription_id":                "sub_comini_pro",
						"has_active_subscription":        true,
						"subscription_plan":              "chatgptproplan",
						"expires_at":                     "2099-12-31T23:59:59Z",
						"renews_at":                      "2099-12-31T23:59:59Z",
						"billing_period":                 "monthly",
						"is_delinquent":                  false,
						"is_active_subscription_gratis":  false,
						"trial":                          nil,
						"discount":                       nil,
					},
					"last_active_subscription": map[string]any{
						"subscription_id":          "sub_comini_pro",
						"purchase_origin_platform": "chatgpt_web",
						"will_renew":               true,
					},
					"can_access_with_session": true,
					"sso_connection_name":     nil,
					"features":                []string{"plugins", "apps", "search_mcp", "tool_suggest"},
					"eligible_offers":         []any{},
				},
			},
			"account_ordering":   []string{CodexAccountID},
			"default_account_id": CodexAccountID,
		})
		return
	}
	if strings.Contains(r.URL.Path, "mfa_info") {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mfa_enabled_v2": false,
		})
		return
	}
	if strings.Contains(r.URL.Path, "verified_access") {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"programs": []any{},
		})
		return
	}
	// /accounts/{account_id}/settings (`rde` schema in main-CqEoQDOU.js)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"beta_settings": map[string]bool{
			"wham_local_access": true,
			"wham_access":       true,
		},
		"beta_settings_str":        map[string]string{},
		"permissions":              []string{},
		"programs":                 []any{},
		"admin_work_mode_enabled":  true,
		"admin_work_local_enabled": true,
	})
}

// HandleWhamStatsigBootstrap serves POST /wham/statsig/bootstrap by fetching live evaluations from ab.chatgpt.com/v1/initialize and embedding the exact Statsig user so Hhi(client) and Ny(publication, user) succeed.
func (h *Handlers) HandleWhamStatsigBootstrap(w http.ResponseWriter, r *http.Request) {
	email := "comini@antigravity.local"
	if h.authMgr != nil {
		if st := h.authMgr.Status(); st.Email != "" {
			email = st.Email
		}
	}

	var reqBody map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
	}
	strField := func(k, def string) string {
		if v, ok := reqBody[k].(string); ok && v != "" {
			return v
		}
		return def
	}
	appSessionID := strField("app_session_id", "")
	appVersion := strField("app_version", "26.1007.71326")
	brandName := strField("brand_name", "chatgpt")
	buildFlavor := strField("build_flavor", "prod")
	locale := strField("locale", "en-US")
	stableID := strField("stable_id", "5c8a2909-e339-442e-8da4-0639923d46d7")
	systemName := strField("system_name", "macOS")
	systemVersion := strField("system_version", "15.5.0")
	windowType := strField("window_type", "electron")
	betaEnabled, _ := reqBody["desktop_app_beta_enabled"].(bool)

	customMap := map[string]any{
		"auth_status":              "logged_in",
		"auth_method":              "chatgpt",
		"account_id":               CodexAccountID,
		"plan_type":                "pro",
		"is_paid":                  true,
		"is_openai_internal":       strings.HasSuffix(email, "@openai.com"),
		"brand_name":               brandName,
		"systemName":               systemName,
		"systemVersion":            systemVersion,
		"codex_window_type":        windowType,
		"codex_build_flavor":       buildFlavor,
		"desktop_app_beta_enabled": betaEnabled,
	}
	if appSessionID != "" {
		customMap["codex_app_session_id"] = appSessionID
	}

	userObj := map[string]any{
		"userID":     CodexUserID,
		"email":      email,
		"locale":     locale,
		"appVersion": appVersion,
		"customIDs": map[string]string{
			"account_id":               CodexAccountID,
			"stableID":                 stableID,
			"source_surface_stable_id": stableID,
		},
		"custom": customMap,
	}

	evalMap := map[string]any{
		"feature_gates":   map[string]any{},
		"dynamic_configs": map[string]any{},
		"layer_configs":   map[string]any{},
		"has_updates":     true,
		"time":            time.Now().UnixMilli(),
	}

	initReqBytes, _ := json.Marshal(map[string]any{
		"user": userObj,
		"hash": "djb2",
	})
	httpClient := &http.Client{Timeout: 6 * time.Second}
	if upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://ab.chatgpt.com/v1/initialize", bytes.NewReader(initReqBytes)); err == nil {
		upReq.Header.Set("Content-Type", "application/json")
		upReq.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) ChatGPT/"+appVersion+" Chrome/136.0.7103.49 Electron/36.2.0 Safari/537.36")
		upReq.Header.Set("statsig-api-key", "client-sYWqzCYMRkUg4DqqiZcR5DGTNl2iD7zNJY0HoeDLzxR")
		upReq.Header.Set("statsig-sdk-type", "js-client")
		if upResp, err := httpClient.Do(upReq); err == nil {
			defer upResp.Body.Close()
			if upResp.StatusCode == http.StatusOK {
				var liveEval map[string]any
				if err := json.NewDecoder(upResp.Body).Decode(&liveEval); err == nil && len(liveEval) > 0 {
					evalMap = liveEval
				}
			}
		}
	}

	evalMap["user"] = userObj
	evalMap["has_updates"] = true
	if _, ok := evalMap["time"]; !ok {
		evalMap["time"] = time.Now().UnixMilli()
	}

	// Inject feature gates: Hide Images, Library, Maps ('...' more menu), while preserving Plugins
	gates, _ := evalMap["feature_gates"].(map[string]any)
	if gates == nil {
		gates = make(map[string]any)
	}
	setGate := func(id string, val bool) {
		gates[id] = map[string]any{
			"value":   val,
			"rule_id": "comini_override",
		}
	}
	// 3085093835: Unified mode gate (hide Images, Maps, Shopping, and '...' overflow menu in sidebar)
	setGate("3085093835", false)
	// 224248639: File library gate (hide Library tab in sidebar)
	setGate("224248639", false)
	// 15851580: Project-level file library
	setGate("15851580", false)
	// 553358387: Library search & integration
	setGate("553358387", false)
	// 1008193688: Image Generation capability
	setGate("1008193688", false)
	// 2357954336: Sidebar visible defaults
	setGate("2357954336", false)
	// 375130565: Hosted cloud / durable executor gate (disable so local threads are not routed to cloud durable host)
	setGate("375130565", false)
	// 4218407052: Marketplace & plugin directory (keep ENABLED so Plugins store remains accessible)
	setGate("4218407052", true)
	evalMap["feature_gates"] = gates

	// Disable layer configs (web_file_library_enabled)
	layers, _ := evalMap["layer_configs"].(map[string]any)
	if layers == nil {
		layers = make(map[string]any)
	}
	layers["3017902351"] = map[string]any{
		"value": map[string]any{
			"web_file_library_enabled": false,
		},
		"rule_id": "comini_override",
	}
	evalMap["layer_configs"] = layers

	statsigInner, _ := json.Marshal(evalMap)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"statsigPayload": string(statsigInner),
	})
}

// HandleWhamOnboardingContext serves GET /wham/onboarding/context.
func (h *Handlers) HandleWhamOnboardingContext(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"role": "coding",
	})
}

// HandleWhamMfaRequirement serves GET /wham/remote/control/mfa_requirement.
func (h *Handlers) HandleWhamMfaRequirement(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"requirement": "none",
	})
}

// HandleWhamSitesAccess serves GET /wham/sites/access.
func (h *Handlers) HandleWhamSitesAccess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"enabled": false,
	})
}

func buildUserSettings() map[string]any {
	return map[string]any{
		"chat_theme":                          "default",
		"lockdown_mode_enabled":               false,
		"image_gen_enabled":                   false,
		"file_library_auto_reference":         false,
		"bazaar_personalization_enabled":      true,
		"instant_answers_enabled":             true,
		"connector_search_enabled":            true,
		"dictation_enabled":                   true,
		"voice_enabled":                       true,
		"enable_flora_network_access":         true,
		"memory_in_search":                    true,
		"training_allowed":                    true,
		"task_suggestions_enabled":            true,
		"developer_mode":                      false,
		"enable_device_code_auth":             true,
		"enable_remote_browser_data":          false,
		"free_ads_opt_out":                    true,
		"moonshine":                           false,
		"sunshine":                            false,
		"user_memory_v2_migrated":             true,
		"tpp_work_mode_quick_send_by_default": false,
	}
}

// HandleSettingsUser serves GET /settings/user (`JXn` schema in app-shared.js).
func (h *Handlers) HandleSettingsUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"announcements": map[string]any{
			"oai/apps/hasSeenOnboarding":         "true",
			"oai/apps/hasCompletedTppOnboarding": "true",
		},
		"beta_settings": map[string]any{
			"memory":                true,
			"m3m_workspace_enabled": true,
		},
		"flags": map[string]any{
			"bazaar_consent_required":                      false,
			"bazaar_personalization_unavailable":           false,
			"file_library_enabled_for_registration_country": false,
			"image_gen_holdout_active":                      true,
		},
		"settings": buildUserSettings(),
	})
}

// HandleSettingsMutation serves PATCH/POST /settings/* mutations.
func (h *Handlers) HandleSettingsMutation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	settings := buildUserSettings()
	feature := r.URL.Query().Get("feature")
	valStr := r.URL.Query().Get("value")
	if feature != "" {
		if valStr == "true" {
			settings[feature] = true
		} else if valStr == "false" {
			settings[feature] = false
		} else {
			settings[feature] = valStr
		}
	}

	resp := make(map[string]any, len(settings)+2)
	for k, v := range settings {
		resp[k] = v
	}
	resp["success"] = true
	resp["settings"] = settings
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleMe serves GET /me (`_Mo` schema in app-initial.js).
func (h *Handlers) HandleMe(w http.ResponseWriter, r *http.Request) {
	email := "comini@antigravity.local"
	if h.authMgr != nil {
		if st := h.authMgr.Status(); st.Email != "" {
			email = st.Email
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      CodexUserID,
		"email":   email,
		"name":    "Comini",
		"picture": nil,
	})
}

// HandleEmptyArray serves endpoints expecting an empty JSON array (`[]`).
func (h *Handlers) HandleEmptyArray(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("[]\n"))
}

// HandleFloraExecutor serves POST/GET /flora/cca/executor (`ky` schema in app.asar).
func (h *Handlers) HandleFloraExecutor(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"executor_id":         "exec_comini_local",
		"custom_display_name": "Comini Local",
	})
}

// HandleTppModels serves GET /tpp/models/ (`SCn` schema in app-shared.js).
func (h *Handlers) HandleTppModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"categories": []map[string]any{
			{
				"category":             "flagship",
				"human_category_name":  "Antigravity",
				"human_category_short": "Antigravity",
				"default_model":        "google-antigravity/gemini-4-argon",
				"subscription_level":   "pro",
			},
		},
		"default_model_slug": "google-antigravity/gemini-4-argon",
		"internal_groups":    []any{},
		"models": []map[string]any{
			{
				"slug":               "google-antigravity/gemini-4-argon",
				"max_tokens":         1048576,
				"title":              "Gemini 4 Argon",
				"description":        "Google Antigravity Gemini 4 Argon",
				"tags":               []string{"pro"},
				"capabilities":       map[string]any{},
				"product_features":   map[string]any{},
				"enabled_tools":      []string{"tools", "canvas"},
			},
			{
				"slug":               "google-antigravity/gemini-3.8-flash",
				"max_tokens":         1048576,
				"title":              "Gemini 3.8 Flash",
				"description":        "Google Antigravity Gemini 3.8 Flash",
				"tags":               []string{"fast"},
				"capabilities":       map[string]any{},
				"product_features":   map[string]any{},
				"enabled_tools":      []string{"tools", "canvas"},
			},
			{
				"slug":               "google-antigravity/claude-opus-5-5",
				"max_tokens":         200000,
				"title":              "Claude Opus 5.5",
				"description":        "Anthropic Claude Opus 5.5",
				"tags":               []string{"pro"},
				"capabilities":       map[string]any{},
				"product_features":   map[string]any{},
				"enabled_tools":      []string{"tools", "canvas"},
			},
			{
				"slug":               "google-antigravity/claude-sonnet-5-5",
				"max_tokens":         200000,
				"title":              "Claude Sonnet 5.5",
				"description":        "Anthropic Claude Sonnet 5.5",
				"tags":               []string{"pro"},
				"capabilities":       map[string]any{},
				"product_features":   map[string]any{},
				"enabled_tools":      []string{"tools", "canvas"},
			},
		},
		"slider_settings": []any{},
		"versions":        []any{},
	})
}

// HandleDeviceCheck serves POST /devicecheck and /backend-api/devicecheck so Electron DeviceCheckCookieManager succeeds.
func (h *Handlers) HandleDeviceCheck(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "_devicecheck",
		Value:    "comini_ok",
		Path:     "/",
		MaxAge:   86400 * 30,
		HttpOnly: true,
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
	})
}

func (h *Handlers) HandleCelsiusWsUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"websocket_url": "ws://localhost:8000/celsius/ws/noop",
		"expires_at":    time.Now().Add(time.Hour).Unix(),
	})
}


func (h *Handlers) HandleEmptyObject(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}\n"))
}

func (h *Handlers) HandleProfiles(w http.ResponseWriter, r *http.Request) {
	email := "comini@antigravity.local"
	if h.authMgr != nil {
		if st := h.authMgr.Status(); st.Email != "" {
			email = st.Email
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"user_id":            CodexUserID,
		"account_id":         CodexAccountID,
		"email":              email,
		"display_name":       email,
		"username":           "comini",
		"profile_visibility": "private",
		"is_self":            true,
		"profile_details": map[string]any{
			"display_name":        email,
			"username":            "comini",
			"profile_picture_url": nil,
		},
		"page": map[string]any{
			"workspace_id": nil,
			"visibility": map[string]any{
				"value": "private",
			},
			"pins": map[string]any{
				"items": []any{},
			},
			"stats":          nil,
			"insights":       nil,
			"top_plugins":    nil,
			"activity_graph": nil,
		},
		"items": []any{},
		"pins": map[string]any{
			"items": []any{},
		},
	})
}

func (h *Handlers) HandleRemoteControlEnroll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"server_id":            "srv_comini_local",
		"environment_id":       "env_comini_local",
		"remote_control_token": "rct_comini_local",
		"expires_at":           time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
}

func (h *Handlers) HandleImagesInit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"groups": []any{},
	})
}

func (h *Handlers) HandleImagesStyles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"styles":            []any{},
		"image_suggestions": []any{},
	})
}

func (h *Handlers) HandleRecentImageGen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":  []any{},
		"cursor": nil,
	})
}

// HandleImagesFallback serves /images/* fallback endpoints (prompt-items, image-tags, etc.).
func (h *Handlers) HandleImagesFallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":             []any{},
		"groups":            []any{},
		"styles":            []any{},
		"image_suggestions": []any{},
		"cursor":            nil,
	})
}

// HandleFilesLibraryStorageUsage serves GET /files/library/storage/usage.
func (h *Handlers) HandleFilesLibraryStorageUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"plan_type":       "pro",
		"limit_tier":      "pro",
		"used_bytes":      0,
		"allowed_bytes":   107374182400,
		"remaining_bytes": 107374182400,
		"is_over_limit":   false,
	})
}

// HandleFilesLibraryFallback serves /files/library/* and /library/* fallback endpoints.
func (h *Handlers) HandleFilesLibraryFallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":        []any{},
		"cursor":       nil,
		"onedrive":     false,
		"dropbox":      false,
		"sharepoint":   false,
		"google_drive": false,
	})
}

func (h *Handlers) HandleGizmosSidebar(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":  []any{},
		"cursor": nil,
	})
}

func (h *Handlers) HandleLocalHome(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"nearby_entities":      []any{},
		"personalized_prompts": []any{},
		"map_center":           nil,
	})
}

func (h *Handlers) HandlePets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"pets":   []any{},
		"cursor": nil,
	})
}

func (h *Handlers) HandleAutoTopUpSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"is_enabled":                         false,
		"payment_method":                     nil,
		"recharge_threshold":                 nil,
		"recharge_target":                    nil,
		"recharge_monthly_limit":             nil,
		"auto_reload_credit_discount_policy": nil,
	})
}

func (h *Handlers) HandleSystemHints(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"system_hints": []any{},
	})
}

func (h *Handlers) HandleTrustedContactEnabled(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"enabled": false,
	})
}

func (h *Handlers) HandleWhamAnalytics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":              []any{},
		"series":            []any{},
		"units":             "tokens",
		"unit":              "credit",
		"data_freshness_ts": nil,
	})
}

func (h *Handlers) HandleWhamTasksList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":  []any{},
		"cursor": nil,
	})
}

func (h *Handlers) HandleWhamChatpassApps(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"apps": []any{},
	})
}

func (h *Handlers) HandleConversationInit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":                        "conversation_detail_metadata",
		"banner_info":                 nil,
		"blocked_features":            []any{},
		"default_model_slug":          "google-antigravity/gemini-3.8-flash",
		"intended_default_model_slug": "google-antigravity/gemini-3.8-flash",
		"limits_progress":             []any{},
		"model_limits":                []any{},
	})
}

func (h *Handlers) HandleFallbackJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && r.Body != nil {
		bodyBytes, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		var rpcReq map[string]any
		if len(bodyBytes) > 0 && json.Unmarshal(bodyBytes, &rpcReq) == nil {
			if rpcReq["jsonrpc"] == "2.0" {
				id, hasID := rpcReq["id"]
				if !hasID {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				method, _ := rpcReq["method"].(string)
				var res any = map[string]any{}
				switch method {
				case "initialize":
					res = map[string]any{
						"protocolVersion": "2025-03-26",
						"capabilities": map[string]any{
							"tools":     map[string]any{},
							"resources": map[string]any{},
							"prompts":   map[string]any{},
						},
						"serverInfo": map[string]any{
							"name":    "codex_apps",
							"version": "1.0.0",
						},
					}
				case "tools/list":
					res = map[string]any{"tools": []any{}}
				case "resources/list":
					res = map[string]any{"resources": []any{}}
				case "prompts/list":
					res = map[string]any{"prompts": []any{}}
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result":  res,
				})
				return
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":    []any{},
		"data":     []any{},
		"plugins":  []any{},
		"results":  []any{},
		"sections": []any{},
		"groups":   []any{},
		"styles":   []any{},
		"apps":     []any{},
		"pets":     []any{},
		"success":  true,
		"cursor":   nil,
	})
}

