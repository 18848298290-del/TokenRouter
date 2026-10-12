package compact

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Failure carries an uncommitted compact response for HTTP handling and recovery.
type Failure struct {
	Payload []byte
	Message string
}

// Models reads provider-specific and global compact model rules on demand.
type Models interface {
	ProviderModel(string) (string, bool)
	GlobalModel() string
	ResolveGlobalModel(string) string
}

// Recovery selects compact recovery from model rules and response payloads.
type Recovery struct {
	Models        Models
	ContextWindow func(string, []byte) bool
	RewriteModel  func([]byte, string) []byte
}

// Request contains the compact intent and retry state for the current request.
type Request struct {
	Explicit, AlreadyRetried bool
	RequestedModel           string
	Body                     []byte
}

// RetryEffects supplies output and resource operations invoked after a recovery decision.
type RetryEffects interface {
	ObserveRetry([]byte, string)
	CloseResponse()
	SetModel(string)
	LogRetry(string, string, string)
}

func (f *Failure) Error() string {
	if f == nil || strings.TrimSpace(f.Message) == "" {
		return "upstream compact request failed"
	}
	return f.Message
}

// ResolveModel prefers the provider compact rule and evaluates the global rule only when needed.
func (r Recovery) ResolveModel(requested string) string {
	requested = strings.TrimSpace(requested)
	if mapped, ok := r.Models.ProviderModel(requested); ok {
		if mapped = strings.TrimSpace(mapped); mapped != "" {
			return mapped
		}
	}
	fallback := strings.TrimSpace(r.Models.GlobalModel())
	if fallback == "" {
		return ""
	}
	return strings.TrimSpace(r.Models.ResolveGlobalModel(fallback))
}

// NewFailure copies a recoverable compact failure; ordinary requests and other errors return nil.
func (r Recovery) NewFailure(explicit bool, payload []byte, message string) *Failure {
	if !explicit || !r.ModelFailure(400, message, payload) {
		return nil
	}
	return &Failure{Payload: append([]byte(nil), payload...), Message: logredact.SanitizeUpstreamQueries(strings.TrimSpace(message))}
}

// Prepare permits one recovery on the same provider. The caller controls output and the retry window.
func (r Recovery) Prepare(in Request, status int, message string, payload []byte) ([]byte, string, bool) {
	if in.AlreadyRetried || !in.Explicit || !r.ModelFailure(status, message, payload) {
		return in.Body, "", false
	}
	fallback := r.ResolveModel(in.RequestedModel)
	current := strings.TrimSpace(gjson.GetBytes(in.Body, "model").String())
	if fallback == "" || strings.EqualFold(fallback, current) {
		return in.Body, "", false
	}
	body := r.RewriteModel(in.Body, fallback)
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, "model").String()), current) {
		return in.Body, "", false
	}
	return body, fallback, true
}

// ApplySignal records the retry, closes the old response, updates the model, then logs diagnostics.
func (r Recovery) ApplySignal(in Request, failure *Failure, p RetryEffects) ([]byte, string, bool) {
	if failure == nil {
		return in.Body, "", false
	}
	body, model, retry := r.Prepare(in, 400, failure.Message, failure.Payload)
	if !retry {
		return in.Body, "", false
	}
	p.ObserveRetry(failure.Payload, failure.Message)
	p.CloseResponse()
	from := strings.TrimSpace(gjson.GetBytes(in.Body, "model").String())
	p.SetModel(model)
	p.LogRetry(from, model, upstream.ExtractErrorCode(failure.Payload))
	return body, model, true
}

// ModelFailure classifies context-window and model-availability errors as eligible for compact recovery.
func (r Recovery) ModelFailure(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if r.ContextWindow(upstreamMsg, upstreamBody) {
		return true
	}
	if statusCode != 400 && statusCode != 404 {
		return false
	}

	values := []string{
		upstream.ExtractErrorCode(upstreamBody),
		upstreamMsg,
		gjson.GetBytes(upstreamBody, "error.type").String(),
		gjson.GetBytes(upstreamBody, "response.error.code").String(),
		gjson.GetBytes(upstreamBody, "response.error.type").String(),
	}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		switch value {
		case "model_not_found", "model_not_available", "unsupported_model", "invalid_model":
			return true
		}
		if ExplicitModelAvailabilityMessage(value) {
			return true
		}
	}
	// An empty failed response can be retried; a response with business or policy error details cannot.
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(upstreamBody, "response.status").String()), "failed") ||
		strings.EqualFold(strings.TrimSpace(gjson.GetBytes(upstreamBody, "status").String()), "failed") {
		for _, path := range []string{
			"error.message", "error.code", "error.type",
			"response.error.message", "response.error.code", "response.error.type",
		} {
			if strings.TrimSpace(gjson.GetBytes(upstreamBody, path).String()) != "" {
				return false
			}
		}
		return strings.TrimSpace(upstreamMsg) == ""
	}
	return false
}

func ExplicitModelAvailabilityMessage(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, phrase := range []string{
		"model not found",
		"model does not exist",
		"model is unavailable",
		"model is not available",
		"model is unsupported",
		"model is not supported",
		"unsupported model",
	} {
		if strings.Contains(value, phrase) {
			return true
		}
	}
	// Require the model as the subject so unsupported output features are not mistaken for unavailable models.
	if strings.HasPrefix(value, "the model ") || strings.HasPrefix(value, "model ") {
		return strings.Contains(value, " does not exist") ||
			strings.Contains(value, " was not found") ||
			strings.Contains(value, " is unavailable") ||
			strings.Contains(value, " is not available")
	}
	return false
}

func NormalizeHTTPErrorPayload(signal *Failure) []byte {
	if signal == nil {
		return nil
	}
	payload := append([]byte(nil), signal.Payload...)
	var terminal struct {
		Error    json.RawMessage `json:"error"`
		Response struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(payload, &terminal) != nil || len(bytes.TrimSpace(terminal.Response.Error)) == 0 ||
		bytes.Equal(bytes.TrimSpace(terminal.Response.Error), []byte("null")) {
		return payload
	}
	// Move a nested stream error to the top-level HTTP error field.
	normalized, err := json.Marshal(struct {
		Error json.RawMessage `json:"error"`
	}{Error: terminal.Response.Error})
	if err != nil {
		return payload
	}
	return normalized
}

// AsFailure unwraps an error chain and requires a nonnil recovery signal.
func AsFailure(err error) (*Failure, bool) {
	var failure *Failure
	ok := errors.As(err, &failure)
	return failure, ok && failure != nil
}
