package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"comini/internal/config"
	"comini/internal/server"
	"comini/internal/session"
	"comini/internal/transport"
)

const (
	Version           = "2.0.0"
	LaunchAgentLabel  = "com.comini.proxy"
	DefaultLogRelPath = ".codex/comini.log"
)

func main() {
	args := os.Args[1:]
	if filepath.Base(os.Args[0]) == "codex" {
		runCodexShim(args)
		return
	}
	if len(args) == 0 {
		runServer()
		return
	}

	cmd := strings.ToLower(strings.TrimSpace(args[0]))
	switch cmd {
	case "serve", "start-foreground", "run":
		runServer()
	case "auth":
		handleAuthCLI(args[1:])
	case "login":
		handleAuthLogin()
	case "status":
		handleServiceStatus()
	case "quota", "models":
		handleQuotaCLI()
	case "service", "daemon":
		handleServiceCLI(args[1:])
	case "version", "-v", "--version":
		fmt.Printf("Comini v%s (Native Google Cloud Code Antigravity Bridge for Codex Desktop)\n", Version)
	case "help", "-h", "--help":
		printUsage()
	case "codex-shim":
		runCodexShim(args[1:])
	default:
		// If invoked via a symlink/binary named "codex", act as the codex app-server shim
		if filepath.Base(os.Args[0]) == "codex" {
			runCodexShim(args)
			return
		}
		fmt.Fprintf(os.Stderr, "Unknown command %q\n\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func runCodexShim(args []string) {
	realBin := "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex.real"
	if _, err := os.Stat(realBin); err != nil {
		realBin = "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex"
	}

	isAppServer := false
	for _, a := range args {
		if strings.Contains(a, "app-server") {
			isAppServer = true
			break
		}
	}

	cmd := exec.Command(realBin, args...)
	cmd.Stderr = os.Stderr

	if !isAppServer {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			os.Exit(1)
		}
		return
	}

	stdinPipe, err := cmd.StdinPipe()
	if err == nil {
		go func() {
			defer stdinPipe.Close()
			buf := make([]byte, 64*1024)
			var pending []byte
			for {
				n, rerr := os.Stdin.Read(buf)
				if n > 0 {
					pending = append(pending, buf[:n]...)
					for {
						idx := -1
						for i, b := range pending {
							if b == '\n' {
								idx = i
								break
							}
						}
						if idx == -1 {
							break
						}
						line := pending[:idx]
						pending = pending[idx+1:]
						s := string(line)
						// Prevent codex app-server from attempting an upstream OAuth refresh with rt_comini_local,
						// which would fail with 401 and wipe ~/.codex/auth.json in memory!
						if strings.Contains(s, `"refreshToken":true`) {
							s = strings.ReplaceAll(s, `"refreshToken":true`, `"refreshToken":false`)
							line = []byte(s)
						}
						// When hostId="durable" (Cloud) queries thread/list with originators=["codex_cloud", ...],
						// return an empty list immediately so local threads are never claimed as Cloud ("durable") threads.
						if strings.Contains(s, `"thread/list"`) && strings.Contains(s, `"originators"`) {
							var req map[string]any
							if err := json.Unmarshal(line, &req); err == nil {
								if method, _ := req["method"].(string); method == "thread/list" {
									if params, ok := req["params"].(map[string]any); ok && params != nil {
										if orig, hasOrig := params["originators"]; hasOrig && orig != nil {
											resp := map[string]any{
												"id": req["id"],
												"result": map[string]any{
													"data":       []any{},
													"nextCursor": nil,
												},
											}
											if out, merr := json.Marshal(resp); merr == nil {
												_, _ = os.Stdout.Write(append(out, '\n'))
												continue
											}
										}
										delete(params, "originators")
										if out, merr := json.Marshal(req); merr == nil {
											line = out
										}
									}
								}
							}
						}
						_, _ = stdinPipe.Write(append(line, '\n'))
					}
				}
				if rerr != nil {
					if len(pending) > 0 {
						_, _ = stdinPipe.Write(pending)
					}
					break
				}
			}
		}()
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cmd.Stdout = os.Stdout
		_ = cmd.Run()
		return
	}
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}

	go func() {
		buf := make([]byte, 64*1024)
		var pending []byte
		for {
			n, rerr := stdoutPipe.Read(buf)
			if n > 0 {
				pending = append(pending, buf[:n]...)
				for {
					idx := -1
					for i, b := range pending {
						if b == '\n' {
							idx = i
							break
						}
					}
					if idx == -1 {
						break
					}
					line := pending[:idx]
					pending = pending[idx+1:]
					outLine := transformAppServerLine(line)
					_, _ = os.Stdout.Write(append(outLine, '\n'))
				}
			}
			if rerr != nil {
				if len(pending) > 0 {
					_, _ = os.Stdout.Write(pending)
				}
				break
			}
		}
	}()

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}

func transformAppServerLine(line []byte) []byte {
	s := string(line)
	if strings.Contains(s, `"userAgent"`) {
		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err == nil {
			if res, ok := msg["result"].(map[string]any); ok {
				if ua, ok := res["userAgent"].(string); ok {
					// Report 0.154.0 so kI.discover uses legacy routing and preserves http://localhost:8000/backend-api
					if idx := strings.Index(ua, "/"); idx != -1 {
						rest := ua[idx+1:]
						if sp := strings.Index(rest, " "); sp != -1 {
							res["userAgent"] = ua[:idx+1] + "0.154.0" + rest[sp:]
						} else {
							res["userAgent"] = ua[:idx+1] + "0.154.0"
						}
						if out, merr := json.Marshal(msg); merr == nil {
							return out
						}
					}
				}
			}
		}
	}
	if strings.Contains(s, `"workspaceRouting"`) {
		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err == nil {
			if res, ok := msg["result"].(map[string]any); ok {
				delete(res, "workspaceRouting")
				if out, merr := json.Marshal(msg); merr == nil {
					return out
				}
			}
		}
	}
	return line
}

func printUsage() {
	fmt.Printf(`Comini v%s — Native Google Antigravity Proxy & CLI for Codex Desktop

Usage:
  comini [serve]                   Run the HTTP proxy server in the foreground (default port: 9876)
  comini status                    Show proxy server health, active account, and service status
  comini quota                     Query live Google Cloud Code Antigravity model quotas

Authentication Commands:
  comini auth login                Authenticate with Google Antigravity via browser OAuth2
  comini auth status               Show current OAuth2 token, expiry, and stored accounts
  comini auth refresh              Force refresh the active OAuth2 access token and persist to disk
  comini auth switch <email|id>    Switch active Google Antigravity account
  comini auth logout [email|all]   Remove stored account credential(s)

Background Service (macOS LaunchAgent):
  comini service install           Install and start Comini as a persistent macOS LaunchAgent
  comini service uninstall         Stop and remove the Comini LaunchAgent
  comini service start             Start the background service
  comini service stop              Stop the background service
  comini service restart           Restart the background service
  comini service status            Show LaunchAgent and port status
  comini service logs              Show the last 50 lines of ~/.codex/comini.log
`, Version)
}

func ensureCodexDesktopBridge(cfg *config.Config) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	codexDir := filepath.Join(home, ".codex")
	_ = os.MkdirAll(codexDir, 0755)

	email := "comini@antigravity.local"
	if st := transport.DefaultAuthManager.Status(); st.Email != "" {
		email = st.Email
	}

	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payloadMap := map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": server.CodexAccountID,
			"chatgpt_plan_type":  "pro",
			"chatgpt_user_id":    server.CodexUserID,
			"user_id":            server.CodexUserID,
		},
		"https://api.openai.com/profile": map[string]any{
			"email":          email,
			"email_verified": true,
		},
		"iss": "https://auth.openai.com",
		"sub": "google-oauth2|comini",
		"iat": 1770000000,
		"exp": 4102444800,
	}
	payloadBytes, _ := json.Marshal(payloadMap)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadBytes)
	jwtTok := fmt.Sprintf("%s.%s.comini_local_sig", headerB64, payloadB64)

	authObj := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      jwtTok,
			"access_token":  jwtTok,
			"refresh_token": "rt_comini_local",
			"account_id":    server.CodexAccountID,
		},
		"last_refresh": time.Now().UTC().Format(time.RFC3339),
	}
	if authData, err := json.MarshalIndent(authObj, "", "  "); err == nil {
		authPath := filepath.Join(codexDir, "auth.json")
		_ = os.WriteFile(authPath, append(authData, '\n'), 0600)
	}

	// Ensure /opt/homebrew/bin/codex symlink points to comini and set CODEX_CLI_PATH=/opt/homebrew/bin/codex
	// so ChatGPT.app spawns our app-server shim (which strips workspaceRouting so localhost:8000 is preserved).
	if selfExe, exeErr := os.Executable(); exeErr == nil {
		_ = os.Symlink(selfExe, "/opt/homebrew/bin/codex")
	}
	_ = exec.Command("launchctl", "setenv", "CODEX_CLI_PATH", "/opt/homebrew/bin/codex").Run()
	_ = exec.Command("launchctl", "unsetenv", "CODEX_APP_SERVER_FORCE_CLI").Run()

	apiBase := "http://localhost:8000/backend-api"
	_ = exec.Command("launchctl", "setenv", "CODEX_API_BASE_URL", apiBase).Run()
}


func runServer() {
	cfg := config.Load()
	ensureCodexDesktopBridge(cfg)

	ccaClient := transport.NewCCAClient(cfg.CCABaseURL, cfg.UserAgent)
	store := session.DefaultSignatureStore
	handlers := server.NewHandlers(cfg, ccaClient, store)
	router := server.NewRouter(handlers)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	st := transport.DefaultAuthManager.Status()
	log.Printf("[Comini v%s] Server listening on http://%s (and http://localhost:8000 for Codex Desktop Electron Auth)", Version, addr)
	log.Printf("[Comini v%s] Upstream CCA: %s | Default Model: %s", Version, cfg.CCABaseURL, cfg.DefaultModel)
	if st.Authenticated {
		log.Printf("[Comini Auth] Active Account: %s (project: %s, source: %s)", st.Email, st.ProjectID, st.Source)
	} else {
		log.Printf("[Comini Auth] ⚠️ No stored OAuth token found. Run 'comini auth login' to authenticate.")
	}

	if cfg.Port != 8000 {
		go func() {
			if err := http.ListenAndServe("127.0.0.1:8000", router); err != nil {
				log.Printf("[Comini] Note: secondary listener on 127.0.0.1:8000 exited: %v", err)
			}
		}()
	}

	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func handleAuthCLI(args []string) {
	sub := "status"
	if len(args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(args[0]))
	}

	switch sub {
	case "login", "add":
		handleAuthLogin()
	case "status", "list", "ls":
		handleAuthStatus()
	case "refresh":
		handleAuthRefresh()
	case "switch", "use":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: comini auth switch <email|account_id>")
			os.Exit(1)
		}
		acc, err := transport.DefaultAuthManager.SwitchAccount(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Failed to switch account: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Switched active account to %s (id: %s, project: %s)\n",
			acc.Credential.Email, acc.ID, acc.Credential.ProjectID)
	case "logout", "remove", "rm":
		target := ""
		if len(args) >= 2 {
			target = args[1]
		}
		if err := transport.DefaultAuthManager.RemoveAccount(target); err != nil {
			fmt.Fprintf(os.Stderr, "❌ Failed to remove account: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✓ Stored credential removed successfully.")
	default:
		fmt.Fprintf(os.Stderr, "Unknown auth subcommand %q\n", sub)
		printUsage()
		os.Exit(1)
	}
}

func handleAuthLogin() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	acc, err := transport.DefaultAuthManager.LoginInteractive(ctx, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Authentication failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n✓ Successfully authenticated with Google Antigravity!\n")
	fmt.Printf("  Account Email : %s\n", acc.Credential.Email)
	fmt.Printf("  Account ID    : %s\n", acc.ID)
	fmt.Printf("  Project ID    : %s\n", acc.Credential.ProjectID)
	fmt.Printf("  Saved To      : %s\n", transport.DefaultAuthManager.AuthFilePath())
}

func handleAuthStatus() {
	st := transport.DefaultAuthManager.Status()
	fmt.Printf("=== Comini Authentication Status ===\n")
	fmt.Printf("Auth File       : %s\n", st.AuthFilePath)
	fmt.Printf("Authenticated   : %v\n", st.Authenticated)
	if !st.Authenticated {
		fmt.Println("\nRun 'comini auth login' to authenticate with your Google Antigravity account.")
		return
	}
	fmt.Printf("Active Email    : %s\n", st.Email)
	fmt.Printf("Active ID       : %s\n", st.ActiveAccountID)
	fmt.Printf("Project ID      : %s\n", st.ProjectID)
	fmt.Printf("Source          : %s\n", st.Source)
	fmt.Printf("Refresh Token   : %v\n", st.HasRefreshToken)
	if !st.ExpiresAt.IsZero() {
		fmt.Printf("Token Expires   : %s (in %ds)\n", st.ExpiresAt.Local().Format(time.RFC3339), st.ExpiresInSec)
	}
	if len(st.Accounts) > 0 {
		fmt.Printf("\nStored Accounts (%d):\n", len(st.Accounts))
		for _, acc := range st.Accounts {
			marker := " "
			if acc.ID == st.ActiveAccountID {
				marker = "*"
			}
			fmt.Printf("  [%s] %s (id: %s, project: %s)\n", marker, acc.Credential.Email, acc.ID, acc.Credential.ProjectID)
		}
	}
}

func handleAuthRefresh() {
	fmt.Println("Refreshing Google Antigravity OAuth2 token...")
	token, proj, err := transport.DefaultAuthManager.ForceRefresh()
	if err != nil || token == "" {
		fmt.Fprintf(os.Stderr, "❌ Token refresh failed: %v\n", err)
		os.Exit(1)
	}
	st := transport.DefaultAuthManager.Status()
	fmt.Printf("✓ Token refreshed and saved to %s\n", st.AuthFilePath)
	fmt.Printf("  Account    : %s\n", st.Email)
	fmt.Printf("  Project ID : %s\n", proj)
	fmt.Printf("  Expires At : %s (in %ds)\n", st.ExpiresAt.Local().Format(time.RFC3339), st.ExpiresInSec)
}

func handleQuotaCLI() {
	token, proj, err := transport.DefaultAuthManager.GetCredentials()
	if err != nil || token == "" {
		fmt.Fprintf(os.Stderr, "❌ Not authenticated: %v\n", err)
		os.Exit(1)
	}
	cfg := config.Load()
	client := transport.NewCCAClient(cfg.CCABaseURL, cfg.UserAgent)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	models, err := client.FetchQuotaAndModels(ctx, "Bearer "+token, proj)
	if err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "UNAUTHENTICATED")) {
		if freshToken, freshProj, refErr := transport.DefaultAuthManager.ForceRefresh(); refErr == nil && freshToken != "" {
			token = freshToken
			proj = freshProj
			models, err = client.FetchQuotaAndModels(ctx, "Bearer "+freshToken, freshProj)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to query quota from Google CCA: %v\n", err)
		os.Exit(1)
	}

	summary, _ := client.FetchUserQuotaSummary(ctx, "Bearer "+token, proj)

	sort.Slice(models, func(i, j int) bool {
		return models[i].ModelID < models[j].ModelID
	})

	st := transport.DefaultAuthManager.Status()
	fmt.Printf("=== Google Antigravity Quota (Account: %s | Project: %s) ===\n\n", st.Email, proj)
	if summary != nil && len(summary.Groups) > 0 {
		fmt.Printf("%-26s | %-12s | %-10s | %-10s | %-20s\n", "QUOTA GROUP", "WINDOW", "USED", "REMAINING", "RESET TIME")
		fmt.Println(strings.Repeat("-", 90))
		for _, g := range summary.Groups {
			for _, b := range g.Buckets {
				remPct := int(math.Round(b.RemainingFraction * 100.0))
				usedPct := 100 - remPct
				resetStr := b.ResetTime
				if resetStr != "" {
					if t, err := time.Parse(time.RFC3339, resetStr); err == nil {
						dur := time.Until(t).Round(time.Minute)
						resetStr = fmt.Sprintf("%s (%s)", t.Local().Format("01-02 15:04"), dur)
					}
				} else {
					resetStr = "-"
				}
				fmt.Printf("%-26s | %-12s | %-10s | %-10s | %-20s\n",
					g.DisplayName, b.Window, fmt.Sprintf("%d%%", usedPct), fmt.Sprintf("%d%%", remPct), resetStr)
			}
		}
		fmt.Println()
	}

	fmt.Printf("%-34s | %-28s | %-10s | %-20s\n", "WIRE MODEL ID", "DISPLAY NAME", "REMAINING", "RESET TIME")
	fmt.Println(strings.Repeat("-", 102))
	for _, m := range models {
		pct := fmt.Sprintf("%.0f%%", m.RemainingFraction*100)
		resetStr := m.ResetTime
		if resetStr != "" {
			if t, err := time.Parse(time.RFC3339, resetStr); err == nil {
				dur := time.Until(t).Round(time.Minute)
				resetStr = fmt.Sprintf("%s (%s)", t.Local().Format("15:04"), dur)
			}
		} else {
			resetStr = "-"
		}
		fmt.Printf("%-34s | %-28s | %-10s | %-20s\n", m.ModelID, m.DisplayName, pct, resetStr)
	}
}

func launchAgentPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist")
}

func logFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, DefaultLogRelPath)
}

func handleServiceStatus() {
	cfg := config.Load()
	addr := fmt.Sprintf("http://%s:%d/health", cfg.Host, cfg.Port)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(addr)

	fmt.Printf("=== Comini Service & Proxy Status ===\n")
	plistPath := launchAgentPlistPath()
	if _, statErr := os.Stat(plistPath); statErr == nil {
		fmt.Printf("LaunchAgent     : Installed (%s)\n", plistPath)
	} else {
		fmt.Printf("LaunchAgent     : Not installed (run 'comini service install' to enable auto-start)\n")
	}

	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		var health map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&health)
		fmt.Printf("Proxy Server    : RUNNING on http://%s:%d\n", cfg.Host, cfg.Port)
		fmt.Printf("Active Account  : %v (project: %v)\n", health["active_account"], health["project_id"])
		fmt.Printf("Default Model   : %v\n", health["default_model"])
	} else {
		fmt.Printf("Proxy Server    : STOPPED on %s:%d\n", cfg.Host, cfg.Port)
	}
	fmt.Println()
	handleAuthStatus()
}

func handleServiceCLI(args []string) {
	sub := "status"
	if len(args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(args[0]))
	}

	switch sub {
	case "status":
		handleServiceStatus()
	case "install":
		installService()
	case "uninstall", "remove":
		uninstallService()
	case "start":
		startService()
	case "stop":
		stopService()
	case "restart":
		stopService()
		time.Sleep(500 * time.Millisecond)
		startService()
	case "logs", "log":
		showServiceLogs()
	default:
		fmt.Fprintf(os.Stderr, "Unknown service subcommand %q\n", sub)
		printUsage()
		os.Exit(1)
	}
}

func installService() {
	cfg := config.Load()
	ensureCodexDesktopBridge(cfg)

	exePath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to resolve executable path: %v\n", err)
		os.Exit(1)
	}
	exePath, _ = filepath.EvalSymlinks(exePath)

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to resolve home directory: %v\n", err)
		os.Exit(1)
	}

	// Copy binary to /opt/homebrew/bin/comini if writable so comini is globally available in PATH
	targetBin := exePath
	brewBin := "/opt/homebrew/bin/comini"
	if exePath != brewBin {
		if data, readErr := os.ReadFile(exePath); readErr == nil {
			tmpBin := brewBin + ".tmp"
			if writeErr := os.WriteFile(tmpBin, data, 0755); writeErr == nil {
				if renameErr := os.Rename(tmpBin, brewBin); renameErr == nil {
					targetBin = brewBin
					fmt.Printf("✓ Installed global CLI binary at %s\n", brewBin)
				} else {
					_ = os.Remove(tmpBin)
				}
			}
		}
	}

	plistPath := launchAgentPlistPath()
	_ = os.MkdirAll(filepath.Dir(plistPath), 0755)
	logPath := logFilePath()
	_ = os.MkdirAll(filepath.Dir(logPath), 0755)

	plistContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>serve</string>
    </array>
    <key>WorkingDirectory</key>
    <string>%s</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>HOME</key>
        <string>%s</string>
        <key>PATH</key>
        <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`, LaunchAgentLabel, targetBin, home, home, logPath, logPath)

	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", uid, plistPath).Run()

	if err := os.WriteFile(plistPath, []byte(plistContent), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to write LaunchAgent plist: %v\n", err)
		os.Exit(1)
	}

	if out, err := exec.Command("launchctl", "bootstrap", uid, plistPath).CombinedOutput(); err != nil {
		// Fallback to launchctl load
		if out2, err2 := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput(); err2 != nil {
			fmt.Fprintf(os.Stderr, "❌ Failed to load LaunchAgent: %v (%s / %s)\n", err2, string(out), string(out2))
			os.Exit(1)
		}
	}

	fmt.Printf("✓ Installed and started LaunchAgent: %s\n", plistPath)
	fmt.Printf("✓ Logs writing to: %s\n", logPath)
}

func uninstallService() {
	plistPath := launchAgentPlistPath()
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", uid, plistPath).Run()
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "❌ Failed to remove %s: %v\n", plistPath, err)
		os.Exit(1)
	}
	fmt.Printf("✓ Stopped and removed LaunchAgent: %s\n", plistPath)
}

func startService() {
	plistPath := launchAgentPlistPath()
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		installService()
		return
	}
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootstrap", uid, plistPath).Run()
	_ = exec.Command("launchctl", "kickstart", "-k", uid+"/"+LaunchAgentLabel).Run()
	fmt.Println("✓ Comini background service started.")
}

func stopService() {
	plistPath := launchAgentPlistPath()
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", uid, plistPath).Run()
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	fmt.Println("✓ Comini background service stopped.")
}

func showServiceLogs() {
	logPath := logFilePath()
	data, err := os.ReadFile(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "No log file found at %s (%v)\n", logPath, err)
		return
	}
	lines := strings.Split(string(data), "\n")
	start := 0
	if len(lines) > 50 {
		start = len(lines) - 50
	}
	_, _ = io.WriteString(os.Stdout, strings.Join(lines[start:], "\n")+"\n")
}
