package gemini

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
)

// Messages compatibility retries rebuild a Gemini request using caller-owned
// conversion state. Signature-related 400 responses can first discard thinking
// history and then tool history, and each successful downgrade consumes one
// slot from the same total attempt budget as ordinary retries.
//
// For an eligible 400, signature handling precedes general error policy and
// reconstructs the consumed response body when recovery stops. Other statuses
// apply policy before status-based retry selection. Retry waits use
// SleepGeminiBackoff, so caller cancellation is observed by request
// construction or transport rather than interrupting that sleep. Responses
// selected for the caller retain the chosen request-ID header name beside
// them.

// ExchangeNotice identifies an upstream exchange observation and its selected
// diagnostic fields.
type ExchangeNotice struct {
	// Platform identifies the selected platform; ProviderName identifies its
	// display name.
	Platform, ProviderName string
	ProviderID             int64 // selected provider audit identifier
	UpstreamStatusCode     int   // upstream HTTP status; zero for transport failure
	// UpstreamRequestID identifies the server request; Kind classifies
	// observation; Message supplies public-safe text; Detail supplies diagnostic
	// text.
	UpstreamRequestID, Kind, Message, Detail string
}

// ExchangeOptions supplies synchronous construction, retry policy,
// diagnostics, and body-recovery callbacks.
type ExchangeOptions struct {
	ProviderID int64 // selected provider audit identifier
	// ProviderName labels provider; Platform labels platform; RequestIDHeader
	// selects the initial request-ID header.
	ProviderName, Platform, RequestIDHeader string
	// MaxRetries is the total attempt count and should be positive. Zero performs
	// no exchange and returns a result with no response.
	MaxRetries int // total request attempt budget
	// CountFallback enables an estimate after native retry exhaustion.
	// EstimateCount is required when this flag is used, and the estimate remains
	// separate from usage.
	CountFallback bool // native token estimate selection
	// Build and Do are required on every attempt. Other callback fields are
	// called synchronously on their applicable error, policy, throttle, or
	// recovery path and callers supply those used by their selected exchange.
	// request and ID-header construction
	Build func(context.Context) (*http.Request, string, error)
	// Do returns a nonnil response and body on success. It owns cleanup for any
	// response returned together with an error because these retry branches do
	// not close that pair.
	// upstream HTTP exchange callback
	Do            func(*http.Request) (*http.Response, error)
	BuildError    func(error) error  // request construction error mapping
	FinalError    func(string) error // terminal transport error mapping
	EstimateCount func() int         // native fallback token estimation
	// error response body consumption
	ReadError func(*http.Response) []byte
	// CheckPolicy returns the readable response used for subsequent status
	// handling, including a nonnil replacement when matched. It owns any original
	// stream consumed or replaced during evaluation.
	// error-policy evaluation and response replacement
	CheckPolicy func(context.Context, *http.Response) (bool, *http.Response)
	ShouldRetry func(int) bool // HTTP status retry eligibility
	// early throttle observation callback
	OnStatus func(context.Context, int, http.Header, []byte)
	// exchange diagnostic observation callback
	Observe  func(ExchangeNotice)
	SetError func(int, string, string) // terminal error state callback
	// Message extracts error text; Detail extracts diagnostic body text.
	Message, Detail func([]byte) string
	Sanitize        func(string) string // diagnostic text redaction callback
	// FilterThinking and FilterTools supply the two Messages recovery stages in
	// that order. ReplaceBody publishes their successfully converted result for
	// the next Build call; callers serialize this evolving request state.
	// FilterThinking downgrades thinking history; FilterTools downgrades thinking
	// and tool history.
	FilterThinking, FilterTools func() []byte
	// ConvertRetry optionally overrides Messages recovery conversion. Nil selects
	// the native Claude-to-Gemini bridge with the dummy thought-signature option
	// for the downgraded request.
	// downgraded request conversion callback
	ConvertRetry func([]byte) ([]byte, error)
	ReplaceBody  func([]byte) // subsequent request body replacement
}

// ExchangeResult carries a selected response or a separate native token
// estimate.
type ExchangeResult struct {
	// Response and EstimatedTokens represent alternative outcomes. A nonnil
	// response body belongs to the caller; a native estimate outcome has no
	// response body to close.
	Response        *http.Response // caller-owned selected HTTP response
	RequestIDHeader string         // selected upstream ID-header name
	EstimatedTokens *int           // independent native token estimate
}

// ExchangeMessages returns the selected Messages-compatible upstream response
// and request-ID header after provider-local retries. Callers supply required
// ExchangeOptions callbacks and close selected bodies; discarded HTTP error
// bodies close before retry or reconstruction.
//
// Signature recovery runs before general policy for eligible 400 responses,
// trying thinking downgrade before tool downgrade and consuming the total
// attempt budget. Other statuses apply policy before retry rules, report 429
// early, and stop retrying recognized scope failures. A downgrade conversion
// on the final attempt can leave the previously closed response selected when
// the loop ends; later body handling then observes that selected response.
func ExchangeMessages(ctx context.Context, options ExchangeOptions) (ExchangeResult, error) {
	requestIDHeader := options.RequestIDHeader
	var resp *http.Response
	signatureRetryStage := 0
	// Rebuild the request and its ID-header selection for every attempt,
	// including after caller body replacement. Captured HTTP error bytes survive
	// stream closing before another request or final error mapping.
	for attempt := 1; attempt <= options.MaxRetries; attempt++ {
		upstreamReq, idHeader, err := options.Build(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return ExchangeResult{}, err
			}
			return ExchangeResult{}, options.BuildError(err)
		}
		requestIDHeader = idHeader

		resp, err = options.Do(upstreamReq)
		if err != nil {
			safeErr := options.Sanitize(err.Error())
			options.Observe(ExchangeNotice{
				Platform:           options.Platform,
				ProviderID:         options.ProviderID,
				ProviderName:       options.ProviderName,
				UpstreamStatusCode: 0,
				Kind:               "request_error",
				Message:            safeErr,
			})
			// Transport failures share the total attempt budget with HTTP retries;
			// wait before rebuilding so repeated failures cannot form an immediate
			// dispatch loop.
			if attempt < options.MaxRetries {
				logger.LegacyPrintf("service.gemini_messages_compat", "Gemini provider %d: upstream request failed, retry %d/%d: %v", options.ProviderID, attempt, options.MaxRetries, err)
				SleepGeminiBackoff(attempt)
				continue
			}
			options.SetError(0, safeErr, "")
			return ExchangeResult{}, options.FinalError("Upstream request failed after retries: " + safeErr)
		}

		// Inspect an eligible 400 before general policy so signature recovery can
		// rebuild the request from progressively downgraded Claude history using its
		// existing attempt budget.
		if resp.StatusCode == http.StatusBadRequest && signatureRetryStage < 2 {
			respBody := options.ReadError(resp)
			_ = resp.Body.Close()

			if IsGeminiSignatureRelatedError(respBody) {
				upstreamReqID := resp.Header.Get(requestIDHeader)
				if upstreamReqID == "" {
					upstreamReqID = resp.Header.Get("x-goog-request-id")
				}
				upstreamMsg := strings.TrimSpace(options.Message(respBody))
				upstreamMsg = options.Sanitize(upstreamMsg)
				upstreamDetail := options.Detail(respBody)
				options.Observe(ExchangeNotice{
					Platform:           options.Platform,
					ProviderID:         options.ProviderID,
					ProviderName:       options.ProviderName,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  upstreamReqID,
					Kind:               "signature_error",
					Message:            upstreamMsg,
					Detail:             upstreamDetail,
				})

				var strippedClaudeBody []byte
				stageName := ""
				switch signatureRetryStage {
				case 0:
					// Start with thinking-only downgrade so tool calls retain their
					// structured representation unless a later signature response requires
					// the broader recovery stage.
					strippedClaudeBody = options.FilterThinking()
					stageName = "thinking-only"
					signatureRetryStage = 1
				default:
					// A subsequent signature failure selects the broader tool-history
					// downgrade from caller state, trading structured tool replay for a
					// request the recovery converter can send.
					strippedClaudeBody = options.FilterTools()
					stageName = "thinking+tools"
					signatureRetryStage = 2
				}
				convert := options.ConvertRetry
				if convert == nil {
					convert = func(body []byte) ([]byte, error) {
						return bridge.NativeConvertClaudeMessagesToGeminiGenerateContent(bridge.NativeGeminiOptions{DummyThoughtSignature: "skip_thought_signature_validator"}, body)
					}
				}
				retryGeminiReq, txErr := convert(strippedClaudeBody)
				if txErr == nil {
					logger.LegacyPrintf("service.gemini_messages_compat", "Gemini provider %d: detected signature-related 400, retrying with downgraded Claude blocks (%s)", options.ProviderID, stageName)
					options.ReplaceBody(retryGeminiReq)
					// Publish the converted body for the next Build call and consume this
					// attempt slot. The shared counter still limits signature and ordinary
					// retries together.
					SleepGeminiBackoff(1)
					continue
				}
			}

			// Rebuild the consumed 400 body when recovery is ineligible or conversion
			// fails so caller error mapping can inspect the captured response text.
			resp = &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     resp.Header.Clone(),
				Body:       io.NopCloser(bytes.NewReader(respBody)),
			}
			break
		}

		// Use the policy-selected response before ordinary status retries so matched
		// administrator rules stop this exchange with the response shape they
		// produced.
		matched, rebuilt := options.CheckPolicy(ctx, resp)
		resp = rebuilt
		if matched {
			break
		}

		if resp.StatusCode >= 400 && options.ShouldRetry(resp.StatusCode) {
			respBody := options.ReadError(resp)
			_ = resp.Body.Close()
			// A scope-rejected 403 requires authorization changes. Rebuild its captured
			// body and return it instead of spending another delay on the same
			// credential scope.
			if resp.StatusCode == http.StatusForbidden && IsGeminiInsufficientScope(resp.Header, respBody) {
				resp = &http.Response{
					StatusCode: resp.StatusCode,
					Header:     resp.Header.Clone(),
					Body:       io.NopCloser(bytes.NewReader(respBody)),
				}
				break
			}
			if resp.StatusCode == http.StatusTooManyRequests {
				// Report throttling before sleeping so the caller can publish the cooldown
				// observation while this attempt is still waiting to retry.
				options.OnStatus(ctx, resp.StatusCode, resp.Header, respBody)
			}
			// Retry this classified HTTP failure while attempt slots remain; its body
			// is already captured and closed before waiting and constructing another
			// request.
			if attempt < options.MaxRetries {
				upstreamReqID := resp.Header.Get(requestIDHeader)
				if upstreamReqID == "" {
					upstreamReqID = resp.Header.Get("x-goog-request-id")
				}
				upstreamMsg := strings.TrimSpace(options.Message(respBody))
				upstreamMsg = options.Sanitize(upstreamMsg)
				upstreamDetail := options.Detail(respBody)
				options.Observe(ExchangeNotice{
					Platform:           options.Platform,
					ProviderID:         options.ProviderID,
					ProviderName:       options.ProviderName,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  upstreamReqID,
					Kind:               "retry",
					Message:            upstreamMsg,
					Detail:             upstreamDetail,
				})

				logger.LegacyPrintf("service.gemini_messages_compat", "Gemini provider %d: upstream status %d, retry %d/%d", options.ProviderID, resp.StatusCode, attempt, options.MaxRetries)
				SleepGeminiBackoff(attempt)
				continue
			}
			// Return a readable copy after exhausting retries because the original
			// error response body was consumed and closed during retry classification.
			resp = &http.Response{
				StatusCode: resp.StatusCode,
				Header:     resp.Header.Clone(),
				Body:       io.NopCloser(bytes.NewReader(respBody)),
			}
			break
		}

		break
	}

	return ExchangeResult{Response: resp, RequestIDHeader: requestIDHeader}, nil
}
