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

// fallback.go selects and applies a replacement model for eligible Compact
// failures on the current provider. Compact recovery can change the model once
// on the same provider when an explicit compact request encounters a recognized
// model or context-window failure. Provider compact mappings take precedence
// over the global model, and an alternative is usable only when it differs from
// the current request model and the body rewrite actually changes that model.
//
// The caller owns the retry window and output state. After a recovery decision,
// ApplySignal observes the rejected response, closes its resources, and updates
// model diagnostics before returning the replacement body. NewFailure copies
// the rejected payload so HTTP handling can promote nested stream errors while
// keeping the captured response available for retry observations.

// Failure carries an uncommitted compact response for HTTP handling and recovery.
type Failure struct {
	// Payload is copied by NewFailure from the rejected response. Recovery reads
	// these bytes, and HTTP normalization copies them before returning an error payload.
	Payload []byte
	Message string // captured upstream failure message
}

// Models reads provider-specific and global compact model rules on demand.
type Models interface {
	// ProviderModel resolves a compact mapping for the requested model on the
	// current provider. A false result or an empty trimmed value lets Recovery
	// consult the global setting next.
	ProviderModel(string) (string, bool)
	// GlobalModel returns the configured global compact fallback model when one
	// exists. Recovery reads it after provider mapping fails, and an empty trimmed
	// value disables that fallback.
	GlobalModel() string
	// ResolveGlobalModel maps the configured global fallback through the current
	// provider's upstream model policy. Recovery trims the result and treats an
	// empty value as the absence of a usable alternative.
	ResolveGlobalModel(string) string
}

// Recovery selects compact recovery from model rules and response payloads.
type Recovery struct {
	// Models supplies provider compact rules and the global fallback resolution.
	// A usable implementation is required when resolving or preparing an alternative model.
	Models Models
	// ContextWindow identifies recoverable context-window failures from the
	// upstream message and payload. Supply this callback before classifying a failure.
	ContextWindow func(string, []byte) bool
	// RewriteModel applies the selected fallback to the request body. Prepare
	// checks the rewritten model, so a callback that leaves it unchanged prevents retry.
	RewriteModel func([]byte, string) []byte
}

// Request contains the compact intent and retry state for the current request.
type Request struct {
	// Explicit identifies compact intent, and AlreadyRetried records the caller's
	// consumed recovery attempt. Prepare requires Explicit and rejects an already
	// retried request before consulting model rules.
	Explicit, AlreadyRetried bool   // requested compact intent; consumed recovery attempt
	RequestedModel           string // compact mapping lookup model
	// Body contains the current upstream model used to reject unchanged rewrites.
	// Failed preparation returns this same slice; rewrite ownership belongs to the callback.
	Body []byte
}

// RetryEffects supplies output and resource operations invoked after a recovery decision.
type RetryEffects interface {
	// ObserveRetry records the rejected response before resources are released.
	// ApplySignal supplies the captured payload and stored message after it
	// has established that a replacement model can be retried.
	// Leave the supplied bytes unchanged so later retry logging reads the same
	// captured error code from that payload.
	ObserveRetry([]byte, string)
	// CloseResponse releases the rejected response before a replacement exchange
	// starts. ApplySignal invokes it after observation, allowing observations to
	// read the old response metadata while it is still available.
	CloseResponse()
	// SetModel publishes the replacement upstream model for request diagnostics.
	// It runs after the rejected response has been released and before retry
	// logging records the old and replacement model names.
	SetModel(string)
	// LogRetry records the old model, replacement model, and extracted upstream
	// error code. ApplySignal invokes it after updating the model so diagnostic
	// readers see the selected replacement.
	LogRetry(string, string, string)
}

// Error returns the stored compact failure message when it is nonblank.
// A nil Failure or blank message returns the generic compact failure text,
// allowing callers to report a usable error for either signal form.
func (f *Failure) Error() string {
	if f == nil || strings.TrimSpace(f.Message) == "" {
		return "upstream compact request failed"
	}
	return f.Message
}

// ResolveModel prefers the provider compact rule and evaluates the global rule only when needed.
// Requested input and resolved values are trimmed. Empty provider mappings
// continue to the global setting, and absent global configuration returns an
// empty string for the caller to reject recovery.
//
// A provider-specific result is the final compact target. A global result still
// passes through ResolveGlobalModel so the current provider's ordinary model
// policy can map that configured default before it is used for recovery.
func (r Recovery) ResolveModel(requested string) string {
	requested = strings.TrimSpace(requested)
	// Provider-specific compact rules can choose an alternative before a global
	// default is read. An unusable mapping leaves the global policy responsible
	// for supplying the current provider's fallback.
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
// Classification uses status 400 for this signal, and the message is trimmed
// and query-sanitized before it reaches later observations or HTTP handling.
func (r Recovery) NewFailure(explicit bool, payload []byte, message string) *Failure {
	if !explicit || !r.ModelFailure(400, message, payload) {
		return nil
	}
	return &Failure{Payload: append([]byte(nil), payload...), Message: logredact.SanitizeUpstreamQueries(strings.TrimSpace(message))}
}

// Prepare returns a rewritten body and replacement model for one compact
// recovery on the current provider. Callers set AlreadyRetried after consuming
// that recovery and decide whether their output and retry window still permit
// another exchange. An ineligible request returns its supplied body slice and false.
//
// The failure needs compact intent and recognized recovery evidence. Model
// rules are evaluated after those checks, and the alternative must differ from
// the current body model. Prepare also verifies the rewrite changed that model,
// preventing an ineffective callback from replaying the same failing request.
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
	// Some body rewriters return their input after an unsuccessful patch.
	// Verify the changed model before granting recovery so an unchanged body
	// cannot consume the caller's single retry.
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, "model").String()), current) {
		return in.Body, "", false
	}
	return body, fallback, true
}

// ApplySignal prepares a replacement compact body from a captured Failure and
// applies retry observations through p. A nil or ineligible signal returns the
// supplied body and false without invoking p; callers provide usable effects when
// recovery succeeds and still own the subsequent exchange.
//
// Observation precedes response closure so the caller can capture rejected
// response metadata. Closure precedes model publication and logging, allowing
// the replacement attempt to start after the old response resource has been
// released. The signal's captured payload supplies the observed error code and
// message throughout that sequence.
func (r Recovery) ApplySignal(in Request, failure *Failure, p RetryEffects) ([]byte, string, bool) {
	if failure == nil {
		return in.Body, "", false
	}
	body, model, retry := r.Prepare(in, 400, failure.Message, failure.Payload)
	if !retry {
		return in.Body, "", false
	}
	// Capture the rejected response before releasing it, then publish the new
	// model and retry diagnostic. This ordering keeps observations tied to the
	// old exchange while the next exchange uses the replacement model.
	p.ObserveRetry(failure.Payload, failure.Message)
	p.CloseResponse()
	from := strings.TrimSpace(gjson.GetBytes(in.Body, "model").String())
	p.SetModel(model)
	p.LogRetry(from, model, upstream.ExtractErrorCode(failure.Payload))
	return body, model, true
}

// ModelFailure classifies context-window and model-availability errors as eligible for compact recovery.
// A supplied context-window classifier can match any status; other model
// evidence is considered for 400 and 404 responses. Known model error codes
// and phrases qualify before the bare-failed-response check.
//
// A failed status with no error fields qualifies only when the message is also
// empty. Other business or policy details stop that final check, keeping an
// unexplained model change from masking a rejection that needs its own handling.
// Callers separately verify compact intent and their remaining retry allowance.
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
	// After known model errors have been checked, a bare failed status has no
	// more specific cause to classify. Any remaining error detail or message
	// prevents this last recovery rule from masking a business or policy rejection.
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

// ExplicitModelAvailabilityMessage recognizes normalized phrases naming an
// unavailable or unsupported model. Broader suffix phrases require model as
// their subject, preventing errors about unsupported output features from being
// classified solely by that wording.
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
	// Require the model as the subject so unsupported output features are not
	// mistaken for unavailable models by these broader suffix phrases.
	if strings.HasPrefix(value, "the model ") || strings.HasPrefix(value, "model ") {
		return strings.Contains(value, " does not exist") ||
			strings.Contains(value, " was not found") ||
			strings.Contains(value, " is unavailable") ||
			strings.Contains(value, " is not available")
	}
	return false
}

// NormalizeHTTPErrorPayload returns an HTTP error payload from a captured
// compact failure, or nil for a nil signal. It copies the captured bytes before
// decoding and returns that copy for invalid JSON or absent nested errors.
//
// A stream terminal event can place its error under response.error, whereas the
// HTTP adapter expects a top-level error object. A nonnull nested error is
// marshaled into that shape; if encoding fails, the copied captured payload
// remains available. The original signal continues to hold the response used
// by retry observations.
func NormalizeHTTPErrorPayload(signal *Failure) []byte {
	if signal == nil {
		return nil
	}
	payload := append([]byte(nil), signal.Payload...)
	var terminal struct {
		Error    json.RawMessage `json:"error"` // top-level captured error
		Response struct {
			Error json.RawMessage `json:"error"` // nested terminal error
		} `json:"response"` // captured stream response
	}
	// Payloads without a decodable, nonnull nested error keep their captured
	// response shape, leaving HTTP error handling the original upstream details to interpret.
	if json.Unmarshal(payload, &terminal) != nil || len(bytes.TrimSpace(terminal.Response.Error)) == 0 ||
		bytes.Equal(bytes.TrimSpace(terminal.Response.Error), []byte("null")) {
		return payload
	}
	// HTTP failure writers read a top-level error. Promote the stream's nested
	// terminal error so the client receives its structured details in that shape.
	normalized, err := json.Marshal(struct {
		Error json.RawMessage `json:"error"` // HTTP response error
	}{Error: terminal.Response.Error})
	if err != nil {
		// A failed encoding leaves the captured response available to the HTTP
		// caller, preserving diagnostic bytes when the normalized shape cannot be produced.
		return payload
	}
	return normalized
}

// AsFailure unwraps an error chain and requires a nonnil recovery signal.
// A typed nil Failure does not qualify even if errors.As matches its type;
// callers can use the boolean before accessing the returned payload or message.
func AsFailure(err error) (*Failure, bool) {
	var failure *Failure
	ok := errors.As(err, &failure)
	return failure, ok && failure != nil
}
