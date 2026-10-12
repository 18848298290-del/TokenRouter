package gemini

// GeminiModel describes a Gemini v1beta model resource.
type GeminiModel struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName,omitempty"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods,omitempty"`
}

// GeminiModelsListResponse is the Gemini v1beta model list response.
type GeminiModelsListResponse struct {
	Models []GeminiModel `json:"models"`
}
