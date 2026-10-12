package grok

import (
	"strings"
	"sync/atomic"
)

const (
	// DefaultResponsesModel applies when a Responses request omits its model.
	DefaultResponsesModel = "grok-4.5"

	// DefaultTextModel applies to text endpoints that allow an omitted model.
	DefaultTextModel = "grok-4.6"

	// Imagine model IDs used by media requests.
	DefaultImagineImageQualityModel = "grok-imagine-image-quality"
	DefaultImagineImageFastModel    = "grok-imagine-image"
	DefaultImagineImage20Model      = "grok-imagine-image-2.0"
	DefaultImagineVideoModel        = "grok-imagine-video"
	DefaultImagineVideo15Model      = "grok-imagine-video-1.5"
)

var runtimeDefaultTextModel atomic.Pointer[string]

// SetRuntimeDefaultTextModel publishes the default for requests that allow an omitted model.
func SetRuntimeDefaultTextModel(model string) {
	runtimeDefaultTextModel.Store(&model)
}

// RuntimeDefaultTextModel returns the configured default; callers resolve an empty value.
func RuntimeDefaultTextModel() string {
	if model := runtimeDefaultTextModel.Load(); model != nil {
		return *model
	}
	return ""
}

// NormalizeModelID trims a model ID and uses DefaultResponsesModel when empty.
func NormalizeModelID(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return DefaultResponsesModel
	}
	return model
}

// ResolveGrokTextResponsesModelID trims the ID and selects a default when empty.
func ResolveGrokTextResponsesModelID(model string, defaultText ...string) string {
	return ResolveDefaultTextModel(model, defaultText...)
}

// ResolveDefaultTextModel trims the model, then tries defaultText[0], then DefaultTextModel.
func ResolveDefaultTextModel(model string, defaultText ...string) string {
	if trimmed := strings.TrimSpace(model); trimmed != "" {
		return trimmed
	}
	if len(defaultText) > 0 && strings.TrimSpace(defaultText[0]) != "" {
		return strings.TrimSpace(defaultText[0])
	}
	return DefaultTextModel
}

// CanonicalImagineVideoModel lowercases and trims the pricing ID, defaulting an empty model.
func CanonicalImagineVideoModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return DefaultImagineVideoModel
	}
	return model
}
