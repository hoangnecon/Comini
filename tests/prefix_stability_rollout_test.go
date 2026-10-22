package tests

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"comini/internal/domain"
	"comini/internal/session"
)

// TestRolloutPrefixStability replays a real Codex rollout through the Gemini prompt builder and
// reports, for every consecutive pair of requests, whether the previous prompt is a byte-identical
// prefix of the next one. Any divergence is a guaranteed KV-cache miss from that point on.
//
//	COMINI_ROLLOUT=~/.codex/sessions/.../rollout-*.jsonl go test ./tests -run TestRolloutPrefixStability -v
//
// The measured cached_input_tokens from the rollout are printed next to each divergence so the
// builder's prefix stability can be compared with what the upstream really cached.
func TestRolloutPrefixStability(t *testing.T) {
	path := os.Getenv("COMINI_ROLLOUT")
	if path == "" {
		t.Skip("set COMINI_ROLLOUT=<rollout.jsonl> to run")
	}

	// Use a private copy of the signature store so replaying never writes to the real one.
	tmpHome := t.TempDir()
	if realHome, err := os.UserHomeDir(); err == nil {
		if data, err := os.ReadFile(filepath.Join(realHome, ".codex", "comini_signatures.json")); err == nil {
			_ = os.MkdirAll(filepath.Join(tmpHome, ".codex"), 0o755)
			_ = os.WriteFile(filepath.Join(tmpHome, ".codex", "comini_signatures.json"), data, 0o644)
		}
	}
	t.Setenv("HOME", tmpHome)
	store := session.NewSignatureStore(30 * 24 * time.Hour)

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	type usage struct {
		input, cached int
		at            time.Time
	}
	var items []domain.InputItem
	var types []string
	type boundary struct {
		upto int // request input = items[:upto]
		use  usage
		line int
	}
	var bounds []boundary

	isOutput := func(it domain.InputItem, typ string) bool {
		switch typ {
		case "reasoning", "function_call", "custom_tool_call", "image_generation_call", "tool_search_call", "web_search_call", "local_shell_call":
			return true
		case "message":
			return it.Role == "assistant"
		}
		return false
	}

	rd := bufio.NewReaderSize(f, 1<<20)
	line := 0
	for {
		raw, err := rd.ReadBytes('\n')
		if len(raw) > 0 {
			line++
			var env struct {
				Type      string          `json:"type"`
				Timestamp string          `json:"timestamp"`
				Payload   json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(raw, &env) == nil {
				switch env.Type {
				case "response_item":
					var it domain.InputItem
					var head struct {
						Type string `json:"type"`
					}
					_ = json.Unmarshal(env.Payload, &head)
					if json.Unmarshal(env.Payload, &it) == nil {
						items = append(items, it)
						types = append(types, head.Type)
					}
				case "token_usage_record":
					var tu struct {
						Usage struct {
							Input  int `json:"input_tokens"`
							Cached int `json:"cached_input_tokens"`
						} `json:"usage"`
					}
					_ = json.Unmarshal(env.Payload, &tu)
					j := len(items)
					for j > 0 && isOutput(items[j-1], types[j-1]) {
						j--
					}
					ts, _ := time.Parse(time.RFC3339Nano, env.Timestamp)
					bounds = append(bounds, boundary{upto: j, use: usage{tu.Usage.Input, tu.Usage.Cached, ts}, line: line})
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bounds) < 2 {
		t.Skipf("rollout has %d requests; need at least 2", len(bounds))
	}

	// Window of requests to analyse (default: the last 120) so the run stays short; progress is
	// printed to stderr every 10 requests.
	win := 120
	if v := os.Getenv("COMINI_ROLLOUT_WINDOW"); v != "" {
		fmt.Sscanf(v, "%d", &win)
	}
	if win > 0 && len(bounds) > win {
		bounds = bounds[len(bounds)-win:]
	}
	fmt.Fprintf(os.Stderr, "[prefix-replay] analysing %d requests from %s\n", len(bounds), filepath.Base(path))

	instr := "stable test instructions"
	build := func(upto int) []string {
		req := &domain.ResponsesRequest{Model: "gemini-4-argon", Instructions: &instr}
		body := session.BuildGeminiWireBodyWithItems(req, items[:upto], "replay_session", nil, "LOW", store)
		out := make([]string, len(body.Contents))
		for i, c := range body.Contents {
			b, _ := json.Marshal(c)
			out[i] = string(b)
		}
		return out
	}

	var prev []string
	stable, diverged := 0, 0
	missTokens, totalIn := 0, 0
	byReason := map[string]int{}
	var details []string
	var misses []string
	for k, b := range bounds {
		if k%10 == 0 {
			fmt.Fprintf(os.Stderr, "[prefix-replay] request %d/%d (history items=%d)\n", k, len(bounds), b.upto)
		}
		cur := build(b.upto)
		totalIn += b.use.input
		if k > 0 && len(prev) > 0 {
			// The previous request's last turn is allowed to differ only by being extended.
			n := len(prev)
			div := -1
			for i := 0; i < n && i < len(cur); i++ {
				if prev[i] != cur[i] {
					div = i
					break
				}
			}
			if div == -1 && len(cur) < n {
				div = len(cur)
			}
			if div == -1 {
				stable++
			} else {
				diverged++
				// A divergence only inside the final turn of the previous prompt is merging of
				// consecutive same-role turns (a normal append) and does not break the cache.
				reason := "mid-history change"
				if div >= n-1 {
					reason = "last-turn extend (benign)"
					diverged--
					stable++
				} else {
					missTokens += b.use.input - b.use.cached
				}
				byReason[reason]++
				if reason != "last-turn extend (benign)" && len(details) < 12 {
					a, c := prev[div], cur[div]
					p := 0
					for p < len(a) && p < len(c) && a[p] == c[p] {
						p++
					}
					lo := p - 40
					if lo < 0 {
						lo = 0
					}
					hiA, hiC := p+90, p+90
					if hiA > len(a) {
						hiA = len(a)
					}
					if hiC > len(c) {
						hiC = len(c)
					}
					details = append(details, fmt.Sprintf("req#%d (rollout line %d) diverges at content %d/%d | measured input=%d cached=%d\n    was: …%s\n    now: …%s",
						k, b.line, div, n, b.use.input, b.use.cached, strings.ReplaceAll(a[lo:hiA], "\n", "\\n"), strings.ReplaceAll(c[lo:hiC], "\n", "\\n")))
				}
			}
		}
		if k > 0 && b.use.input >= 50000 && b.use.cached*2 < b.use.input {
			gap := b.use.at.Sub(bounds[k-1].use.at).Seconds()
			misses = append(misses, fmt.Sprintf("req#%d line %d: input=%d cached=%d (%.0f%%) gap-since-prev-response=%.0fs", k, b.line, b.use.input, b.use.cached, 100*float64(b.use.cached)/float64(b.use.input), gap))
		}
		prev = cur
	}

	keys := make([]string, 0, len(byReason))
	for k := range byReason {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t.Logf("rollout %s: %d requests, prompt-prefix stable=%d diverged(mid-history)=%d", filepath.Base(path), len(bounds), stable, diverged)
	for _, k := range keys {
		t.Logf("  %-28s %d", k, byReason[k])
	}
	t.Logf("  measured uncached input tokens on diverged requests: %d of total input %d", missTokens, totalIn)
	t.Logf("  measured upstream cache misses (<50%% hit, input>=50k): %d", len(misses))
	for _, m := range misses {
		t.Log("    " + m)
	}
	for _, d := range details {
		t.Log("  " + d)
	}
}
