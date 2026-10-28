package harness

import (
	"regexp"
	"strings"
)

var (
	codexGPT5Regex       = regexp.MustCompile(`(?i)(?:You are Codex,\s*(?:a coding agent|an agent)\s*based on\s*[a-z0-9_.-]+(?:\.[0-9]+)*\.|based on\s*GPT-[0-9a-z_.-]+|You are Codex,\s*a coding agent\.)`)
	codexBrandRegex      = regexp.MustCompile(`(?i)# Codex desktop context`)
	claudeSDKRegex       = regexp.MustCompile(`(?s)You are Claude, a helpful AI assistant.*?(?:\n\n|$)`)
	trailingNoticeRegex  = regexp.MustCompile(`(?s)\n\n(?:---\n)?(?:Notice|Reminder|System Instruction): Please maintain.*?$`)
	billingHeaderRegex   = regexp.MustCompile(`(?m)^x-anthropic-billing-header:[^\n]*\n*`)
	claudeAgentSdkRegex  = regexp.MustCompile(`(?s)You are a Claude agent, built on Anthropic's Claude Agent SDK\..*?(?:\n\n|$)`)
	modelSwitchRegex     = regexp.MustCompile(`(?s)<model_switch>.*?</model_switch>`)
	generatedImageRegex  = regexp.MustCompile(`(?s)\[Generated Image based on prompt:\s*".*?"(?:\.\s*Saved to:\s*.*?)?\]`)
	leakedObsRegex       = regexp.MustCompile(`(?s)\[Observation:\s*.*?\]`)
	imageOmittedRegex    = regexp.MustCompile(`(?s)\[Image omitted.*?\]`)
)

const (
	neutralIdentity          = "You are an expert AI software engineering assistant."
	googleBrevityInstruction = "Output style for this session:\n- While you are still working (between tool calls), keep any text you emit to a single short line; do not narrate at length.\n- Do detailed reasoning internally, not as visible intermediate output.\n- Prefer taking the next tool action over explaining; keep calling tools until the task is complete.\n- This applies only to intermediate progress text. Your final answer after the work is done is exempt: write it in full and at whatever length the task requires."
	katexRenderingGuidance   = "KaTeX rendering environment contract:\n- Single-dollar inline math is disabled in client UI (`singleDollarTextMath: false`). NEVER use single dollar `$formula$` for math; single dollars are treated as plain text. Always use `\\(...\\)` or inline `$$formula$$` (e.g. `\\(C_{\\mathrm{in}}\\)` or `$$C_{\\mathrm{in}}$$`).\n- Display / block math must be placed on its own isolated lines with blank lines before and after, using `\\[...\\]` or `$$\\n...\\n$$`.\n- Inside math delimiters, never include markdown formatting (such as `**`, `*`, or bullet lists `1.`). Wrap textual labels or words in `\\text{...}` or `\\mathrm{...}` (e.g. `\\mathrm{fine}`, `\\text{coarse}`).\n- Never let math delimiters cross paragraphs or list items. Close delimiters cleanly before writing prose.\n- Escape literal dollar signs as `\\$` (e.g. `\\$100`) or wrap shell variables in backticks (`$PATH`, `$HOME`)."
)

// SanitizeMessageText cleans model_switch blocks and leaks from historical message text.
func SanitizeMessageText(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	cleaned := modelSwitchRegex.ReplaceAllString(text, "[System: Model switch acknowledged; conversation context continues.]")
	cleaned = billingHeaderRegex.ReplaceAllString(cleaned, "")
	cleaned = claudeAgentSdkRegex.ReplaceAllString(cleaned, "")
	cleaned = codexGPT5Regex.ReplaceAllString(cleaned, neutralIdentity)
	cleaned = codexBrandRegex.ReplaceAllString(cleaned, "# Desktop client context")
	cleaned = generatedImageRegex.ReplaceAllString(cleaned, "")
	cleaned = leakedObsRegex.ReplaceAllString(cleaned, "")
	cleaned = imageOmittedRegex.ReplaceAllString(cleaned, "")
	return strings.TrimSpace(cleaned)
}

// BuildToolCatalogNudge constructs explicit tool catalog contract steering Gemini to use native tools.
// Kept static (independent of dynamic toolNames list) so SystemInstruction never mutates mid-session when tool_search discovers tools, preserving 100% of the prefix KV cache.
func BuildToolCatalogNudge(toolNames []string) string {
	if len(toolNames) == 0 {
		return ""
	}
	nudge := "Tool contract: use the current declared tool catalog as ground truth. Call only declared tool names with their declared schema argument keys."
	for _, n := range toolNames {
		if n == "image_generation" || n == "generate_image" {
			// Older replayed history may show image calls with only a prompt; make the full schema explicit.
			nudge += " image_generation: whenever the user provides or refers to an existing image, pass its local file path(s) in image_paths (max 3) to edit or use it as a reference, even if earlier calls in the history only show a prompt."
			break
		}
	}
	return nudge
}

// SanitizeSystemInstruction preserves the full system prompt while removing any trailing Claude artifacts that trigger upstream rejections.
func SanitizeSystemInstruction(instructions *string, toolNames []string) *string {
	var baseText string
	if instructions != nil {
		baseText = *instructions
	}

	cleaned := billingHeaderRegex.ReplaceAllString(baseText, "")
	cleaned = claudeAgentSdkRegex.ReplaceAllString(cleaned, "")
	cleaned = claudeSDKRegex.ReplaceAllString(cleaned, "")
	cleaned = trailingNoticeRegex.ReplaceAllString(cleaned, "")
	cleaned = codexGPT5Regex.ReplaceAllString(cleaned, neutralIdentity)
	cleaned = codexBrandRegex.ReplaceAllString(cleaned, "# Desktop client context")
	cleaned = strings.TrimSpace(cleaned)

	var parts []string
	if len(cleaned) > 0 {
		parts = append(parts, cleaned)
	}

	nudge := BuildToolCatalogNudge(toolNames)
	if nudge != "" {
		parts = append(parts, nudge)
	}

	if !strings.Contains(cleaned, "singleDollarTextMath") && !strings.Contains(cleaned, "KaTeX") {
		parts = append(parts, katexRenderingGuidance)
	}

	if len(parts) == 0 {
		return nil
	}

	res := strings.Join(parts, "\n\n")
	return &res
}
