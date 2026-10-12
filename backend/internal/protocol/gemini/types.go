package gemini

import "encoding/json"

// Gemini reasoning settings can select a level or a numeric budget, with an
// explicit zero budget different from an omitted control. The custom codec
// records budget presence and emits one selected control, giving a nonempty
// level precedence over either an explicit or inferred budget.
//
// Request and response fields retain content parts, tool references, and usage
// observations in their Gemini shapes. Sampling pointers distinguish a supplied
// zero from omission. Response adapters interpret prompt/cache and output/thought
// counts according to their billing conversion, while modality details remain
// separate observations; ImageOutputTokens selects the first uppercase IMAGE
// entry rather than summing duplicate details. These types encode fields and
// do not establish a model's capability or validate complete replay history.

// GeminiRequest holds content and generation controls for a Gemini generation call.
type GeminiRequest struct {
	Contents          []GeminiContent         `json:"contents"`                    // ordered conversation content
	SystemInstruction *GeminiContent          `json:"systemInstruction,omitempty"` // optional system content
	GenerationConfig  *GeminiGenerationConfig `json:"generationConfig,omitempty"`  // optional generation controls
	Tools             []GeminiToolDeclaration `json:"tools,omitempty"`             // advertised tool declarations
	ToolConfig        *GeminiToolConfig       `json:"toolConfig,omitempty"`        // optional invocation policy
	SafetySettings    []GeminiSafetySetting   `json:"safetySettings,omitempty"`    // requested safety thresholds
	SessionID         string                  `json:"sessionId,omitempty"`         // upstream conversation identifier
}

// GeminiContent groups ordered content parts with their supplied conversation role.
type GeminiContent struct {
	Role  string       `json:"role"`  // supplied conversation role
	Parts []GeminiPart `json:"parts"` // ordered content parts
}

// GeminiPart holds text, reasoning metadata, media, or function-call content fields.
type GeminiPart struct {
	Text             string                  `json:"text,omitempty"`             // readable part text
	Thought          bool                    `json:"thought,omitempty"`          // reasoning text marker
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"` // opaque replay signature
	InlineData       *GeminiInlineData       `json:"inlineData,omitempty"`       // encoded media source
	FunctionCall     *GeminiFunctionCall     `json:"functionCall,omitempty"`     // model function invocation
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"` // function result content
}

// GeminiInlineData pairs encoded media bytes with their supplied MIME type.
type GeminiInlineData struct {
	MimeType string `json:"mimeType"` // media MIME type
	Data     string `json:"data"`     // base64
}

// GeminiFunctionCall holds a named model invocation and its decoded argument value.
type GeminiFunctionCall struct {
	Name string `json:"name"`           // supplied function name
	Args any    `json:"args,omitempty"` // decoded function arguments
	ID   string `json:"id,omitempty"`   // optional invocation identifier
}

// GeminiFunctionResponse supplies a named invocation result back to the conversation.
type GeminiFunctionResponse struct {
	Name     string         `json:"name"`         // supplied function name
	Response map[string]any `json:"response"`     // decoded function result
	ID       string         `json:"id,omitempty"` // optional invocation identifier
}

// GeminiGenerationConfig holds output limits, sampling, reasoning, and optional image controls.
type GeminiGenerationConfig struct {
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"` // maximum generated tokens
	// Temperature, TopP, and TopK retain supplied zero values through pointers.
	// Nil omits each setting; the caller's conversion options can suppress them
	// independently before this request reaches the selected upstream.
	Temperature    *float64              `json:"temperature,omitempty"`    // optional sampling temperature
	TopP           *float64              `json:"topP,omitempty"`           // optional nucleus probability
	TopK           *int                  `json:"topK,omitempty"`           // optional sampling candidate-count limit
	ThinkingConfig *GeminiThinkingConfig `json:"thinkingConfig,omitempty"` // optional reasoning control
	StopSequences  []string              `json:"stopSequences,omitempty"`  // requested stopping strings
	ImageConfig    *GeminiImageConfig    `json:"imageConfig,omitempty"`    // optional image controls
}

// GeminiImageConfig holds size and aspect-ratio labels requested for generated images.
type GeminiImageConfig struct {
	// AspectRatio can carry labels such as 1:1, 16:9, 9:16, 4:3, or 3:4;
	// ImageSize can carry 1K, 2K, or 4K. These fields retain supplied labels
	// while callers resolve which controls the selected model supports.
	AspectRatio string `json:"aspectRatio,omitempty"` // requested aspect ratio
	ImageSize   string `json:"imageSize,omitempty"`   // requested size label
}

// GeminiThinkingConfig retains reasoning budget presence and the level selected for encoding.
type GeminiThinkingConfig struct {
	// ThinkingLevel, ThinkingBudget, and BudgetSet select the encoded control.
	// A nonempty level wins; otherwise a nonzero budget or BudgetSet emits
	// the numeric budget, including explicit zero. IncludeThoughts is always encoded.
	IncludeThoughts bool   `json:"includeThoughts"`          // reasoning output selection
	ThinkingBudget  int    `json:"thinkingBudget,omitempty"` // requested reasoning tokens
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`  // requested reasoning level
	// BudgetSet records an explicit numeric budget, including zero. UnmarshalJSON
	// sets it for a nonnull budget; programmatic callers set it when zero is
	// intentional. MarshalJSON still gives a nonempty level precedence.
	BudgetSet bool `json:"-"` // explicit budget presence
}

// GeminiToolDeclaration holds function declarations and optional built-in search or execution settings.
type GeminiToolDeclaration struct {
	FunctionDeclarations []GeminiFunctionDecl `json:"functionDeclarations,omitempty"` // named function declarations
	GoogleSearch         *GeminiGoogleSearch  `json:"googleSearch,omitempty"`         // built-in search settings
	CodeExecution        *GeminiCodeExecution `json:"codeExecution,omitempty"`        // built-in execution marker
}

// GeminiCodeExecution marks the built-in code-execution tool in a declaration.
type GeminiCodeExecution struct{}

// GeminiFunctionDecl describes a named function with decoded schema and descriptive text.
type GeminiFunctionDecl struct {
	Name        string         `json:"name"`                  // supplied function name
	Description string         `json:"description,omitempty"` // supplied function description
	Parameters  map[string]any `json:"parameters,omitempty"`  // decoded JSON Schema
}

// GeminiGoogleSearch holds optional enriched-content settings for the built-in search tool.
type GeminiGoogleSearch struct {
	EnhancedContent *GeminiEnhancedContent `json:"enhancedContent,omitempty"` // optional enriched results
}

// GeminiEnhancedContent selects optional image results within enriched search output.
type GeminiEnhancedContent struct {
	ImageSearch *GeminiImageSearch `json:"imageSearch,omitempty"` // optional image search
}

// GeminiImageSearch holds the requested count limit for enriched image-search results.
type GeminiImageSearch struct {
	MaxResultCount int `json:"maxResultCount,omitempty"` // requested result count
}

// GeminiToolConfig selects function-calling and optional server-side tool invocation settings.
type GeminiToolConfig struct {
	FunctionCallingConfig *GeminiFunctionCallingConfig `json:"functionCallingConfig,omitempty"` // optional function-calling mode
	// IncludeServerSideToolInvocations distinguishes omission from a supplied
	// false value. The Antigravity transformer sets true for mixed function and
	// built-in declarations after resolving that request's tool configuration.
	IncludeServerSideToolInvocations *bool `json:"includeServerSideToolInvocations,omitempty"` // optional server-invocation selection
}

// GeminiFunctionCallingConfig carries the requested mode for model function invocation.
type GeminiFunctionCallingConfig struct {
	Mode string `json:"mode,omitempty"` // VALIDATED, AUTO, or NONE
}

// GeminiSafetySetting pairs a supplied safety category with its filtering threshold.
type GeminiSafetySetting struct {
	Category  string `json:"category"`  // supplied safety category
	Threshold string `json:"threshold"` // supplied filtering threshold
}

// GeminiResponse holds generated candidates, usage, and returned response identity metadata.
type GeminiResponse struct {
	Candidates    []GeminiCandidate    `json:"candidates,omitempty"`    // returned generation candidates
	UsageMetadata *GeminiUsageMetadata `json:"usageMetadata,omitempty"` // reported token observations
	ResponseID    string               `json:"responseId,omitempty"`    // upstream response identifier
	ModelVersion  string               `json:"modelVersion,omitempty"`  // upstream model version
}

// GeminiCandidate holds one generated content candidate and its finish/source metadata.
type GeminiCandidate struct {
	Content           *GeminiContent           `json:"content,omitempty"`           // returned candidate content
	FinishReason      string                   `json:"finishReason,omitempty"`      // supplied finish label
	Index             int                      `json:"index,omitempty"`             // candidate output position
	GroundingMetadata *GeminiGroundingMetadata `json:"groundingMetadata,omitempty"` // optional source metadata
}

// GeminiTokenDetail pairs a modality label with its reported token count.
type GeminiTokenDetail struct {
	Modality   string `json:"modality"`   // upstream modality label
	TokenCount int    `json:"tokenCount"` // reported modality tokens
}

// GeminiUsageMetadata holds aggregate token observations and per-modality input/output details.
type GeminiUsageMetadata struct {
	// PromptTokenCount and CachedContentTokenCount overlap in the
	// GeminiToAnthropicResponseProcessor calculation, which subtracts cache
	// from prompt input. It adds CandidatesTokenCount and ThoughtsTokenCount
	// for output, while TotalTokenCount remains the supplier's reported aggregate.
	PromptTokenCount        int                 `json:"promptTokenCount,omitempty"`        // reported prompt tokens
	CandidatesTokenCount    int                 `json:"candidatesTokenCount,omitempty"`    // reported candidate tokens
	CachedContentTokenCount int                 `json:"cachedContentTokenCount,omitempty"` // reported cache-read tokens
	TotalTokenCount         int                 `json:"totalTokenCount,omitempty"`         // reported total tokens
	ThoughtsTokenCount      int                 `json:"thoughtsTokenCount,omitempty"`      // reported reasoning tokens
	CandidatesTokensDetails []GeminiTokenDetail `json:"candidatesTokensDetails,omitempty"` // output modality observations
	PromptTokensDetails     []GeminiTokenDetail `json:"promptTokensDetails,omitempty"`     // input modality observations
}

// GeminiGroundingMetadata holds search queries and source references used for grounding.
type GeminiGroundingMetadata struct {
	WebSearchQueries []string               `json:"webSearchQueries,omitempty"` // reported search queries
	GroundingChunks  []GeminiGroundingChunk `json:"groundingChunks,omitempty"`  // reported source references
}

// GeminiGroundingChunk supplies an optional web-source reference for grounded response content.
type GeminiGroundingChunk struct {
	Web *GeminiGroundingWeb `json:"web,omitempty"` // optional web source
}

// GeminiGroundingWeb pairs a supplied source title with its URI.
type GeminiGroundingWeb struct {
	Title string `json:"title,omitempty"` // supplied source title
	URI   string `json:"uri,omitempty"`   // supplied source URI
}

// MarshalJSON encodes one reasoning control while retaining the request's
// explicit budget presence. A nonempty ThinkingLevel suppresses the budget;
// otherwise BudgetSet or a nonzero ThinkingBudget supplies a numeric value.
//
// An explicit zero survives through a budget pointer, while an absent zero
// budget is omitted. IncludeThoughts is encoded even when false. The codec
// performs no model-level or numeric-range validation; callers resolve those
// rules before sending this representation, and encoding errors are returned.
func (c GeminiThinkingConfig) MarshalJSON() ([]byte, error) {
	var budget *int
	// Level and budget controls have different target representations.
	// Use a pointer for the selected budget so explicit zero survives omitempty
	// while level-based requests carry no simultaneous budget field.
	if c.ThinkingLevel == "" && (c.BudgetSet || c.ThinkingBudget != 0) {
		budget = &c.ThinkingBudget
	}
	return json.Marshal(struct {
		IncludeThoughts bool   `json:"includeThoughts"`          // reasoning output selection
		Budget          *int   `json:"thinkingBudget,omitempty"` // optional budget value
		Level           string `json:"thinkingLevel,omitempty"`  // optional level value
	}{c.IncludeThoughts, budget, c.ThinkingLevel})
}

// UnmarshalJSON decodes reasoning fields into a nonnil writable receiver,
// recording whether a nonnull budget was supplied, including zero. Successful
// null-object input clears the receiver just like an object with absent fields.
//
// Decoding uses temporary state, so a decoding error leaves the receiver
// unchanged. Both level and budget can be stored when input supplies them;
// MarshalJSON applies level precedence when encoding later. Budget presence is
// separate from its numeric value, allowing callers to distinguish omission
// from an explicit disable request without deriving that distinction from zero alone.
func (c *GeminiThinkingConfig) UnmarshalJSON(body []byte) error {
	var value struct {
		IncludeThoughts bool   `json:"includeThoughts"` // reasoning output selection
		Budget          *int   `json:"thinkingBudget"`  // optional budget value
		Level           string `json:"thinkingLevel"`   // optional level value
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

// ImageOutputTokens returns the first exact IMAGE modality count from a
// nonnil metadata receiver, or zero when none exists. Duplicate IMAGE entries
// are not summed, and the supplied count is returned without range validation.
func (m *GeminiUsageMetadata) ImageOutputTokens() int {
	for _, d := range m.CandidatesTokensDetails {
		if d.Modality == "IMAGE" {
			return d.TokenCount
		}
	}
	return 0
}
