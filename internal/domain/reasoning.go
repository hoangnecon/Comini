package domain

// ReasoningLevel represents a single rung in Codex catalog.
type ReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

// DefaultReasoningLadder declares the 3 rungs (low, medium, high).
var DefaultReasoningLadder = []ReasoningLevel{
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
