package grok

import (
	"strings"
	"sync/atomic"
)

// Missing model IDs use different defaults for Responses normalization and
// general text requests. Nonblank model IDs retain their full spelling after
// trimming, while video pricing normalization lowercases the full ID. Runtime
// settings publish a separate text default that callers choose to read.

// Endpoint defaults apply when model text is blank; these full IDs also name
// the image and video models selected by media request callers.
const (
	// DefaultResponsesModel applies when a Responses request omits its model.
	DefaultResponsesModel = "grok-4.5"

	// DefaultTextModel applies to text endpoints that allow an omitted model.
	DefaultTextModel = "grok-4.6"

	// Imagine model IDs used by media requests.
	DefaultImagineImageQualityModel = "grok-imagine-image-quality" // quality image model ID
	DefaultImagineImageFastModel    = "grok-imagine-image"         // fast image model ID
	DefaultImagineImage20Model      = "grok-imagine-image-2.0"     // image 2.0 model ID
	DefaultImagineVideoModel        = "grok-imagine-video"         // default video model ID
	DefaultImagineVideo15Model      = "grok-imagine-video-1.5"     // video 1.5 model ID
)

// runtimeDefaultTextModel publishes complete configured strings to concurrent
// readers; endpoint callers decide whether an empty setting needs a default.
var runtimeDefaultTextModel atomic.Pointer[string]

// SetRuntimeDefaultTextModel atomically publishes the supplied string as the
// runtime text default. The value is stored without trimming or validation;
// readers observe one complete stored value, including an empty string.
func SetRuntimeDefaultTextModel(model string) {
	runtimeDefaultTextModel.Store(&model)
}

// RuntimeDefaultTextModel returns the atomically published text default, or
// empty when none has been stored. The result is returned as supplied;
// callers resolve blanks through their endpoint's default-model rules.
func RuntimeDefaultTextModel() string {
	if model := runtimeDefaultTextModel.Load(); model != nil {
		return *model
	}
	return ""
}

// NormalizeModelID trims a model ID and selects DefaultResponsesModel for
// blank input. Nonblank IDs keep their complete spelling and case, so callers
// can pass vendor aliases and suffixed IDs through unchanged.
func NormalizeModelID(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return DefaultResponsesModel
	}
	return model
}

// ResolveGrokTextResponsesModelID resolves a text model through
// ResolveDefaultTextModel. Blank input considers the first optional default,
// then DefaultTextModel; it does not read the runtime default unless callers
// pass that value.
func ResolveGrokTextResponsesModelID(model string, defaultText ...string) string {
	return ResolveDefaultTextModel(model, defaultText...)
}

// ResolveDefaultTextModel selects a trimmed explicit model, a nonblank
// trimmed first optional default, or DefaultTextModel. Additional defaultText
// arguments are ignored even when the first is blank, so callers put the
// desired endpoint default in the first position.
func ResolveDefaultTextModel(model string, defaultText ...string) string {
	if trimmed := strings.TrimSpace(model); trimmed != "" {
		return trimmed
	}
	if len(defaultText) > 0 && strings.TrimSpace(defaultText[0]) != "" {
		return strings.TrimSpace(defaultText[0])
	}
	return DefaultTextModel
}

// CanonicalImagineVideoModel trims and lowercases the complete video pricing
// ID. Blank input selects DefaultImagineVideoModel; nonblank suffixed or
// vendor-qualified IDs retain their full identity after that case
// normalization.
func CanonicalImagineVideoModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return DefaultImagineVideoModel
	}
	return model
}
