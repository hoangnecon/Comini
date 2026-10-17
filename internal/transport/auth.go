package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	GoogleClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	GoogleClientSecret = "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"
	GoogleTokenURL     = "https://oauth2.googleapis.com/token"
	GoogleAuthURL      = "https://accounts.google.com/o/oauth2/v2/auth"
	GoogleUserInfoURL  = "https://www.googleapis.com/oauth2/v2/userinfo"
)

var googleOAuthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// AccountCredential holds the OAuth2 credentials and metadata for a single Google Antigravity account.
type AccountCredential struct {
	Access    string  `json:"access"`
	Refresh   string  `json:"refresh"`
	Expires   float64 `json:"expires"`
	Email     string  `json:"email,omitempty"`
	Source    string  `json:"source,omitempty"`
	ProjectID string  `json:"projectId,omitempty"`
}

// AccountEntry represents a stored account in comini_auth.json.
type AccountEntry struct {
	ID         string            `json:"id"`
	Credential AccountCredential `json:"credential"`
	AddedAt    int64             `json:"addedAt,omitempty"`
	LoginID    string            `json:"loginId,omitempty"`
}

// ProviderAuthStore holds the active account and list of accounts for Google Antigravity.
type ProviderAuthStore struct {
	ActiveAccountID   string         `json:"activeAccountId"`
	Accounts          []AccountEntry `json:"accounts"`
	SelectionRevision string         `json:"selectionRevision,omitempty"`
}

// AuthFileStore is the top-level structure persisted to ~/.codex/comini_auth.json.
type AuthFileStore struct {
	GoogleAntigravity ProviderAuthStore `json:"google-antigravity"`
}

// AuthStatus summarizes the current authentication state for CLI and HTTP inspection.
type AuthStatus struct {
	Authenticated   bool           `json:"authenticated"`
	ActiveAccountID string         `json:"active_account_id,omitempty"`
	Email           string         `json:"email,omitempty"`
	ProjectID       string         `json:"project_id,omitempty"`
	Source          string         `json:"source,omitempty"`
	ExpiresAt       time.Time      `json:"expires_at,omitempty"`
	ExpiresInSec    int64          `json:"expires_in_seconds,omitempty"`
	HasRefreshToken bool           `json:"has_refresh_token"`
	AuthFilePath    string         `json:"auth_file_path"`
	TotalAccounts   int            `json:"total_accounts"`
	Accounts        []AccountEntry `json:"accounts,omitempty"`
}

type AuthManager struct {
	mu           sync.RWMutex
	authFilePath string
	accountID    string
	email        string
	source       string
	accessToken  string
	refreshToken string
	expiresAt    time.Time
	projectID    string
	store        AuthFileStore
}

var DefaultAuthManager = NewAuthManager()

func DefaultAuthFilePath() string {
	if custom := strings.TrimSpace(os.Getenv("COMINI_AUTH_FILE")); custom != "" {
		return custom
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "comini_auth.json"
	}
	return filepath.Join(home, ".codex", "comini_auth.json")
}

func NewAuthManager() *AuthManager {
	return NewAuthManagerWithPath(DefaultAuthFilePath())
}

func NewAuthManagerWithPath(authPath string) *AuthManager {
	am := &AuthManager{
		authFilePath: authPath,
	}
	am.loadStoredCredential()
	return am
}

func (a *AuthManager) AuthFilePath() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.authFilePath
}

func (a *AuthManager) loadStoredCredential() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.authFilePath == "" {
		a.authFilePath = DefaultAuthFilePath()
	}

	// 1. Load from native Comini auth store (~/.codex/comini_auth.json)
	if data, err := os.ReadFile(a.authFilePath); err == nil {
		var fileStore AuthFileStore
		if err := json.Unmarshal(data, &fileStore); err == nil && len(fileStore.GoogleAntigravity.Accounts) > 0 {
			a.store = fileStore
			ga := fileStore.GoogleAntigravity
			selectedIdx := 0
			for i, acc := range ga.Accounts {
				if ga.ActiveAccountID != "" && acc.ID == ga.ActiveAccountID {
					selectedIdx = i
					break
				}
			}
			acc := ga.Accounts[selectedIdx]
			a.accountID = acc.ID
			a.email = acc.Credential.Email
			a.source = acc.Credential.Source
			if acc.Credential.Access != "" {
				a.accessToken = acc.Credential.Access
			}
			if acc.Credential.Refresh != "" {
				a.refreshToken = acc.Credential.Refresh
			}
			if acc.Credential.ProjectID != "" {
				a.projectID = acc.Credential.ProjectID
			}
			if acc.Credential.Expires > 0 {
				a.expiresAt = time.UnixMilli(int64(acc.Credential.Expires))
			}
			return
		}
	}

	// 2. Fallback to ~/.gemini/jetski-standalone-oauth-token if no account loaded
	if a.accessToken == "" && a.refreshToken == "" {
		if home, err := os.UserHomeDir(); err == nil {
			jetskiPath := filepath.Join(home, ".gemini", "jetski-standalone-oauth-token")
			if data, err := os.ReadFile(jetskiPath); err == nil {
				var jStruct struct {
					Token string `json:"token"`
				}
				if err := json.Unmarshal(data, &jStruct); err == nil && jStruct.Token != "" {
					a.accessToken = jStruct.Token
					a.source = "jetski-standalone"
					a.projectID = "aicode-consumers"
					a.expiresAt = time.Now().Add(50 * time.Minute)
				}
			} else if raw := strings.TrimSpace(string(data)); strings.HasPrefix(raw, "ya29.") {
				a.accessToken = raw
				a.source = "jetski-standalone"
				a.projectID = "aicode-consumers"
				a.expiresAt = time.Now().Add(50 * time.Minute)
			}
		}
	}
}

// saveLocked persists the current store state atomically with 0600 permissions.
// Caller must hold a.mu.Lock().
func (a *AuthManager) saveLocked() error {
	if a.authFilePath == "" {
		a.authFilePath = DefaultAuthFilePath()
	}
	dir := filepath.Dir(a.authFilePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create auth directory %s: %w", dir, err)
	}

	// Update active account in a.store if present
	updated := false
	for i := range a.store.GoogleAntigravity.Accounts {
		acc := &a.store.GoogleAntigravity.Accounts[i]
		if a.accountID != "" && acc.ID == a.accountID {
			acc.Credential.Access = a.accessToken
			if a.refreshToken != "" {
				acc.Credential.Refresh = a.refreshToken
			}
			if !a.expiresAt.IsZero() {
				acc.Credential.Expires = float64(a.expiresAt.UnixMilli())
			}
			if a.projectID != "" {
				acc.Credential.ProjectID = a.projectID
			}
			if a.email != "" {
				acc.Credential.Email = a.email
			}
			if a.source != "" {
				acc.Credential.Source = a.source
			}
			updated = true
			break
		}
	}

	if !updated && (a.accessToken != "" || a.refreshToken != "") {
		if a.accountID == "" {
			b := make([]byte, 16)
			_, _ = rand.Read(b)
			a.accountID = hex.EncodeToString(b)
		}
		if a.projectID == "" {
			a.projectID = "aicode-consumers"
		}
		if a.source == "" {
			a.source = "oauth"
		}
		entry := AccountEntry{
			ID: a.accountID,
			Credential: AccountCredential{
				Access:    a.accessToken,
				Refresh:   a.refreshToken,
				Expires:   float64(a.expiresAt.UnixMilli()),
				Email:     a.email,
				Source:    a.source,
				ProjectID: a.projectID,
			},
			AddedAt: time.Now().UnixMilli(),
		}
		a.store.GoogleAntigravity.Accounts = append(a.store.GoogleAntigravity.Accounts, entry)
		a.store.GoogleAntigravity.ActiveAccountID = a.accountID
	}

	data, err := json.MarshalIndent(a.store, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal auth store: %w", err)
	}

	tmpPath := a.authFilePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write temp auth file: %w", err)
	}
	if err := os.Rename(tmpPath, a.authFilePath); err != nil {
		return fmt.Errorf("failed to commit auth file: %w", err)
	}
	return nil
}

// GetCredentials returns a valid access token and project ID, auto-refreshing and persisting to disk if expired.
func (a *AuthManager) GetCredentials() (string, string, error) {
	a.loadStoredCredential()

	a.mu.RLock()
	token := a.accessToken
	refresh := a.refreshToken
	exp := a.expiresAt
	project := a.projectID
	a.mu.RUnlock()

	if project == "" {
		project = "aicode-consumers"
	}

	// If token exists and is valid for at least 3 more minutes, return it
	if token != "" && !exp.IsZero() && time.Now().Before(exp.Add(-3*time.Minute)) {
		return token, project, nil
	}

	if refresh == "" {
		if token != "" {
			return token, project, nil
		}
		return "", project, fmt.Errorf("no stored Antigravity token found (run 'comini auth login' to authenticate)")
	}

	// Refresh token and persist to disk
	newToken, newExp, err := a.refreshOAuthToken(refresh)
	if err != nil {
		if token != "" {
			return token, project, nil
		}
		return "", project, fmt.Errorf("failed to refresh OAuth token: %w", err)
	}

	a.mu.Lock()
	a.accessToken = newToken
	a.expiresAt = newExp
	_ = a.saveLocked()
	a.mu.Unlock()

	return newToken, project, nil
}

// ForceRefresh forces a token refresh from Google OAuth endpoint regardless of current expiry and persists it.
func (a *AuthManager) ForceRefresh() (string, string, error) {
	a.loadStoredCredential()

	a.mu.RLock()
	refresh := a.refreshToken
	project := a.projectID
	a.mu.RUnlock()

	if project == "" {
		project = "aicode-consumers"
	}

	if refresh == "" {
		return a.GetCredentials()
	}

	newToken, newExp, err := a.refreshOAuthToken(refresh)
	if err != nil {
		return a.GetCredentials()
	}

	a.mu.Lock()
	a.accessToken = newToken
	a.expiresAt = newExp
	_ = a.saveLocked()
	a.mu.Unlock()

	return newToken, project, nil
}

// Status returns the current authentication state.
func (a *AuthManager) Status() AuthStatus {
	a.loadStoredCredential()

	a.mu.RLock()
	defer a.mu.RUnlock()

	proj := a.projectID
	if proj == "" {
		proj = "aicode-consumers"
	}

	var expSec int64
	if !a.expiresAt.IsZero() {
		expSec = int64(time.Until(a.expiresAt).Seconds())
	}

	// Redact sensitive tokens in account list snapshot
	redactedAccounts := make([]AccountEntry, len(a.store.GoogleAntigravity.Accounts))
	for i, acc := range a.store.GoogleAntigravity.Accounts {
		redactedAccounts[i] = acc
		if len(redactedAccounts[i].Credential.Access) > 12 {
			redactedAccounts[i].Credential.Access = redactedAccounts[i].Credential.Access[:12] + "..."
		}
		if len(redactedAccounts[i].Credential.Refresh) > 8 {
			redactedAccounts[i].Credential.Refresh = redactedAccounts[i].Credential.Refresh[:8] + "..."
		}
	}

	return AuthStatus{
		Authenticated:   a.accessToken != "" || a.refreshToken != "",
		ActiveAccountID: a.accountID,
		Email:           a.email,
		ProjectID:       proj,
		Source:          a.source,
		ExpiresAt:       a.expiresAt,
		ExpiresInSec:    expSec,
		HasRefreshToken: a.refreshToken != "",
		AuthFilePath:    a.authFilePath,
		TotalAccounts:   len(a.store.GoogleAntigravity.Accounts),
		Accounts:        redactedAccounts,
	}
}

// SwitchAccount switches the active account by email or account ID.
func (a *AuthManager) SwitchAccount(target string) (*AccountEntry, error) {
	a.loadStoredCredential()

	a.mu.Lock()
	defer a.mu.Unlock()

	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("account email or ID is required")
	}

	for _, acc := range a.store.GoogleAntigravity.Accounts {
		if strings.EqualFold(acc.ID, target) || strings.EqualFold(acc.Credential.Email, target) {
			a.store.GoogleAntigravity.ActiveAccountID = acc.ID
			a.accountID = acc.ID
			a.email = acc.Credential.Email
			a.source = acc.Credential.Source
			a.accessToken = acc.Credential.Access
			a.refreshToken = acc.Credential.Refresh
			a.projectID = acc.Credential.ProjectID
			if acc.Credential.Expires > 0 {
				a.expiresAt = time.UnixMilli(int64(acc.Credential.Expires))
			}
			if err := a.saveLocked(); err != nil {
				return nil, err
			}
			copyAcc := acc
			return &copyAcc, nil
		}
	}

	return nil, fmt.Errorf("account %q not found in %s", target, a.authFilePath)
}

// RemoveAccount removes an account by email or ID (or clears all if target is "all" or empty when 1 account).
func (a *AuthManager) RemoveAccount(target string) error {
	a.loadStoredCredential()

	a.mu.Lock()
	defer a.mu.Unlock()

	target = strings.TrimSpace(target)
	if target == "" || strings.EqualFold(target, "all") {
		a.store = AuthFileStore{}
		a.accountID = ""
		a.email = ""
		a.accessToken = ""
		a.refreshToken = ""
		a.expiresAt = time.Time{}
		_ = os.Remove(a.authFilePath)
		return nil
	}

	var remaining []AccountEntry
	removed := false
	for _, acc := range a.store.GoogleAntigravity.Accounts {
		if strings.EqualFold(acc.ID, target) || strings.EqualFold(acc.Credential.Email, target) {
			removed = true
			continue
		}
		remaining = append(remaining, acc)
	}

	if !removed {
		return fmt.Errorf("account %q not found", target)
	}

	a.store.GoogleAntigravity.Accounts = remaining
	if len(remaining) == 0 {
		a.store.GoogleAntigravity.ActiveAccountID = ""
		a.accountID = ""
		a.email = ""
		a.accessToken = ""
		a.refreshToken = ""
		a.expiresAt = time.Time{}
		_ = os.Remove(a.authFilePath)
		return nil
	}

	// Select first remaining if active was removed
	activeFound := false
	for _, acc := range remaining {
		if acc.ID == a.store.GoogleAntigravity.ActiveAccountID {
			activeFound = true
			break
		}
	}
	if !activeFound {
		first := remaining[0]
		a.store.GoogleAntigravity.ActiveAccountID = first.ID
		a.accountID = first.ID
		a.email = first.Credential.Email
		a.source = first.Credential.Source
		a.accessToken = first.Credential.Access
		a.refreshToken = first.Credential.Refresh
		a.projectID = first.Credential.ProjectID
		if first.Credential.Expires > 0 {
			a.expiresAt = time.UnixMilli(int64(first.Credential.Expires))
		}
	}

	return a.saveLocked()
}

// LoginInteractive performs a browser-based Google OAuth2 flow for Cloud Code Antigravity and saves the credential.
func (a *AuthManager) LoginInteractive(ctx context.Context, openBrowser bool) (*AccountEntry, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to start local OAuth callback listener: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", port)

	stateBytes := make([]byte, 16)
	_, _ = rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

	authURLParams := url.Values{}
	authURLParams.Set("client_id", GoogleClientID)
	authURLParams.Set("redirect_uri", redirectURI)
	authURLParams.Set("response_type", "code")
	authURLParams.Set("scope", strings.Join(googleOAuthScopes, " "))
	authURLParams.Set("access_type", "offline")
	authURLParams.Set("prompt", "consent")
	authURLParams.Set("state", state)

	fullAuthURL := GoogleAuthURL + "?" + authURLParams.Encode()

	type callbackResult struct {
		code string
		err  error
	}
	resCh := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
			resCh <- callbackResult{err: fmt.Errorf("invalid OAuth state parameter")}
			return
		}
		if errMsg := r.URL.Query().Get("error"); errMsg != "" {
			http.Error(w, "OAuth error: "+errMsg, http.StatusBadRequest)
			resCh <- callbackResult{err: fmt.Errorf("oauth error from Google: %s", errMsg)}
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			resCh <- callbackResult{err: fmt.Errorf("missing authorization code")}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `<!DOCTYPE html><html><head><title>Comini Authentication</title><style>body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;background:#0d1117;color:#e6edf3;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}.card{background:#161b22;border:1px solid #30363d;border-radius:12px;padding:32px 48px;text-align:center;box-shadow:0 8px 24px rgba(0,0,0,0.4)}h1{color:#3fb950;margin-top:0}</style></head><body><div class="card"><h1>✓ Comini Authenticated</h1><p>Google Antigravity OAuth token has been saved. You may close this tab and return to your terminal.</p></div></body></html>`)
		resCh <- callbackResult{code: code}
	})

	srv := &http.Server{Handler: mux}
	go func() {
		_ = srv.Serve(listener)
	}()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	fmt.Printf("\n[Comini Auth] Opening browser for Google Antigravity OAuth login...\n")
	fmt.Printf("If the browser does not open automatically, visit:\n\n  %s\n\nWaiting for authentication callback on %s ...\n", fullAuthURL, redirectURI)

	if openBrowser {
		_ = openURLInBrowser(fullAuthURL)
	}

	var code string
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-resCh:
		if res.err != nil {
			return nil, res.err
		}
		code = res.code
	}

	// Exchange authorization code for access + refresh tokens
	tokenData := url.Values{}
	tokenData.Set("client_id", GoogleClientID)
	tokenData.Set("client_secret", GoogleClientSecret)
	tokenData.Set("code", code)
	tokenData.Set("grant_type", "authorization_code")
	tokenData.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, GoogleTokenURL, bytes.NewBufferString(tokenData.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token exchange failed with HTTP %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenPayload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenPayload); err != nil {
		return nil, fmt.Errorf("failed to decode token response: %w", err)
	}

	if tokenPayload.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token returned from Google")
	}

	expiresAt := time.Now().Add(time.Duration(tokenPayload.ExpiresIn) * time.Second)
	email := fetchGoogleUserEmail(ctx, tokenPayload.AccessToken)
	projectID := discoverAntigravityProject(ctx, tokenPayload.AccessToken)

	return a.SaveAccount(email, tokenPayload.AccessToken, tokenPayload.RefreshToken, projectID, "oauth", expiresAt)
}

// SaveAccount adds or updates an account in the store and sets it as the active account.
func (a *AuthManager) SaveAccount(email, accessToken, refreshToken, projectID, source string, expiresAt time.Time) (*AccountEntry, error) {
	a.loadStoredCredential()

	a.mu.Lock()
	defer a.mu.Unlock()

	if projectID == "" {
		projectID = "aicode-consumers"
	}
	if source == "" {
		source = "oauth"
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(55 * time.Minute)
	}

	var matched *AccountEntry
	for i := range a.store.GoogleAntigravity.Accounts {
		acc := &a.store.GoogleAntigravity.Accounts[i]
		if (email != "" && strings.EqualFold(acc.Credential.Email, email)) ||
			(refreshToken != "" && acc.Credential.Refresh == refreshToken) {
			acc.Credential.Access = accessToken
			if refreshToken != "" {
				acc.Credential.Refresh = refreshToken
			}
			acc.Credential.Expires = float64(expiresAt.UnixMilli())
			if email != "" {
				acc.Credential.Email = email
			}
			acc.Credential.ProjectID = projectID
			acc.Credential.Source = source
			matched = acc
			break
		}
	}

	if matched == nil {
		idBytes := make([]byte, 16)
		_, _ = rand.Read(idBytes)
		newEntry := AccountEntry{
			ID: hex.EncodeToString(idBytes),
			Credential: AccountCredential{
				Access:    accessToken,
				Refresh:   refreshToken,
				Expires:   float64(expiresAt.UnixMilli()),
				Email:     email,
				Source:    source,
				ProjectID: projectID,
			},
			AddedAt: time.Now().UnixMilli(),
		}
		a.store.GoogleAntigravity.Accounts = append(a.store.GoogleAntigravity.Accounts, newEntry)
		matched = &a.store.GoogleAntigravity.Accounts[len(a.store.GoogleAntigravity.Accounts)-1]
	}

	a.store.GoogleAntigravity.ActiveAccountID = matched.ID
	a.accountID = matched.ID
	a.email = matched.Credential.Email
	a.source = matched.Credential.Source
	a.accessToken = matched.Credential.Access
	a.refreshToken = matched.Credential.Refresh
	a.projectID = matched.Credential.ProjectID
	a.expiresAt = expiresAt

	if err := a.saveLocked(); err != nil {
		return nil, err
	}

	out := *matched
	return &out, nil
}

func fetchGoogleUserEmail(ctx context.Context, accessToken string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GoogleUserInfoURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var info struct {
		Email string `json:"email"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&info)
	return info.Email
}

func discoverAntigravityProject(ctx context.Context, accessToken string) string {
	payload := []byte(`{"metadata":{"ideType":"ANTIGRAVITY","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", bytes.NewReader(payload))
	if err != nil {
		return "aicode-consumers"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "antigravity/hub/2.24.0 (aidev_client; os_type=darwin; arch=arm64)")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "aicode-consumers"
	}
	defer resp.Body.Close()

	var result struct {
		CloudaicompanionProject any `json:"cloudaicompanionProject"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
		switch v := result.CloudaicompanionProject.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case map[string]any:
			if id, ok := v["id"].(string); ok && strings.TrimSpace(id) != "" {
				return strings.TrimSpace(id)
			}
		}
	}
	return "aicode-consumers"
}

func openURLInBrowser(targetURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", targetURL)
	case "linux":
		cmd = exec.Command("xdg-open", targetURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL)
	default:
		return fmt.Errorf("unsupported platform")
	}
	return cmd.Start()
}

func (a *AuthManager) refreshOAuthToken(refreshToken string) (string, time.Time, error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("client_id", GoogleClientID)
	data.Set("client_secret", GoogleClientSecret)
	data.Set("refresh_token", refreshToken)

	req, err := http.NewRequest(http.MethodPost, GoogleTokenURL, bytes.NewBufferString(data.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", time.Time{}, fmt.Errorf("refresh token error status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", time.Time{}, err
	}

	if payload.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("empty access token returned")
	}

	expires := time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	return payload.AccessToken, expires, nil
}
