package gemini

// Gemini model resources identify models by name and advertise their supported
// generation methods. The list response retains those resource descriptions
// and optional display labels as supplied; callers decide which advertised
// methods and model names belong in their own requestable catalog.

// GeminiModel describes a named model resource returned in Gemini catalogue JSON.
type GeminiModel struct {
	Name                       string   `json:"name"`                                 // upstream resource name
	DisplayName                string   `json:"displayName,omitempty"`                // upstream display name
	SupportedGenerationMethods []string `json:"supportedGenerationMethods,omitempty"` // advertised generation methods
}

// GeminiModelsListResponse is the Gemini v1beta model list response.
type GeminiModelsListResponse struct {
	Models []GeminiModel `json:"models"` // returned model resources
}
