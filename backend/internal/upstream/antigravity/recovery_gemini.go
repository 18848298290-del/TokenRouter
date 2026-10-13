package antigravity

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	googlewire "github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

// Gemini recovery first considers a configured alternate model for a
// missing-model failure, then considers signature cleanup for a selected bad
// request. The injected request bytes remain the basis for both preparations,
// with project/model routing rebuilt around each candidate body.
//
// Captured initial failures are read and closed before reconstruction.
// Successful alternate or signature responses replace the selected response,
// while the separately retained error-body and content-type observations can
// remain from the initial failure. Failed signature retries update those
// observations together with their rebuilt response. Preparation errors and
// provider-switch signals propagate after closing the selected body; ordinary
// request/serialization failures retain the earlier captured failure for the
// caller.

// GeminiRecoveryInput supplies injected request bytes and provider routing
// observations for Gemini recovery.
type GeminiRecoveryInput struct {
	ProviderID int64 // selected recovery provider identifier
	// ProviderName labels diagnostics; ProjectID supplies envelope project
	// identity; Model selects the mapped route; Action selects the API operation;
	// AccessToken supplies bearer authentication.
	ProviderName, ProjectID, Model, Action, AccessToken string
	// Body supplies prepared inner Gemini JSON for retry preparation; callbacks
	// can return replacement bytes, and callers keep supplied storage stable
	// during recovery.
	Body []byte
}

// GeminiRecoveryOptions supplies preparation, retry policy, and diagnostics
// for selected Gemini failure recovery.
type GeminiRecoveryOptions struct {
	// Prepare optionally prepares inner bytes for each selected model; an error
	// propagates and closes the currently selected response through deferred
	// cleanup.
	Prepare func(string, []byte) ([]byte, error)
	// Retry handles wrapped signature-retry bytes and returns a nonnil response
	// on nil error; failed bodies are captured and successful bodies return open.
	// The callback cleans up responses returned together with errors.
	Retry func([]byte) (*http.Response, error)
	// Do sends the direct alternate-model request; nil-error results require a
	// nonnil response, and unsuccessful returned bodies are closed by recovery.
	Do func(*http.Request) (*http.Response, error)
	// FallbackEnabled permits missing-model recovery; SignatureEnabled permits
	// signature cleanup. Both receive ctx and are required when their respective
	// selected failure conditions are considered.
	FallbackEnabled, SignatureEnabled func(context.Context) bool
	// FallbackModel supplies the alternate route when missing-model recovery is
	// enabled; blank or unchanged identity skips that direct retry.
	FallbackModel func(context.Context) string
	// IsModelNotFound recognizes the captured initial failure before
	// alternate-model selection; required whenever enabled fallback considers a
	// failed response.
	IsModelNotFound func(int, []byte) bool
	// CleanSignatures rewrites injected request bytes for an eligible signature
	// retry; required when the request contains the exact camelCase signature
	// key.
	CleanSignatures func([]byte) []byte
	// ReadErrorBody captures failed response data; recovery closes the original
	// body afterward and reconstructs selected failure bytes for later readers.
	ReadErrorBody func(*http.Response) []byte
	// ErrorDetail selects permitted diagnostic detail from captured signature
	// errors; required whenever those recovery observations are emitted.
	ErrorDetail func([]byte) string
	// Observe records selected signature recovery diagnostics synchronously,
	// including retry failure and provider switching, while response ownership
	// remains with recovery.
	Observe func(RetryObservation)
}

// GeminiRecoveryResult combines the selected response with separately retained
// recovery error-body and content-type observations.
type GeminiRecoveryResult struct {
	// Response holds the selected response; success returns its body open, while
	// a propagated recovery failure closes that selected body before returning.
	Response *http.Response
	// ErrorBody retains captured error bytes and can remain populated after a
	// successful replacement, so inspect Response status when deciding their
	// relevance.
	ErrorBody []byte
	// ContentType starts from the initial response and updates on failed
	// signature replacement; successful replacements can retain that earlier
	// content-type observation.
	ContentType string
}

// RecoverGemini returns the selected response and retained recovery
// observations for a nonnil response/body. Successful returned bodies remain
// open for the caller, while a propagated failure closes the selected body
// through deferred cleanup.
//
// Missing-model recovery uses an optional prepared alternate body and the
// default NewAPIRequest route, then signature recovery checks captured error
// text and exact request-key presence. Successful replacements can retain
// earlier ErrorBody and ContentType values, so response status determines
// whether those observations still describe the selected result. Failed
// signature responses are captured, closed, and rebuilt. Preparation errors
// and provider-switch errors propagate; ordinary retry/build errors are
// observed or skipped while retaining the selected failure.
func RecoverGemini(ctx context.Context, input GeminiRecoveryInput, resp *http.Response, options GeminiRecoveryOptions) (result GeminiRecoveryResult, failure error) {
	var respBody []byte
	contentType := resp.Header.Get("Content-Type")
	// Publish the selected response and separately retained observations on every
	// return; propagated failure also closes its body because the caller receives
	// an error.
	defer func() {
		result.Response = resp
		result.ErrorBody = respBody
		result.ContentType = contentType
		if failure != nil && resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	if resp.StatusCode < 400 {
		return result, nil
	}
	// Capture and close the initial failure before rebuilding its bytes, allowing
	// preparation and retry selection to inspect the error without rereading a
	// consumed network stream.
	respBody = options.ReadErrorBody(resp)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	projectID, mappedModel, injectedBody, upstreamAction, accessToken := input.ProjectID, input.Model, input.Body, input.Action, input.AccessToken
	// Try a distinct configured alternate model after initial missing-model
	// classification, leaving the captured failure selected when direct retry
	// construction or dispatch does not succeed.
	if options.FallbackEnabled(ctx) &&
		options.IsModelNotFound(resp.StatusCode, respBody) {
		fallbackModel := options.FallbackModel(ctx)
		if fallbackModel != "" && fallbackModel != mappedModel {
			logger.LegacyPrintf("service.antigravity_gateway", "[Antigravity] Model not found (%s), retrying with fallback model %s (provider: %s)", mappedModel, fallbackModel, input.ProviderName)

			fallbackBody := injectedBody
			if options.Prepare != nil {
				var err error
				fallbackBody, err = options.Prepare(fallbackModel, injectedBody)
				if err != nil {
					return result, err
				}
			}
			fallbackWrapped, err := WrapV1InternalRequest(projectID, fallbackModel, fallbackBody)
			if err == nil {
				fallbackReq, err := NewAPIRequest(ctx, upstreamAction, accessToken, fallbackWrapped)
				if err == nil {
					fallbackResp, err := options.Do(fallbackReq)
					// A successful alternate replaces the selected failure after closing
					// its current body. An unsuccessful direct response closes here while
					// the captured initial failure remains selected.
					if err == nil && fallbackResp.StatusCode < 400 {
						_ = resp.Body.Close()
						resp = fallbackResp
					} else if fallbackResp != nil {
						_ = fallbackResp.Body.Close()
					}
				}
			}
		}
	}

	// Inspect captured signature-error text after alternate-model handling and
	// require the exact camelCase request key before cleanup, so unrelated bad
	// requests retain their selected failure.
	signatureCheckBody := respBody
	if unwrapped, unwrapErr := (&ResponseAdapter{}).UnwrapV1InternalResponse(respBody); unwrapErr == nil && len(unwrapped) > 0 {
		signatureCheckBody = unwrapped
	}
	if resp.StatusCode == http.StatusBadRequest &&
		options.SignatureEnabled(ctx) &&
		IsSignatureRelatedError(signatureCheckBody) &&
		bytes.Contains(injectedBody, []byte(`"thoughtSignature"`)) {
		upstreamMsg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(googlewire.ExtractPlatformMessage(signatureCheckBody)))
		upstreamDetail := options.ErrorDetail(signatureCheckBody)
		options.Observe(RetryObservation{
			ProviderID:         input.ProviderID,
			ProviderName:       input.ProviderName,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "signature_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})

		logger.LegacyPrintf("service.antigravity_gateway", "Antigravity Gemini provider %d: detected signature-related 400, retrying with cleaned thought signatures", input.ProviderID)

		cleanedInjectedBody := options.CleanSignatures(injectedBody)
		if options.Prepare != nil {
			var err error
			cleanedInjectedBody, err = options.Prepare(mappedModel, cleanedInjectedBody)
			if err != nil {
				return result, err
			}
		}
		retryWrappedBody, wrapErr := WrapV1InternalRequest(projectID, mappedModel, cleanedInjectedBody)
		if wrapErr == nil {
			retryResult, retryErr := options.Retry(retryWrappedBody)
			if retryErr == nil {
				retryResp := retryResult
				if retryResp.StatusCode < 400 {
					resp = retryResp
				} else {
					retryRespBody := options.ReadErrorBody(retryResp)
					_ = retryResp.Body.Close()
					retryOpsBody := retryRespBody
					if retryUnwrapped, unwrapErr := (&ResponseAdapter{}).UnwrapV1InternalResponse(retryRespBody); unwrapErr == nil && len(retryUnwrapped) > 0 {
						retryOpsBody = retryUnwrapped
					}
					options.Observe(RetryObservation{
						ProviderID:         input.ProviderID,
						ProviderName:       input.ProviderName,
						UpstreamStatusCode: retryResp.StatusCode,
						UpstreamRequestID:  retryResp.Header.Get("x-request-id"),
						Kind:               "signature_retry",
						Message:            logredact.SanitizeUpstreamQueries(strings.TrimSpace(googlewire.ExtractPlatformMessage(retryOpsBody))),
						Detail:             options.ErrorDetail(retryOpsBody),
					})
					// Replace captured observations together with the failed signature
					// response, since that retry body is closed and later error handling
					// needs a readable reconstructed stream.
					respBody = retryRespBody
					resp = &http.Response{
						StatusCode: retryResp.StatusCode,
						Header:     retryResp.Header.Clone(),
						Body:       io.NopCloser(bytes.NewReader(retryRespBody)),
					}
					contentType = resp.Header.Get("Content-Type")
				}
			} else {
				if switchErr, ok := IsAntigravityProviderSwitchError(retryErr); ok {
					options.Observe(RetryObservation{
						ProviderID:         input.ProviderID,
						ProviderName:       input.ProviderName,
						UpstreamStatusCode: http.StatusServiceUnavailable,
						Kind:               "failover",
						Message:            logredact.SanitizeUpstreamQueries(retryErr.Error()),
					})
					return result, switchErr
				}
				options.Observe(RetryObservation{
					ProviderID:         input.ProviderID,
					ProviderName:       input.ProviderName,
					UpstreamStatusCode: 0,
					Kind:               "signature_retry_request_error",
					Message:            logredact.SanitizeUpstreamQueries(retryErr.Error()),
				})
				logger.LegacyPrintf("service.antigravity_gateway", "Antigravity Gemini provider %d: signature retry request failed: %v", input.ProviderID, retryErr)
			}
		} else {
			logger.LegacyPrintf("service.antigravity_gateway", "Antigravity Gemini provider %d: signature retry wrap failed: %v", input.ProviderID, wrapErr)
		}
	}

	return result, nil
}
