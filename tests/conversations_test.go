package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"comini/internal/server"
)

func TestConversationsEndpoints(t *testing.T) {
	handlers := server.NewHandlers(nil, nil, nil)

	// 1. Test GET /conversations?is_archived=true
	{
		req := httptest.NewRequest(http.MethodGet, "/conversations?is_archived=true&limit=10&offset=0", nil)
		w := httptest.NewRecorder()
		handlers.HandleConversations(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for /conversations?is_archived=true, got %d", w.Code)
		}

		var resp struct {
			Items []map[string]any `json:"items"`
			Total int              `json:"total"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if resp.Total == 0 {
			t.Logf("Note: no archived items in DB, total: %d", resp.Total)
		} else {
			if len(resp.Items) == 0 {
				t.Fatalf("expected non-empty items when total > 0")
			}
			for _, it := range resp.Items {
				if it["is_archived"] != true {
					t.Fatalf("expected item to have is_archived=true, got %+v", it)
				}
				if it["id"] == nil || it["title"] == nil {
					t.Fatalf("missing required conversation fields: %+v", it)
				}
			}
		}
	}

	// 2. Test GET /conversations?is_archived=false
	{
		req := httptest.NewRequest(http.MethodGet, "/conversations?is_archived=false&limit=5&offset=0", nil)
		w := httptest.NewRecorder()
		handlers.HandleConversations(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for /conversations?is_archived=false, got %d", w.Code)
		}

		var resp struct {
			Items []map[string]any `json:"items"`
			Total int              `json:"total"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		for _, it := range resp.Items {
			if it["is_archived"] != false {
				t.Fatalf("expected item to have is_archived=false, got %+v", it)
			}
		}
	}

	// 3. Test GET /conversation/{id}
	{
		req := httptest.NewRequest(http.MethodGet, "/conversation/test-thread-uuid-1234", nil)
		w := httptest.NewRecorder()
		handlers.HandleConversationSingle(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for single conversation, got %d", w.Code)
		}

		var resp map[string]any
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode single conversation: %v", err)
		}

		if resp["mapping"] == nil {
			t.Fatalf("mapping MUST NOT be null (must be empty object {}): %+v", resp)
		}
		if resp["title"] == nil || resp["conversation_id"] == nil {
			t.Fatalf("missing title or conversation_id: %+v", resp)
		}
	}

	// 4. Test PATCH /conversation/{id}
	{
		req := httptest.NewRequest(http.MethodPatch, "/conversation/test-thread-uuid-1234", strings.NewReader(`{"is_archived":true}`))
		w := httptest.NewRecorder()
		handlers.HandleConversationSingle(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for patch conversation, got %d", w.Code)
		}

		var resp map[string]any
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode patch response: %v", err)
		}
		if resp["success"] != true {
			t.Fatalf("expected success=true, got %+v", resp)
		}
	}
}
