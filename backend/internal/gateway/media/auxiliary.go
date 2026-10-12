package media

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// Media completion depends on the operation and observed units: realtime
// requires audio and positive elapsed time, immediate image recording
// requires image output, and asynchronous video creation defers completion.
// Small request predicates preserve those distinctions while optional model
// rewriting delegates body and content-type changes to the selected codec.

// RealtimeAudioUsage returns realtime audio units in elapsed minutes when
// audio was observed and elapsed is positive. Otherwise it returns nil so
// completion can omit audio usage with no measured billable interval.
func RealtimeAudioUsage(elapsed time.Duration, audioObserved bool) *protocol.AudioUsage {
	if !audioObserved || elapsed <= 0 {
		return nil
	}
	// Convert the measured elapsed duration to minute-based audio units.
	return &protocol.AudioUsage{Mode: "realtime", DurationOrUnits: elapsed.Minutes()}
}

// TTSInputText returns the trimmed first string in input, text, then prompt
// order. A blank string stops selection with empty text; null or nonstring
// values permit a later field, while invalid JSON returns empty.
func TTSInputText(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload map[string]json.RawMessage
	// Malformed request JSON supplies no usable text field, so return the
	// empty extraction result and let the request adapter own validation.
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	for _, key := range []string{"input", "text", "prompt"} {
		if raw, ok := payload[key]; ok {
			// Skip null and nonstring fields so the next supported text key can
			// supply input; a decoded empty string still wins by field priority.
			if len(raw) == 0 || raw[0] != '"' {
				continue
			}
			var value string
			if json.Unmarshal(raw, &value) == nil {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

// AlphaEndpointUnsupported reports whether an API-key search response
// permits unsupported-endpoint switching. Only 404 and 405 qualify, and
// OAuth or other credential paths keep their separate status handling.
func AlphaEndpointUnsupported(apiKey bool, statusCode int) bool {
	return apiKey && (statusCode == 404 || statusCode == 405)
}

// AlphaProviderErrorSideEffects reports whether search status handling
// permits provider-health effects. Authentication status 401 and
// missing/unsupported endpoint statuses 404 and 405 suppress those effects;
// other codes permit them.
func AlphaProviderErrorSideEffects(statusCode int) bool {
	switch statusCode {
	case 401, 404, 405:
		return false
	default:
		return true
	}
}

// RequiredModel extracts a nonblank string model with gjson and reports its
// presence. The trim argument controls returned spelling after blank
// validation; callers validate the complete JSON payload separately.
func RequiredModel(body []byte, trim bool) (string, bool) {
	value := gjson.GetBytes(body, "model")
	if !value.Exists() || value.Type != gjson.String || strings.TrimSpace(value.String()) == "" {
		return "", false
	}
	if trim {
		return strings.TrimSpace(value.String()), true
	}
	return value.String(), true
}

// IsVideoCreate recognizes generation, editing and extension operation
// labels for asynchronous video creation. The completion adapter uses true
// to retain task state for later completion billing; labels are matched
// exactly.
func IsVideoCreate(endpoint string) bool {
	switch endpoint {
	case "videos_generations", "videos_edits", "videos_extensions":
		return true
	default:
		return false
	}
}

// RecordImmediateImages permits completion for image generation or editing
// with a nonblank request model and positive observed image count. Other
// operations, including video creation and status lookup, return false.
func RecordImmediateImages(endpoint, requestModel string, imageCount int) bool {
	if strings.TrimSpace(requestModel) == "" || imageCount <= 0 {
		return false
	}
	return endpoint == "images_generations" || endpoint == "images_edits"
}

// RewriteMappedMediaBody delegates model rewriting when mapped is true and
// model is nonblank. Callers supply a usable rewrite callback on that path;
// otherwise returned body storage and content type remain shared with the
// input.
func RewriteMappedMediaBody(body []byte, contentType string, mapped bool, model string, rewrite func([]byte, string, string) ([]byte, string, error)) ([]byte, string, error) {
	if !mapped || strings.TrimSpace(model) == "" {
		return body, contentType, nil
	}
	// A usable group mapping selects codec rewriting, which can change both
	// payload encoding and Content-Type; retain the codec's error for the
	// request adapter.
	return rewrite(body, contentType, model)
}
