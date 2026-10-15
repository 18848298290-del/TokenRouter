package bedrock

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// http_execute.go executes authenticated Bedrock requests with bounded waits
// between eligible HTTP retries.
// Bedrock retry policy applies to eligible HTTP statuses after a newly built
// request is sent. Request construction selects bearer or SigV4
// authentication, and the final response body transfers to the caller for
// protocol handling.
//
// The elapsed budget is checked before another retry wait and caps that wait;
// it is not a deadline on an in-flight HTTP request. Error bytes are captured
// before a discarded body closes so retry observations still have diagnostic
// content. Transport errors end this loop immediately, while the context-aware
// wait can cancel before the next request. Caller delay, status, and body
// callbacks define the platform policy used by this technical sequence.

// RequestOptions supplies the regional Invoke endpoint identity,
// authentication mode, and transport.
type RequestOptions struct {
	// ModelID identifies the routed model; Region identifies the signing/source
	// region; APIKey supplies bearer credentials.
	ModelID, Region, APIKey string
	// Stream selects response framing; APIKeyMode selects bearer authentication.
	Stream, APIKeyMode bool
	// Signer is required when APIKeyMode is false. Its configured signing region
	// must match the source region selected for the constructed Invoke URL.
	Signer *BedrockSigner // SigV4 request signing instance
	// prepared request HTTP exchange
	// Do is required for every attempt and returns a usable response body on
	// success; a response returned together with an error is closed here.
	Do func(*http.Request) (*http.Response, error)
}

// RetryPolicy supplies HTTP retry eligibility, waiting limits, diagnostics,
// and transport-error mapping.
type RetryPolicy struct {
	// MaxAttempts is the total dispatch count and must be positive for an
	// exchange. Nonpositive values perform no request and return an
	// empty-response error.
	MaxAttempts int // total request attempt budget
	// MaxElapsed includes dispatch time and limits retry eligibility and waiting.
	// Nonpositive values permit the first request but prevent eligible HTTP retries.
	MaxElapsed time.Duration
	// Delay and ReadErrorBody are required when another eligible HTTP retry fits
	// MaxElapsed. ShouldRetry is optional; nil prevents status-based retry
	// selection.
	Delay          func(int) time.Duration              // one-based retry delay calculation
	ShouldRetry    func(int) bool                       // HTTP status retry eligibility
	ReadErrorBody  func(*http.Response) ([]byte, error) // retry error-body prefix reader
	TransportError func(error, string) error            // optional transport failure mapping
	// optional retry diagnostic observation
	ObserveRetry func(*http.Response, []byte, string, int, time.Duration)
}

// ExecuteUpstream returns a final Bedrock response after eligible HTTP
// retries. Callers provide positive MaxAttempts and the callbacks required by
// their policy, keep body bytes immutable through exchange, and close the
// returned response.
//
// Each request is built and signed separately. Eligible HTTP failures
// excluding 400 can retry while elapsed time still permits a positive wait;
// that wait is shortened to the remaining budget and observes context
// cancellation. Error-read failures retain the callback's returned bytes for
// observation, and discarded bodies close before waiting. Transport failures
// return immediately after closing any accompanying response; request duration
// remains governed by the supplied transport.
func ExecuteUpstream(ctx context.Context, body []byte, options RequestOptions, policy RetryPolicy) (*http.Response, error) {
	var resp *http.Response
	var err error
	retryStart := time.Now()
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		var upstreamReq *http.Request
		if options.APIKeyMode {
			upstreamReq, err = BuildRequestAPIKey(ctx, body, options.ModelID, options.Region, options.Stream, options.APIKey)
		} else {
			upstreamReq, err = BuildRequest(ctx, body, options.ModelID, options.Region, options.Stream, options.Signer)
		}
		if err != nil {
			return nil, err
		}

		resp, err = options.Do(upstreamReq)
		if err != nil {
			// Transport errors end this loop immediately. Close an accompanying response
			// before error mapping because no caller receives that response for cleanup.
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if policy.TransportError != nil {
				return nil, policy.TransportError(err, upstreamReq.URL.String())
			}
			return nil, err
		}

		if resp.StatusCode >= 400 && resp.StatusCode != http.StatusBadRequest && policy.ShouldRetry != nil && policy.ShouldRetry(resp.StatusCode) {
			if attempt < policy.MaxAttempts {
				elapsed := time.Since(retryStart)
				if elapsed >= policy.MaxElapsed {
					break
				}

				delay := policy.Delay(attempt)
				remaining := policy.MaxElapsed - elapsed
				// Cap this wait by the remaining elapsed budget because prior request time
				// already counts against permission to perform another HTTP retry.
				if delay > remaining {
					delay = remaining
				}
				if delay <= 0 {
					break
				}

				// Capture error bytes before closing the discarded body; the observer can
				// retain partial diagnostics after a read failure while the next request
				// uses a new body.
				respBody, _ := policy.ReadErrorBody(resp)
				_ = resp.Body.Close()
				if policy.ObserveRetry != nil {
					policy.ObserveRetry(resp, respBody, upstreamReq.URL.String(), attempt, delay)
				}

				// Wait after response closing and retry observation so cancellation can
				// stop the next dispatch without leaving the discarded server stream owned
				// by this loop.
				if err := upstream.WaitContext(ctx, delay); err != nil {
					return nil, err
				}
				continue
			}
			break
		}

		break
	}
	if resp == nil || resp.Body == nil {
		return nil, errors.New("upstream request failed: empty response")
	}
	return resp, nil
}
