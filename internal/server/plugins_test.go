package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"comini/internal/config"
	"comini/internal/session"
	"comini/internal/transport"
)

func newTestHandlers() *Handlers {
	cfg := &config.Config{
		Host:         "127.0.0.1",
		Port:         9876,
		DefaultModel: "google-antigravity/gemini-3.8-flash",
	}
	ccaClient := transport.NewCCAClient(cfg.CCABaseURL, cfg.UserAgent)
	store := session.DefaultSignatureStore
	return NewHandlers(cfg, ccaClient, store)
}

func TestPluginStore_Load(t *testing.T) {
	store := GetDefaultPluginStore()
	store.mu.RLock()
	defer store.mu.RUnlock()

	if len(store.pluginsList) == 0 {
		t.Fatalf("expected plugins to be loaded from disk, got 0")
	}

	github := store.resolvePlugin("github")
	if github == nil {
		t.Fatalf("expected 'github' plugin to be resolved")
	}

	if github.Manifest.Interface.DisplayName != "GitHub" {
		t.Errorf("expected DisplayName 'GitHub', got %q", github.Manifest.Interface.DisplayName)
	}

	if github.Manifest.Interface.Category != "Developer Tools" {
		t.Errorf("expected Category 'Developer Tools', got %q", github.Manifest.Interface.Category)
	}
}

func TestHandlePsPluginsHome(t *testing.T) {
	h := newTestHandlers()
	router := NewRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/ps/plugins/home", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var resp struct {
		Sections []struct {
			ID      string           `json:"id"`
			Title   string           `json:"title"`
			URLSlug string           `json:"url_slug"`
			Plugins []map[string]any `json:"plugins"`
		} `json:"sections"`
	}

	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(resp.Sections) == 0 {
		t.Fatalf("expected sections to be returned, got 0")
	}

	// Verify featured section
	if resp.Sections[0].ID != "featured" {
		t.Errorf("expected first section to be 'featured', got %q", resp.Sections[0].ID)
	}

	// Verify that plugins have valid asset URLs
	for _, s := range resp.Sections {
		for _, p := range s.Plugins {
			iconURL, ok := p["icon_url"].(string)
			if ok && iconURL != "" && !strings.HasPrefix(iconURL, "http://127.0.0.1:9876/plugins/assets/") {
				t.Errorf("expected icon_url to start with asset prefix, got %q", iconURL)
			}
		}
	}
}

func TestHandlePsPluginsInstalled(t *testing.T) {
	h := newTestHandlers()
	router := NewRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/ps/plugins/installed?limit=1000", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var resp struct {
		Plugins []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Status  string `json:"status"`
			Enabled bool   `json:"enabled"`
			Release struct {
				DisplayName string `json:"display_name"`
				Interface   struct {
					LogoURL     string `json:"logo_url"`
					Category    string `json:"category"`
				} `json:"interface"`
			} `json:"release"`
		} `json:"plugins"`
	}

	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	// In ~/.codex/config.toml, browser, visualize, slack, google-calendar, etc. are installed
	if len(resp.Plugins) == 0 {
		t.Logf("no installed plugins in config.toml, or empty list")
	} else {
		for _, p := range resp.Plugins {
			if !p.Enabled {
				t.Errorf("expected installed plugin %s to have enabled=true", p.Name)
			}
			if p.Status != "ENABLED" {
				t.Errorf("expected installed plugin %s to have status=ENABLED, got %s", p.Name, p.Status)
			}
		}
	}
}

func TestHandlePluginAssets(t *testing.T) {
	h := newTestHandlers()
	router := NewRouter(h)

	// Test serving github.png
	req := httptest.NewRequest(http.MethodGet, "/plugins/assets/github/github.png", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200 for github.png, got %d", rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/png") {
		t.Errorf("expected Content-Type image/png, got %q", contentType)
	}

	if rr.Body.Len() == 0 {
		t.Errorf("expected non-empty image body")
	}

	// Test non-existent asset
	missingReq := httptest.NewRequest(http.MethodGet, "/plugins/assets/github/non_existent.png", nil)
	missingRR := httptest.NewRecorder()
	router.ServeHTTP(missingRR, missingReq)

	if missingRR.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing asset, got %d", missingRR.Code)
	}
}

func TestHandlePsPluginCategories(t *testing.T) {
	h := newTestHandlers()
	router := NewRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/ps/plugin-categories/developer-tools/plugins", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var resp struct {
		Category struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"category"`
		Plugins []map[string]any `json:"plugins"`
	}

	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if resp.Category.ID != "developer-tools" {
		t.Errorf("expected category ID 'developer-tools', got %q", resp.Category.ID)
	}

	if len(resp.Plugins) == 0 {
		t.Errorf("expected plugins under developer-tools, got 0")
	}
}

func TestHandlePsPluginMutations(t *testing.T) {
	h := newTestHandlers()
	router := NewRouter(h)

	// Install github
	reqInstall := httptest.NewRequest(http.MethodPost, "/ps/plugins/github/install", nil)
	rrInstall := httptest.NewRecorder()
	router.ServeHTTP(rrInstall, reqInstall)

	if rrInstall.Code != http.StatusOK {
		t.Fatalf("expected 200 for install, got %d", rrInstall.Code)
	}

	var installResp map[string]any
	_ = json.Unmarshal(rrInstall.Body.Bytes(), &installResp)
	if installResp["status"] != "ENABLED" || installResp["enabled"] != true {
		t.Errorf("unexpected install response: %v", installResp)
	}

	// Uninstall github
	reqUninstall := httptest.NewRequest(http.MethodPost, "/ps/plugins/github/uninstall", nil)
	rrUninstall := httptest.NewRecorder()
	router.ServeHTTP(rrUninstall, reqUninstall)

	if rrUninstall.Code != http.StatusOK {
		t.Fatalf("expected 200 for uninstall, got %d", rrUninstall.Code)
	}

	var uninstallResp map[string]any
	_ = json.Unmarshal(rrUninstall.Body.Bytes(), &uninstallResp)
	if uninstallResp["status"] != "AVAILABLE" || uninstallResp["enabled"] != false {
		t.Errorf("unexpected uninstall response: %v", uninstallResp)
	}
}
