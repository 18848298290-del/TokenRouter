package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tokenestimate"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	gemininative "github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

// Messages count clients receive input_tokens even when the selected provider
// uses Gemini's native totalTokens response. Provider dispatch can instead write
// a local estimate or an unsupported response, so a handled nil error does not
// imply that a network count exchange occurred for every platform.
//
// Gemini preparation converts the Messages body, resolves its upstream model,
// and wraps the result under generateContentRequest for native countTokens.
// The output adapter reads successful totalTokens events and retains a local
// estimate for the runtime's OAuth-scope rejection path. Both paths write the
// Messages count shape, while other native failures use the shared Google
// error mapping translated back into an Anthropic error.

// messagesCountGoogleOutput translates native count output through the shared Google error adapter.
type messagesCountGoogleOutput struct {
	GoogleBoundary              // shared Google error mapping
	context        *gin.Context // client HTTP output context
	// fallback is estimated from the converted generation body before forwarding.
	// Count uses this value when the runtime substitutes counting after an OAuth-scope rejection.
	fallback int
}

// messagesCountSink validates native count data before writing the Messages count response.
type messagesCountSink struct {
	output *messagesCountGoogleOutput // Messages count output adapter
	// status is installed by Begin and checked by Emit. Callers begin the
	// exchange before emitting data so error-status payloads cannot be accepted as successful counts.
	status int
}

// ForwardSelectedCountTokens dispatches to the selected provider's count path without generation slots or usage
// submission.
// Missing target or parsed input returns an error. Qoder and Antigravity write
// unsupported output with nil error; Grok estimates locally. Other platforms
// require their matching bound executor, and missing execution support writes
// unavailable output with a terminal error.
func ForwardSelectedCountTokens(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, parsed *requeststate.ParsedRequest, messages *MessagesExecutor, auxiliary *OpenAIAuxiliary, gemini *GeminiExecutor) error {
	if target == nil || parsed == nil {
		return errors.New("count_tokens target is unavailable")
	}
	switch target.Record.Platform {
	case "qoder", "antigravity":
		WriteAnthropicError(c, http.StatusNotFound, "not_found_error", "", "count_tokens endpoint is not supported for this provider")
		return nil
	case "grok":
		// This provider path supplies a local Messages estimate. Return the same
		// client count shape while leaving network execution and generation resources out of this branch.
		count, err := tokenestimate.Anthropic(parsed.Body.Bytes())
		if err != nil {
			WriteAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "", "Failed to parse request body")
			return err
		}
		c.JSON(http.StatusOK, gin.H{"input_tokens": count})
		return nil
	case "openai", "kimi", "zhipu", "deepseek":
		if auxiliary != nil {
			return auxiliary.ForwardCountTokensAsAnthropic(ctx, c, target, parsed.Body.Bytes(), parsed.Model)
		}
	case "anthropic":
		if messages != nil {
			return messages.ForwardCountTokens(ctx, c, target, parsed)
		}
	case "gemini":
		if gemini != nil && gemini.Runtime != nil {
			return forwardGeminiMessagesCount(ctx, c, target, parsed, gemini)
		}
	}
	// An absent matching executor cannot supply a provider count. Write a local
	// unavailable response before returning the terminal dispatch error to the count loop.
	WriteAnthropicError(c, http.StatusServiceUnavailable, "api_error", "", "Token count service is unavailable")
	return errors.New("count_tokens executor is unavailable")
}

// forwardGeminiMessagesCount converts system prompts and tools for native Gemini counting and its OAuth scope retry.
// The caller supplies a configured runtime and parsed Messages input. Conversion
// errors write bad-request output, while JSON patch errors return directly.
//
// Upstream model resolution precedes insertion of the models/ name into the
// converted body. Native counting receives that body in generateContentRequest,
// retaining its converted system and tool fields. A local estimate of the same
// body is captured before forwarding for the scope-rejection Count callback;
// normal native output instead passes through the totalTokens sink.
func forwardGeminiMessagesCount(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, parsed *requeststate.ParsedRequest, executor *GeminiExecutor) error {
	// Convert Messages system, messages, and tools together, then insert the
	// resolved model and wrap the generation request. Gemini counting consumes
	// that nested shape, and the scope fallback estimates the same converted input.
	body, err := bridge.NativeConvertClaudeMessagesToGeminiGenerateContent(bridge.NativeGeminiOptions{}, parsed.Body.Bytes())
	if err != nil {
		WriteAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "", "Failed to parse request body")
		return err
	}
	upstreamModel := gatewayadapter.ExecutionModelPolicy(target).UpstreamModel(ctx, parsed.Model)
	body, err = sjson.SetBytes(body, "model", "models/"+upstreamModel)
	if err != nil {
		return err
	}
	request, err := json.Marshal(map[string]json.RawMessage{"generateContentRequest": body})
	if err != nil {
		return err
	}
	output := &messagesCountGoogleOutput{GoogleBoundary: NewGoogleBoundary(c, executor.Runtime.Options, false), context: c, fallback: gemininative.EstimateGeminiCountTokens(body)}
	_, err = executor.Runtime.ForwardNative(ctx, output, target, parsed.Model, "countTokens", false, request)
	return err
}

// Count writes the estimate captured before native Gemini forwarding.
// The runtime-supplied count argument is ignored. Scope-error fallback therefore
// uses the adapter's converted generation-body estimate and writes HTTP 200
// with the Messages input_tokens field.
func (o *messagesCountGoogleOutput) Count(_ int) {
	o.context.JSON(http.StatusOK, gin.H{"input_tokens": o.fallback})
}

// GoogleError translates a native status and message into an Anthropic upstream error.
func (o *messagesCountGoogleOutput) GoogleError(status int, message string) error {
	return o.ClaudeError(status, "upstream_error", message)
}

// GeminiNativeUpstreamError maps the native error after optional OAuth envelope unwrapping.
// The supplied response provides its status and request ID identifies upstream
// evidence. GoogleBoundary handles error classification and returns the mapped failure to native execution.
func (o *messagesCountGoogleOutput) GeminiNativeUpstreamError(target *gatewayadapter.ExecutionProvider, response *http.Response, body []byte, requestID string, oauth bool) error {
	return o.GeminiMappedError(target, response.StatusCode, requestID, gemininative.UnwrapIfNeeded(oauth, body))
}

// Sink creates a native-output sink bound to this Messages count response adapter.
func (o *messagesCountGoogleOutput) Sink() upstream.OutputSink { return &messagesCountSink{output: o} }

// Begin records upstream status for subsequent native count data validation.
func (s *messagesCountSink) Begin(head upstream.OutputHead) error { s.status = head.Status; return nil }

// Emit writes nonempty native count data as a Messages input_tokens response.
// Empty data is ignored. HTTP error status, absent or nonnumeric totalTokens,
// and negative Int results write a bad-gateway error; other numeric values
// use gjson.Int conversion before output.
func (s *messagesCountSink) Emit(event upstream.OutputEvent) error {
	if len(event.Data) == 0 {
		return nil
	}
	// Native count output uses a different field name from Messages. Check its
	// status and numeric shape before the integer accessor supplies the client-facing count.
	count := gjson.GetBytes(event.Data, "totalTokens")
	if s.status >= http.StatusBadRequest || !count.Exists() || count.Type != gjson.Number || count.Int() < 0 {
		return s.output.ClaudeError(http.StatusBadGateway, "upstream_error", "Invalid token count response")
	}
	s.output.context.JSON(http.StatusOK, gin.H{"input_tokens": count.Int()})
	return nil
}
