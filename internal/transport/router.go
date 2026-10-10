package transport

import (
	"strings"
	"comini/internal/domain"
)

type ResolvedModel struct {
	WireModelID   string
	ThinkingLevel string
}

// ResolveModelAndEffort maps client requested model and effort to CCA wire model and ThinkingLevel according to Antigravity specification.
func ResolveModelAndEffort(modelID string, effort string) ResolvedModel {
	m := strings.ToLower(strings.TrimSpace(modelID))
	m = strings.TrimPrefix(m, "google-antigravity/")
	m = strings.TrimPrefix(m, "agy/")

	e := strings.ToLower(strings.TrimSpace(effort))
	if e == "" {
		if strings.HasSuffix(m, "-low") {
			e = "low"
			m = strings.TrimSuffix(m, "-low")
		} else if strings.HasSuffix(m, "-medium") {
			e = "medium"
			m = strings.TrimSuffix(m, "-medium")
		} else if strings.HasSuffix(m, "-high") {
			e = "high"
			m = strings.TrimSuffix(m, "-high")
		} else {
			e = "high"
		}
	}

	thinkingLevel := domain.MapEffortToGemini(e)

	// 1. Direct wire model pass-through
	switch m {
	case "gemini-3.8-flash-high", "gemini-3.8-flash-medium", "gemini-3.8-flash-low",
		"gemini-3.7-flash-tiered", "gemini-pro-agent", "gemini-3.1-pro-low",
		"claude-sonnet-4-6", "claude-opus-4-6-thinking",
		"gemini-3.1-flash-image", "gpt-oss-120b-medium":
		return ResolvedModel{
			WireModelID:   m,
			ThinkingLevel: thinkingLevel,
		}
	}

	// 2. Claude models
	if strings.Contains(m, "claude") {
		if strings.Contains(m, "opus") {
			return ResolvedModel{
				WireModelID:   "claude-opus-4-6-thinking",
				ThinkingLevel: thinkingLevel,
			}
		}
		return ResolvedModel{
			WireModelID:   "claude-sonnet-4-6",
			ThinkingLevel: thinkingLevel,
		}
	}

	// 3. Gemini 3.8 Flash models (wire models encode effort directly: -low, -medium, -high)
	if strings.Contains(m, "3.8") {
		tier := "high"
		if e == "low" || e == "medium" {
			tier = e
		}
		return ResolvedModel{
			WireModelID:   "gemini-3.8-flash-" + tier,
			ThinkingLevel: "",
		}
	}

	// 4. Gemini Pro / Reasoner models (low effort -> gemini-3.1-pro-low, high/default -> gemini-pro-agent)
	if strings.Contains(m, "pro") || strings.Contains(m, "reasoner") {
		if e == "low" {
			return ResolvedModel{
				WireModelID:   "gemini-3.1-pro-low",
				ThinkingLevel: thinkingLevel,
			}
		}
		return ResolvedModel{
			WireModelID:   "gemini-pro-agent",
			ThinkingLevel: thinkingLevel,
		}
	}

	// 5. Gemini Flash-Image
	if strings.Contains(m, "image") {
		return ResolvedModel{
			WireModelID:   "gemini-3.1-flash-image",
			ThinkingLevel: thinkingLevel,
		}
	}

	// 6. Gemini 3.7 Flash models (wire models encode effort directly: -low, -medium, -high)
	tier := "high"
	if e == "low" || e == "medium" {
		tier = e
	}
	return ResolvedModel{
		WireModelID:   "gemini-3.7-flash-" + tier,
		ThinkingLevel: thinkingLevel,
	}
}
