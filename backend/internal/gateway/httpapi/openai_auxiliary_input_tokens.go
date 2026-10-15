package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tokenestimate"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openai_auxiliary_input_tokens.go executes native Responses input-token
// preflight and supplies local estimates for unsupported counting paths.
// Native input_tokens preflight returns the upstream count payload for
// supported OpenAI targets and estimates locally for selected compatibility
// targets. Preparation retains the requested model alongside provider mapping,
// allowing count results and failure policy to refer to the actual upstream
// model.
//
// Local-provider selection and unsupported upstream endpoints produce a
// successful response.input_tokens estimate, with a minimum for missing or
// invalid estimates. A generic upstream 404 uses this path, while OAuth scope
// failures follow the shared compatibility predicate. Other failures apply
// provider-health and retry policy before writing terminal OpenAI output or
// returning failover evidence.

// ForwardResponsesInputTokens handles one selected provider's native
// token-count preflight. Unsupported platforms receive a handled 404. Grok, CN,
// and configured compatibility targets use local estimation; other targets
// resolve credentials and send converted counting input through the upstream
// counter, which owns response-body cleanup.
//
// An upstream 404 or recognized OAuth incompatibility becomes a local estimate.
// Other HTTP failures may return failover evidence without writing output. A
// nil error can accompany a locally handled result or unsupported response, so
// this auxiliary call is separate from generation completion eligibility.
func (s *OpenAIAuxiliary) ForwardResponsesInputTokens(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
) error {
	if provider == nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusServiceUnavailable, "api_error", "No available OpenAI providers")
		return fmt.Errorf("responses input_tokens: missing provider")
	}

	// Restrict this native count path to the platforms with a prepared
	// Responses or local-estimate representation; other selections receive
	// a handled unsupported response.
	if provider.Record.Platform != "openai" && !provider.View().IsGrok() && !provider.View().IsCNProvider() {
		writeOpenAIResponsesInputTokensError(c, http.StatusNotFound, "not_found_error", "Responses input token counting is not supported for this provider")
		return nil
	}

	prepared, err := gatewayprovider.PrepareNativeInputTokens(body, provider)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return err
	}

	// Compatibility target selection can choose local counting before
	// credentials or network preparation, so a custom relay does not need
	// an upstream counting endpoint.
	if gatewayprovider.EstimateInputTokensLocally(provider) {
		writeOpenAIResponsesInputTokensFallback(c, provider, prepared, 0, "local_provider")
		return nil
	}

	token, _, err := s.Requests.Credentials.Resolve(ctx, gatewayprovider.ExecutionRecord(provider))
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to get access token")
		return fmt.Errorf("responses input_tokens: get access token: %w", err)
	}

	upstreamBody, err := wirejson.Marshal(prepared.Request)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("responses input_tokens: marshal request: %w", err)
	}
	upstreamReq, err := s.buildInputTokensUpstreamRequest(ctx, c, provider, upstreamBody, token)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("responses input_tokens: build request: %w", err)
	}
	proxyURL := ""
	if provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}
	if s.Requests.Transport == nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		return fmt.Errorf("responses input_tokens: upstream client is unavailable")
	}
	return openai.CountNativeInputTokens(upstreamReq, openai.NativeInputTokensOptions{
		Enter: s.Enter,
		Do: func(req *http.Request) (*http.Response, error) {
			return s.Requests.Transport.Do(req, proxyURL, provider.Record.ID, provider.Record.Concurrency)
		},
		TransportError: func(err error) error {
			safeErr := logredact.SanitizeUpstreamQueries(err.Error())
			SetOpsUpstreamError(c, 0, safeErr, "")
			writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
			return fmt.Errorf("responses input_tokens: upstream request failed: %s", safeErr)
		},
		ReadBody: s.readResponsesInputTokensBody,
		HTTPError: func(resp *http.Response, respBody []byte) error {
			// Native preflight treats any 404 as endpoint
			// incompatibility. OAuth scope evidence uses the same
			// local-estimate path before ordinary health or
			// failover policy.
			if resp.StatusCode == http.StatusNotFound || (provider.Record.Type == capability.ProviderTypeOAuth && isOpenAIOAuthInputTokensUnsupported(resp.StatusCode, respBody)) {
				writeOpenAIResponsesInputTokensFallback(c, provider, prepared, resp.StatusCode, "upstream_unsupported")
				return nil
			}
			return s.handleResponsesInputTokensUpstreamError(ctx, c, provider, prepared, resp, respBody)
		},
		WriteError: func(status int, kind, message string) { writeOpenAIResponsesInputTokensError(c, status, kind, message) },
	}, ResponseSink{Writer: c.Writer})
}

// writeOpenAIResponsesInputTokensFallback writes the native shape of a local
// count estimate. Missing prepared input, estimator errors, and nonpositive
// results use the minimum. Provider and model logging tolerate absent metadata;
// the response includes object response.input_tokens and the selected positive
// count.
func writeOpenAIResponsesInputTokensFallback(c *gin.Context, provider *gatewayprovider.ExecutionProvider, prepared *gatewayprovider.InputTokensPrepared, statusCode int, reason string) {
	// Missing or unusable prepared input cannot provide a positive count.
	// Keep the compatibility minimum unless estimation succeeds with a
	// positive token count.
	estimated := openAIInputTokensFallbackMinimum
	if prepared != nil {
		if got, err := tokenestimate.Responses(prepared.Request); err == nil && got > 0 {
			estimated = got
		}
	}
	providerID := int64(0)
	model := ""
	if provider != nil {
		providerID = provider.Record.ID
	}
	if prepared != nil {
		model = prepared.UpstreamModel
	}
	logging.L().Info("openai responses input_tokens: local estimate fallback",
		zap.Int64("provider_id", providerID),
		zap.Int("upstream_status", statusCode),
		zap.Int("estimated_input_tokens", estimated),
		zap.String("upstream_model", model),
		zap.String("reason", reason),
	)
	c.JSON(http.StatusOK, gin.H{
		"object":       "response.input_tokens",
		"input_tokens": estimated,
	})
}

// writeOpenAIResponsesInputTokensError writes native preflight failures with
// OpenAI error framing.
func writeOpenAIResponsesInputTokensError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{"error": gin.H{"type": errType, "message": message}})
}

// readResponsesInputTokensBody returns nonempty bytes captured by the shared
// error-body reader. That reader applies its byte limit and discards read
// errors, so returned bytes can be partial. An empty capture becomes a counting
// read error.
func (s *OpenAIAuxiliary) readResponsesInputTokensBody(resp *http.Response) ([]byte, error) {
	// The shared reader can return partial bytes after a read error or size
	// limit; this adapter rejects empty captures and leaves numeric-count
	// validation upstream.
	body := s.Output.ReadErrorBody(resp)
	if len(body) == 0 {
		return nil, fmt.Errorf("responses input_tokens: empty upstream response")
	}
	return body, nil
}

// handleResponsesInputTokensUpstreamError applies health and retry policy to a
// captured count failure. Callers pass prepared input and response metadata
// after compatibility recovery has been ruled out. Generic-policy decisions
// write a 500; retry decisions return captured body/header evidence, and
// remaining failures write safe client-facing output.
func (s *OpenAIAuxiliary) handleResponsesInputTokensUpstreamError(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	prepared *gatewayprovider.InputTokensPrepared,
	resp *http.Response,
	body []byte,
) error {
	upstreamMsg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(body)))
	var decision providercore.UpstreamErrorDecision
	if provider.Record.Platform == capability.PlatformGrok {
		decision = gatewayprovider.ApplyGrokExecutionHealth(ctx, s.Output.GrokHealth, provider, resp.StatusCode, resp.Header, body, "", prepared.UpstreamModel)
	} else {
		decision = gatewayprovider.ApplyOpenAIResponseHealth(ctx, s.Output.Health, provider, resp.StatusCode, resp.Header, body, false, prepared.UpstreamModel)
	}
	if decision.ShouldReturnGenericError() {
		writeOpenAIResponsesInputTokensError(c, http.StatusInternalServerError, "upstream_error", "Upstream gateway error")
		return fmt.Errorf("responses input_tokens: upstream error %d (custom policy)", resp.StatusCode)
	}
	defaultFailover := gatewayprovider.ShouldFailoverOpenAIResponse(resp.StatusCode, upstreamMsg, body)
	if provider.Record.Platform == capability.PlatformGrok {
		defaultFailover = gatewayprovider.ShouldFailoverGrokResponse(resp.StatusCode, body)
	}
	// Return retry classification with captured response evidence before
	// writing a client error, keeping the preflight loop's provider-switch
	// opportunity available for this failure.
	if decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode, defaultFailover) {
		return &forwardcore.UpstreamFailoverError{
			StatusCode:              resp.StatusCode,
			ResponseBody:            body,
			ResponseHeaders:         resp.Header.Clone(),
			RetryableOnSameProvider: decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode),
		}
	}
	SetOpsUpstreamError(c, resp.StatusCode, upstreamMsg, "")
	message := "Upstream request failed"
	if resp.StatusCode == http.StatusTooManyRequests {
		message = "Rate limit exceeded"
	} else if resp.StatusCode >= 500 {
		message = "Upstream service temporarily unavailable"
	}
	writeOpenAIResponsesInputTokensError(c, resp.StatusCode, "upstream_error", message)
	if upstreamMsg == "" {
		return fmt.Errorf("responses input_tokens: upstream error %d", resp.StatusCode)
	}
	return fmt.Errorf("responses input_tokens: upstream error %d message=%s", resp.StatusCode, upstreamMsg)
}
