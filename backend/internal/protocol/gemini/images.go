package gemini

// images.go defines Gemini image generation requests and their nested image
// and thinking settings.
// Creative image requests carry prompt and reference-image parts with
// generateContent configuration. Image size and aspect ratio are nested in
// imageConfig, while optional reasoning settings occupy thinkingConfig; keeping
// those objects separate supplies the request shape used by BuildImageRequest.

// ImageGenerationConfig contains creative generateContent output and image-generation settings.
type ImageGenerationConfig struct {
	ResponseModalities []string             `json:"responseModalities"`       // requested output modalities
	ImageConfig        *ImageConfig         `json:"imageConfig,omitempty"`    // optional image settings
	ThinkingConfig     *ImageThinkingConfig `json:"thinkingConfig,omitempty"` // optional reasoning settings
}

// ImageConfig contains image settings; imageSize belongs inside this object.
type ImageConfig struct {
	ImageSize   string `json:"imageSize,omitempty"`   // requested size label
	AspectRatio string `json:"aspectRatio,omitempty"` // requested aspect ratio
}

// ImageThinkingConfig controls image-model reasoning effort and whether thoughts are returned.
type ImageThinkingConfig struct {
	ThinkingLevel   string `json:"thinkingLevel"`   // requested reasoning level
	IncludeThoughts bool   `json:"includeThoughts"` // reasoning output selection
}

// ImageGenerateRequest is the creative workspace generateContent request body.
type ImageGenerateRequest struct {
	Contents         []BatchContent        `json:"contents"`         // prompt and image parts
	GenerationConfig ImageGenerationConfig `json:"generationConfig"` // creative generation settings
}
