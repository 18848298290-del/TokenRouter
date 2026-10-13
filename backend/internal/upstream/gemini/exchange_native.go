package gemini

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// Native Gemini retries can return a local count estimate after transport or
// eligible status attempts are exhausted. Error policy and recognized scope
// failures stop before that fallback, leaving the caller to handle their
// selected response.
//
// Every attempt rebuilds the request and its ID-header selection. Retried
// error bodies close after capture and final error responses are reconstructed
// from captured bytes. Status 429 is reported before retry delay so caller
// health state can affect concurrent requests. A count estimate carries its
// own result pointer, while normal final responses transfer body ownership to
// the caller.

// ExchangeNative returns the selected upstream response and request-ID header
// after its total attempt budget. Callers supply callbacks for the selected
// ExchangeOptions paths and close the returned body.
//
// Policy matches stop before status retry, scope-rejected 403 responses are
// rebuilt and returned, and 429 invokes caller observation before waiting.
// Transport retries use the same attempt count and an uninterruptible backoff
// sleep. Native fallback can instead return an independent estimated token
// count after eligible retry exhaustion; that result is separate from server
// usage.
func ExchangeNative(ctx context.Context, options ExchangeOptions) (ExchangeResult, error) {
	requestIDHeader := options.RequestIDHeader
	var resp *http.Response
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
			// After the retry budget ends, a native count fallback returns its local
			// estimate separately from usage and avoids exposing the final transport
			// failure as generation output.
			if options.CountFallback {
				estimated := options.EstimateCount()
				return ExchangeResult{EstimatedTokens: &estimated}, nil
			}
			options.SetError(0, safeErr, "")
			return ExchangeResult{}, options.FinalError("Upstream request failed after retries: " + safeErr)
		}

		// Apply error policy before ordinary status retry so a matched rule keeps
		// control over the chosen response and prevents another provider-local
		// attempt.
		matched, rebuilt := options.CheckPolicy(ctx, resp)
		resp = rebuilt
		if matched {
			break
		}

		if resp.StatusCode >= 400 && options.ShouldRetry(resp.StatusCode) {
			respBody := options.ReadError(resp)
			_ = resp.Body.Close()
			// Rebuild the consumed scope-rejection body and return it because another
			// retry with the same bearer permissions cannot resolve the authorization
			// requirement.
			if resp.StatusCode == http.StatusForbidden && IsGeminiInsufficientScope(resp.Header, respBody) {
				resp = &http.Response{
					StatusCode: resp.StatusCode,
					Header:     resp.Header.Clone(),
					Body:       io.NopCloser(bytes.NewReader(respBody)),
				}
				break
			}
			// Publish the throttle observation before waiting so other caller requests
			// can react to this provider cooldown while the current attempt is delayed.
			if resp.StatusCode == http.StatusTooManyRequests {
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
			// After the eligible HTTP retry budget ends, return the configured count
			// estimate separately from usage; policy matches and recognized scope
			// responses already selected their own earlier outcomes.
			if options.CountFallback {
				estimated := options.EstimateCount()
				return ExchangeResult{EstimatedTokens: &estimated}, nil
			}
			// Reconstruct the final error response from captured bytes because its
			// original body was consumed and closed during retry handling; native
			// callers still need readable error content.
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
