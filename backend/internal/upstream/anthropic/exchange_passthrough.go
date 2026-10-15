package anthropic

import (
	"bytes"
	"context"
	"net/http"
	"time"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// exchange_passthrough.go executes passthrough requests and synchronizes
// prepared bytes before provider-local retries.
// Passthrough requests can acquire beta-sanitized wire bytes during
// construction. When parsed input is present, those bytes are published before
// sending so the caller's request representation follows the payload actually
// dispatched.
//
// Provider-local retries apply to configured error statuses other than HTTP
// 400; passthrough leaves body-repair decisions out of that retry path. Each
// retry consumes diagnostic bytes, closes the discarded response, and waits
// under the caller context within the remaining elapsed budget. The final
// response remains open for its consumer. Transport errors close any
// accompanying response body, and the returned wire bytes identify the latest
// constructed request even when building, synchronization, or dispatch fails.

// ExchangePassthrough returns the selected API-key response and latest
// constructed wire bytes. With SynchronizeBody enabled, changed bytes pass
// through ReplaceBody and become the next retry input before dispatch. Callers
// supply the active callbacks, a positive MaxAttempts, and response-body
// cleanup after handoff.
//
// Retryable statuses other than HTTP 400 use Delay clipped to MaxElapsed.
// Diagnostic reads are best effort, discarded bodies close before waiting, and
// cancellation during that wait returns its error. Transport errors pass
// through TransportError after closing any accompanying body. Context release
// occurs after Build and before Do, requiring a still-usable request context.
func ExchangePassthrough(ctx context.Context, body []byte, options ExchangeOptions) (*http.Response, []byte, error) {
	var resp *http.Response
	lastWireBody := body
	retryStart := time.Now()
	for attempt := 1; attempt <= options.MaxAttempts; attempt++ {
		// Context release follows construction and precedes dispatch. The
		// callback therefore supplies a request context that remains usable
		// when its release function returns.
		upstreamCtx, releaseUpstreamCtx := options.Context(ctx, options.Stream)
		upstreamReq, wireBody, err := options.Build(upstreamCtx, body)
		releaseUpstreamCtx()
		if err != nil {
			return nil, lastWireBody, err
		}
		lastWireBody = wireBody
		if options.SynchronizeBody && !bytes.Equal(wireBody, body) {
			// Beta sanitization can change the sent body. Update parsed caller
			// state before dispatch, then use those same bytes as the next
			// retry input.
			if err := options.ReplaceBody(wireBody); err != nil {
				return nil, lastWireBody, err
			}
			body = wireBody
		}

		resp, err = options.Do(upstreamReq)
		if err != nil {
			// A failed sender can still return a response handle. Close that
			// unused body before transport-error classification returns the
			// recorded wire bytes to the caller.
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			return nil, lastWireBody, options.TransportError(ctx, err, upstreamReq.URL.String())
		}

		// Configured non-400 failures use provider-local status retry. An HTTP
		// 400 rejection is returned directly for caller inspection instead of
		// entering ordinary signature or budget repair.
		if resp.StatusCode >= 400 && resp.StatusCode != http.StatusBadRequest && options.ShouldRetry(resp.StatusCode) {
			if attempt < options.MaxAttempts {
				elapsed := time.Since(retryStart)
				if elapsed >= options.MaxElapsed {
					break
				}

				delay := options.Delay(attempt)
				remaining := options.MaxElapsed - elapsed
				// Clip the selected wait to the remaining budget observed
				// before diagnostic reads. This limits scheduled backoff while
				// a later dispatch retains its own transport policy.
				if delay > remaining {
					delay = remaining
				}
				if delay <= 0 {
					break
				}

				// A diagnostic read can fail while retry remains eligible. Keep
				// its available bytes for observation and close the discarded
				// response before waiting to dispatch again.
				respBody, _ := options.ReadErrorBody(resp)
				_ = resp.Body.Close()
				options.Observe(ExchangeNotice{
					Platform:           options.Platform,
					ProviderID:         options.ProviderID,
					ProviderName:       options.ProviderName,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  resp.Header.Get("x-request-id"),
					UpstreamURL:        options.SafeURL(upstreamReq.URL.String()),
					Passthrough:        true,
					Kind:               "retry",
					Message:            options.ErrorMessage(respBody),
					Detail:             options.Detail(respBody),
				})
				logger.LegacyPrintf("service.gateway", "Anthropic passthrough provider %d: upstream error %d, retry %d/%d after %v (elapsed=%v/%v)",
					options.ProviderID, resp.StatusCode, attempt, options.MaxAttempts, delay, elapsed, options.MaxElapsed)
				// Cancellation can end the caller-context backoff after
				// response cleanup, preventing another dispatch while
				// preserving the latest constructed wire body for the failure
				// result.
				if err := upstream.WaitContext(ctx, delay); err != nil {
					return nil, lastWireBody, err
				}
				continue
			}
			break
		}

		break
	}

	return resp, lastWireBody, nil
}
