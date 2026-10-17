package server

import (
	"log"
	"net/http"
	"strings"
)

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

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
	mux.HandleFunc("/v1/chat/completions", h.HandleResponses)

	// Codex Desktop & codex app-server Quota / Account / Settings endpoints
	mux.HandleFunc("/wham/usage", h.HandleWhamUsage)
	mux.HandleFunc("/codex/usage", h.HandleWhamUsage)
	mux.HandleFunc("/wham/accounts/check", h.HandleWhamAccountsCheck)
	mux.HandleFunc("/wham/rate-limit-reset-credits", h.HandleWhamResetCredits)
	mux.HandleFunc("/wham/rate-limit-reset-credits/", h.HandleWhamResetCredits)
	mux.HandleFunc("/wham/statsig/bootstrap", h.HandleWhamStatsigBootstrap)
	mux.HandleFunc("/wham/onboarding/context", h.HandleWhamOnboardingContext)
	mux.HandleFunc("/wham/work/settings", h.HandleWhamWorkSettings)
	mux.HandleFunc("/wham/predictions/config", h.HandleWhamPredictionsConfig)
	mux.HandleFunc("/wham/remote/control/mfa_requirement", h.HandleWhamMfaRequirement)
	mux.HandleFunc("/wham/sites/access", h.HandleWhamSitesAccess)
	mux.HandleFunc("/wham/github/connections", h.HandleEmptyArray)
	mux.HandleFunc("/accounts/", h.HandleAccountsFallback)
	mux.HandleFunc("/accounts", h.HandleAccountsFallback)
	mux.HandleFunc("/settings/user", h.HandleSettingsUser)
	mux.HandleFunc("/wham/settings/user", h.HandleSettingsUser)
	mux.HandleFunc("/settings/", h.HandleSettingsMutation)
	mux.HandleFunc("/settings", h.HandleSettingsMutation)
	mux.HandleFunc("/me", h.HandleMe)
	mux.HandleFunc("/pins", h.HandleEmptyArray)
	mux.HandleFunc("/ps/plugins/", h.HandlePluginsInstalled)
	mux.HandleFunc("/ps/plugins/home", h.HandlePluginsHome)
	mux.HandleFunc("/ps/plugins", h.HandlePluginsInstalled)
	mux.HandleFunc("/ps/plugin-categories/", h.HandlePluginCategories)
	mux.HandleFunc("/ps/plugin-categories", h.HandlePluginCategories)
	mux.HandleFunc("/plugins/featured", h.HandlePluginsFeatured)
	mux.HandleFunc("/plugins/user-suggestions", h.HandleEmptyObject)
	mux.HandleFunc("/plugins/", h.HandlePluginsInstalled)
	mux.HandleFunc("/plugins", h.HandlePluginsInstalled)
	mux.HandleFunc("/ps/apps/", h.HandlePsApps)
	mux.HandleFunc("/ps/apps", h.HandlePsApps)
	mux.HandleFunc("/ps/connectors/by-template", h.HandleEmptyArray)
	mux.HandleFunc("/connectors/directory/", h.HandleConnectorsDirectoryList)
	mux.HandleFunc("/aip/first-party/eligibility", h.HandleEmptyObject)
	mux.HandleFunc("/aip/connectors/", h.HandleAipConnectors)
	mux.HandleFunc("/tpp/models/", h.HandleTppModels)
	mux.HandleFunc("/tpp/models", h.HandleTppModels)
	mux.HandleFunc("/flora/cca/executor", h.HandleFloraExecutor)
	mux.HandleFunc("/flora/cca/executor/", h.HandleFloraExecutor)
	mux.HandleFunc("/devicecheck", h.HandleDeviceCheck)
	mux.HandleFunc("/celsius/ws/user", h.HandleCelsiusWsUser)
	mux.HandleFunc("/conversations", h.HandleConversations)
	mux.HandleFunc("/conversations/", h.HandleConversations)
	mux.HandleFunc("/conversation/init", h.HandleConversationInit)
	mux.HandleFunc("/conversation/", h.HandleConversationSingle)
	mux.HandleFunc("/wham/remote/control/server/enroll", h.HandleRemoteControlEnroll)
	mux.HandleFunc("/wham/remote/control/server/refresh", h.HandleRemoteControlEnroll)
	mux.HandleFunc("/wham/tasks/list", h.HandleWhamTasksList)
	mux.HandleFunc("/wham/analytics/", h.HandleWhamAnalytics)
	mux.HandleFunc("/wham/usage/chatpass/apps", h.HandleWhamChatpassApps)
	mux.HandleFunc("/wham/usage/credit-usage-events", h.HandleWhamAnalytics)
	mux.HandleFunc("/wham/usage/daily-token-usage-breakdown", h.HandleWhamAnalytics)
	mux.HandleFunc("/wham/usage/", h.HandleWhamAnalytics)
	mux.HandleFunc("/beacons/", h.HandleEmptyObject)
	mux.HandleFunc("/profiles/", h.HandleProfiles)
	mux.HandleFunc("/wham/profiles/", h.HandleProfiles)
	mux.HandleFunc("/subscriptions/auto_top_up/settings", h.HandleAutoTopUpSettings)
	mux.HandleFunc("/subscriptions/credits/discount-offer", h.HandleSubscriptionsDiscountOffer)
	mux.HandleFunc("/subscriptions/", h.HandleEmptyObject)
	mux.HandleFunc("/amphora/notifications", h.HandleConversations)
	mux.HandleFunc("/amphora/", h.HandleEmptyObject)
	mux.HandleFunc("/amphora", h.HandleEmptyObject)
	mux.HandleFunc("/automations", h.HandleConversations)
	mux.HandleFunc("/automations/", h.HandleEmptyObject)
	mux.HandleFunc("/suggested_automations", h.HandleEmptyArray)
	mux.HandleFunc("/images/init", h.HandleImagesInit)
	mux.HandleFunc("/images/styles", h.HandleImagesStyles)
	mux.HandleFunc("/images/", h.HandleImagesFallback)
	mux.HandleFunc("/images", h.HandleImagesFallback)
	mux.HandleFunc("/my/recent/image_gen", h.HandleRecentImageGen)
	mux.HandleFunc("/my/recent/", h.HandleRecentImageGen)
	mux.HandleFunc("/my/recent", h.HandleRecentImageGen)
	mux.HandleFunc("/files/library/storage/usage", h.HandleFilesLibraryStorageUsage)
	mux.HandleFunc("/files/library/", h.HandleFilesLibraryFallback)
	mux.HandleFunc("/files/library", h.HandleFilesLibraryFallback)
	mux.HandleFunc("/files/", h.HandleFilesLibraryFallback)
	mux.HandleFunc("/files", h.HandleFilesLibraryFallback)
	mux.HandleFunc("/library/", h.HandleFilesLibraryFallback)
	mux.HandleFunc("/library", h.HandleFilesLibraryFallback)
	mux.HandleFunc("/gizmos/snorlax/sidebar", h.HandleGizmosSidebar)
	mux.HandleFunc("/local/home", h.HandleLocalHome)
	mux.HandleFunc("/pets", h.HandlePets)
	mux.HandleFunc("/pets/", h.HandlePets)
	mux.HandleFunc("/system_hints", h.HandleSystemHints)
	mux.HandleFunc("/trusted_contact/enabled", h.HandleTrustedContactEnabled)
	mux.HandleFunc("/trusted_contact", h.HandleEmptyArray)
	mux.HandleFunc("/sentinel/heartbeat", h.HandleEmptyObject)
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

		// Strip /backend-api or /api prefix so both direct and backend-api routes match cleanly
		if strings.HasPrefix(r.URL.Path, "/backend-api") {
			r2 := *r
			u2 := *r.URL
			u2.Path = strings.TrimPrefix(r.URL.Path, "/backend-api")
			if u2.Path == "" {
				u2.Path = "/"
			}
			r2.URL = &u2
			mux.ServeHTTP(lrw, &r2)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			r2 := *r
			u2 := *r.URL
			u2.Path = strings.TrimPrefix(r.URL.Path, "/api")
			if u2.Path == "" {
				u2.Path = "/"
			}
			r2.URL = &u2
			mux.ServeHTTP(lrw, &r2)
			return
		}

		mux.ServeHTTP(lrw, r)
	})
}
