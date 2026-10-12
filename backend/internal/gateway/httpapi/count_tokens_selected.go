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

// messagesCountGoogleOutput converts count responses while GoogleBoundary handles Gemini execution and errors.
type messagesCountGoogleOutput struct {
	GoogleBoundary
	context  *gin.Context
	fallback int
}

type messagesCountSink struct {
	output *messagesCountGoogleOutput
	status int
}

// ForwardSelectedCountTokens dispatches to the selected provider's count path without generation slots or usage
// submission.
func ForwardSelectedCountTokens(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, parsed *requeststate.ParsedRequest, messages *MessagesExecutor, auxiliary *OpenAIAuxiliary, gemini *GeminiExecutor) error {
	if target == nil || parsed == nil {
		return errors.New("count_tokens target is unavailable")
	}
	switch target.Record.Platform {
	case "qoder", "antigravity":
		WriteAnthropicError(c, http.StatusNotFound, "not_found_error", "", "count_tokens endpoint is not supported for this provider")
		return nil
	case "grok":
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
	WriteAnthropicError(c, http.StatusServiceUnavailable, "api_error", "", "Token count service is unavailable")
	return errors.New("count_tokens executor is unavailable")
}

// forwardGeminiMessagesCount converts system prompts and tools for native Gemini counting and its OAuth scope retry.
func forwardGeminiMessagesCount(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, parsed *requeststate.ParsedRequest, executor *GeminiExecutor) error {
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

func (o *messagesCountGoogleOutput) Count(_ int) {
	o.context.JSON(http.StatusOK, gin.H{"input_tokens": o.fallback})
}

func (o *messagesCountGoogleOutput) GoogleError(status int, message string) error {
	return o.ClaudeError(status, "upstream_error", message)
}

func (o *messagesCountGoogleOutput) GeminiNativeUpstreamError(target *gatewayadapter.ExecutionProvider, response *http.Response, body []byte, requestID string, oauth bool) error {
	return o.GeminiMappedError(target, response.StatusCode, requestID, gemininative.UnwrapIfNeeded(oauth, body))
}

func (o *messagesCountGoogleOutput) Sink() upstream.OutputSink { return &messagesCountSink{output: o} }

func (s *messagesCountSink) Begin(head upstream.OutputHead) error { s.status = head.Status; return nil }

func (s *messagesCountSink) Emit(event upstream.OutputEvent) error {
	if len(event.Data) == 0 {
		return nil
	}
	count := gjson.GetBytes(event.Data, "totalTokens")
	if s.status >= http.StatusBadRequest || !count.Exists() || count.Type != gjson.Number || count.Int() < 0 {
		return s.output.ClaudeError(http.StatusBadGateway, "upstream_error", "Invalid token count response")
	}
	s.output.context.JSON(http.StatusOK, gin.H{"input_tokens": count.Int()})
	return nil
}
