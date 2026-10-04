package domain

import "encoding/json"

type GeminiPart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	FunctionCall     *GeminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`
	InlineData       *GeminiInlineData       `json:"inlineData,omitempty"`
}

func (p *GeminiPart) UnmarshalJSON(data []byte) error {
	type Alias GeminiPart
	aux := struct {
		*Alias
		ThoughtSigSnake string            `json:"thought_signature,omitempty"`
		InlineDataSnake *GeminiInlineData `json:"inline_data,omitempty"`
	}{
		Alias: (*Alias)(p),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if p.ThoughtSignature == "" && aux.ThoughtSigSnake != "" {
		p.ThoughtSignature = aux.ThoughtSigSnake
	}
	if p.InlineData == nil && aux.InlineDataSnake != nil {
		p.InlineData = aux.InlineDataSnake
	}
	return nil
}

type GeminiFunctionCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type GeminiFunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type GeminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

func (d *GeminiInlineData) UnmarshalJSON(data []byte) error {
	type Alias GeminiInlineData
	aux := struct {
		*Alias
		MimeTypeSnake string `json:"mime_type,omitempty"`
	}{
		Alias: (*Alias)(d),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if d.MimeType == "" && aux.MimeTypeSnake != "" {
		d.MimeType = aux.MimeTypeSnake
	}
	return nil
}

type GeminiContent struct {
	Role  string       `json:"role"`
	Parts []GeminiPart `json:"parts"`
}

type GeminiSystemInstruction struct {
	Parts []GeminiPart `json:"parts"`
}

type GeminiTool struct {
	FunctionDeclarations []map[string]any `json:"functionDeclarations"`
}

type GeminiFunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type GeminiToolConfig struct {
	FunctionCallingConfig *GeminiFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type GeminiThinkingConfig struct {
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
}

type GeminiGenerationConfig struct {
	ThinkingConfig  *GeminiThinkingConfig `json:"thinkingConfig,omitempty"`
	Temperature     *float64              `json:"temperature,omitempty"`
	TopP            *float64              `json:"topP,omitempty"`
	MaxOutputTokens *int                  `json:"maxOutputTokens,omitempty"`
}

type GeminiWireBody struct {
	Contents          []GeminiContent          `json:"contents"`
	SystemInstruction *GeminiSystemInstruction `json:"systemInstruction,omitempty"`
	Tools             []GeminiTool             `json:"tools,omitempty"`
	ToolConfig        *GeminiToolConfig        `json:"toolConfig,omitempty"`
	GenerationConfig  *GeminiGenerationConfig  `json:"generationConfig,omitempty"`
	SessionID         string                   `json:"sessionId,omitempty"`
}

type CCAEnvelope struct {
	Model       string         `json:"model"`
	UserAgent   string         `json:"userAgent"`
	RequestType string         `json:"requestType"`
	Project     string         `json:"project,omitempty"`
	RequestID   string         `json:"requestId"`
	Request     GeminiWireBody `json:"request"`
}

type UsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
}

type GeminiCandidate struct {
	Content      GeminiContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
}

type GeminiStreamChunk struct {
	Candidates    []GeminiCandidate `json:"candidates,omitempty"`
	UsageMetadata *UsageMetadata    `json:"usageMetadata,omitempty"`
}

type CCARootChunk struct {
	Response      *GeminiStreamChunk `json:"response,omitempty"`
	Candidates    []GeminiCandidate  `json:"candidates,omitempty"`
	UsageMetadata *UsageMetadata     `json:"usageMetadata,omitempty"`
}
