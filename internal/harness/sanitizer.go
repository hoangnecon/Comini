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
	katexRenderingGuidance   = "- Mathematical expressions and LaTeX typesetting (KaTeX environment):\n  * The client application renders markdown math with KaTeX, and single-dollar inline math is disabled (singleDollarTextMath: false). Math is typeset only inside explicit, supported delimiters; everything else is displayed literally.\n  * Every piece of mathematical content must be written as KaTeX inside a supported delimiter, however small it is or however casually it appears in a sentence. This covers formulas, variables, symbols, operators, relations, Greek letters, subscripts, superscripts, arrows, and set or logic notation. Never leave a LaTeX command outside a delimiter.\n  * Single dollar signs never produce math, so never use them as math delimiters in any context. Their content is printed as plain text.\n  * Inline math: use \\(...\\) or $$...$$ on the same line as the surrounding text. Display math: place the expression on its own dedicated lines with blank lines before and after, using \\[...\\] or $$ fences.\n  * Math vs text boundary: inside math delimiters write only valid KaTeX, and never put markdown formatting inside them. Wrap any textual labels or words in \\text{...} or \\mathrm{...}.\n  * Delimiter containment: close every delimiter in the same paragraph, list item, or table cell where it was opened. Never let one cross paragraphs or regular text, and never nest delimiters.\n  * Literal dollar signs: code spans and code blocks are never typeset, so write raw source there. Escape a literal dollar sign in prose as \\$ or place it in a code span, and never let non-math uses of the dollar sign form an unescaped pair.\n  * Before sending a response, check that no formula sits outside a delimiter and that every delimiter is balanced."
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
