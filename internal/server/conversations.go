package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ─── DB helpers ────────────────────────────────────────────────────────────────

func getCodexDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "state_5.sqlite")
}

func getRealCodexCLI() string {
	realBin := "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex.real"
	if _, err := os.Stat(realBin); err == nil {
		return realBin
	}
	return "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex"
}

func cleanUUID(id string) string {
	var sb strings.Builder
	for _, ch := range id {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' {
			sb.WriteRune(ch)
		}
	}
	return sb.String()
}

// cleanThreadTitle strips system-injected context from a thread title/first_user_message,
// returning a human-readable short title.
//
// Codex CLI stores the full user message (including ambient UI state XML, file
// context blocks, etc.) as the title. We extract just the "## My request:" section,
// or fall back to the first non-empty non-tag line, truncated to 100 chars.
func cleanThreadTitle(raw string) string {
	if raw == "" {
		return "Chat"
	}

	// 1. Try to extract "## My request:" section
	const requestMarker = "## My request:"
	if idx := strings.Index(raw, requestMarker); idx != -1 {
		rest := strings.TrimSpace(raw[idx+len(requestMarker):])
		// Take only first non-empty line
		line := firstNonEmptyLine(rest)
		if line != "" {
			return truncate(line, 100)
		}
	}

	// 2. Strip XML-like blocks (<...>...</...> or single tags)
	xmlBlock := regexp.MustCompile(`(?s)<[^>]+>.*?</[^>]+>`)
	cleaned := xmlBlock.ReplaceAllString(raw, " ")
	singleTag := regexp.MustCompile(`<[^>]+>`)
	cleaned = singleTag.ReplaceAllString(cleaned, " ")

	// 3. Strip markdown heading lines that look like system context
	lines := strings.Split(cleaned, "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		// Skip lines that are obviously system/meta
		if strings.HasPrefix(l, "# Files mentioned") ||
			strings.HasPrefix(l, "## ") ||
			strings.HasPrefix(l, "Distinguish instructions") {
			continue
		}
		return truncate(l, 100)
	}

	// 4. Last resort: truncate raw
	return truncate(strings.TrimSpace(raw), 100)
}

func firstNonEmptyLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			return l
		}
	}
	return ""
}

func truncate(s string, maxChars int) string {
	if utf8.RuneCountInString(s) <= maxChars {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxChars]) + "…"
}

// ─── DB row types ──────────────────────────────────────────────────────────────

type ThreadDBRow struct {
	ID               string  `json:"id"`
	Title            string  `json:"title"`
	FirstUserMessage string  `json:"first_user_message"`
	Preview          string  `json:"preview"`
	CreatedAt        int64   `json:"created_at"`
	UpdatedAt        int64   `json:"updated_at"`
	Archived         int     `json:"archived"`
	IsPinned         int     `json:"is_pinned"`
	ProjectID        *string `json:"project_id"`
}

// bestTitle returns the cleanest possible title for a thread row.
func (r ThreadDBRow) bestTitle() string {
	// Prefer first_user_message if it's different from title and shorter
	if r.FirstUserMessage != "" && r.FirstUserMessage != r.Title {
		return cleanThreadTitle(r.FirstUserMessage)
	}
	return cleanThreadTitle(r.Title)
}

// ─── Query helpers ──────────────────────────────────────────────────────────────

// sqliteQuery runs an sqlite3 JSON query and unmarshals the result into dest.
// Returns the raw output bytes and any error.
func sqliteQuery(dbPath, query string, dest interface{}) ([]byte, error) {
	cmd := exec.Command("sqlite3", "-json", dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("sqlite3 exec: %w", err)
	}
	if len(out) == 0 {
		return out, nil // empty result set
	}
	if dest != nil {
		if err := json.Unmarshal(out, dest); err != nil {
			return out, fmt.Errorf("json unmarshal: %w", err)
		}
	}
	return out, nil
}

func sqliteExec(dbPath, query string) error {
	cmd := exec.Command("sqlite3", dbPath, query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sqlite3 exec: %w | output: %s", err, string(out))
	}
	return nil
}

const threadSelectFields = `id, title, first_user_message, preview, created_at, updated_at, archived, is_pinned, project_id`

// queryThreadsFromDB returns (items, total) for the conversation list endpoint.
// The items use the exact schema the ChatGPT app expects:
//
//	{ conversation_id, title, create_time (float), update_time (float),
//	  is_archived, is_starred, gizmo_id, conversation_origin, workspace_id }
func queryThreadsFromDB(isArchived bool, limit, offset int) ([]map[string]any, int) {
	dbPath := getCodexDBPath()
	if dbPath == "" {
		return []map[string]any{}, 0
	}
	if _, err := os.Stat(dbPath); err != nil {
		return []map[string]any{}, 0
	}

	archivedVal := 0
	if isArchived {
		archivedVal = 1
	}

	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}

	// Count
	countQ := fmt.Sprintf("SELECT count(*) FROM threads WHERE archived = %d AND preview != '';", archivedVal)
	countCmd := exec.Command("sqlite3", dbPath, countQ)
	countOut, _ := countCmd.Output()
	total, _ := strconv.Atoi(strings.TrimSpace(string(countOut)))

	// Fetch rows — only threads that have preview (non-empty = real user chat)
	// Order by recency_at_ms DESC for proper recency ordering
	query := fmt.Sprintf(
		`SELECT %s FROM threads WHERE archived = %d AND preview != '' ORDER BY recency_at_ms DESC LIMIT %d OFFSET %d;`,
		threadSelectFields, archivedVal, limit, offset,
	)

	var rows []ThreadDBRow
	if _, err := sqliteQuery(dbPath, query, &rows); err != nil {
		log.Printf("[Comini Conversations] ⚠️ queryThreadsFromDB error: %v", err)
		return []map[string]any{}, total
	}

	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		items = append(items, threadToListItem(r))
	}

	log.Printf("[Comini Conversations] 📋 List archived=%v limit=%d offset=%d → %d items (total=%d)", isArchived, limit, offset, len(items), total)
	return items, total
}

// threadToListItem converts a ThreadDBRow to the map the ChatGPT app expects in list responses.
func threadToListItem(r ThreadDBRow) map[string]any {
	return map[string]any{
		"id":                  r.ID,
		"conversation_id":     r.ID,
		"title":               r.bestTitle(),
		"create_time":         float64(r.CreatedAt),
		"update_time":         float64(r.UpdatedAt),
		"is_archived":         r.Archived == 1,
		"is_starred":          r.IsPinned == 1,
		"gizmo_id":            nil,
		"conversation_origin": nil,
		"workspace_id":        nil,
	}
}

// querySingleThreadFromDB returns a single conversation object as expected by GET /conversation/{id}.
func querySingleThreadFromDB(id string) (map[string]any, bool) {
	cleanID := cleanUUID(id)
	if cleanID == "" {
		return nil, false
	}
	dbPath := getCodexDBPath()
	if dbPath == "" {
		return nil, false
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, false
	}

	query := fmt.Sprintf(
		`SELECT %s FROM threads WHERE id = '%s' LIMIT 1;`,
		threadSelectFields, cleanID,
	)

	var rows []ThreadDBRow
	if _, err := sqliteQuery(dbPath, query, &rows); err != nil {
		log.Printf("[Comini Conversations] ⚠️ querySingleThreadFromDB(%s) error: %v", cleanID, err)
		return nil, false
	}
	if len(rows) == 0 {
		return nil, false
	}

	r := rows[0]
	return map[string]any{
		"conversation_id":      r.ID,
		"title":                r.bestTitle(),
		"create_time":          float64(r.CreatedAt),
		"update_time":          float64(r.UpdatedAt),
		"conversation_origin":  nil,
		"gizmo_id":             nil,
		"current_node":         nil,
		"mapping":              map[string]any{},
		"moderation_results":   []any{},
		"safe_urls":            []any{},
		"blocked_urls":         []any{},
		"is_archived":          r.Archived == 1,
		"is_starred":           r.IsPinned == 1,
		"is_read_only":         false,
		"default_model_slug":   "google-antigravity/gemini-3.8-flash",
		"disabled_tool_ids":    []any{},
	}, true
}

// ─── Write operations ──────────────────────────────────────────────────────────

func setThreadArchived(id string, archived bool) error {
	cleanID := cleanUUID(id)
	if cleanID == "" {
		return fmt.Errorf("invalid thread id")
	}
	dbPath := getCodexDBPath()
	if dbPath == "" {
		return fmt.Errorf("db not found")
	}
	if _, err := os.Stat(dbPath); err != nil {
		return err
	}

	var updateQuery string
	if archived {
		updateQuery = fmt.Sprintf("UPDATE threads SET archived = 1, archived_at = strftime('%%s', 'now') WHERE id = '%s';", cleanID)
	} else {
		updateQuery = fmt.Sprintf("UPDATE threads SET archived = 0, archived_at = NULL WHERE id = '%s';", cleanID)
	}

	if err := sqliteExec(dbPath, updateQuery); err != nil {
		log.Printf("[Comini Conversations] ⚠️ setThreadArchived(%s, %v): %v", cleanID, archived, err)
	}

	// Fire-and-forget: tell the Codex CLI too (for its own state consistency)
	action := "unarchive"
	if archived {
		action = "archive"
	}
	go func() {
		out, err := exec.Command(getRealCodexCLI(), action, cleanID).CombinedOutput()
		if err != nil {
			log.Printf("[Comini Conversations] ℹ️ codex %s %s: %v | %s", action, cleanID, err, string(out))
		}
	}()

	return nil
}

func setThreadTitle(id, title string) error {
	cleanID := cleanUUID(id)
	if cleanID == "" {
		return fmt.Errorf("invalid thread id")
	}
	dbPath := getCodexDBPath()
	if dbPath == "" {
		return fmt.Errorf("db not found")
	}

	escapedTitle := strings.ReplaceAll(title, "'", "''")
	query := fmt.Sprintf("UPDATE threads SET title = '%s' WHERE id = '%s';", escapedTitle, cleanID)
	return sqliteExec(dbPath, query)
}

// ─── HTTP Handlers ─────────────────────────────────────────────────────────────

// HandleConversations serves ChatGPT Web cloud conversation endpoints:
//   - POST /conversations/batch
//   - GET  /conversations/search
//   - PATCH /conversations  (bulk)
//   - GET  /conversations   (list)
//
// IMPORTANT ARCHITECTURE NOTE:
// Local Codex threads in ~/.codex/state_5.sqlite are already loaded natively by ChatGPT.app
// via `codex app-server` (`thread/list` -> `host_id = "local"`, `source_kind = "vscode"`,
// preserving `cwd` for project grouping and full `.jsonl`/`.jsonl.zst` turn history).
// Returning local `state_5.sqlite` threads from `/backend-api/conversations` causes
// ChatGPT.app's `wQe` sync worker (`main-CLu1A25j.js:3454153`) and `GGc` archived list
// to insert duplicate `source_kind = "chatgpt"` rows with `cwd = NULL` and empty `mapping: {}`,
// which mixes project threads into general Chats and opens blank/null `/c/<id>` views!
func (h *Handlers) HandleConversations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimSuffix(r.URL.Path, "/")

	// ── 1. POST /conversations/batch ─────────────────────────────────────────
	if strings.HasSuffix(path, "/batch") && r.Method == http.MethodPost {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]map[string]any{})
		return
	}

	// ── 2. GET /conversations/search ─────────────────────────────────────────
	if strings.HasSuffix(path, "/search") {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items":  []map[string]any{},
			"cursor": nil,
		})
		return
	}

	// ── 3. PATCH /conversations (bulk ops like archive-all) ───────────────────
	if r.Method == http.MethodPatch {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		return
	}

	// ── 4. GET /conversations (ChatGPT Web cloud list) ────────────────────────
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":                     []map[string]any{},
		"total":                     0,
		"limit":                     limit,
		"offset":                    offset,
		"has_missing_conversations": false,
		"cursor":                    nil,
	})
}

// HandleConversationSingle serves GET/PATCH/DELETE /conversation/{conversation_id}.
func (h *Handlers) HandleConversationSingle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimSuffix(r.URL.Path, "/")
	parts := strings.Split(path, "/")

	// Extract ID — handle sub-paths like /conversation/{id}/lock
	convID := parts[len(parts)-1]
	var subAction string
	if len(parts) >= 2 {
		// Check if the last segment is an action keyword
		lastSeg := parts[len(parts)-1]
		switch lastSeg {
		case "lock", "unlock_permanently", "async-status", "rating", "share":
			// /conversation/{id}/lock etc.
			if len(parts) >= 2 {
				convID = parts[len(parts)-2]
				subAction = lastSeg
			}
		}
	}

	if subAction != "" {
		log.Printf("[Comini Conversations] sub-action %q on conv %s", subAction, convID)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		return
	}

	cleanID := cleanUUID(convID)
	log.Printf("[Comini Conversations] %s /conversation/%s", r.Method, cleanID)

	switch r.Method {
	case http.MethodGet:
		conv, found := querySingleThreadFromDB(cleanID)
		if !found {
			log.Printf("[Comini Conversations] ⚠️ conv %s not in DB, returning placeholder", cleanID)
			conv = placeholderConv(cleanID)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(conv)

	case http.MethodPatch:
		var reqBody map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
		}
		log.Printf("[Comini Conversations] PATCH conv %s body: %v", cleanID, reqBody)

		var isArchivedResult any = nil

		if isArchivedVal, hasArchived := reqBody["is_archived"]; hasArchived {
			if b, ok := isArchivedVal.(bool); ok {
				if err := setThreadArchived(cleanID, b); err != nil {
					log.Printf("[Comini Conversations] ⚠️ setThreadArchived error: %v", err)
				} else {
					log.Printf("[Comini Conversations] ✅ conv %s archived=%v", cleanID, b)
				}
				isArchivedResult = b
			}
		}

		if titleVal, ok := reqBody["title"].(string); ok && titleVal != "" {
			_ = setThreadTitle(cleanID, titleVal)
		}

		// is_visible: false → archive
		if isVis, ok := reqBody["is_visible"].(bool); ok && !isVis {
			_ = setThreadArchived(cleanID, true)
			isArchivedResult = true
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":         true,
			"conversation_id": cleanID,
			"is_archived":     isArchivedResult,
		})

	case http.MethodDelete:
		if err := setThreadArchived(cleanID, true); err != nil {
			log.Printf("[Comini Conversations] ⚠️ delete-archive error: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})

	default:
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	}
}

func placeholderConv(id string) map[string]any {
	return map[string]any{
		"conversation_id":      id,
		"title":                "Chat",
		"create_time":          float64(0),
		"update_time":          float64(0),
		"conversation_origin":  nil,
		"gizmo_id":             nil,
		"current_node":         nil,
		"mapping":              map[string]any{},
		"moderation_results":   []any{},
		"safe_urls":            []any{},
		"blocked_urls":         []any{},
		"is_archived":          false,
		"is_starred":           false,
		"is_read_only":         false,
		"default_model_slug":   "google-antigravity/gemini-3.8-flash",
		"disabled_tool_ids":    []any{},
	}
}
