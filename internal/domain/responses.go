package domain

import "encoding/json"

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
}

func (i *InputItem) UnmarshalJSON(data []byte) error {
	type Alias InputItem
	aux := &struct {
		*Alias
		AltCallID           string          `json:"callId"`
		AltAggregatedOutput string          `json:"aggregatedOutput"`
		AltThreadGoal       string          `json:"thread_goal"`
		AltCmd              string          `json:"cmd"`
		RawArgs             json.RawMessage `json:"arguments"`
		RawInput            json.RawMessage `json:"input"`
	}{
		Alias: (*Alias)(i),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
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
