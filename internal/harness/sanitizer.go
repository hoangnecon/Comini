package harness

import (
	"regexp"
	"strings"
)

var (
	codexGPT5Regex       = regexp.MustCompile(`(?i)(?:You are Codex,\s*(?:a coding agent|an agent)\s*based on\s*[a-z0-9_.-]+(?:\.[0-9]+)*\.|based on\s*GPT-[0-9a-z_.-]+)`)
	claudeSDKRegex       = regexp.MustCompile(`(?s)You are Claude, a helpful AI assistant.*?(?:\n\n|$)`)
	trailingNoticeRegex  = regexp.MustCompile(`(?s)\n\n(?:---\n)?(?:Notice|Reminder|System Instruction): Please maintain.*?$`)
	billingHeaderRegex   = regexp.MustCompile(`(?m)^x-anthropic-billing-header:[^\n]*\n*`)
	claudeAgentSdkRegex  = regexp.MustCompile(`(?s)You are a Claude agent, built on Anthropic's Claude Agent SDK\..*?(?:\n\n|$)`)
	modelSwitchRegex     = regexp.MustCompile(`(?s)<model_switch>.*?</model_switch>`)
)

const (
	neutralIdentity          = "You are Codex, a coding agent."
	googleBrevityInstruction = "Output style for this session:\n- While you are still working (between tool calls), keep any text you emit to a single short line; do not narrate at length.\n- Do detailed reasoning internally, not as visible intermediate output.\n- Prefer taking the next tool action over explaining; keep calling tools until the task is complete.\n- This applies only to intermediate progress text. Your final answer after the work is done is exempt: write it in full and at whatever length the task requires."
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
	return strings.TrimSpace(cleaned)
}

// BuildToolCatalogNudge constructs explicit tool catalog contract steering Gemini to use native tools.
func BuildToolCatalogNudge(toolNames []string) string {
	if len(toolNames) == 0 {
		return ""
	}
	quoted := make([]string, len(toolNames))
	for i, name := range toolNames {
		quoted[i] = "`" + name + "`"
	}
	return "Tool contract: use the current tool catalog as ground truth. Valid tool names for this turn are exactly " +
		strings.Join(quoted, ", ") +
		". Call only listed names with their listed argument keys. When modifying, updating, or creating files, always call the `apply_patch` tool with the proper patch format instead of running shell write commands."
}

// SanitizeSystemInstruction preserves the full system prompt while removing any trailing Claude artifacts that trigger upstream rejections.
func SanitizeSystemInstruction(instructions *string, toolNames []string) *string {
	if instructions == nil {
		return nil
	}
	baseText := *instructions
	if strings.TrimSpace(baseText) == "" {
		return nil
	}

	cleaned := billingHeaderRegex.ReplaceAllString(baseText, "")
	cleaned = claudeAgentSdkRegex.ReplaceAllString(cleaned, "")
	cleaned = claudeSDKRegex.ReplaceAllString(cleaned, "")
	cleaned = trailingNoticeRegex.ReplaceAllString(cleaned, "")
	cleaned = codexGPT5Regex.ReplaceAllString(cleaned, neutralIdentity)
	cleaned = strings.TrimSpace(cleaned)

	var parts []string
	if len(cleaned) > 0 {
		parts = append(parts, cleaned)
	}

	nudge := BuildToolCatalogNudge(toolNames)
	if nudge != "" {
		parts = append(parts, nudge)
	}

	res := strings.Join(parts, "\n\n")
	return &res
}
