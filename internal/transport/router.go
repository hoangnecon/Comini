package transport

import (
	"strings"
	"comini/internal/domain"
)

type ResolvedModel struct {
	WireModelID     string
	ThinkingLevel   string
	ThinkingBudget  *int
	MaxOutputTokens int
}

func intPtr(v int) *int {
	return &v
}

// applyAGY2ModelDetails populates ThinkingLevel, ThinkingBudget, and MaxOutputTokens
// matching Antigravity 2.0 Hub's BuildGenerateContentRequest (0x101992a6c) and live v1internal:fetchAvailableModels.
func applyAGY2ModelDetails(wireModelID string, fallbackThinkingLevel string) ResolvedModel {
	switch wireModelID {
	case "gemini-4-argon-low":
		return ResolvedModel{WireModelID: wireModelID, ThinkingLevel: "LOW", MaxOutputTokens: 65536}
	case "gemini-4-argon-medium":
		return ResolvedModel{WireModelID: wireModelID, ThinkingLevel: "MEDIUM", MaxOutputTokens: 65536}
	case "gemini-4-argon-high":
		return ResolvedModel{WireModelID: wireModelID, ThinkingLevel: "HIGH", MaxOutputTokens: 65536}

	case "gemini-3.8-flash-low":
		return ResolvedModel{WireModelID: wireModelID, ThinkingLevel: "", ThinkingBudget: intPtr(1000), MaxOutputTokens: 65536}
	case "gemini-3.8-flash-medium":
		return ResolvedModel{WireModelID: wireModelID, ThinkingLevel: "", ThinkingBudget: intPtr(4000), MaxOutputTokens: 65536}
	case "gemini-3.8-flash-high":
		return ResolvedModel{WireModelID: wireModelID, ThinkingLevel: "", ThinkingBudget: intPtr(-1), MaxOutputTokens: 65536}

	case "claude-opus-5-5-low", "claude-sonnet-5-5-low":
		return ResolvedModel{WireModelID: wireModelID, ThinkingLevel: "LOW", MaxOutputTokens: 128000}
	case "claude-opus-5-5-medium", "claude-sonnet-5-5-medium":
		return ResolvedModel{WireModelID: wireModelID, ThinkingLevel: "MEDIUM", MaxOutputTokens: 128000}
	case "claude-opus-5-5-high", "claude-sonnet-5-5-high":
		return ResolvedModel{WireModelID: wireModelID, ThinkingLevel: "HIGH", MaxOutputTokens: 128000}

	case "claude-opus-4-6-thinking":
		return ResolvedModel{WireModelID: wireModelID, ThinkingLevel: "", ThinkingBudget: intPtr(10240), MaxOutputTokens: 64000}
	case "claude-sonnet-4-6":
		return ResolvedModel{WireModelID: wireModelID, ThinkingLevel: "", MaxOutputTokens: 64000}

	case "gpt-oss-120b-medium":
		return ResolvedModel{WireModelID: wireModelID, ThinkingLevel: "", MaxOutputTokens: 16384}

	default:
		return ResolvedModel{
			WireModelID:     wireModelID,
			ThinkingLevel:   fallbackThinkingLevel,
			MaxOutputTokens: 65536,
		}
	}
}

// ResolveModelAndEffort maps client requested model and effort to CCA wire model and ThinkingConfig according to Antigravity 2.0 Hub specification.
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
	case "gemini-4-argon-high", "gemini-4-argon-medium", "gemini-4-argon-low",
		"gemini-3.8-flash-high", "gemini-3.8-flash-medium", "gemini-3.8-flash-low",
		"gemini-3.7-flash-tiered", "gemini-pro-agent", "gemini-3.1-pro-low",
		"claude-sonnet-5-5-low", "claude-sonnet-5-5-medium", "claude-sonnet-5-5-high",
		"claude-opus-5-5-low", "claude-opus-5-5-medium", "claude-opus-5-5-high",
		"claude-sonnet-4-6", "claude-opus-4-6-thinking",
		"gemini-3.1-flash-image", "gpt-oss-120b-medium":
		return applyAGY2ModelDetails(m, thinkingLevel)
	}

	// 2. Gemini 4 Argon models (wire models encode effort directly: -low, -medium, -high)
	// Exact 1:1 3-rung mapping matching Antigravity 2.0 Hub:
	// - low    -> gemini-4-argon-low    (MODEL_PLACEHOLDER_M419, thinkingLevel="LOW", maxOutputTokens=65536)
	// - medium -> gemini-4-argon-medium (MODEL_PLACEHOLDER_M418, thinkingLevel="MEDIUM", maxOutputTokens=65536)
	// - high   -> gemini-4-argon-high   (MODEL_PLACEHOLDER_M417, thinkingLevel="HIGH", maxOutputTokens=65536)
	if strings.Contains(m, "argon") || strings.Contains(m, "gemini-4") {
		tier := "high"
		if e == "low" || e == "medium" {
			tier = e
		}
		return applyAGY2ModelDetails("gemini-4-argon-"+tier, thinkingLevel)
	}

	// 3. Claude models
	if strings.Contains(m, "claude") {
		if strings.Contains(m, "4-6") || strings.Contains(m, "4.6") {
			if strings.Contains(m, "opus") {
				return applyAGY2ModelDetails("claude-opus-4-6-thinking", thinkingLevel)
			}
			return applyAGY2ModelDetails("claude-sonnet-4-6", thinkingLevel)
		}
		tier := "high"
		if e == "low" || e == "medium" {
			tier = e
		}
		if strings.Contains(m, "opus") {
			return applyAGY2ModelDetails("claude-opus-5-5-"+tier, thinkingLevel)
		}
		return applyAGY2ModelDetails("claude-sonnet-5-5-"+tier, thinkingLevel)
	}

	// 4. Gemini 3.8 Flash models (wire models encode effort directly: -low, -medium, -high)
	if strings.Contains(m, "3.8") {
		tier := "high"
		if e == "low" || e == "medium" {
			tier = e
		}
		return applyAGY2ModelDetails("gemini-3.8-flash-"+tier, thinkingLevel)
	}

	// 5. Gemini Pro / Reasoner models (low effort -> gemini-3.1-pro-low, high/default -> gemini-pro-agent)
	if strings.Contains(m, "pro") || strings.Contains(m, "reasoner") {
		if e == "low" {
			return applyAGY2ModelDetails("gemini-3.1-pro-low", thinkingLevel)
		}
		return applyAGY2ModelDetails("gemini-pro-agent", thinkingLevel)
	}

	// 6. Gemini Flash-Image
	if strings.Contains(m, "image") {
		return applyAGY2ModelDetails("gemini-3.1-flash-image", thinkingLevel)
	}

	// 7. Gemini 3.7 Flash models (Google CCA migrated 3.7 to 3.8 Flash; 3.7 returns 404 upstream)
	if strings.Contains(m, "3.7") {
		tier := "high"
		if e == "low" || e == "medium" {
			tier = e
		}
		return applyAGY2ModelDetails("gemini-3.8-flash-"+tier, thinkingLevel)
	}

	// 8. Default Flash models (all general flash requests map to Gemini 3.8 Flash)
	tier := "high"
	if e == "low" || e == "medium" {
		tier = e
	}
	return applyAGY2ModelDetails("gemini-3.8-flash-"+tier, thinkingLevel)
}
