package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	protocolforward "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tokenestimate"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Messages-compatible counting converts Anthropic input to the Responses
// counting format while retaining client, billing, and upstream model names in
// preparation. Grok and CN targets instead estimate Anthropic input locally.
// Other targets send a controlled input_tokens request and return the count
// using Anthropic's response shape, independently of generation completion.
//
// OAuth scope and endpoint-compatibility failures select local Responses
// estimation before provider-health handling. A positive estimate becomes the
// result; failed or nonpositive estimation uses a minimum count. Other HTTP
// failures apply the selected provider's error policy and can return failover
// evidence to the count caller, while an unsupported API-key endpoint receives
// a handled 404 response.

// openaiPlatformAPIInputTokensURL selects the official count endpoint, and
// openAIInputTokensFallbackMinimum supplies a positive result when
// compatibility estimation fails or reports no tokens.
const (
	// openaiPlatformAPIInputTokensURL is the public Responses count
	// endpoint.
	openaiPlatformAPIInputTokensURL = "https://api.openai.com/v1/responses/input_tokens"
	// openAIInputTokensFallbackMinimum is the minimum handled estimate.
	openAIInputTokensFallbackMinimum = 1
)

// ForwardCountTokensAsAnthropic returns a Messages-compatible count using the
// selected provider. Grok and CN targets estimate Anthropic input locally.
// Other targets convert the body and map its model before credential resolution
// and upstream counting; the upstream counter closes its response body before
// returning.
//
// OAuth incompatibility uses local estimation without reaching provider-health
// updates. Unsupported API-key counting writes 404 and returns nil, while other
// HTTP failures can return classified failover errors or write terminal output.
// Callers therefore inspect the written response as well as the returned error.
func (s *OpenAIAuxiliary) ForwardCountTokensAsAnthropic(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	defaultMappedModel string,
) error {
	if provider == nil {
		writeAnthropicCountTokensError(c, http.StatusServiceUnavailable, "api_error", "No available OpenAI providers")
		return fmt.Errorf("count_tokens: missing provider")
	}

	// Grok and CN counts use the local Anthropic estimator regardless of
	// configured upstream protocol, leaving their inference transport and
	// health state unused here.
	if provider.View().IsCNProvider() || provider.View().IsGrok() {
		estimated, err := tokenestimate.Anthropic(body)
		if err != nil {
			writeAnthropicCountTokensError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
			return fmt.Errorf("count_tokens: estimate cn provider input tokens: %w", err)
		}
		logging.L().Debug("openai count_tokens: cn provider local estimate",
			zap.Int64("provider_id", provider.Record.ID),
			zap.Int("estimated_input_tokens", estimated),
		)
		c.JSON(http.StatusOK, gin.H{
			"input_tokens": estimated,
		})
		return nil
	}

	prepared, err := gatewayprovider.PrepareAnthropicInputTokens(body, provider, defaultMappedModel)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return err
	}

	upstreamBody, err := wirejson.Marshal(prepared.Request)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("marshal openai input_tokens body: %w", err)
	}

	logging.L().Debug("openai count_tokens: model mapping applied",
		zap.Int64("provider_id", provider.Record.ID),
		zap.String("original_model", prepared.OriginalModel),
		zap.String("normalized_model", prepared.NormalizedModel),
		zap.String("billing_model", prepared.BillingModel),
		zap.String("upstream_model", prepared.UpstreamModel),
	)

	token, _, err := s.Requests.Credentials.Resolve(ctx, gatewayprovider.ExecutionRecord(provider))
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to get access token")
		return fmt.Errorf("get access token: %w", err)
	}

	upstreamReq, err := s.buildInputTokensUpstreamRequest(ctx, c, provider, upstreamBody, token)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("build input_tokens request: %w", err)
	}

	proxyURL := ""
	if provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}
	return openai.CountInputTokens(upstreamReq, openai.InputTokensOptions{
		Enter: s.Enter,
		Do: func(req *http.Request) (*http.Response, error) {
			return s.Requests.Transport.Do(req, proxyURL, provider.Record.ID, provider.Record.Concurrency)
		},
		TransportError: func(err error) error {
			safeErr := logredact.SanitizeUpstreamQueries(err.Error())
			SetOpsUpstreamError(c, 0, safeErr, "")
			AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
				Platform:           provider.Record.Platform,
				ProviderID:         provider.Record.ID,
				ProviderName:       provider.Record.Name,
				UpstreamStatusCode: 0,
				Kind:               "request_error",
				Message:            safeErr,
			})
			writeAnthropicCountTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
			return fmt.Errorf("openai input_tokens upstream request failed: %s", safeErr)
		},
		HTTPError: func(resp *http.Response, respBody []byte) error {
			upstreamMsg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(respBody)))
			// Scope and endpoint incompatibility can block OAuth
			// counting despite usable inference credentials. Handle
			// those counts locally before any provider-health
			// update.
			if provider.Record.Type == capability.ProviderTypeOAuth && isOpenAIOAuthInputTokensUnsupported(resp.StatusCode, respBody) {
				writeOpenAIOAuthInputTokensFallback(c, provider, prepared, resp.StatusCode)
				return nil
			}
			// An explicitly absent API-key count endpoint is a
			// handled client 404, so the single-count caller stops
			// without treating it as another exchange failure.
			if isOpenAIInputTokensUnsupported(resp.StatusCode, respBody) {
				writeAnthropicCountTokensError(c, http.StatusNotFound, "not_found_error", "Token counting is not supported by upstream")
				return nil
			}
			var decision providercore.UpstreamErrorDecision
			if provider.Record.Platform == capability.PlatformGrok {
				decision = gatewayprovider.ApplyGrokExecutionHealth(ctx, s.Output.GrokHealth, provider, resp.StatusCode, resp.Header, respBody, "", prepared.UpstreamModel)
			} else {
				decision = gatewayprovider.ApplyOpenAIResponseHealth(ctx, s.Output.Health, provider, resp.StatusCode, resp.Header, respBody, false, prepared.UpstreamModel)
			}
			if decision.ShouldReturnGenericError() {
				writeAnthropicCountTokensError(c, http.StatusInternalServerError, "upstream_error", "Upstream gateway error")
				return fmt.Errorf("input_tokens upstream error: %d (not in custom error codes)", resp.StatusCode)
			}
			defaultFailover := gatewayprovider.ShouldFailoverOpenAIResponse(resp.StatusCode, upstreamMsg, respBody)
			if provider.Record.Platform == capability.PlatformGrok {
				defaultFailover = gatewayprovider.ShouldFailoverGrokResponse(resp.StatusCode, respBody)
			}
			// Keep the captured HTTP body and headers with retry
			// classification, letting an outer counting caller
			// distinguish provider recovery from already-written
			// terminal output.
			if decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode, defaultFailover) {
				return &protocolforward.UpstreamFailoverError{
					StatusCode:              resp.StatusCode,
					ResponseBody:            respBody,
					ResponseHeaders:         resp.Header.Clone(),
					RetryableOnSameProvider: decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode),
				}
			}

			upstreamDetail := ""
			if s.Output.Options.LogUpstreamErrorBody {
				maxBytes := s.Output.Options.LogUpstreamErrorBodyMaxBytes
				if maxBytes <= 0 {
					maxBytes = 2048
				}
				// Bound optional diagnostic body detail at a
				// UTF-8 character boundary, while retry
				// classification above retains the captured
				// response bytes for its own handling.
				upstreamDetail = logredact.TruncateUTF8(string(respBody), maxBytes)
			}
			SetOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
			AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
				Platform:           provider.Record.Platform,
				ProviderID:         provider.Record.ID,
				ProviderName:       provider.Record.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				Kind:               "request_error",
				Message:            upstreamMsg,
				Detail:             upstreamDetail,
			})

			errMsg := "Upstream request failed"
			switch resp.StatusCode {
			case http.StatusTooManyRequests:
				errMsg = "Rate limit exceeded"
			case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
				errMsg = "Upstream service temporarily unavailable"
			}
			writeAnthropicCountTokensError(c, resp.StatusCode, "upstream_error", errMsg)
			if upstreamMsg == "" {
				return fmt.Errorf("input_tokens upstream error: %d", resp.StatusCode)
			}
			return fmt.Errorf("input_tokens upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
		},
		WriteError: func(status int, kind, message string) { writeAnthropicCountTokensError(c, status, kind, message) },
	}, ResponseSink{Writer: c.Writer})
}

// buildInputTokensUpstreamRequest constructs a counting POST with
// selected-provider authentication. API-key targets may replace the official
// base after validation. Inbound user-agent and language headers are read when
// available, and the upstream builder applies provider user-agent and header
// overrides afterward.
func (s *OpenAIAuxiliary) buildInputTokensUpstreamRequest(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	token string,
) (*http.Request, error) {
	targetURL := openaiPlatformAPIInputTokensURL
	if provider.Record.Type == capability.ProviderTypeAPIKey {
		if baseURL := gatewayprovider.ExecutionProtocolTarget(provider).GetOpenAIBaseURL(); strings.TrimSpace(baseURL) != "" {
			validatedURL, err := s.Requests.ValidateBaseURL(baseURL)
			if err != nil {
				return nil, err
			}
			targetURL = httpclient.BuildOpenAIResponsesInputTokensURL(validatedURL)
		}
	}

	options := s.Requests.ResponseOptions(ctx, c, provider, token, targetURL, false)
	options.ForwardHeaders = func() http.Header {
		if c == nil || c.Request == nil {
			return nil
		}
		return c.Request.Header
	}
	return openai.BuildInputTokensRequest(ctx, body, options, provider.View().GetOpenAIUserAgent)
}

// writeAnthropicCountTokensError writes the count failure using Anthropic error
// framing.
func writeAnthropicCountTokensError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// isOpenAIInputTokensUnsupported recognizes a 404 explicitly describing an
// absent input_tokens endpoint. It lowercases extracted error text and requires
// both the endpoint name and not-found wording; a generic 404 remains outside
// this predicate.
func isOpenAIInputTokensUnsupported(statusCode int, body []byte) bool {
	if statusCode != http.StatusNotFound {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(upstream.ExtractErrorMessage(body)))
	return strings.Contains(msg, "input_tokens") && strings.Contains(msg, "not found")
}

// writeOpenAIOAuthInputTokensFallback writes a local count after OAuth counting
// incompatibility. Callers supply nonnil prepared input and provider metadata.
// A positive estimate is returned; failed or nonpositive estimation writes the
// minimum count and logs the estimate outcome with upstream status and model.
func writeOpenAIOAuthInputTokensFallback(c *gin.Context, provider *gatewayprovider.ExecutionProvider, prepared *gatewayprovider.InputTokensPrepared, statusCode int) {
	// An unavailable or zero estimate still needs a positive compatibility
	// count. Keep the minimum unless the prepared Responses estimator
	// supplies positive evidence.
	estimated := openAIInputTokensFallbackMinimum
	if got, err := tokenestimate.Responses(prepared.Request); err == nil {
		if got > 0 {
			estimated = got
		}
		logging.L().Info("openai count_tokens: oauth fallback to local tiktoken estimate",
			zap.Int64("provider_id", provider.Record.ID),
			zap.Int("upstream_status", statusCode),
			zap.Int("estimated_input_tokens", estimated),
			zap.String("upstream_model", prepared.UpstreamModel),
		)
	} else {
		logging.L().Warn("openai count_tokens: oauth local tiktoken fallback failed, using minimum estimate",
			zap.Int64("provider_id", provider.Record.ID),
			zap.Int("upstream_status", statusCode),
			zap.Int("estimated_input_tokens", estimated),
			zap.String("upstream_model", prepared.UpstreamModel),
			zap.Error(err),
		)
	}

	c.JSON(http.StatusOK, gin.H{
		"input_tokens": estimated,
	})
}

// isOpenAIOAuthInputTokensUnsupported identifies scope or endpoint failures
// eligible for local estimation. Only 401, 403, and 404 are considered.
// Missing-scope evidence, an HTML 403, and recognized input_tokens unsupported
// text select compatibility recovery before ordinary provider-health handling.
func isOpenAIOAuthInputTokensUnsupported(statusCode int, body []byte) bool {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
	default:
		return false
	}

	bodyLower := strings.ToLower(string(body))
	msg := strings.ToLower(strings.TrimSpace(upstream.ExtractErrorMessage(body)))
	code := strings.ToLower(strings.TrimSpace(upstream.ExtractErrorCode(body)))

	if code == "missing_scope" ||
		strings.Contains(bodyLower, "api.responses.write") ||
		strings.Contains(bodyLower, "missing scopes") ||
		strings.Contains(bodyLower, "insufficient_scope") {
		return true
	}

	if statusCode == http.StatusNotFound && isOpenAIInputTokensUnsupported(statusCode, body) {
		return true
	}

	// HTML 403 bodies are classified as counting incompatibility on this
	// OAuth path, selecting local estimation before the forwarding adapter
	// applies provider-health policy.
	if statusCode == http.StatusForbidden && upstream.IsHTMLResponse(body) {
		return true
	}

	return strings.Contains(msg, "input_tokens") &&
		(strings.Contains(msg, "not found") ||
			strings.Contains(msg, "not supported") ||
			strings.Contains(msg, "unsupported"))
}
