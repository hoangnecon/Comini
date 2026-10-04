package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	GoogleClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	GoogleClientSecret = "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"
	GoogleTokenURL     = "https://oauth2.googleapis.com/token"
)

type AuthManager struct {
	mu           sync.RWMutex
	accessToken  string
	refreshToken string
	expiresAt    time.Time
	projectID    string
}

var DefaultAuthManager = NewAuthManager()

func NewAuthManager() *AuthManager {
	am := &AuthManager{}
	am.loadStoredCredential()
	return am
}

func (a *AuthManager) loadStoredCredential() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	// 1. Try ~/.opencodex/auth.json
	authPath := filepath.Join(home, ".opencodex", "auth.json")
	if data, err := os.ReadFile(authPath); err == nil {
		var root map[string]json.RawMessage
		if err := json.Unmarshal(data, &root); err == nil {
			if rawGA, ok := root["google-antigravity"]; ok {
				var gaStruct struct {
					ActiveAccountID string `json:"activeAccountId"`
					Accounts        []struct {
						ID         string `json:"id"`
						Credential struct {
							Access    string  `json:"access"`
							Refresh   string  `json:"refresh"`
							Expires   float64 `json:"expires"`
							ProjectID string  `json:"projectId"`
						} `json:"credential"`
					} `json:"accounts"`
				}
				if err := json.Unmarshal(rawGA, &gaStruct); err == nil {
					for _, acc := range gaStruct.Accounts {
						if gaStruct.ActiveAccountID == "" || acc.ID == gaStruct.ActiveAccountID {
							a.mu.Lock()
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
							a.mu.Unlock()
							break
						}
					}
				}
			}
		}
	}

	// 2. Fallback to ~/.gemini/jetski-standalone-oauth-token if access token is empty
	a.mu.RLock()
	curToken := a.accessToken
	curRefresh := a.refreshToken
	a.mu.RUnlock()

	if curToken == "" && curRefresh == "" {
		jetskiPath := filepath.Join(home, ".gemini", "jetski-standalone-oauth-token")
		if data, err := os.ReadFile(jetskiPath); err == nil {
			var jStruct struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(data, &jStruct); err == nil && jStruct.Token != "" {
				a.mu.Lock()
				a.accessToken = jStruct.Token
				a.expiresAt = time.Now().Add(50 * time.Minute)
				a.mu.Unlock()
			}
		}
	}
}

// GetCredentials returns a valid access token and project ID, auto-refreshing if expired.
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
		return "", project, fmt.Errorf("no stored Antigravity token found")
	}

	// Refresh token
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
	a.mu.Unlock()

	return newToken, project, nil
}

// ForceRefresh forces a token refresh from Google OAuth endpoint regardless of current expiry.
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
	a.mu.Unlock()

	return newToken, project, nil
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
		return "", time.Time{}, fmt.Errorf("refresh token error status %d", resp.StatusCode)
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
