package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"comini/internal/server"
)

func TestHandleWhamStatsigBootstrap(t *testing.T) {
	handlers := server.NewHandlers(nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wham/statsig/bootstrap", nil)
	w := httptest.NewRecorder()

	handlers.HandleWhamStatsigBootstrap(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp struct {
		StatsigPayload string `json:"statsigPayload"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode outer response: %v", err)
	}

	if resp.StatsigPayload == "" {
		t.Fatalf("statsigPayload is empty")
	}

	var inner struct {
		FeatureGates map[string]struct {
			Value bool `json:"value"`
		} `json:"feature_gates"`
		LayerConfigs map[string]struct {
			Value map[string]any `json:"value"`
		} `json:"layer_configs"`
	}

	if err := json.Unmarshal([]byte(resp.StatsigPayload), &inner); err != nil {
		t.Fatalf("failed to unmarshal statsigPayload: %v", err)
	}

	// 1. Verify gate 3085093835 (Master gate for 2-layer Navigation Rail + Contextual Sidebar is enabled)
	gateUnified, ok := inner.FeatureGates["3085093835"]
	if !ok || !gateUnified.Value {
		t.Fatalf("expected feature gate 3085093835 to be true (enabled), got %+v", inner.FeatureGates["3085093835"])
	}

	// 2. Verify gate 224248639 (File library is hidden)
	gateLibrary, ok := inner.FeatureGates["224248639"]
	if !ok || gateLibrary.Value {
		t.Fatalf("expected feature gate 224248639 to be false (hidden), got %+v", inner.FeatureGates["224248639"])
	}

	// 3. Verify layer 3017902351 (web_file_library_enabled is false)
	layerLib, ok := inner.LayerConfigs["3017902351"]
	if !ok {
		t.Fatalf("expected layer 3017902351 in layer_configs")
	}
	if enabled, ok := layerLib.Value["web_file_library_enabled"].(bool); !ok || enabled {
		t.Fatalf("expected web_file_library_enabled to be false, got %v", layerLib.Value)
	}

	// 4. Verify gate 4218407052 (Plugins store remains accessible)
	gatePlugins, ok := inner.FeatureGates["4218407052"]
	if !ok || !gatePlugins.Value {
		t.Fatalf("expected feature gate 4218407052 to be true (Plugins accessible), got %+v", inner.FeatureGates["4218407052"])
	}
}
