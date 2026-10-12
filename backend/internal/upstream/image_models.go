package upstream

import "strings"

// IsGeminiImageGenerationModel recognizes the Gemini 3.1 Flash, 3 Pro, and 2.5 Flash
// image families, including suffixed variants and the optional models/ prefix.
func IsGeminiImageGenerationModel(model string) bool {
	modelLower := strings.ToLower(model)

	modelLower = strings.TrimPrefix(modelLower, "models/")

	return modelLower == "gemini-3.1-flash-image" ||
		modelLower == "gemini-3.1-flash-image-preview" ||
		strings.HasPrefix(modelLower, "gemini-3.1-flash-image-") ||
		modelLower == "gemini-3-pro-image" ||
		modelLower == "gemini-3-pro-image-preview" ||
		strings.HasPrefix(modelLower, "gemini-3-pro-image-") ||
		modelLower == "gemini-2.5-flash-image" ||
		modelLower == "gemini-2.5-flash-image-preview" ||
		strings.HasPrefix(modelLower, "gemini-2.5-flash-image-")
}

// IsGPTImageGenerationModel reports whether the model belongs to the gpt-image- family.
func IsGPTImageGenerationModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "gpt-image-")
}

func IsGrokImageGenerationModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model == "grok-imagine" ||
		model == "grok-imagine-edit" ||
		strings.HasPrefix(model, "grok-imagine-image")
}
