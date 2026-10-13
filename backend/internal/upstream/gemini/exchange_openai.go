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

// OpenAI-compatible Gemini requests retry before their output adapter starts
// conversion. Error-policy matches stop before ordinary status retry
// selection, and recognized insufficient-scope failures retain a readable
// response for caller error handling.
//
// Each attempt rebuilds the request and selects its request-ID header. Error
// bodies are captured and closed before retry, then rebuilt when retries end.
// Throttling observations reach caller state before sleeping; transport errors
// consume the same total attempt budget. The caller closes the selected final
// body and performs protocol conversion after this exchange returns.

// ExchangeOpenAI returns the selected upstream response and request-ID header
// after its total attempt budget. Callers supply callbacks for the selected
// ExchangeOptions paths and close the returned body.
//
// Policy matches stop before status retry, scope-rejected 403 responses are
// rebuilt and returned, and 429 invokes caller observation before waiting.
// Transport retries use the same attempt count and an uninterruptible backoff
// sleep. Final transport failure returns the caller-mapped error, while final
// HTTP failure retains captured bytes for protocol error mapping.
func ExchangeOpenAI(ctx context.Context, options ExchangeOptions) (ExchangeResult, error) {
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
				logger.LegacyPrintf("service.gemini_chat_completions", "Gemini provider %d: upstream request failed, retry %d/%d: %v", options.ProviderID, attempt, options.MaxRetries, err)
				SleepGeminiBackoff(attempt)
				continue
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
				options.Observe(ExchangeNotice{
					Platform:           options.Platform,
					ProviderID:         options.ProviderID,
					ProviderName:       options.ProviderName,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  upstreamReqID,
					Kind:               "retry",
					Message:            upstreamMsg,
				})
				logger.LegacyPrintf("service.gemini_chat_completions", "Gemini provider %d: upstream status %d, retry %d/%d", options.ProviderID, resp.StatusCode, attempt, options.MaxRetries)
				SleepGeminiBackoff(attempt)
				continue
			}
			// Reconstruct the final captured error body because classification consumed
			// the original stream; the caller still needs readable bytes for
			// OpenAI-compatible error conversion.
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
