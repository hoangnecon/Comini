package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"comini/internal/domain"
)

var defaultCandidateBaseURLs = []string{
	"https://cloudcode-pa.googleapis.com",
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

	// Resilient dialer with fallback to Google Public DNS (8.8.8.8:53) if system DNS fails
	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
		Resolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: 3 * time.Second}
				conn, err := d.DialContext(ctx, network, address)
				if err != nil {
					return d.DialContext(ctx, "udp", "8.8.8.8:53")
				}
				return conn, nil
			},
		},
	}

	return &CCAClient{
		baseURLs:  urls,
		userAgent: userAgent,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext:           dialer.DialContext,
				ResponseHeaderTimeout: 90 * time.Second,
				IdleConnTimeout:       90 * time.Second,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   10,
				ForceAttemptHTTP2:     true,
			},
			Timeout: 0,
		},
	}
}

func genRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("agent-%s", hex.EncodeToString(b))
}

// StreamGenerateContent sends CCA envelope request and returns response stream with automatic multi-endpoint fallback and 429 rate-limit backoff.
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
	const maxAttempts = 5

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
		req.Header.Set("User-Agent", "antigravity/ide/2.5.5 (aidev_client; os_type=darwin; arch=arm64)")
		if authHeader != "" {
			ah := authHeader
			if !strings.HasPrefix(ah, "Bearer ") {
				ah = "Bearer " + ah
			}
			req.Header.Set("Authorization", ah)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("CCA request to %s failed: %w", currentBaseURL, err)
			log.Printf("[CCA Warning] ⚠️ Endpoint %s connection/DNS error: %v. Retrying with fallback...", currentBaseURL, err)
			time.Sleep(time.Duration(150*(attempt+1)) * time.Millisecond)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		lastErr = fmt.Errorf("upstream error HTTP %d: %s", resp.StatusCode, string(bodyBytes))

		// Specific handling for HTTP 429 (Rate Limit / Resource Exhausted)
		if resp.StatusCode == http.StatusTooManyRequests {
			backoffDuration := time.Duration(800*(attempt+1)) * time.Millisecond
			log.Printf("[CCA Warning] ⚠️ Endpoint %s returned HTTP 429 (Resource Exhausted). Backing off %v before retry %d/%d...",
				currentBaseURL, backoffDuration, attempt+2, maxAttempts)
			time.Sleep(backoffDuration)
			continue
		}

		if resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout ||
			resp.StatusCode == http.StatusNotFound ||
			resp.StatusCode == http.StatusInternalServerError {
			log.Printf("[CCA Warning] ⚠️ Endpoint %s returned HTTP %d. Retrying on fallback...", currentBaseURL, resp.StatusCode)
			time.Sleep(time.Duration(200*(attempt+1)) * time.Millisecond)
			continue
		}

		log.Printf("[CCA Error] ❌ HTTP %d: %s | payloadBytes=%d", resp.StatusCode, string(bodyBytes), len(payloadBytes))
		return nil, lastErr
	}

	return nil, lastErr
}
