package domain

// ReasoningLevel represents a single rung in Codex catalog.
type ReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

// DeepSeekReasoningLadder declares the 3 rungs (low, high, ultra)
// where low & high are visible on the UI slider, and ultra is reserved for agent task delegation.
var DeepSeekReasoningLadder = []ReasoningLevel{
	{Effort: "low", Description: "Fast responses with lighter reasoning"},
	{Effort: "medium", Description: "Balanced reasoning for everyday tasks"},
	{Effort: "high", Description: "Greater reasoning depth for complex problems"},
}

// MapEffortToGemini maps the requested effort level to Google CCA ThinkingLevel enum.
func MapEffortToGemini(effort string) string {
	switch effort {
	case "low", "minimal":
		return "low"
	case "medium":
		return "medium"
	case "high", "xhigh", "max", "ultra":
		return "high"
	default:
		return "high"
	}
}
