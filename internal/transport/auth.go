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

	authPath := filepath.Join(home, ".opencodex", "auth.json")
	data, err := os.ReadFile(authPath)
	if err != nil {
		return
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return
	}

	rawGA, ok := root["google-antigravity"]
	if !ok {
		return
	}

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

	if err := json.Unmarshal(rawGA, &gaStruct); err != nil {
		return
	}

	for _, acc := range gaStruct.Accounts {
		if gaStruct.ActiveAccountID == "" || acc.ID == gaStruct.ActiveAccountID {
			a.mu.Lock()
			a.accessToken = acc.Credential.Access
			a.refreshToken = acc.Credential.Refresh
			a.projectID = acc.Credential.ProjectID
			if acc.Credential.Expires > 0 {
				a.expiresAt = time.UnixMilli(int64(acc.Credential.Expires))
			}
			a.mu.Unlock()
			break
		}
	}
}

// GetCredentials returns a valid access token and project ID, auto-refreshing if expired.
func (a *AuthManager) GetCredentials() (string, string, error) {
	a.mu.RLock()
	token := a.accessToken
	refresh := a.refreshToken
	exp := a.expiresAt
	project := a.projectID
	a.mu.RUnlock()

	if token != "" && time.Now().Before(exp.Add(-2*time.Minute)) {
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
