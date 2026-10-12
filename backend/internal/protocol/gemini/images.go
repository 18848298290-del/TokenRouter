package gemini

// ImageGenerationConfig configures Gemini generateContent requests from the creative workspace.
type ImageGenerationConfig struct {
	ResponseModalities []string             `json:"responseModalities"`
	ImageConfig        *ImageConfig         `json:"imageConfig,omitempty"`
	ThinkingConfig     *ImageThinkingConfig `json:"thinkingConfig,omitempty"`
}

// ImageConfig contains image settings; imageSize belongs inside this object.
type ImageConfig struct {
	ImageSize   string `json:"imageSize,omitempty"`
	AspectRatio string `json:"aspectRatio,omitempty"`
}

// ImageThinkingConfig controls image-model reasoning effort and whether thoughts are returned.
type ImageThinkingConfig struct {
	ThinkingLevel   string `json:"thinkingLevel"`
	IncludeThoughts bool   `json:"includeThoughts"`
}

// ImageGenerateRequest is the creative workspace generateContent request body.
type ImageGenerateRequest struct {
	Contents         []BatchContent        `json:"contents"`
	GenerationConfig ImageGenerationConfig `json:"generationConfig"`
}
