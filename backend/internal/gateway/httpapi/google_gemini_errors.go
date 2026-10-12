package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/googleforward"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	gemininative "github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

// Gemini failures reach clients through native Google, Messages or OpenAI-
// compatible envelopes. Error observation retains the upstream status and
// request identity before final presentation. Native output can deliver an
// unwrapped upstream body, while compatibility output combines Google error
// categories, HTTP status and configured response rules.
//
// Messages mapping lets recognized Google categories supply selected status
// overrides before HTTP defaults fill missing fields. OpenAI compatibility has
// its own status precedence and permits a nonempty upstream message for pool
// providers. Commit markers and route-specific Responses output keep outer
// error handling from appending another response after a final error has
// already been presented.

// GeminiOutput holds the current HTTP request and fixed output options.
type GeminiOutput struct {
	GoogleOutput                       // synchronous request output
	Options      googleforward.Options // fixed error presentation settings
}

// GeminiCustomCodeSkippedError records the rejected upstream status, request
// identity and sanitized diagnostics, then invokes write once. The caller
// supplies its generic response writer and remains responsible for that
// writer's commit behavior.
func (s *GeminiOutput) GeminiCustomCodeSkippedError(provider *gatewayprovider.ExecutionProvider, upstreamStatus int, upstreamRequestID string, body []byte, write func()) error {
	c := s.Context

	upstreamMsg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(body)))
	upstreamDetail := s.Options.ErrorDetail(body)
	SetOpsUpstreamError(c, upstreamStatus, upstreamMsg, upstreamDetail)
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
		Platform: provider.Record.Platform,

		ProviderID: provider.Record.ID,

		ProviderName: provider.Record.Name,

		UpstreamStatusCode: upstreamStatus,

		UpstreamRequestID: upstreamRequestID,

		Kind: "http_error",

		Message: upstreamMsg,

		Detail: upstreamDetail,
	})
	write()
	if upstreamMsg == "" {
		return fmt.Errorf("gemini upstream error: %d (not in custom error codes)", upstreamStatus)
	}
	return fmt.Errorf("gemini upstream error: %d (not in custom error codes) message=%s", upstreamStatus, upstreamMsg)
}

// GeminiNativeUpstreamError unwraps an OAuth body when needed and writes it
// with the upstream status. Diagnostics are recorded before committing the
// response, and absent Content-Type defaults to JSON; the response remains
// caller-owned.
func (s *GeminiOutput) GeminiNativeUpstreamError(provider *gatewayprovider.ExecutionProvider, resp *http.Response, respBody []byte, requestID string, isOAuth bool) error {
	c := s.Context

	respBody = gemininative.UnwrapIfNeeded(isOAuth, respBody)
	upstreamMsg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(respBody)))
	upstreamDetail := s.Options.ErrorDetail(respBody)
	SetOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
		Platform: provider.Record.Platform,

		ProviderID: provider.Record.ID,

		ProviderName: provider.Record.Name,

		UpstreamStatusCode: resp.StatusCode,

		UpstreamRequestID: requestID,

		Kind: "http_error",

		Message: upstreamMsg,

		Detail: upstreamDetail,
	})
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	MarkResponseCommitted(c)
	c.Data(resp.StatusCode, contentType, respBody)
	if upstreamMsg == "" {
		return fmt.Errorf("gemini upstream error: %d", resp.StatusCode)
	}
	return fmt.Errorf("gemini upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
}

// GeminiMappedError presents a Gemini failure through the Messages envelope
// after recording sanitized upstream diagnostics. The provider and Gin context
// are required, and the final response is marked committed before output. A
// matching administrator rule supplies the client status, type and message
// before built-in mapping applies.
//
// Without a rule, recognized Google status fields supply an error type and
// selected status overrides. HTTP-status defaults fill the remaining fields;
// HTTP 400 can expose the sanitized upstream message, while authentication and
// permission failures use configured generic text. The returned error retains
// diagnostic status and message for the caller even after response
// presentation.
func (s *GeminiOutput) GeminiMappedError(provider *gatewayprovider.ExecutionProvider, upstreamStatus int, upstreamRequestID string, body []byte) error {
	c := s.Context

	MarkResponseCommitted(c)
	upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(body))
	upstreamMsg = logredact.SanitizeUpstreamQueries(upstreamMsg)
	upstreamDetail := ""
	if s.Options.LogErrorBody {
		maxBytes := s.Options.LogErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		// Bound diagnostic retention when configured logging is enabled; the full
		// supplied body still participates in error-rule matching and category mapping.
		upstreamDetail = logredact.TruncateUTF8(string(body), maxBytes)
	}
	SetOpsUpstreamError(c, upstreamStatus, upstreamMsg, upstreamDetail)
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
		Platform: provider.Record.Platform,

		ProviderID: provider.Record.ID,

		ProviderName: provider.Record.Name,

		UpstreamStatusCode: upstreamStatus,

		UpstreamRequestID: upstreamRequestID,

		Kind: "http_error",

		Message: upstreamMsg,

		Detail: upstreamDetail,
	})

	if s.Options.LogErrorBody {
		logging.LegacyPrintf("service.gemini_messages_compat", "[Gemini] upstream error %d: %s", upstreamStatus, logredact.TruncateLine(body, s.Options.LogErrorBodyMaxBytes))
	}

	if status, errType, errMsg, matched := ApplyErrorPassthroughRule(
		c,
		capability.PlatformGemini,
		upstreamStatus,
		body,
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	); matched {
		c.JSON(status, gin.H{
			"type":  "error",
			"error": gin.H{"type": errType, "message": errMsg},
		})
		if upstreamMsg == "" {
			upstreamMsg = errMsg
		}
		if upstreamMsg == "" {
			return fmt.Errorf("upstream error: %d (passthrough rule matched)", upstreamStatus)
		}
		return fmt.Errorf("upstream error: %d (passthrough rule matched) message=%s", upstreamStatus, upstreamMsg)
	}

	var statusCode int
	var errType, errMsg string

	// Messages mapping fills body-derived category and status first; HTTP defaults
	// below complete fields left unset while preserving those supplied overrides.
	if mapped := MapGeminiErrorBodyToClaudeError(body); mapped != nil {
		errType = mapped.Type
		if mapped.Message != "" {
			errMsg = mapped.Message
		}
		if mapped.StatusCode > 0 {
			statusCode = mapped.StatusCode
		}
	}

	switch upstreamStatus {
	case 400:
		if statusCode == 0 {
			statusCode = http.StatusBadRequest
		}
		if errType == "" {
			errType = "invalid_request_error"
		}
		if errMsg == "" {
			if upstreamMsg != "" {
				errMsg = upstreamMsg
			} else {
				errMsg = "Invalid request"
			}
		}
	case 401:
		if statusCode == 0 {
			statusCode = http.StatusBadGateway
		}
		if errType == "" {
			errType = "authentication_error"
		}
		if errMsg == "" {
			errMsg = "Upstream authentication failed, please contact administrator"
		}
	case 403:
		if statusCode == 0 {
			statusCode = http.StatusBadGateway
		}
		if errType == "" {
			errType = "permission_error"
		}
		if errMsg == "" {
			errMsg = "Upstream access forbidden, please contact administrator"
		}
	case 404:
		if statusCode == 0 {
			statusCode = http.StatusNotFound
		}
		if errType == "" {
			errType = "not_found_error"
		}
		if errMsg == "" {
			errMsg = "Resource not found"
		}
	case 429:
		if statusCode == 0 {
			statusCode = http.StatusTooManyRequests
		}
		if errType == "" {
			errType = "rate_limit_error"
		}
		if errMsg == "" {
			errMsg = "Upstream rate limit exceeded, please retry later"
		}
	case 529:
		if statusCode == 0 {
			statusCode = http.StatusServiceUnavailable
		}
		if errType == "" {
			errType = "overloaded_error"
		}
		if errMsg == "" {
			errMsg = "Upstream service overloaded, please retry later"
		}
	case 500, 502, 503, 504:
		if statusCode == 0 {
			statusCode = http.StatusBadGateway
		}
		if errType == "" {
			switch upstreamStatus {
			case 504:
				errType = "timeout_error"
			case 503:
				errType = "overloaded_error"
			default:
				errType = "api_error"
			}
		}
		if errMsg == "" {
			errMsg = "Upstream service temporarily unavailable"
		}
	default:
		if statusCode == 0 {
			statusCode = http.StatusBadGateway
		}
		if errType == "" {
			errType = "upstream_error"
		}
		if errMsg == "" {
			errMsg = "Upstream request failed"
		}
	}

	c.JSON(statusCode, gin.H{
		"type":  "error",
		"error": gin.H{"type": errType, "message": errMsg},
	})
	if upstreamMsg == "" {
		return fmt.Errorf("upstream error: %d", upstreamStatus)
	}
	return fmt.Errorf("upstream error: %d message=%s", upstreamStatus, upstreamMsg)
}

// ClaudeError commits and writes the supplied Messages error envelope, then
// returns a message error. Callers invoke it for final presentation after
// classifying whether this attempt can still be retried.
func (s *GeminiOutput) ClaudeError(status int, errType, message string) error {
	c := s.Context

	MarkResponseCommitted(c)
	c.JSON(status, gin.H{
		"type":  "error",
		"error": gin.H{"type": errType, "message": message},
	})
	return fmt.Errorf("%s", message)
}

// GoogleError commits a native Google envelope with the shared status-name
// mapping. It returns the supplied message as an error after writing, allowing
// execution to report the same failure to its caller.
func (s *GeminiOutput) GoogleError(status int, message string) error {
	c := s.Context

	MarkResponseCommitted(c)
	c.JSON(status, gin.H{
		"error": gin.H{
			"code":    status,
			"message": message,
			"status":  HTTPStatusToGoogleStatus(status),
		},
	})
	return fmt.Errorf("%s", message)
}

// GeminiOpenAICompatMappedError records Gemini upstream diagnostics and
// chooses an OpenAI-compatible client error. A nonnil provider additionally
// contributes an Ops event; rule matching and body interpretation use the
// Gemini platform regardless of provider metadata. Administrator response
// rules take precedence over built-in message and status selection.
//
// Google error fields seed the compatible category and selected status
// overrides, then recognized HTTP statuses apply this path's own defaults. A
// nonempty upstream message can replace the generic default, and a pool
// provider exposes that message for every status at or above 400. The protocol
// argument selects Responses or Chat presentation after this decision.
func (s *GeminiOutput) GeminiOpenAICompatMappedError(
	provider *gatewayprovider.ExecutionProvider,
	upstreamStatus int,
	upstreamRequestID string,
	body []byte,
	protocol gemininative.OpenAICompatProtocol,
) error {
	c := s.Context

	upstreamMsg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(body)))
	SetOpsUpstreamError(c, upstreamStatus, upstreamMsg, "")
	if provider != nil {
		AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
			Platform: provider.Record.Platform,

			ProviderID: provider.Record.ID,

			ProviderName: provider.Record.Name,

			UpstreamStatusCode: upstreamStatus,

			UpstreamRequestID: upstreamRequestID,

			Kind: "http_error",

			Message: upstreamMsg,
		})
	}

	if status, errType, errMsg, matched := ApplyErrorPassthroughRule(
		c,
		capability.PlatformGemini,
		upstreamStatus,
		body,
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	); matched {
		return s.GeminiOpenAICompatError(protocol, status, errType, errMsg)
	}

	statusCode := http.StatusBadGateway
	errType := "upstream_error"
	errMsg := "Upstream request failed"
	// Compatible mapping starts from the body category; the HTTP branch below
	// has its own precedence for 404, 429 and overloaded upstream responses.
	if mapped := MapGeminiErrorBodyToClaudeError(body); mapped != nil {
		if mapped.Type != "" {
			errType = mapped.Type
		}
		if mapped.Message != "" {
			errMsg = mapped.Message
		}
		if mapped.StatusCode > 0 {
			statusCode = mapped.StatusCode
		}
	}

	switch upstreamStatus {
	case http.StatusBadRequest:
		if statusCode == http.StatusBadGateway {
			statusCode = http.StatusBadRequest
		}
		if errType == "upstream_error" {
			errType = "invalid_request_error"
		}
		if errMsg == "Upstream request failed" {
			errMsg = "Invalid request"
		}
	case http.StatusNotFound:
		statusCode = http.StatusNotFound
		if errType == "upstream_error" {
			errType = "not_found_error"
		}
		if errMsg == "Upstream request failed" {
			errMsg = "Resource not found"
		}
	case http.StatusTooManyRequests:
		statusCode = http.StatusTooManyRequests
		if errType == "upstream_error" {
			errType = "rate_limit_error"
		}
		if errMsg == "Upstream request failed" {
			errMsg = "Upstream rate limit exceeded, please retry later"
		}
	case 529:
		statusCode = http.StatusServiceUnavailable
		if errType == "upstream_error" {
			errType = "overloaded_error"
		}
		if errMsg == "Upstream request failed" {
			errMsg = "Upstream service overloaded, please retry later"
		}
	}

	if upstreamMsg != "" && errMsg == "Upstream request failed" {
		errMsg = upstreamMsg
	}
	// Pool failures expose their sanitized upstream message after built-in mapping,
	// helping clients distinguish pooled provider errors when no response rule matched.
	if provider != nil && provider.View().IsPoolMode() && upstreamStatus >= http.StatusBadRequest && upstreamMsg != "" {
		errMsg = upstreamMsg
	}
	return s.GeminiOpenAICompatError(protocol, statusCode, errType, errMsg)
}

// GeminiOpenAICompatError writes the envelope selected by the compatible
// client protocol. Responses uses ForwardConversionOutput and a commit
// callback; other values use ChatError, with the message returned as an error.
func (s *GeminiOutput) GeminiOpenAICompatError(
	protocol gemininative.OpenAICompatProtocol,
	status int,
	errType string,
	message string,
) error {
	c := s.Context

	if protocol == gemininative.OpenAICompatResponses {
		(&ForwardConversionOutput{Context: c, Responses: true, Commit: func() { MarkResponseCommitted(c) }}).Error(status, errType, message)
		return fmt.Errorf("%s", message)
	}
	return s.ChatError(status, errType, message)
}

// ChatError writes the supplied OpenAI Chat error type and message as JSON. It
// returns a message error after presentation; this method itself does not
// install a response-commit marker.
func (s *GeminiOutput) ChatError(status int, errType, message string) error {
	c := s.Context

	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
	return fmt.Errorf("%s", message)
}

// HTTPStatusToGoogleStatus returns the shared Google status name for an HTTP
// status.
func HTTPStatusToGoogleStatus(status int) string { return httpx.HTTPStatusToGoogleStatus(status) }
