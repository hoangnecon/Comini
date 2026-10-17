package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type callEntry struct {
	Signature    string    `json:"sig"`
	FunctionName string    `json:"fn"`
	Timestamp    time.Time `json:"ts"`
}

type persistedStoreData struct {
	Calls         map[string]callEntry `json:"calls"`
	BySessionCall map[string]string    `json:"by_session_call"`
}

type SignatureStore struct {
	mu            sync.RWMutex
	calls         map[string]callEntry
	bySessionCall map[string]string
	filePath      string
	ttl           time.Duration
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
		calls:         make(map[string]callEntry),
		bySessionCall: make(map[string]string),
		filePath:      filePath,
		ttl:           ttl,
	}
	s.load()
	return s
}

func (s *SignatureStore) pruneExpiredLocked() {
	now := time.Now()
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
		s.pruneExpiredLocked()
	}
}

func (s *SignatureStore) saveLocked() {
	if s.filePath == "" {
		return
	}
	s.pruneExpiredLocked()
	p := persistedStoreData{
		Calls:         s.calls,
		BySessionCall: s.bySessionCall,
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

func (s *SignatureStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = make(map[string]callEntry)
	s.bySessionCall = make(map[string]string)
	s.saveLocked()
}
