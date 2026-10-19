package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"comini/internal/domain"
)

const agyHubUserAgent = "antigravity/hub/2.24.0 (aidev_client; os_type=darwin; arch=arm64; auth_method=google_oauth)"

var defaultCandidateBaseURLs = []string{
	"https://daily-cloudcode-pa.googleapis.com",
}

type CCAClient struct {
	baseURLs   []string
	userAgent  string
	httpClient *http.Client
}

func NewCCAClient(baseURL, userAgent string) *CCAClient {
	var urls []string
	seen := make(map[string]bool)

	addURL := func(u string) {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u != "" && !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}

	if baseURL != "" {
		addURL(baseURL)
	}
	for _, fallback := range defaultCandidateBaseURLs {
		addURL(fallback)
	}

	// Exact match of Antigravity 2.0 Hub CreateCloudCodeHTTPClient (0x1019974b0):
	// Non-nil TLSClientConfig without ForceAttemptHTTP2 forces HTTP/1.1 (h1) with native system DNS and no header timeout.
	return &CCAClient{
		baseURLs:  urls,
		userAgent: userAgent,
		httpClient: &http.Client{
			Transport: &http.Transport{
				Proxy:           http.ProxyFromEnvironment,
				TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
			},
			Timeout: 0,
		},
	}
}

func genRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("agent/%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// StreamGenerateContent sends CCA envelope request and returns response stream with automatic retry and 429 rate-limit backoff.
func (c *CCAClient) StreamGenerateContent(
	ctx context.Context,
	wireModelID string,
	body domain.GeminiWireBody,
	authHeader string,
	projectID string,
) (*http.Response, error) {
	if projectID == "" {
		projectID = "aicode-consumers"
	}

	// Exact match of Antigravity 2.0 Hub BuildGenerateContentRequest (0x1019929ac -> #0xc0) &
	// StreamGenerateContent (0x101993228 clears ServiceAccount #0xb0, preserving SessionId #0xc0).
	envelope := domain.CCAEnvelope{
		Model:       wireModelID,
		UserAgent:   "antigravity",
		RequestType: "agent",
		Project:     projectID,
		RequestID:   genRequestID(),
		Request:     body,
	}

	payloadBytes, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal CCA envelope: %w", err)
	}

	var lastErr error
	maxAttempts := 5
	if len(c.baseURLs) == 0 {
		return nil, fmt.Errorf("no CCA base URLs configured")
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		currentBaseURL := c.baseURLs[attempt%len(c.baseURLs)]
		url := fmt.Sprintf("%s/v1internal:streamGenerateContent?alt=sse", currentBaseURL)

		log.Printf("[CCA Outbound Wire] 📡 Target: %s (attempt %d/%d) | Model: %s | payloadBytes: %d | contents: %d",
			url, attempt+1, maxAttempts, wireModelID, len(payloadBytes), len(body.Contents))

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payloadBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create http request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", agyHubUserAgent)
		if authHeader != "" {
			ah := authHeader
			if !strings.HasPrefix(ah, "Bearer ") {
				ah = "Bearer " + ah
			}
			req.Header.Set("Authorization", ah)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				// User or client intentionally canceled/interrupted this request (e.g. Stop button pressed in Codex).
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("CCA request to %s failed: %w", currentBaseURL, err)
			log.Printf("[CCA Warning] ⚠️ Endpoint %s connection/DNS error: %v. Retrying with fallback...", currentBaseURL, err)
			backoff := time.Duration(200*(attempt+1))*time.Millisecond + time.Duration(time.Now().UnixNano()%100)*time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			continue
		}

		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		lastErr = fmt.Errorf("upstream error HTTP %d: %s", resp.StatusCode, string(bodyBytes))

		// Specific handling for HTTP 429 (Rate Limit / Resource Exhausted) - Exponential backoff on same model
		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt == maxAttempts-1 {
				log.Printf("[CCA Error] ❌ Endpoint %s returned HTTP 429 (Resource Exhausted). Retry budget exhausted (%d attempts): %s", currentBaseURL, maxAttempts, string(bodyBytes))
				return nil, lastErr
			}
			// Exponential backoff with jitter: attempt 0: ~1.2s, attempt 1: ~2.2s, attempt 2: ~3.5s, attempt 3: ~4.5s
			baseMs := 1000 * (1 << attempt)
			if baseMs > 4000 {
				baseMs = 4000
			}
			jitter := time.Duration(time.Now().UnixNano()%300) * time.Millisecond
			backoff := time.Duration(baseMs)*time.Millisecond + jitter
			log.Printf("[CCA Warning] ⚠️ Endpoint %s returned HTTP 429 (Resource Exhausted). Backing off %v on model %s before retry (attempt %d/%d)...", currentBaseURL, backoff, wireModelID, attempt+1, maxAttempts)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			continue
		}

		if resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout ||
			resp.StatusCode == http.StatusNotFound ||
			resp.StatusCode == http.StatusInternalServerError {
			if attempt == maxAttempts-1 {
				log.Printf("[CCA Error] ❌ Endpoint %s returned HTTP %d. Retry budget exhausted (%d attempts)", currentBaseURL, resp.StatusCode, maxAttempts)
				return nil, lastErr
			}
			backoff := time.Duration(300*(attempt+1))*time.Millisecond + time.Duration(time.Now().UnixNano()%150)*time.Millisecond
			log.Printf("[CCA Warning] ⚠️ Endpoint %s returned HTTP %d. Retrying in %v (attempt %d/%d)...", currentBaseURL, resp.StatusCode, backoff, attempt+1, maxAttempts)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			continue
		}

		log.Printf("[CCA Error] ❌ HTTP %d: %s | payloadBytes=%d", resp.StatusCode, string(bodyBytes), len(payloadBytes))
		return nil, lastErr
	}

	return nil, lastErr
}

// GenerateImage calls the Gemini Imagen endpoint (gemini-3.1-flash-image) and saves the result to local disk.
func (c *CCAClient) GenerateImage(
	ctx context.Context,
	prompt string,
	aspectRatio string,
	authHeader string,
	projectID string,
) (string, string, error) {
	if projectID == "" {
		projectID = "aicode-consumers"
	}

	promptText := strings.TrimSpace(prompt)
	if aspectRatio != "" && !strings.Contains(strings.ToLower(promptText), "aspect ratio") {
		promptText = fmt.Sprintf("%s, aspect ratio: %s", promptText, aspectRatio)
	}

	reqPayload := map[string]any{
		"project": projectID,
		"model":   "gemini-3.1-flash-image",
		"request": map[string]any{
			"contents": []map[string]any{
				{
					"role": "user",
					"parts": []map[string]any{
						{"text": promptText},
					},
				},
			},
		},
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal image request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < len(c.baseURLs); attempt++ {
		targetBase := c.baseURLs[attempt]
		targetURL := fmt.Sprintf("%s/v1internal:generateContent", targetBase)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payloadBytes))
		if err != nil {
			return "", "", err
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", agyHubUserAgent)
		if authHeader != "" {
			ah := authHeader
			if !strings.HasPrefix(ah, "Bearer ") {
				ah = "Bearer " + ah
			}
			req.Header.Set("Authorization", ah)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("upstream error HTTP %d: %s", resp.StatusCode, string(respBody))
			if resp.StatusCode == http.StatusUnauthorized {
				return "", "", lastErr
			}
			continue
		}

		// Parse candidates and inlineData
		var root map[string]any
		if err := json.Unmarshal(respBody, &root); err != nil {
			lastErr = fmt.Errorf("failed to parse upstream JSON: %w", err)
			continue
		}

		var candidates []any
		if cList, ok := root["candidates"].([]any); ok {
			candidates = cList
		} else if respObj, ok := root["response"].(map[string]any); ok {
			if cList, ok := respObj["candidates"].([]any); ok {
				candidates = cList
			}
		}

		if len(candidates) == 0 {
			lastErr = fmt.Errorf("no candidates returned by image model")
			continue
		}

		var rawBase64 string
		mimeType := "image/jpeg"

		cand0, _ := candidates[0].(map[string]any)
		content, _ := cand0["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for _, pAny := range parts {
			pMap, _ := pAny.(map[string]any)
			if inline, ok := pMap["inlineData"].(map[string]any); ok {
				if d, ok := inline["data"].(string); ok && d != "" {
					rawBase64 = d
					if m, ok := inline["mimeType"].(string); ok && m != "" {
						mimeType = m
					}
					break
				}
			} else if inline, ok := pMap["inline_data"].(map[string]any); ok {
				if d, ok := inline["data"].(string); ok && d != "" {
					rawBase64 = d
					if m, ok := inline["mime_type"].(string); ok && m != "" {
						mimeType = m
					}
					break
				}
			}
		}

		if rawBase64 == "" {
			lastErr = fmt.Errorf("no inline image data found in candidate parts")
			continue
		}

		imgBytes, err := base64.StdEncoding.DecodeString(rawBase64)
		if err != nil {
			return "", "", fmt.Errorf("failed to decode base64 image data: %w", err)
		}

		home, _ := os.UserHomeDir()
		imagesDir := filepath.Join(home, ".codex", "images")
		_ = os.MkdirAll(imagesDir, 0755)

		ext := ".jpg"
		if strings.Contains(mimeType, "png") {
			ext = ".png"
		}
		fileName := fmt.Sprintf("img_%d_%s%s", time.Now().Unix(), strings.TrimPrefix(genRequestID(), "agent/")[:8], ext)
		savedPath := filepath.Join(imagesDir, fileName)

		if err := os.WriteFile(savedPath, imgBytes, 0644); err != nil {
			return "", "", fmt.Errorf("failed to save image to %s: %w", savedPath, err)
		}

		dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, rawBase64)
		return savedPath, dataURL, nil
	}

	if lastErr != nil {
		return "", "", lastErr
	}
	return "", "", fmt.Errorf("failed to generate image across all candidate base URLs")
}

// QuotaModelInfo holds real-time quota and metadata for a single model returned by CCA.
type QuotaModelInfo struct {
	ModelID           string  `json:"model_id"`
	DisplayName       string  `json:"display_name"`
	RemainingFraction float64 `json:"remaining_fraction"`
	ResetTime         string  `json:"reset_time,omitempty"`
	MaxTokens         int     `json:"max_tokens,omitempty"`
	MaxOutputTokens   int     `json:"max_output_tokens,omitempty"`
}

// FetchQuotaAndModels queries Google CCA fetchAvailableModels endpoint for live model quotas and limits.
func (c *CCAClient) FetchQuotaAndModels(ctx context.Context, authHeader string, projectID string) ([]QuotaModelInfo, error) {
	if projectID == "" {
		projectID = "aicode-consumers"
	}

	payloadBytes, _ := json.Marshal(map[string]any{
		"project": projectID,
	})

	var lastErr error
	for _, baseURL := range c.baseURLs {
		targetURL := fmt.Sprintf("%s/v1internal:fetchAvailableModels", baseURL)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payloadBytes))
		if err != nil {
			lastErr = err
			continue
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", agyHubUserAgent)
		if authHeader != "" {
			ah := authHeader
			if !strings.HasPrefix(ah, "Bearer ") {
				ah = "Bearer " + ah
			}
			req.Header.Set("Authorization", ah)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(bodyBytes))
			continue
		}

		var parsed struct {
			Models map[string]struct {
				DisplayName     string `json:"displayName"`
				MaxTokens       int    `json:"maxTokens"`
				MaxOutputTokens int    `json:"maxOutputTokens"`
				QuotaInfo       struct {
					RemainingFraction *float64 `json:"remainingFraction"`
					ResetTime         string   `json:"resetTime"`
				} `json:"quotaInfo"`
			} `json:"models"`
		}
		if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
			lastErr = err
			continue
		}

		var list []QuotaModelInfo
		for id, m := range parsed.Models {
			rem := 1.0
			if m.QuotaInfo.RemainingFraction != nil {
				rem = *m.QuotaInfo.RemainingFraction
			}
			list = append(list, QuotaModelInfo{
				ModelID:           id,
				DisplayName:       m.DisplayName,
				RemainingFraction: rem,
				ResetTime:         m.QuotaInfo.ResetTime,
				MaxTokens:         m.MaxTokens,
				MaxOutputTokens:   m.MaxOutputTokens,
			})
		}
		return list, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("failed to fetch available models from CCA")
}

// UserQuotaBucket represents a single rate-limit bucket (e.g., gemini-5h, gemini-weekly, 3p-5h, 3p-weekly) from CCA.
type UserQuotaBucket struct {
	BucketID          string  `json:"bucketId"`
	DisplayName       string  `json:"displayName"`
	Window            string  `json:"window"`
	ResetTime         string  `json:"resetTime"`
	Description       string  `json:"description"`
	RemainingFraction float64 `json:"remainingFraction"`
}

// UserQuotaGroup represents a model group in CCA's retrieveUserQuotaSummary response.
type UserQuotaGroup struct {
	DisplayName string            `json:"displayName"`
	Description string            `json:"description"`
	Buckets     []UserQuotaBucket `json:"buckets"`
}

// UserQuotaSummary represents the response from Google CCA /v1internal:retrieveUserQuotaSummary.
type UserQuotaSummary struct {
	Groups      []UserQuotaGroup `json:"groups"`
	Description string           `json:"description"`
}

// FetchUserQuotaSummary queries Google CCA retrieveUserQuotaSummary endpoint for exact 5h and weekly quota buckets.
func (c *CCAClient) FetchUserQuotaSummary(ctx context.Context, authHeader string, projectID string) (*UserQuotaSummary, error) {
	if projectID == "" {
		projectID = "aicode-consumers"
	}

	payloadBytes, _ := json.Marshal(map[string]any{
		"project": projectID,
	})

	var lastErr error
	for _, baseURL := range c.baseURLs {
		targetURL := fmt.Sprintf("%s/v1internal:retrieveUserQuotaSummary", baseURL)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payloadBytes))
		if err != nil {
			lastErr = err
			continue
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", agyHubUserAgent)
		if authHeader != "" {
			ah := authHeader
			if !strings.HasPrefix(ah, "Bearer ") {
				ah = "Bearer " + ah
			}
			req.Header.Set("Authorization", ah)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(bodyBytes))
			continue
		}

		var summary UserQuotaSummary
		if err := json.Unmarshal(bodyBytes, &summary); err != nil {
			lastErr = err
			continue
		}
		return &summary, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("failed to retrieve user quota summary from CCA")
}


