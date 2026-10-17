package server

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
)

var (
	iconCacheMu sync.RWMutex
	iconCache   = make(map[string]string)
)

// getPluginIconDataURI returns a clean, crisp SVG Data URI (base64) for a plugin icon.
func getPluginIconDataURI(slug, displayName, category string) string {
	slugLower := strings.ToLower(slug)
	cacheKey := slugLower + "|" + category

	iconCacheMu.RLock()
	if uri, ok := iconCache[cacheKey]; ok {
		iconCacheMu.RUnlock()
		return uri
	}
	iconCacheMu.RUnlock()

	svg := buildPluginSVG(slugLower, displayName, category)
	uri := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg))

	iconCacheMu.Lock()
	iconCache[cacheKey] = uri
	iconCacheMu.Unlock()

	return uri
}

func buildPluginSVG(slug, displayName, category string) string {
	// 1. Specific brand vector icons
	switch {
	case strings.Contains(slug, "github"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#24292e"/><path fill="#ffffff" fill-rule="evenodd" d="M20 9a11 11 0 00-3.48 21.43c.55.1.75-.24.75-.53v-1.87c-3.06.67-3.7-1.48-3.7-1.48-.5-1.27-1.22-1.61-1.22-1.61-1-.68.08-.67.08-.67 1.1.08 1.68 1.13 1.68 1.13.98 1.68 2.57 1.2 3.2.92.1-.72.38-1.2.7-1.48-2.45-.28-5.02-1.22-5.02-5.45 0-1.2.43-2.19 1.14-2.96-.12-.28-.5-1.4.1-2.92 0 0 .93-.3 3.03 1.13a10.57 10.57 0 015.52 0c2.1-1.43 3.03-1.13 3.03-1.13.6 1.52.22 2.64.1 2.92.71.77 1.13 1.76 1.13 2.96 0 4.24-2.58 5.16-5.04 5.44.4.34.75 1.01.75 2.04v3.02c0 .3.2.64.76.53A11 11 0 0020 9z"/></svg>`
	case strings.Contains(slug, "gitlab"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#292961"/><path fill="#e24329" d="M20 29.5l4.8-14.8H15.2L20 29.5z"/><path fill="#fc6d26" d="M20 29.5L15.2 14.7H9.5L20 29.5z"/><path fill="#fca326" d="M9.5 14.7L8.2 18.7c-.2.6 0 1.2.5 1.6L20 29.5 9.5 14.7z"/><path fill="#fc6d26" d="M20 29.5l4.8-14.8h5.7L20 29.5z"/><path fill="#fca326" d="M30.5 14.7l1.3 4c.2.6 0 1.2-.5 1.6L20 29.5l10.5-14.8z"/></svg>`
	case strings.Contains(slug, "figma"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#1e1e1e"/><circle cx="26" cy="20" r="4" fill="#1abcfe"/><path d="M14 12a4 4 0 014-4h4v8h-4a4 4 0 01-4-4z" fill="#f24e1e"/><path d="M22 8h4a4 4 0 010 8h-4V8z" fill="#ff7262"/><path d="M14 20a4 4 0 014-4h4v8h-4a4 4 0 01-4-4z" fill="#a259ff"/><path d="M14 28a4 4 0 014-4h4v4a4 4 0 01-8 0z" fill="#0acf83"/></svg>`
	case strings.Contains(slug, "docker"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#0db7ed"/><path fill="#ffffff" d="M33 19c-.4-2.5-2.6-3.4-3.8-3.3-.2-.8-.8-1.5-1.6-1.9-.9-.4-2-.4-2.8 0-.4-1.2-1.5-2-2.8-2h-3v5h12.3c.7.7 1.2 1.5 1.7 2.2zM10 17h3v3h-3zm4 0h3v3h-3zm4 0h3v3h-3zm-8 4h3v3H10zm4 0h3v3h-3zm4 0h3v3h-3zm4 0h3v3h-3zm-8-8h3v3h-3zm4 0h3v3h-3zm15 11c-.5 0-1.5.1-2.4-.4-1-.5-2.2-.4-3.2 0-1.3.5-3 .5-4.4 0-1.3-.5-3-.5-4.4 0-1.4.5-3 .5-4.4 0-1.2-.4-2.5-.2-3.6.4-1.1.6-1.6.4-1.6.4C4.8 28.5 9 32 17 32c9.5 0 15.8-5.3 16-11z"/></svg>`
	case strings.Contains(slug, "kubernetes") || slug == "k8s":
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#326ce5"/><circle cx="20" cy="20" r="5" fill="none" stroke="#fff" stroke-width="2.5"/><path fill="#ffffff" d="M20 7l4 3v5l-4-3-4 3v-5l4-3zm11.3 6.5l1.5 4.8-3.8 3.3-3.8-3.3 1.5-4.8 4.6 0zm0 13l-4.6 0-1.5-4.8 3.8-3.3 3.8 3.3-1.5 4.8zm-11.3 6.5l-4-3v-5l4 3 4-3v5l-4 3zm-11.3-6.5l-1.5-4.8 3.8-3.3 3.8 3.3-1.5 4.8-4.6 0zm0-13l4.6 0 1.5 4.8-3.8 3.3-3.8-3.3 1.5-4.8z"/></svg>`
	case strings.Contains(slug, "slack"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#4a154b"/><path d="M12 21a2.5 2.5 0 012.5-2.5H17v5h-2.5A2.5 2.5 0 0112 21zm6.5 0a2.5 2.5 0 012.5-2.5 2.5 2.5 0 012.5 2.5v6a2.5 2.5 0 01-5 0v-6z" fill="#36c5f0"/><path d="M21 12a2.5 2.5 0 012.5 2.5V17h-5v-2.5A2.5 2.5 0 0121 12zm0 6.5a2.5 2.5 0 012.5 2.5 2.5 2.5 0 01-2.5 2.5h-6a2.5 2.5 0 010-5h6z" fill="#2eb67d"/><path d="M28 19a2.5 2.5 0 01-2.5 2.5H23v-5h2.5A2.5 2.5 0 0128 19zm-6.5 0a2.5 2.5 0 01-2.5 2.5 2.5 2.5 0 01-2.5-2.5v-6a2.5 2.5 0 015 0v6z" fill="#ecb22e"/><path d="M19 28a2.5 2.5 0 01-2.5-2.5V23h5v2.5A2.5 2.5 0 0119 28zm0-6.5a2.5 2.5 0 01-2.5-2.5 2.5 2.5 0 012.5-2.5h6a2.5 2.5 0 010 5h-6z" fill="#e01e5a"/></svg>`
	case strings.Contains(slug, "discord"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#5865f2"/><path fill="#ffffff" d="M28.4 12.8a18.2 18.2 0 00-4.5-1.4.1.1 0 00-.1.1c-.2.4-.4.8-.6 1.2a16.8 16.8 0 00-5.1 0 10.9 10.9 0 00-.6-1.2.1.1 0 00-.1-.1 18.3 18.3 0 00-4.5 1.4.1.1 0 00-.1.1C10 17.5 9.1 22 9.5 26.6a.1.1 0 00.1.1 18.4 18.4 0 005.6 2.8.1.1 0 00.1 0c.4-.6.8-1.3 1.2-2a.1.1 0 00-.1-.1 12.1 12.1 0 01-1.7-.8.1.1 0 010-.2c.1-.1.3-.2.4-.3a.1.1 0 01.1 0c3.5 1.6 7.4 1.6 10.9 0a.1.1 0 01.1 0c.1.1.3.2.4.3a.1.1 0 010 .2c-.5.3-1.1.6-1.7.8a.1.1 0 00-.1.1c.4.7.8 1.4 1.2 2a.1.1 0 00.1 0 18.3 18.3 0 005.6-2.8.1.1 0 00.1-.1c.5-5.2-.8-9.8-3.5-13.8a.1.1 0 00-.1-.1zM15.4 23.3c-1.1 0-2-1-2-2.2s.9-2.2 2-2.2 2 1 2 2.2c0 1.2-.9 2.2-2 2.2zm9.2 0c-1.1 0-2-1-2-2.2s.9-2.2 2-2.2 2 1 2 2.2c0 1.2-.9 2.2-2 2.2z"/></svg>`
	case strings.Contains(slug, "linear"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#5e6ad2"/><path fill="#ffffff" fill-rule="evenodd" d="M11 20a9 9 0 0115.36-6.36l-1.42 1.42A7 7 0 1013 20h-2zm4.3 6.36A9 9 0 0129 20h-2a7 7 0 00-9.9-4.95l-1.41-1.41A9 9 0 0115.3 26.36z"/></svg>`
	case strings.Contains(slug, "notion"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#000000"/><path fill="#ffffff" d="M12 11.5l10.2-1.3c1.2-.2 1.8.4 1.8 1.6v15.4c0 1-.5 1.5-1.5 1.6L12 30c-1 .2-1.7-.4-1.7-1.5V13c0-1.1.7-1.4 1.7-1.5zm3.2 3.8v11.4l6-8.2v8.5h2V14.7l-6 8.3v-7.7h-2z"/></svg>`
	case strings.Contains(slug, "trello"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#0079bf"/><rect x="12" y="11" width="6.5" height="18" rx="1.5" fill="#ffffff"/><rect x="21.5" y="11" width="6.5" height="11" rx="1.5" fill="#ffffff"/></svg>`
	case strings.Contains(slug, "google-drive") || strings.Contains(slug, "gdrive"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#ffffff"/><path fill="#0066da" d="M15.4 27h17.2l-4.3-7.5H11.1L15.4 27z"/><path fill="#00ac47" d="M24 12H15.4L6.8 27l4.3 0L24 12z"/><path fill="#ea4335" d="M28.3 19.5l4.3 7.5L24 12h-8.6l12.9 7.5z"/></svg>`
	case strings.Contains(slug, "google-docs"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#4285f4"/><rect x="11" y="9" width="18" height="22" rx="2" fill="#ffffff"/><path fill="#4285f4" d="M14 15h12v2H14zm0 4h12v2H14zm0 4h8v2H14z"/></svg>`
	case strings.Contains(slug, "google-sheets") || slug == "spreadsheets":
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#0f9d58"/><rect x="11" y="9" width="18" height="22" rx="2" fill="#ffffff"/><path fill="#0f9d58" d="M14 14h12v12H14zm2 2v3h3v-3h-3zm5 0v3h3v-3h-3zm-5 5v3h3v-3h-3zm5 0v3h3v-3h-3z"/></svg>`
	case strings.Contains(slug, "google-slides") || slug == "presentations":
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#f4b400"/><rect x="10" y="11" width="20" height="18" rx="2" fill="#ffffff"/><rect x="13" y="14" width="14" height="12" rx="1" fill="#f4b400"/></svg>`
	case strings.Contains(slug, "pdf"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#d93829"/><rect x="11" y="9" width="18" height="22" rx="2" fill="#ffffff"/><text x="20" y="24" fill="#d93829" font-size="9" font-family="-apple-system,sans-serif" font-weight="900" text-anchor="middle">PDF</text></svg>`
	case strings.Contains(slug, "documents"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#0284c7"/><rect x="11" y="9" width="18" height="22" rx="2" fill="#ffffff"/><path fill="#0284c7" d="M14 14h12v2H14zm0 4h12v2H14zm0 4h8v2H14z"/></svg>`
	case strings.Contains(slug, "wolfram"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#dd1100"/><path fill="#ffffff" d="M20 10l2.5 5.5L28 13l-1.5 5.8 5.5 2.2-5.5 2.2L28 29l-5.5-2.5L20 32l-2.5-5.5L12 29l1.5-5.8L8 21l5.5-2.2L12 13l5.5 2.5z"/></svg>`
	case strings.Contains(slug, "stripe"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#635bff"/><path fill="#ffffff" d="M19.5 17.2c0-1.2.9-1.7 2.4-1.7 1.8 0 3.7.6 4.9 1.3V13c-1.3-.5-3-.8-4.9-.8-4.5 0-7.3 2.3-7.3 6.1 0 5.8 8 5 8 7.5 0 1.4-1.2 1.8-2.8 1.8-2.1 0-4.3-.8-5.7-1.8v3.9c1.6.7 3.6 1.1 5.7 1.1 4.7 0 7.7-2.3 7.7-6.2 0-6.2-8-5.3-8-7.4z"/></svg>`
	case strings.Contains(slug, "postgres"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#336791"/><path fill="#ffffff" d="M20 11c-5 0-8 3.5-8 8.5 0 3.8 2.2 6.5 5 7.5v-3c-1.5-.8-2.5-2.4-2.5-4.5 0-3 1.8-5.5 5.5-5.5s5.5 2.5 5.5 5.5c0 2.1-1 3.7-2.5 4.5v3c2.8-1 5-3.7 5-7.5 0-5-3-8.5-8-8.5z"/></svg>`
	case strings.Contains(slug, "mysql"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#00758f"/><text x="20" y="24" fill="#f29111" font-size="10" font-family="-apple-system,sans-serif" font-weight="900" text-anchor="middle">MySQL</text></svg>`
	case strings.Contains(slug, "redis"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#dc382d"/><path fill="#ffffff" d="M20 11l9 4.5-9 4.5-9-4.5zM11 18.5l9 4.5v6.5l-9-4.5zm18 0v6.5l-9 4.5v-6.5z"/></svg>`
	case strings.Contains(slug, "mongodb"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#13aa52"/><path fill="#ffffff" d="M20 9s-7 6.5-7 13.5c0 5 3.5 8.5 7 8.5s7-3.5 7-8.5C27 15.5 20 9 20 9zm0 20c-2.5 0-5-2.5-5-6.5 0-3.5 3-7 5-8.5 2 1.5 5 5 5 8.5 0 4-2.5 6.5-5 6.5z"/></svg>`
	case strings.Contains(slug, "snowflake"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#29b5e8"/><path fill="#ffffff" stroke="#ffffff" stroke-width="1.5" stroke-linecap="round" d="M20 10v20m-8.7-15l17.4 10m-17.4 0l17.4-10"/></svg>`
	case strings.Contains(slug, "sentry"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#362d59"/><path fill="#ffffff" d="M23.5 12h-7l-5.5 16h18l-5.5-16zm-3.5 5l2.5 7h-5l2.5-7z"/></svg>`
	case strings.Contains(slug, "datadog"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#632ca6"/><path fill="#ffffff" d="M14 13h12v14H14zM16 15v10h8V15h-8z"/></svg>`
	case strings.Contains(slug, "postman"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="10" fill="#ff6c37"/><circle cx="20" cy="20" r="8" fill="#ffffff"/><path fill="#ff6c37" d="M17 17l6 3-6 3z"/></svg>`
	case strings.Contains(slug, "deep-research"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><defs><linearGradient id="dr" x1="0" y1="0" x2="1" y2="1"><stop offset="0%" stop-color="#8b5cf6"/><stop offset="100%" stop-color="#ec4899"/></linearGradient></defs><rect width="40" height="40" rx="10" fill="url(#dr)"/><circle cx="18" cy="18" r="6" fill="none" stroke="#ffffff" stroke-width="2.5"/><path d="M22.5 22.5L29 29" stroke="#ffffff" stroke-width="2.5" stroke-linecap="round"/><circle cx="18" cy="18" r="2" fill="#ffffff"/></svg>`
	case strings.Contains(slug, "code-review"):
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><defs><linearGradient id="cr" x1="0" y1="0" x2="1" y2="1"><stop offset="0%" stop-color="#10b981"/><stop offset="100%" stop-color="#059669"/></linearGradient></defs><rect width="40" height="40" rx="10" fill="url(#cr)"/><circle cx="14" cy="15" r="3" fill="none" stroke="#fff" stroke-width="2"/><circle cx="14" cy="25" r="3" fill="none" stroke="#fff" stroke-width="2"/><circle cx="26" cy="20" r="3" fill="none" stroke="#fff" stroke-width="2"/><path d="M14 18v4M16 16c4 1 6 3 8 3" fill="none" stroke="#fff" stroke-width="2"/></svg>`
	}

	// 2. High-quality category gradients + initial badge
	bgGradient1, bgGradient2 := getCategoryGradients(category)
	initials := getInitials(displayName)

	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40">
<defs>
  <linearGradient id="grad" x1="0%%" y1="0%%" x2="100%%" y2="100%%">
    <stop offset="0%%" stop-color="%s"/>
    <stop offset="100%%" stop-color="%s"/>
  </linearGradient>
</defs>
<rect width="40" height="40" rx="10" fill="url(#grad)"/>
<text x="20" y="25" fill="#ffffff" font-size="14" font-family="-apple-system,BlinkMacSystemFont,'SF Pro Display','Segoe UI',sans-serif" font-weight="700" text-anchor="middle" letter-spacing="-0.5">%s</text>
</svg>`, bgGradient1, bgGradient2, initials)
}

func getCategoryGradients(category string) (string, string) {
	switch category {
	case "Developer Tools":
		return "#6366f1", "#4338ca"
	case "Productivity":
		return "#3b82f6", "#1d4ed8"
	case "Documents & Office":
		return "#0ea5e9", "#0284c7"
	case "Data & Research":
		return "#8b5cf6", "#6d28d9"
	case "Design & Creative":
		return "#ec4899", "#be185d"
	case "Sales & Finance":
		return "#10b981", "#047857"
	default:
		return "#3b82f6", "#1d4ed8"
	}
}

func getInitials(displayName string) string {
	parts := strings.Fields(displayName)
	if len(parts) == 0 {
		return "PL"
	}
	if len(parts) == 1 {
		runes := []rune(parts[0])
		if len(runes) >= 2 {
			return strings.ToUpper(string(runes[:2]))
		}
		return strings.ToUpper(string(runes))
	}
	r1 := []rune(parts[0])
	r2 := []rune(parts[1])
	if len(r1) > 0 && len(r2) > 0 {
		return strings.ToUpper(string(r1[0]) + string(r2[0]))
	}
	return "PL"
}
