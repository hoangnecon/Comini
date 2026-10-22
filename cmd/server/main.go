package main

import (
	"context"
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
	Version           = "2.1.0"
	LaunchAgentLabel  = "com.comini.proxy"
	DefaultLogRelPath = ".codex/comini.log"
)

func main() {
	args := os.Args[1:]
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
		fmt.Printf("Comini v%s (Pure Responses API Gateway for Google Cloud Code Antigravity)\n", Version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command %q\n\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Printf(`Comini v%s — Pure Responses API Gateway for Google Cloud Code Antigravity

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

func runServer() {
	cfg := config.Load()

	ccaClient := transport.NewCCAClient(cfg.CCABaseURL, cfg.UserAgent)
	store := session.DefaultSignatureStore
	handlers := server.NewHandlers(cfg, ccaClient, store)
	router := server.NewRouter(handlers)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	st := transport.DefaultAuthManager.Status()
	log.Printf("[Comini v%s] Responses API Gateway listening on http://%s", Version, addr)
	log.Printf("[Comini v%s] Upstream CCA: %s | Default Model: %s", Version, cfg.CCABaseURL, cfg.DefaultModel)
	if st.Authenticated {
		log.Printf("[Comini Auth] Active Account: %s (project: %s, source: %s)", st.Email, st.ProjectID, st.Source)
	} else {
		log.Printf("[Comini Auth] ⚠️ No stored OAuth token found. Run 'comini auth login' to authenticate.")
	}

	s := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 30 * time.Second,
	}

	if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
		if target == "" || strings.EqualFold(target, "all") {
			fmt.Println("✓ Logged out and cleared all accounts.")
		} else {
			fmt.Printf("✓ Removed account %s\n", target)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown auth subcommand %q\n", sub)
		printUsage()
		os.Exit(1)
	}
}

func handleAuthLogin() {
	fmt.Println("Initiating Google Antigravity OAuth2 flow...")
	fmt.Println("A browser window will open shortly. Please grant permissions to continue.")
	acc, err := transport.DefaultAuthManager.LoginInteractive(context.Background(), true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Authentication failed: %v\n", err)
		os.Exit(1)
	}

	email := acc.Credential.Email
	if email == "" {
		email = acc.LoginID
	}
	fmt.Printf("\n✓ Successfully authenticated with Google Antigravity!\n")
	fmt.Printf("  Account    : %s\n", email)
	fmt.Printf("  Account ID : %s\n", acc.ID)
	fmt.Printf("  Project ID : %s\n", acc.Credential.ProjectID)
	fmt.Printf("  Saved to   : %s\n\n", transport.DefaultAuthFilePath())
}

func handleAuthStatus() {
	st := transport.DefaultAuthManager.Status()
	fmt.Println("=== Comini Authentication Status ===")
	fmt.Printf("Auth File       : %s\n", st.AuthFilePath)
	fmt.Printf("Authenticated   : %v\n", st.Authenticated)
	if st.Authenticated {
		fmt.Printf("Active Email    : %s\n", st.Email)
		fmt.Printf("Active ID       : %s\n", st.ActiveAccountID)
		fmt.Printf("Project ID      : %s\n", st.ProjectID)
		fmt.Printf("Source          : %s\n", st.Source)
		fmt.Printf("Refresh Token   : %v\n", st.HasRefreshToken)
		if !st.ExpiresAt.IsZero() {
			fmt.Printf("Token Expires   : %s (in %ds)\n", st.ExpiresAt.Local().Format(time.RFC3339), st.ExpiresInSec)
		}
	} else {
		fmt.Println("Status          : No active credentials found. Run 'comini auth login'.")
	}

	if st.TotalAccounts > 0 {
		fmt.Printf("\nStored Accounts (%d):\n", st.TotalAccounts)
		for _, a := range st.Accounts {
			mark := " "
			if a.ID == st.ActiveAccountID {
				mark = "*"
			}
			email := a.Credential.Email
			if email == "" {
				email = a.LoginID
			}
			fmt.Printf("  [%s] %s (id: %s, project: %s)\n", mark, email, a.ID, a.Credential.ProjectID)
		}
	}
}

func handleAuthRefresh() {
	fmt.Println("Refreshing active Google Antigravity OAuth2 access token...")
	tok, proj, err := transport.DefaultAuthManager.ForceRefresh()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Token refresh failed: %v\n", err)
		os.Exit(1)
	}
	st := transport.DefaultAuthManager.Status()
	fmt.Printf("✓ Successfully refreshed token for %s (project: %s)\n", st.Email, proj)
	if len(tok) > 12 {
		fmt.Printf("  New Access Token: %s...%s\n", tok[:8], tok[len(tok)-4:])
	}
	fmt.Printf("  Expires At      : %s\n", st.ExpiresAt.Local().Format(time.RFC3339))
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
	fmt.Printf("=== Comini LaunchAgent Service Status (%s) ===\n", LaunchAgentLabel)
	plist := launchAgentPlistPath()
	fmt.Printf("Plist Path : %s\n", plist)

	if _, err := os.Stat(plist); os.IsNotExist(err) {
		fmt.Println("Installed  : false (run 'comini service install' to install)")
		return
	}
	fmt.Println("Installed  : true")

	uid := fmt.Sprintf("gui/%d", os.Getuid())
	out, err := exec.Command("launchctl", "print", uid+"/"+LaunchAgentLabel).CombinedOutput()
	if err != nil {
		fmt.Printf("Running    : false (%s)\n", strings.TrimSpace(string(out)))
	} else {
		state := "unknown"
		lines := strings.Split(string(out), "\n")
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "state =") {
				state = strings.TrimPrefix(l, "state = ")
				break
			}
		}
		fmt.Printf("Running    : true (state: %s)\n", state)
	}

	cfg := config.Load()
	addr := fmt.Sprintf("http://127.0.0.1:%d/health", cfg.Port)
	client := &http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get(addr); err == nil {
		fmt.Printf("Port %-6d: ACTIVE (HTTP %d)\n", cfg.Port, resp.StatusCode)
		_ = resp.Body.Close()
	} else {
		fmt.Printf("Port %-6d: NOT RESPONDING (%v)\n", cfg.Port, err)
	}
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
