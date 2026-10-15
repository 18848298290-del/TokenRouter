package upstream

import "strings"

// image_models.go recognizes Gemini, GPT, and Grok image model families for
// upstream routing decisions.
// Image request consumers recognize model families before selecting a platform
// image path. Gemini names accept a models/ prefix and family suffixes, while
// GPT and Grok names use their own trimmed prefix or exact-name checks for
// routing.

// IsGeminiImageGenerationModel reports whether model names the Gemini 3.1
// Flash, 3 Pro, or 2.5 Flash image family. Matching ignores case and removes a
// leading models/ prefix; surrounding whitespace stays significant, and family
// suffixes beginning with a hyphen also match.
func IsGeminiImageGenerationModel(model string) bool {
	modelLower := strings.ToLower(model)

	// Native model resource names can include models/ before the family name.
	// Remove that prefix so image-routing consumers recognize the same listed families.
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

// IsGPTImageGenerationModel reports whether trimmed, case-folded text starts
// with gpt-image-. Any suffix after that prefix is accepted here; model
// availability and provider support are checked by routing consumers.
func IsGPTImageGenerationModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "gpt-image-")
}

// IsGrokImageGenerationModel reports whether trimmed, case-folded text names a
// supported Grok image family. grok-imagine and grok-imagine-edit match
// exactly; names beginning with grok-imagine-image also match, including
// suffixed variants.
func IsGrokImageGenerationModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model == "grok-imagine" ||
		model == "grok-imagine-edit" ||
		strings.HasPrefix(model, "grok-imagine-image")
}
