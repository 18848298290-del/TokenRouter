package gemini

import "encoding/json"

// GeminiRequest is a generateContent request body.
type GeminiRequest struct {
	Contents          []GeminiContent         `json:"contents"`
	SystemInstruction *GeminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *GeminiGenerationConfig `json:"generationConfig,omitempty"`
	Tools             []GeminiToolDeclaration `json:"tools,omitempty"`
	ToolConfig        *GeminiToolConfig       `json:"toolConfig,omitempty"`
	SafetySettings    []GeminiSafetySetting   `json:"safetySettings,omitempty"`
	SessionID         string                  `json:"sessionId,omitempty"`
}

// GeminiContent groups parts under a conversation role.
type GeminiContent struct {
	Role  string       `json:"role"` // user, model
	Parts []GeminiPart `json:"parts"`
}

// GeminiPart carries text, thinking, inline data, or a function call or result.
type GeminiPart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	InlineData       *GeminiInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *GeminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`
}

// GeminiInlineData carries base64-encoded media with its MIME type.
type GeminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// GeminiFunctionCall is a model-requested function invocation.
type GeminiFunctionCall struct {
	Name string `json:"name"`
	Args any    `json:"args,omitempty"`
	ID   string `json:"id,omitempty"`
}

// GeminiFunctionResponse carries a function result back to the model.
type GeminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
	ID       string         `json:"id,omitempty"`
}

// GeminiGenerationConfig contains generation, sampling, and thinking controls.
type GeminiGenerationConfig struct {
	MaxOutputTokens int                   `json:"maxOutputTokens,omitempty"`
	Temperature     *float64              `json:"temperature,omitempty"`
	TopP            *float64              `json:"topP,omitempty"`
	TopK            *int                  `json:"topK,omitempty"`
	ThinkingConfig  *GeminiThinkingConfig `json:"thinkingConfig,omitempty"`
	StopSequences   []string              `json:"stopSequences,omitempty"`
	ImageConfig     *GeminiImageConfig    `json:"imageConfig,omitempty"`
}

// GeminiImageConfig contains settings for models that support image generation.
type GeminiImageConfig struct {
	AspectRatio string `json:"aspectRatio,omitempty"` // "1:1", "16:9", "9:16", "4:3", "3:4"
	ImageSize   string `json:"imageSize,omitempty"`   // "1K", "2K", "4K"
}

// GeminiThinkingConfig selects level or budget controls; a nonempty level takes precedence during encoding.
type GeminiThinkingConfig struct {
	IncludeThoughts bool   `json:"includeThoughts"`
	ThinkingBudget  int    `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	// BudgetSet retains an explicit zero budget when no thinking level is selected.
	BudgetSet bool `json:"-"`
}

// GeminiToolDeclaration groups function declarations or built-in tools.
type GeminiToolDeclaration struct {
	FunctionDeclarations []GeminiFunctionDecl `json:"functionDeclarations,omitempty"`
	GoogleSearch         *GeminiGoogleSearch  `json:"googleSearch,omitempty"`
	CodeExecution        *GeminiCodeExecution `json:"codeExecution,omitempty"`
}

// GeminiCodeExecution enables the built-in code execution tool.
type GeminiCodeExecution struct{}

// GeminiFunctionDecl describes a callable function and its parameter schema.
type GeminiFunctionDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// GeminiGoogleSearch configures the built-in Google Search tool.
type GeminiGoogleSearch struct {
	EnhancedContent *GeminiEnhancedContent `json:"enhancedContent,omitempty"`
}

// GeminiEnhancedContent configures enriched search results.
type GeminiEnhancedContent struct {
	ImageSearch *GeminiImageSearch `json:"imageSearch,omitempty"`
}

// GeminiImageSearch configures the image search result limit.
type GeminiImageSearch struct {
	MaxResultCount int `json:"maxResultCount,omitempty"`
}

// GeminiToolConfig contains function-calling and built-in tool execution settings.
type GeminiToolConfig struct {
	FunctionCallingConfig *GeminiFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
	// Enable server-side invocation when mixing function declarations with built-in tools.
	IncludeServerSideToolInvocations *bool `json:"includeServerSideToolInvocations,omitempty"`
}

// GeminiFunctionCallingConfig selects how the model calls functions.
type GeminiFunctionCallingConfig struct {
	Mode string `json:"mode,omitempty"` // VALIDATED, AUTO, NONE
}

// GeminiSafetySetting pairs a safety category with its threshold.
type GeminiSafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// GeminiResponse contains generated candidates and token usage.
type GeminiResponse struct {
	Candidates    []GeminiCandidate    `json:"candidates,omitempty"`
	UsageMetadata *GeminiUsageMetadata `json:"usageMetadata,omitempty"`
	ResponseID    string               `json:"responseId,omitempty"`
	ModelVersion  string               `json:"modelVersion,omitempty"`
}

// GeminiCandidate is one generated response candidate.
type GeminiCandidate struct {
	Content           *GeminiContent           `json:"content,omitempty"`
	FinishReason      string                   `json:"finishReason,omitempty"`
	Index             int                      `json:"index,omitempty"`
	GroundingMetadata *GeminiGroundingMetadata `json:"groundingMetadata,omitempty"`
}

// GeminiTokenDetail reports token usage for one modality.
type GeminiTokenDetail struct {
	Modality   string `json:"modality"`
	TokenCount int    `json:"tokenCount"`
}

// GeminiUsageMetadata contains Gemini token counts and modality breakdowns.
type GeminiUsageMetadata struct {
	PromptTokenCount        int                 `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int                 `json:"candidatesTokenCount,omitempty"`
	CachedContentTokenCount int                 `json:"cachedContentTokenCount,omitempty"`
	TotalTokenCount         int                 `json:"totalTokenCount,omitempty"`
	ThoughtsTokenCount      int                 `json:"thoughtsTokenCount,omitempty"` // Thinking tokens are billed as output.
	CandidatesTokensDetails []GeminiTokenDetail `json:"candidatesTokensDetails,omitempty"`
	PromptTokensDetails     []GeminiTokenDetail `json:"promptTokensDetails,omitempty"`
}

// GeminiGroundingMetadata contains search queries and grounding sources.
type GeminiGroundingMetadata struct {
	WebSearchQueries []string               `json:"webSearchQueries,omitempty"`
	GroundingChunks  []GeminiGroundingChunk `json:"groundingChunks,omitempty"`
}

// GeminiGroundingChunk identifies a source used to ground the response.
type GeminiGroundingChunk struct {
	Web *GeminiGroundingWeb `json:"web,omitempty"`
}

// GeminiGroundingWeb identifies a web source by title and URI.
type GeminiGroundingWeb struct {
	Title string `json:"title,omitempty"`
	URI   string `json:"uri,omitempty"`
}

func (c GeminiThinkingConfig) MarshalJSON() ([]byte, error) {
	var budget *int
	if c.ThinkingLevel == "" && (c.BudgetSet || c.ThinkingBudget != 0) {
		budget = &c.ThinkingBudget
	}
	return json.Marshal(struct {
		IncludeThoughts bool   `json:"includeThoughts"`
		Budget          *int   `json:"thinkingBudget,omitempty"`
		Level           string `json:"thinkingLevel,omitempty"`
	}{c.IncludeThoughts, budget, c.ThinkingLevel})
}

func (c *GeminiThinkingConfig) UnmarshalJSON(body []byte) error {
	var value struct {
		IncludeThoughts bool   `json:"includeThoughts"`
		Budget          *int   `json:"thinkingBudget"`
		Level           string `json:"thinkingLevel"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return err
	}
	*c = GeminiThinkingConfig{IncludeThoughts: value.IncludeThoughts, ThinkingLevel: value.Level, BudgetSet: value.Budget != nil}
	if value.Budget != nil {
		c.ThinkingBudget = *value.Budget
	}
	return nil
}

// ImageOutputTokens returns the first IMAGE modality count, or zero when absent.
func (m *GeminiUsageMetadata) ImageOutputTokens() int {
	for _, d := range m.CandidatesTokensDetails {
		if d.Modality == "IMAGE" {
			return d.TokenCount
		}
	}
	return 0
}
