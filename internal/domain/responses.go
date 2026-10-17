package domain

import (
	"encoding/json"
	"strings"
)

type ResponsesRequest struct {
	Model              string                 `json:"model"`
	Input              json.RawMessage        `json:"input,omitempty"`
	Instructions       *string                `json:"instructions,omitempty"`
	Tools              []ToolDefinition       `json:"tools,omitempty"`
	ToolChoice         any                    `json:"tool_choice,omitempty"`
	MaxOutputTokens    *int                   `json:"max_output_tokens,omitempty"`
	Temperature        *float64               `json:"temperature,omitempty"`
	TopP               *float64               `json:"top_p,omitempty"`
	Stream             *bool                  `json:"stream,omitempty"`
	Reasoning          *ReasoningConfig       `json:"reasoning,omitempty"`
	ReasoningEffort    string                 `json:"reasoning_effort,omitempty"`
	PreviousResponseID string                 `json:"previous_response_id,omitempty"`
	Store              *bool                  `json:"store,omitempty"`
}

type ReasoningConfig struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type ToolDefinition struct {
	Type        string                 `json:"type"`
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]any         `json:"parameters,omitempty"`
	Strict      bool                   `json:"strict,omitempty"`
	Tools       []ToolDefinition       `json:"tools,omitempty"`
}

type ToolMeta struct {
	OriginalName  string
	Namespace     string
	CanonicalName string
	IsCustom      bool
	IsToolSearch  bool
	IsImageGen    bool
}

type InputItem struct {
	ID               string           `json:"id,omitempty"`
	CallID           string           `json:"call_id,omitempty"`
	Type             string           `json:"type,omitempty"`
	Role             string           `json:"role,omitempty"`
	Name             string           `json:"name,omitempty"`
	Namespace        string           `json:"namespace,omitempty"`
	Arguments        string           `json:"arguments,omitempty"`
	Input            string           `json:"input,omitempty"`
	Output           json.RawMessage  `json:"output,omitempty"`
	Content          json.RawMessage  `json:"content,omitempty"`
	Payload          json.RawMessage  `json:"payload,omitempty"`
	Tools            []ToolDefinition `json:"tools,omitempty"`
	Text             string           `json:"text,omitempty"`
	Path             string           `json:"path,omitempty"`
	Server           string           `json:"server,omitempty"`
	Tool             string           `json:"tool,omitempty"`
	Command          string           `json:"command,omitempty"`
	AggregatedOutput string           `json:"aggregated_output,omitempty"`
	Stdout           string           `json:"stdout,omitempty"`
	Stderr           string           `json:"stderr,omitempty"`
	Goal             string           `json:"goal,omitempty"`
	Summary          json.RawMessage  `json:"summary,omitempty"`
	RevisedPrompt    string           `json:"revised_prompt,omitempty"`
	Result           string           `json:"result,omitempty"`
	SavedPath        string           `json:"saved_path,omitempty"`
	Prompt           string           `json:"prompt,omitempty"`
	Status           string           `json:"status,omitempty"`
	Failure          string           `json:"failure,omitempty"`
	AspectRatio      string           `json:"aspect_ratio,omitempty"`
	Effort           string           `json:"effort,omitempty"`
}

func (i *InputItem) UnmarshalJSON(data []byte) error {
	type Alias InputItem
	aux := &struct {
		*Alias
		AltCallID           string          `json:"callId"`
		AltAggregatedOutput string          `json:"aggregatedOutput"`
		AltThreadGoal       string          `json:"thread_goal"`
		AltCmd              string          `json:"cmd"`
		AltRevisedPrompt    string          `json:"revisedPrompt"`
		AltSavedPath        string          `json:"savedPath"`
		AltStatus           string          `json:"status"`
		AltFailure          string          `json:"failure"`
		AltAspectRatio      string          `json:"aspectRatio"`
		RawArgs             json.RawMessage `json:"arguments"`
		RawInput            json.RawMessage `json:"input"`
	}{
		Alias: (*Alias)(i),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if i.Status == "" && aux.AltStatus != "" {
		i.Status = aux.AltStatus
	}
	if i.Failure == "" && aux.AltFailure != "" {
		i.Failure = aux.AltFailure
	}
	if i.AspectRatio == "" && aux.AltAspectRatio != "" {
		i.AspectRatio = aux.AltAspectRatio
	}
	if i.CallID == "" && aux.AltCallID != "" {
		i.CallID = aux.AltCallID
	}
	if i.AggregatedOutput == "" && aux.AltAggregatedOutput != "" {
		i.AggregatedOutput = aux.AltAggregatedOutput
	}
	if i.Goal == "" && aux.AltThreadGoal != "" {
		i.Goal = aux.AltThreadGoal
	}
	if i.Command == "" && aux.AltCmd != "" {
		i.Command = aux.AltCmd
	}
	if i.RevisedPrompt == "" && aux.AltRevisedPrompt != "" {
		i.RevisedPrompt = aux.AltRevisedPrompt
	}
	if i.SavedPath == "" && aux.AltSavedPath != "" {
		i.SavedPath = aux.AltSavedPath
	}
	if i.Arguments == "" && len(aux.RawArgs) > 0 {
		var s string
		if err := json.Unmarshal(aux.RawArgs, &s); err == nil {
			i.Arguments = s
		} else {
			i.Arguments = string(aux.RawArgs)
		}
	}
	if i.Input == "" && len(aux.RawInput) > 0 {
		var s string
		if err := json.Unmarshal(aux.RawInput, &s); err == nil {
			i.Input = s
		} else {
			i.Input = string(aux.RawInput)
		}
	}

	// Immediate Heap Optimization: Strip large image dataURL / base64 from Result.
	// Codex Desktop stores 1MB-1.5MB base64 JPEG in `result` for each image_generation_call.
	// Comini uses SavedPath, Prompt, and RevisedPrompt; Result is never transmitted to Gemini.
	// Clearing it here frees ~50MB-150MB of RAM per turn immediately upon unmarshaling.
	if len(i.Result) > 500 && (strings.HasPrefix(i.Result, "data:image/") ||
		strings.HasPrefix(i.Result, "/9j/") ||
		strings.HasPrefix(i.Result, "iVBORw0KGgo") ||
		strings.HasPrefix(i.Result, "UklGR") ||
		strings.HasPrefix(i.Result, "R0lGOD")) {
		i.Result = ""
	}

	return nil
}

type ContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *ImageURLPart   `json:"image_url,omitempty"`
}

type ImageURLPart struct {
	URL string `json:"url"`
}

type ModelCatalogEntry struct {
	ID                     string           `json:"id"`
	Object                 string           `json:"object"`
	Created                int64            `json:"created"`
	OwnedBy                string           `json:"owned_by"`
	SupportedReasoningLevels []ReasoningLevel `json:"supported_reasoning_levels,omitempty"`
	ContextWindow          int              `json:"context_window,omitempty"`
	MaxContextWindow       int              `json:"max_context_window,omitempty"`
	InputModalities        []string         `json:"input_modalities,omitempty"`
}

type ModelCatalogResponse struct {
	Object string              `json:"object"`
	Data   []ModelCatalogEntry `json:"data"`
}
