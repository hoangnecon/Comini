package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"comini/internal/domain"
)

type callEntry struct {
	Signature    string    `json:"sig"`
	FunctionName string    `json:"fn"`
	Timestamp    time.Time `json:"ts"`
}

// imageCallEntry keeps the full argument set of an image_generation call. Codex only echoes
// id/status/revised_prompt/result back in later requests, so image_paths/image_name/aspect_ratio
// would otherwise be lost from the replayed history.
type imageCallEntry struct {
	Args      map[string]any `json:"args"`
	Timestamp time.Time      `json:"ts"`
}

type persistedStoreData struct {
	ImageCalls     map[string]imageCallEntry          `json:"image_calls,omitempty"`
	Calls          map[string]callEntry               `json:"calls"`
	BySessionCall  map[string]string                  `json:"by_session_call"`
	SessionCutoffs map[string]int                     `json:"session_cutoffs,omitempty"`
	SessionTools   map[string][]domain.ToolDefinition `json:"session_tools,omitempty"`
}

type SignatureStore struct {
	mu             sync.RWMutex
	calls          map[string]callEntry
	bySessionCall  map[string]string
	sessionCutoffs map[string]int
	sessionTools   map[string][]domain.ToolDefinition
	imageCalls     map[string]imageCallEntry
	filePath       string
	ttl            time.Duration
}

var DefaultSignatureStore = NewSignatureStore(30 * 24 * time.Hour)

func resolveStoreFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".codex")
	_ = os.MkdirAll(dir, 0755)
	return filepath.Join(dir, "comini_signatures.json")
}

func NewSignatureStore(ttl time.Duration) *SignatureStore {
	filePath := resolveStoreFilePath()
	s := &SignatureStore{
		calls:          make(map[string]callEntry),
		bySessionCall:  make(map[string]string),
		sessionCutoffs: make(map[string]int),
		sessionTools:   make(map[string][]domain.ToolDefinition),
		imageCalls:     make(map[string]imageCallEntry),
		filePath:       filePath,
		ttl:            ttl,
	}
	s.load()
	return s
}

func (s *SignatureStore) pruneExpiredLocked() {
	now := time.Now()
	if s.imageCalls == nil {
		s.imageCalls = make(map[string]imageCallEntry)
	}
	for id, entry := range s.calls {
		if now.Sub(entry.Timestamp) > s.ttl {
			delete(s.calls, id)
		}
	}
	// Bound maximum capacity to prevent unbounded disk growth
	if len(s.calls) > 10000 {
		type kv struct {
			id string
			ts time.Time
		}
		list := make([]kv, 0, len(s.calls))
		for id, entry := range s.calls {
			list = append(list, kv{id: id, ts: entry.Timestamp})
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].ts.Before(list[j].ts)
		})
		// Prune oldest 20%
		for i := 0; i < len(list)/5; i++ {
			delete(s.calls, list[i].id)
		}
	}
	if len(s.bySessionCall) > 10000 {
		s.bySessionCall = make(map[string]string)
	}
	if len(s.sessionCutoffs) > 2000 {
		s.sessionCutoffs = make(map[string]int)
	}
	if len(s.sessionTools) > 500 {
		s.sessionTools = make(map[string][]domain.ToolDefinition)
	}
	for id, entry := range s.imageCalls {
		if now.Sub(entry.Timestamp) > s.ttl {
			delete(s.imageCalls, id)
		}
	}
	if len(s.imageCalls) > 5000 {
		s.imageCalls = make(map[string]imageCallEntry)
	}
}

// SetImageCall records the full image_generation arguments under every given key (item id,
// call id, saved path) so later history replays can restore image_paths/image_name/aspect_ratio.
func (s *SignatureStore) SetImageCall(args map[string]any, keys ...string) {
	if len(args) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if strings.TrimSpace(k) == "" {
			continue
		}
		s.imageCalls[k] = imageCallEntry{Args: args, Timestamp: time.Now()}
	}
	s.saveLocked()
}

// GetImageCall returns the stored image_generation arguments for the first key that matches.
func (s *SignatureStore) GetImageCall(keys ...string) (map[string]any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range keys {
		if e, ok := s.imageCalls[k]; ok && time.Since(e.Timestamp) <= s.ttl {
			return e.Args, true
		}
	}
	return nil, false
}

func (s *SignatureStore) load() {
	if s.filePath == "" {
		return
	}
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return
	}
	var p persistedStoreData
	if err := json.Unmarshal(data, &p); err == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if p.Calls != nil {
			s.calls = p.Calls
		}
		if p.BySessionCall != nil {
			s.bySessionCall = p.BySessionCall
		}
		if p.SessionCutoffs != nil {
			s.sessionCutoffs = p.SessionCutoffs
		}
		if p.SessionTools != nil {
			s.sessionTools = p.SessionTools
		}
		if p.ImageCalls != nil {
			s.imageCalls = p.ImageCalls
		}
		s.pruneExpiredLocked()
	}
}

func (s *SignatureStore) saveLocked() {
	if s.filePath == "" {
		return
	}
	s.pruneExpiredLocked()
	p := persistedStoreData{
		Calls:          s.calls,
		BySessionCall:  s.bySessionCall,
		SessionCutoffs: s.sessionCutoffs,
		SessionTools:   s.sessionTools,
		ImageCalls:     s.imageCalls,
	}
	data, err := json.Marshal(p)
	if err == nil {
		_ = os.WriteFile(s.filePath, data, 0644)
	}
}

// CanonicalJSON recursively serializes arbitrary JSON objects with keys sorted lexicographically.
func CanonicalJSON(val any) string {
	if val == nil {
		return "null"
	}
	switch v := val.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, "\""+k+"\":"+CanonicalJSON(v[k]))
		}
		return "{" + strings.Join(pairs, ",") + "}"
	case []any:
		items := make([]string, 0, len(v))
		for _, item := range v {
			items = append(items, CanonicalJSON(item))
		}
		return "[" + strings.Join(items, ",") + "]"
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func FunctionCallKey(funcName string, args any) string {
	return funcName + "::" + CanonicalJSON(args)
}

func (s *SignatureStore) Set(callID, signature, functionName string) {
	if callID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	existing := s.calls[callID]
	if signature == "" {
		signature = existing.Signature
	}
	if functionName == "" {
		functionName = existing.FunctionName
	}

	s.calls[callID] = callEntry{
		Signature:    signature,
		FunctionName: functionName,
		Timestamp:    time.Now(),
	}
	s.saveLocked()
}

func (s *SignatureStore) SetSessionCall(sessionID, funcName string, args any, signature string) {
	if sessionID == "" || funcName == "" || signature == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	k := sessionID + "::" + FunctionCallKey(funcName, args)
	s.bySessionCall[k] = signature
	s.saveLocked()
}

func (s *SignatureStore) Get(callID string) (signature string, functionName string, ok bool) {
	if callID == "" {
		return "", "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, found := s.calls[callID]
	if !found {
		return "", "", false
	}

	if time.Since(entry.Timestamp) > s.ttl {
		return "", "", false
	}

	return entry.Signature, entry.FunctionName, true
}

func (s *SignatureStore) GetSessionCall(sessionID, funcName string, args any) (string, bool) {
	if sessionID == "" || funcName == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	k := sessionID + "::" + FunctionCallKey(funcName, args)
	sig, found := s.bySessionCall[k]
	return sig, found && sig != ""
}

func (s *SignatureStore) FindAnyCall(funcName string, args any) (string, bool) {
	if funcName == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	suffix := "::" + FunctionCallKey(funcName, args)
	var candidates []string
	for k, sig := range s.bySessionCall {
		if strings.HasSuffix(k, suffix) && sig != "" {
			candidates = append(candidates, k)
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.Strings(candidates)
	return s.bySessionCall[candidates[0]], true
}

func (s *SignatureStore) GetSessionCutoff(sessionID string) (int, bool) {
	if s == nil || sessionID == "" {
		return 0, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.sessionCutoffs[sessionID]
	return v, ok && v > 0
}

func (s *SignatureStore) SetSessionCutoff(sessionID string, cutoff int) {
	if s == nil || sessionID == "" || cutoff <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionCutoffs == nil {
		s.sessionCutoffs = make(map[string]int)
	}
	if prev, ok := s.sessionCutoffs[sessionID]; ok && prev == cutoff {
		return
	}
	s.sessionCutoffs[sessionID] = cutoff
	s.saveLocked()
}

func (s *SignatureStore) RecordSessionTools(sessionID string, tools []domain.ToolDefinition) {
	if s == nil || sessionID == "" || len(tools) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionTools == nil {
		s.sessionTools = make(map[string][]domain.ToolDefinition)
	}
	existing := s.sessionTools[sessionID]
	seen := make(map[string]bool, len(existing))
	for _, t := range existing {
		if t.Name != "" {
			seen[t.Name] = true
		}
	}
	changed := false
	for _, t := range tools {
		if t.Name != "" && !seen[t.Name] {
			seen[t.Name] = true
			existing = append(existing, t)
			changed = true
		}
	}
	if changed {
		s.sessionTools[sessionID] = existing
		s.saveLocked()
	}
}

func (s *SignatureStore) GetSessionTools(sessionID string) []domain.ToolDefinition {
	if s == nil || sessionID == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	tools := s.sessionTools[sessionID]
	if len(tools) == 0 {
		return nil
	}
	out := make([]domain.ToolDefinition, len(tools))
	copy(out, tools)
	return out
}

func (s *SignatureStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = make(map[string]callEntry)
	s.bySessionCall = make(map[string]string)
	s.sessionCutoffs = make(map[string]int)
	s.sessionTools = make(map[string][]domain.ToolDefinition)
	s.imageCalls = make(map[string]imageCallEntry)
	s.saveLocked()

}
