package server

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RawPluginManifest represents .codex-plugin/plugin.json metadata.
type RawPluginManifest struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Homepage    string   `json:"homepage"`
	Keywords    []string `json:"keywords"`
	Interface   struct {
		DisplayName       string   `json:"displayName"`
		ShortDescription  string   `json:"shortDescription"`
		LongDescription   string   `json:"longDescription"`
		DeveloperName     string   `json:"developerName"`
		Category          string   `json:"category"`
		Capabilities      []string `json:"capabilities"`
		WebsiteURL        string   `json:"websiteURL"`
		PrivacyPolicyURL  string   `json:"privacyPolicyURL"`
		TermsOfServiceURL string   `json:"termsOfServiceURL"`
		DefaultPrompt     any      `json:"defaultPrompt"`
		ComposerIcon      string   `json:"composerIcon"`
		Logo              string   `json:"logo"`
		LogoDark          string   `json:"logoDark"`
		BrandColor        string   `json:"brandColor"`
		Screenshots       []string `json:"screenshots"`
	} `json:"interface"`
}

// RawAppManifest represents .app.json connector mapping.
type RawAppManifest struct {
	Apps map[string]struct {
		ID       string `json:"id"`
		Required bool   `json:"required"`
	} `json:"apps"`
}

// RawMCPManifest represents .mcp.json server definition.
type RawMCPManifest struct {
	MCPServers map[string]any `json:"mcpServers"`
}

// LocalPlugin holds the parsed metadata and directory path of a genuine plugin on disk.
type LocalPlugin struct {
	Name           string
	ID             string
	Dir            string
	CanonicalAppID string
	AppIDs         []string
	MCPServerKeys  []string
	Manifest       RawPluginManifest
	Enabled        bool
}

// PluginStore manages discovery, metadata caching, and asset resolution for local plugins.
type PluginStore struct {
	mu           sync.RWMutex
	plugins      map[string]*LocalPlugin // indexed by Name, ID, and scoped names
	pluginsList  []*LocalPlugin
	appIDMap     map[string]*LocalPlugin // maps canonical app_id -> LocalPlugin
	installedSet map[string]bool
	lastLoadedAt time.Time
}

var (
	defaultPluginStore *PluginStore
	pluginStoreOnce    sync.Once
)

func GetDefaultPluginStore() *PluginStore {
	pluginStoreOnce.Do(func() {
		defaultPluginStore = NewPluginStore()
		defaultPluginStore.Reload()
	})
	return defaultPluginStore
}

func (h *Handlers) getPluginStore() *PluginStore {
	return GetDefaultPluginStore()
}

func NewPluginStore() *PluginStore {
	return &PluginStore{
		plugins:      make(map[string]*LocalPlugin),
		appIDMap:     make(map[string]*LocalPlugin),
		installedSet: make(map[string]bool),
	}
}

func makePluginID(name string) string {
	h := md5.Sum([]byte(name))
	return fmt.Sprintf("plugin_connector_1p_%s", hex.EncodeToString(h[:]))
}

// categoryToSlug converts display category names to url_slug (e.g. "Developer Tools" -> "developer-tools").
func categoryToSlug(cat string) string {
	s := strings.ToLower(cat)
	s = strings.ReplaceAll(s, "&", "and")
	s = strings.ReplaceAll(s, " ", "-")
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	res := b.String()
	for strings.Contains(res, "--") {
		res = strings.ReplaceAll(res, "--", "-")
	}
	return strings.Trim(res, "-")
}

// Reload scans all local plugin directories and ~/.codex/config.toml to index genuine plugins.
func (s *PluginStore) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.plugins = make(map[string]*LocalPlugin)
	s.appIDMap = make(map[string]*LocalPlugin)
	s.pluginsList = nil

	// 1. Read installed plugins from ~/.codex/config.toml
	s.loadInstalledFromConfig()

	// 2. Discover marketplace directories
	homeDir, _ := os.UserHomeDir()
	discoveryDirs := []string{
		filepath.Join(homeDir, ".codex", ".tmp", "plugins", "plugins"),
		"/Applications/ChatGPT.app/Contents/Resources/plugins/openai-bundled/plugins",
		filepath.Join(homeDir, ".codex", ".tmp", "bundled-marketplaces", "openai-bundled", "plugins"),
		filepath.Join(homeDir, ".cache", "codex-runtimes", "codex-primary-runtime", "plugins", "openai-primary-runtime", "plugins"),
	}

	seenDirs := make(map[string]bool)

	for _, d := range discoveryDirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			pName := entry.Name()
			pDir := filepath.Join(d, pName)
			if seenDirs[pName] {
				continue
			}

			pjPath := filepath.Join(pDir, ".codex-plugin", "plugin.json")
			pjData, err := os.ReadFile(pjPath)
			if err != nil {
				continue
			}

			var manifest RawPluginManifest
			if err := json.Unmarshal(pjData, &manifest); err != nil {
				continue
			}
			if manifest.Name == "" {
				manifest.Name = pName
			}

			lp := &LocalPlugin{
				Name:     pName,
				ID:       makePluginID(pName),
				Dir:      pDir,
				Manifest: manifest,
				Enabled:  s.installedSet[pName] || s.installedSet[pName+"@openai-curated"] || s.installedSet[pName+"@openai-bundled"] || s.installedSet[pName+"@openai-primary-runtime"],
			}

			// Read .app.json
			appPath := filepath.Join(pDir, ".app.json")
			if appData, err := os.ReadFile(appPath); err == nil {
				var appManifest RawAppManifest
				if err := json.Unmarshal(appData, &appManifest); err == nil {
					for _, app := range appManifest.Apps {
						if app.ID != "" {
							lp.AppIDs = append(lp.AppIDs, app.ID)
							if lp.CanonicalAppID == "" {
								lp.CanonicalAppID = app.ID
							}
							s.appIDMap[app.ID] = lp
						}
					}
				}
			}

			// Read .mcp.json
			mcpPath := filepath.Join(pDir, ".mcp.json")
			if mcpData, err := os.ReadFile(mcpPath); err == nil {
				var mcpManifest RawMCPManifest
				if err := json.Unmarshal(mcpData, &mcpManifest); err == nil {
					for key := range mcpManifest.MCPServers {
						lp.MCPServerKeys = append(lp.MCPServerKeys, key)
					}
				}
			}

			seenDirs[pName] = true
			s.plugins[pName] = lp
			s.plugins[lp.ID] = lp
			s.plugins[pName+"@openai-curated-remote"] = lp
			s.plugins[pName+"@openai-curated"] = lp
			s.plugins[pName+"@openai-bundled"] = lp
			s.plugins[pName+"@openai-primary-runtime"] = lp
			s.pluginsList = append(s.pluginsList, lp)
		}
	}

	// Sort list by DisplayName
	sort.Slice(s.pluginsList, func(i, j int) bool {
		nameI := s.pluginsList[i].Manifest.Interface.DisplayName
		if nameI == "" {
			nameI = s.pluginsList[i].Name
		}
		nameJ := s.pluginsList[j].Manifest.Interface.DisplayName
		if nameJ == "" {
			nameJ = s.pluginsList[j].Name
		}
		return strings.ToLower(nameI) < strings.ToLower(nameJ)
	})

	s.lastLoadedAt = time.Now()
	log.Printf("[Comini Plugins] Loaded %d genuine plugins from local marketplaces (%d installed)",
		len(s.pluginsList), len(s.installedSet))
}

func (s *PluginStore) loadInstalledFromConfig() {
	homeDir, _ := os.UserHomeDir()
	configPath := filepath.Join(homeDir, ".codex", "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}

	lines := strings.Split(string(data), "\n")
	var currentPluginKey string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[plugins.\"") && strings.HasSuffix(line, "\"]") {
			currentPluginKey = strings.TrimSuffix(strings.TrimPrefix(line, "[plugins.\""), "\"]")
			// e.g. "github@openai-curated" or "browser@openai-bundled"
			s.installedSet[currentPluginKey] = true
			parts := strings.SplitN(currentPluginKey, "@", 2)
			s.installedSet[parts[0]] = true
		} else if strings.HasPrefix(line, "enabled = false") && currentPluginKey != "" {
			s.installedSet[currentPluginKey] = false
			parts := strings.SplitN(currentPluginKey, "@", 2)
			s.installedSet[parts[0]] = false
		}
	}
}

func (s *PluginStore) getAssetURL(p *LocalPlugin, relPath string) string {
	if relPath == "" {
		return ""
	}
	base := filepath.Base(relPath)
	if base == "." || base == "/" || base == "" {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:9876/plugins/assets/%s/%s", p.Name, base)
}

// ResolveAsset returns the full path of an authentic asset on disk, with path traversal prevention.
func (s *PluginStore) ResolveAsset(pluginName, filename string) (string, error) {
	s.mu.RLock()
	p, ok := s.plugins[pluginName]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("plugin %s not found", pluginName)
	}

	cleanFilename := filepath.Base(filename)
	candidates := []string{
		filepath.Join(p.Dir, "assets", cleanFilename),
		filepath.Join(p.Dir, cleanFilename),
	}

	for _, cand := range candidates {
		rel, err := filepath.Rel(p.Dir, cand)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand, nil
		}
	}

	return "", fmt.Errorf("asset %s not found in plugin %s", cleanFilename, pluginName)
}

// ToSummaryJSON returns the summary schema consumed by /ps/plugins/home and category pages.
func (s *PluginStore) ToSummaryJSON(p *LocalPlugin) map[string]any {
	iconURL := s.getAssetURL(p, p.Manifest.Interface.Logo)
	iconDarkURL := s.getAssetURL(p, p.Manifest.Interface.LogoDark)
	if iconDarkURL == "" {
		iconDarkURL = iconURL
	}

	status := "AVAILABLE"
	if p.Enabled {
		status = "ENABLED"
	}

	dispName := p.Manifest.Interface.DisplayName
	if dispName == "" {
		dispName = p.Name
	}

	shortDesc := p.Manifest.Interface.ShortDescription
	if shortDesc == "" {
		shortDesc = p.Manifest.Description
	}

	keywords := p.Manifest.Keywords
	if len(keywords) == 0 {
		keywords = []string{p.Name}
	}

	return map[string]any{
		"id":                      p.ID,
		"name":                    p.Name,
		"display_name":            dispName,
		"short_description":       shortDesc,
		"icon_url":                iconURL,
		"icon_url_dark":           iconDarkURL,
		"requires_local_executor": false,
		"is_template":             false,
		"status":                  status,
		"keywords":                keywords,
	}
}

// ToFullJSON returns the complete plugin schema consumed by /ps/plugins/installed and /ps/plugins/{id}.
func (s *PluginStore) ToFullJSON(p *LocalPlugin) map[string]any {
	iconURL := s.getAssetURL(p, p.Manifest.Interface.Logo)
	iconDarkURL := s.getAssetURL(p, p.Manifest.Interface.LogoDark)
	if iconDarkURL == "" {
		iconDarkURL = iconURL
	}
	composerIconURL := s.getAssetURL(p, p.Manifest.Interface.ComposerIcon)
	if composerIconURL == "" {
		composerIconURL = iconURL
	}

	status := "AVAILABLE"
	if p.Enabled {
		status = "ENABLED"
	}

	dispName := p.Manifest.Interface.DisplayName
	if dispName == "" {
		dispName = p.Name
	}

	shortDesc := p.Manifest.Interface.ShortDescription
	if shortDesc == "" {
		shortDesc = p.Manifest.Description
	}

	longDesc := p.Manifest.Interface.LongDescription
	if longDesc == "" {
		longDesc = p.Manifest.Description
	}

	keywords := p.Manifest.Keywords
	if len(keywords) == 0 {
		keywords = []string{p.Name}
	}

	cat := p.Manifest.Interface.Category
	if cat == "" {
		cat = "Productivity"
	}

	devName := p.Manifest.Interface.DeveloperName
	if devName == "" {
		devName = "OpenAI"
	}

	capabilities := p.Manifest.Interface.Capabilities
	if len(capabilities) == 0 {
		capabilities = []string{"Interactive"}
	}

	var defaultPrompts []string
	switch v := p.Manifest.Interface.DefaultPrompt.(type) {
	case string:
		if v != "" {
			defaultPrompts = []string{v}
		}
	case []any:
		for _, item := range v {
			if str, ok := item.(string); ok && str != "" {
				defaultPrompts = append(defaultPrompts, str)
			}
		}
	}

	var mcpServers []map[string]any
	for _, key := range p.MCPServerKeys {
		mcpServers = append(mcpServers, map[string]any{"key": key})
	}
	if len(mcpServers) == 0 {
		mcpServers = []map[string]any{{"key": p.Name}}
	}

	var appIDs []string
	if len(p.AppIDs) > 0 {
		appIDs = p.AppIDs
	} else if p.CanonicalAppID != "" {
		appIDs = []string{p.CanonicalAppID}
	} else {
		appIDs = []string{}
	}

	skills := []map[string]any{
		{
			"name":                    p.Name,
			"description":             shortDesc,
			"plugin_release_skill_id": fmt.Sprintf("skill_%s_default", p.Name),
			"interface": map[string]any{
				"display_name":      dispName,
				"short_description": shortDesc,
				"brand_color":       p.Manifest.Interface.BrandColor,
				"icon_large_url":    iconURL,
				"icon_small_url":    composerIconURL,
			},
		},
	}

	release := map[string]any{
		"version":                 p.Manifest.Version,
		"display_name":            dispName,
		"description":             shortDesc,
		"keywords":                keywords,
		"requires_local_executor": false,
		"app_ids":                 appIDs,
		"onboarding_skill_name":   p.Name,
		"skills":                  skills,
		"app_templates":           []any{},
		"mcp_servers":             mcpServers,
		"interface": map[string]any{
			"brand_color":           p.Manifest.Interface.BrandColor,
			"category":              cat,
			"capabilities":          capabilities,
			"composer_icon_url":     composerIconURL,
			"default_prompts":       defaultPrompts,
			"developer_name":        devName,
			"logo_url":              iconURL,
			"logo_url_dark":         iconDarkURL,
			"long_description":      longDesc,
			"privacy_policy_url":    p.Manifest.Interface.PrivacyPolicyURL,
			"terms_of_service_url":  p.Manifest.Interface.TermsOfServiceURL,
			"website_url":           p.Manifest.Interface.WebsiteURL,
			"screenshot_urls":       p.Manifest.Interface.Screenshots,
		},
	}

	return map[string]any{
		"id":                      p.ID,
		"name":                    p.Name,
		"display_name":            dispName,
		"short_description":       shortDesc,
		"icon_url":                iconURL,
		"icon_url_dark":           iconDarkURL,
		"status":                  status,
		"enabled":                 p.Enabled,
		"canonical_app_id":        p.CanonicalAppID,
		"disabled_skill_names":    []string{},
		"installation_policy":     "AVAILABLE",
		"authentication_policy":   "ON_USE",
		"requires_local_executor": false,
		"is_template":             false,
		"keywords":                keywords,
		"release":                 release,
	}
}

// --- HTTP HANDLERS ---

// HandlePluginAssets serves real image files directly from disk.
func (h *Handlers) HandlePluginAssets(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "..") {
		http.NotFound(w, r)
		return
	}
	relPath := strings.TrimPrefix(r.URL.Path, "/plugins/assets/")
	parts := strings.SplitN(relPath, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	pluginName := parts[0]
	filename := parts[1]

	filePath, err := h.getPluginStore().ResolveAsset(pluginName, filename)
	if err != nil || filePath == "" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, filePath)
}

// HandlePsPluginsHome serves featured and category sections for the Plugins Store homepage.
func (h *Handlers) HandlePsPluginsHome(w http.ResponseWriter, r *http.Request) {
	store := h.getPluginStore()
	store.mu.RLock()
	defer store.mu.RUnlock()

	type section struct {
		ID      string           `json:"id"`
		Title   string           `json:"title"`
		URLSlug string           `json:"url_slug"`
		Plugins []map[string]any `json:"plugins"`
	}

	catMap := make(map[string][]map[string]any)
	catNames := make(map[string]string)
	var featured []map[string]any

	featuredNames := map[string]bool{
		"github": true, "google-calendar": true, "google-drive": true, "slack": true,
		"figma": true, "browser": true, "visualize": true, "latex": true,
		"cloudflare": true, "clickup": true, "airtable": true, "notion": true,
	}

	for _, p := range store.pluginsList {
		summary := store.ToSummaryJSON(p)
		cat := p.Manifest.Interface.Category
		if cat == "" {
			cat = "Productivity"
		}
		slug := categoryToSlug(cat)
		catMap[slug] = append(catMap[slug], summary)
		catNames[slug] = cat

		if featuredNames[p.Name] {
			featured = append(featured, summary)
		}
	}

	var sections []section
	if len(featured) > 0 {
		sections = append(sections, section{
			ID:      "featured",
			Title:   "Featured",
			URLSlug: "featured",
			Plugins: featured,
		})
	}

	orderedSlugs := []string{
		"developer-tools", "productivity", "creativity", "communication",
		"data-and-analytics", "education-and-research", "finance",
		"business-and-operations", "scientific-research", "security",
		"engineering", "design", "research",
	}

	seen := make(map[string]bool)
	for _, slug := range orderedSlugs {
		if plist, ok := catMap[slug]; ok && len(plist) > 0 {
			sections = append(sections, section{
				ID:      slug,
				Title:   catNames[slug],
				URLSlug: slug,
				Plugins: plist,
			})
			seen[slug] = true
		}
	}

	for slug, plist := range catMap {
		if !seen[slug] && len(plist) > 0 {
			sections = append(sections, section{
				ID:      slug,
				Title:   catNames[slug],
				URLSlug: slug,
				Plugins: plist,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sections": sections,
	})
}

// HandlePsPluginsInstalled serves installed plugins.
func (h *Handlers) HandlePsPluginsInstalled(w http.ResponseWriter, r *http.Request) {
	store := h.getPluginStore()
	store.mu.RLock()
	defer store.mu.RUnlock()

	var installed []map[string]any
	for _, p := range store.pluginsList {
		if p.Enabled {
			installed = append(installed, store.ToFullJSON(p))
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"plugins": installed,
		"pagination": map[string]any{
			"next_page_token": nil,
		},
	})
}

// HandlePsPluginCategories serves plugins filtered by category.
func (h *Handlers) HandlePsPluginCategories(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/ps/plugin-categories/")
	slug = strings.TrimSuffix(slug, "/plugins")
	slug = strings.Trim(slug, "/")

	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	store := h.getPluginStore()
	store.mu.RLock()
	defer store.mu.RUnlock()

	var matched []map[string]any
	catTitle := slug
	for _, p := range store.pluginsList {
		pSlug := categoryToSlug(p.Manifest.Interface.Category)
		if slug != "all" && pSlug != slug && slug != p.Manifest.Interface.Category {
			continue
		}
		if catTitle == slug && p.Manifest.Interface.Category != "" {
			catTitle = p.Manifest.Interface.Category
		}

		if q != "" {
			combined := strings.ToLower(fmt.Sprintf("%s %s %s %s",
				p.Name,
				p.Manifest.Interface.DisplayName,
				p.Manifest.Interface.ShortDescription,
				strings.Join(p.Manifest.Keywords, " ")))
			if !strings.Contains(combined, q) {
				continue
			}
		}

		matched = append(matched, store.ToSummaryJSON(p))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"category": map[string]any{
			"id":          slug,
			"title":       catTitle,
			"url_slug":    slug,
			"description": catTitle,
		},
		"plugins": matched,
		"pagination": map[string]any{
			"limit":           50,
			"next_page_token": nil,
		},
	})
}

// HandlePsPluginsList serves all plugins in full format.
func (h *Handlers) HandlePsPluginsList(w http.ResponseWriter, r *http.Request) {
	store := h.getPluginStore()
	store.mu.RLock()
	defer store.mu.RUnlock()

	var list []map[string]any
	for _, p := range store.pluginsList {
		list = append(list, store.ToFullJSON(p))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"plugins": list,
		"pagination": map[string]any{
			"next_page_token": nil,
		},
	})
}

// HandlePsPluginsSearch searches plugins by query parameter q.
func (h *Handlers) HandlePsPluginsSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	store := h.getPluginStore()
	store.mu.RLock()
	defer store.mu.RUnlock()

	var list []map[string]any
	for _, p := range store.pluginsList {
		if q != "" {
			combined := strings.ToLower(fmt.Sprintf("%s %s %s %s",
				p.Name,
				p.Manifest.Interface.DisplayName,
				p.Manifest.Interface.ShortDescription,
				strings.Join(p.Manifest.Keywords, " ")))
			if !strings.Contains(combined, q) {
				continue
			}
		}
		list = append(list, store.ToFullJSON(p))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"plugins": list,
		"pagination": map[string]any{
			"next_page_token": nil,
		},
	})
}

// HandlePsPluginsBatch serves batch plugin requests.
func (h *Handlers) HandlePsPluginsBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PluginIDs []string `json:"plugin_ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	store := h.getPluginStore()
	store.mu.RLock()
	defer store.mu.RUnlock()

	var results []map[string]any
	for _, id := range req.PluginIDs {
		p := store.resolvePlugin(id)
		if p != nil {
			results = append(results, map[string]any{
				"requested_plugin_id": id,
				"plugin":              store.ToFullJSON(p),
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results": results,
	})
}

// HandlePsAppsBatch serves batch app/connector requests.
func (h *Handlers) HandlePsAppsBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AppIDs       []string `json:"app_ids"`
		IncludeTools bool     `json:"include_tools"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	store := h.getPluginStore()
	store.mu.RLock()
	defer store.mu.RUnlock()

	var apps []map[string]any
	for _, appID := range req.AppIDs {
		p := store.appIDMap[appID]
		if p != nil {
			iconURL := store.getAssetURL(p, p.Manifest.Interface.Logo)
			iconDarkURL := store.getAssetURL(p, p.Manifest.Interface.LogoDark)
			if iconDarkURL == "" {
				iconDarkURL = iconURL
			}
			dispName := p.Manifest.Interface.DisplayName
			if dispName == "" {
				dispName = p.Name
			}
			apps = append(apps, map[string]any{
				"id":            appID,
				"name":          dispName,
				"icon_url":      iconURL,
				"icon_dark_url": iconDarkURL,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"apps": apps,
	})
}

// HandlePsCanonicalApp resolves an app_id to a plugin_id.
func (h *Handlers) HandlePsCanonicalApp(w http.ResponseWriter, r *http.Request) {
	appID := strings.TrimPrefix(r.URL.Path, "/ps/plugins/canonical-app/")
	appID = strings.Trim(appID, "/")

	store := h.getPluginStore()
	store.mu.RLock()
	p := store.appIDMap[appID]
	store.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if p != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plugin_id": p.ID,
		})
	} else {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plugin_id": nil,
		})
	}
}

// HandlePsPluginsSuggested serves suggested plugins for Codex.
func (h *Handlers) HandlePsPluginsSuggested(w http.ResponseWriter, r *http.Request) {
	store := h.getPluginStore()
	store.mu.RLock()
	defer store.mu.RUnlock()

	var suggested []map[string]any
	for _, p := range store.pluginsList {
		if len(suggested) >= 12 {
			break
		}
		suggested = append(suggested, store.ToSummaryJSON(p))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"plugins": suggested,
	})
}

// HandlePsWorkspacePlugins serves empty personal/workspace created/shared lists.
func (h *Handlers) HandlePsWorkspacePlugins(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"plugins": []any{},
		"pagination": map[string]any{
			"next_page_token": nil,
		},
	})
}

// HandlePsEventSubscriptions serves empty event subscriptions batch.
func (h *Handlers) HandlePsEventSubscriptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"subscriptions": []any{},
	})
}

func (s *PluginStore) resolvePlugin(idOrName string) *LocalPlugin {
	if p, ok := s.plugins[idOrName]; ok {
		return p
	}
	clean := strings.TrimPrefix(idOrName, "plugin_connector_1p_")
	clean = strings.SplitN(clean, "@", 2)[0]
	if p, ok := s.plugins[clean]; ok {
		return p
	}
	return nil
}

// HandlePsPluginSubroutes handles detail view and mutations for /ps/plugins/{plugin_id}[/action].
func (h *Handlers) HandlePsPluginSubroutes(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/ps/plugins/")
	parts := strings.Split(sub, "/")
	pluginID := parts[0]

	store := h.getPluginStore()

	// Mutation actions: install, uninstall, enable, disable
	if len(parts) >= 2 {
		action := parts[1]
		w.Header().Set("Content-Type", "application/json")

		store.mu.Lock()
		p := store.resolvePlugin(pluginID)
		if p != nil {
			switch action {
			case "install", "enable":
				p.Enabled = true
				store.installedSet[p.Name] = true
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status":  "ENABLED",
					"enabled": true,
				})
				store.mu.Unlock()
				return
			case "uninstall":
				p.Enabled = false
				store.installedSet[p.Name] = false
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status":  "AVAILABLE",
					"enabled": false,
				})
				store.mu.Unlock()
				return
			case "disable":
				p.Enabled = false
				store.installedSet[p.Name] = false
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status":  "DISABLED_BY_USER",
					"enabled": false,
				})
				store.mu.Unlock()
				return
			case "skills":
				// /ps/plugins/{plugin_id}/skills/{skill_name}
				skillName := ""
				if len(parts) >= 3 {
					skillName = parts[2]
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"name": skillName,
					"interface": map[string]any{
						"display_name":      p.Manifest.Interface.DisplayName,
						"short_description": p.Manifest.Interface.ShortDescription,
					},
				})
				store.mu.Unlock()
				return
			}
		}
		store.mu.Unlock()
	}

	// Single plugin detail read: GET /ps/plugins/{plugin_id}
	store.mu.RLock()
	p := store.resolvePlugin(pluginID)
	defer store.mu.RUnlock()

	if p == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": fmt.Sprintf("Plugin %s not found", pluginID),
				"type":    "invalid_request_error",
				"code":    "not_found",
			},
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(store.ToFullJSON(p))
}
