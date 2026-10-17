package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"comini/internal/server"
)

func TestPluginsEndpoints(t *testing.T) {
	handlers := server.NewHandlers(nil, nil, nil)

	// 1. Test /ps/plugins/home
	{
		req := httptest.NewRequest(http.MethodGet, "/ps/plugins/home", nil)
		w := httptest.NewRecorder()
		handlers.HandlePluginsHome(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for /ps/plugins/home, got %d", w.Code)
		}

		var resp struct {
			Sections []map[string]any `json:"sections"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode /ps/plugins/home response: %v", err)
		}
		if len(resp.Sections) < 7 {
			t.Fatalf("expected at least 7 sections in /ps/plugins/home, got %d", len(resp.Sections))
		}
		for _, s := range resp.Sections {
			plugins, ok := s["plugins"].([]any)
			if !ok || len(plugins) == 0 {
				t.Fatalf("expected non-empty plugins in section %v", s["id"])
			}
		}
	}

	// 2. Test /plugins/featured
	{
		req := httptest.NewRequest(http.MethodGet, "/plugins/featured", nil)
		w := httptest.NewRecorder()
		handlers.HandlePluginsFeatured(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for /plugins/featured, got %d", w.Code)
		}
		var featured []string
		if err := json.NewDecoder(w.Body).Decode(&featured); err != nil {
			t.Fatalf("failed to decode /plugins/featured response: %v", err)
		}
		if len(featured) < 10 {
			t.Fatalf("expected at least 10 featured plugin IDs, got %d", len(featured))
		}
	}

	// 3. Test /ps/plugins/list?scope=GLOBAL
	{
		req := httptest.NewRequest(http.MethodGet, "/ps/plugins/list?scope=GLOBAL", nil)
		w := httptest.NewRecorder()
		handlers.HandlePluginsInstalled(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for /ps/plugins/list, got %d", w.Code)
		}
		var resp struct {
			Plugins []map[string]any `json:"plugins"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode /ps/plugins/list response: %v", err)
		}
		if len(resp.Plugins) < 80 {
			t.Fatalf("expected at least 80 plugins for GLOBAL scope, got %d", len(resp.Plugins))
		}
	}

	// 4. Test /ps/plugin-categories/{category}/plugins for multiple categories
	categories := []string{"featured", "developer-tools", "productivity", "documents-office", "data-research", "design-creative", "business-sales"}
	for _, cat := range categories {
		req := httptest.NewRequest(http.MethodGet, "/ps/plugin-categories/"+cat+"/plugins", nil)
		w := httptest.NewRecorder()
		handlers.HandlePluginCategories(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for /ps/plugin-categories/%s/plugins, got %d", cat, w.Code)
		}
		var resp struct {
			Plugins []map[string]any `json:"plugins"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode category %s response: %v", cat, err)
		}
		if len(resp.Plugins) == 0 {
			t.Fatalf("expected non-empty plugins for category %s", cat)
		}
	}

	// 5. Test /connectors/directory/list
	{
		req := httptest.NewRequest(http.MethodGet, "/connectors/directory/list", nil)
		w := httptest.NewRecorder()
		handlers.HandleConnectorsDirectoryList(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for /connectors/directory/list, got %d", w.Code)
		}
		var resp struct {
			Apps []map[string]any `json:"apps"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode connectors directory: %v", err)
		}
		if len(resp.Apps) < 80 {
			t.Fatalf("expected at least 80 apps in connectors directory, got %d", len(resp.Apps))
		}
	}

	// 6. Test /ps/apps/batch
	{
		req := httptest.NewRequest(http.MethodPost, "/ps/apps/batch", strings.NewReader(`{"app_ids":["app_github","app_slack"]}`))
		w := httptest.NewRecorder()
		handlers.HandlePsApps(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for /ps/apps/batch, got %d", w.Code)
		}
		var resp struct {
			Apps []map[string]any `json:"apps"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode /ps/apps/batch: %v", err)
		}
		if len(resp.Apps) != 2 {
			t.Fatalf("expected 2 apps in /ps/apps/batch, got %d", len(resp.Apps))
		}
	}

	// 7. Test /aip/connectors/links/list_accessible
	{
		req := httptest.NewRequest(http.MethodPost, "/aip/connectors/links/list_accessible", strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		handlers.HandleAipConnectors(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for links/list_accessible, got %d", w.Code)
		}
		var resp struct {
			Links []any `json:"links"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode links: %v", err)
		}
		if resp.Links == nil {
			t.Fatalf("expected non-nil links array")
		}
	}

	// 8. Test /ps/plugins/canonical-app/{app_id}
	{
		req := httptest.NewRequest(http.MethodGet, "/ps/plugins/canonical-app/app_snowflake", nil)
		w := httptest.NewRecorder()
		handlers.HandlePluginsInstalled(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for canonical-app, got %d", w.Code)
		}
		var resp struct {
			PluginID string         `json:"plugin_id"`
			Plugin   map[string]any `json:"plugin"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode canonical-app: %v", err)
		}
		if resp.PluginID != "app_snowflake" || resp.Plugin == nil {
			t.Fatalf("expected valid canonical app response, got %+v", resp)
		}
	}

	// 9. Test /ps/plugins/installed?scope=GLOBAL
	{
		req := httptest.NewRequest(http.MethodGet, "/ps/plugins/installed?scope=GLOBAL", nil)
		w := httptest.NewRecorder()
		handlers.HandlePluginsInstalled(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for /ps/plugins/installed?scope=GLOBAL, got %d", w.Code)
		}
		var resp struct {
			Plugins []map[string]any `json:"plugins"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode /ps/plugins/installed: %v", err)
		}
		if len(resp.Plugins) < 80 {
			t.Fatalf("expected all public plugins when scope=GLOBAL, got %d", len(resp.Plugins))
		}
		for _, p := range resp.Plugins {
			st, _ := p["status"].(string)
			if st != "AVAILABLE" && st != "ENABLED" && st != "DISABLED_BY_ADMIN" {
				t.Fatalf("plugin %v has invalid Rust enum status: %q (must be AVAILABLE, ENABLED, or DISABLED_BY_ADMIN)", p["id"], st)
			}
			if p["status"] == "INSTALLED" {
				t.Fatalf("forbidden status 'INSTALLED' found for plugin %v", p["id"])
			}
			av, _ := p["availability"].(string)
			if av != st {
				t.Fatalf("plugin %v availability %q does not match status %q", p["id"], av, st)
			}
		}
	}

	// 10. Test dynamic install and uninstall
	{
		// Install custom plugin
		reqInstall := httptest.NewRequest(http.MethodPost, "/ps/plugins/plugin_datadog/install", nil)
		wInstall := httptest.NewRecorder()
		handlers.HandlePluginsInstalled(wInstall, reqInstall)
		if wInstall.Code != http.StatusOK {
			t.Fatalf("expected 200 for /install, got %d", wInstall.Code)
		}

		// Verify it's ENABLED
		reqGet := httptest.NewRequest(http.MethodGet, "/ps/plugins/plugin_datadog", nil)
		wGet := httptest.NewRecorder()
		handlers.HandlePluginsInstalled(wGet, reqGet)
		var item map[string]any
		if err := json.NewDecoder(wGet.Body).Decode(&item); err != nil {
			t.Fatalf("failed to decode plugin details: %v", err)
		}
		if item["status"] != "ENABLED" || item["enabled"] != true {
			t.Fatalf("expected plugin_datadog to be ENABLED, got %+v", item)
		}

		// Uninstall
		reqUninstall := httptest.NewRequest(http.MethodPost, "/ps/plugins/plugin_datadog/uninstall", nil)
		wUninstall := httptest.NewRecorder()
		handlers.HandlePluginsInstalled(wUninstall, reqUninstall)
		if wUninstall.Code != http.StatusOK {
			t.Fatalf("expected 200 for /uninstall, got %d", wUninstall.Code)
		}

		// Verify it's AVAILABLE
		wGet2 := httptest.NewRecorder()
		handlers.HandlePluginsInstalled(wGet2, reqGet)
		var item2 map[string]any
		if err := json.NewDecoder(wGet2.Body).Decode(&item2); err != nil {
			t.Fatalf("failed to decode plugin details: %v", err)
		}
		if item2["status"] != "AVAILABLE" || item2["enabled"] != false {
			t.Fatalf("expected plugin_datadog to be AVAILABLE after uninstall, got %+v", item2)
		}
	}
}
