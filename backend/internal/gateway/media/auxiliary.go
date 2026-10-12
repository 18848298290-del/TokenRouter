package media

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// RealtimeAudioUsage returns elapsed minutes when audio was observed and duration is positive.
func RealtimeAudioUsage(elapsed time.Duration, audioObserved bool) *protocol.AudioUsage {
	if !audioObserved || elapsed <= 0 {
		return nil
	}
	return &protocol.AudioUsage{Mode: "realtime", DurationOrUnits: elapsed.Minutes()}
}

// TTSInputText returns the first string among input, text, and prompt, trimmed and possibly empty.
func TTSInputText(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	for _, key := range []string{"input", "text", "prompt"} {
		if raw, ok := payload[key]; ok {
			// Skip JSON null so later fields can supply the text.
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

// AlphaEndpointUnsupported permits switching for API key search endpoints returning 404 or 405.
func AlphaEndpointUnsupported(apiKey bool, statusCode int) bool {
	return apiKey && (statusCode == 404 || statusCode == 405)
}

// AlphaProviderErrorSideEffects reports whether a search endpoint error should update provider health.
func AlphaProviderErrorSideEffects(statusCode int) bool {
	switch statusCode {
	case 401, 404, 405:
		return false
	default:
		return true
	}
}

// RequiredModel reads a nonblank string model and optionally trims its returned value.
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

// IsVideoCreate identifies asynchronous creation endpoints, which defer billing until completion.
func IsVideoCreate(endpoint string) bool {
	switch endpoint {
	case "videos_generations", "videos_edits", "videos_extensions":
		return true
	default:
		return false
	}
}

// RecordImmediateImages permits completion recording for image generation or editing with actual output.
func RecordImmediateImages(endpoint, requestModel string, imageCount int) bool {
	if strings.TrimSpace(requestModel) == "" || imageCount <= 0 {
		return false
	}
	return endpoint == "images_generations" || endpoint == "images_edits"
}

// RewriteMappedMediaBody returns the body and content type unchanged when no usable mapping exists.
func RewriteMappedMediaBody(body []byte, contentType string, mapped bool, model string, rewrite func([]byte, string, string) ([]byte, string, error)) ([]byte, string, error) {
	if !mapped || strings.TrimSpace(model) == "" {
		return body, contentType, nil
	}
	return rewrite(body, contentType, model)
}
