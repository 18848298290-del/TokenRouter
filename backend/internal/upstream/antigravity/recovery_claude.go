package antigravity

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	googlewire "github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

// recovery_claude.go retries rejected Claude requests with staged signature
// cleanup and thinking-budget correction.
// A rejected Claude conversion can retry with less signature-sensitive content
// before attempting a separate budget correction. Each signature stage starts
// from the supplied request and its own copied message slice, so tool
// degradation does not silently become the input to every later stage.
//
// The selected response and captured error text advance together as retry
// outcomes arrive. Thinking-only recovery precedes thinking/tool recovery, and
// an error that leaves signature classification stops further degradation.
// Budget correction uses initial transform options and skips adaptive
// thinking. Failed bodies become readable captured responses, while
// transport/transform failures can leave the selected earlier failure;
// provider retry and logging callbacks own external state.

// ClaudeRecoveryInput supplies original request state and provider routing
// observations for Claude recovery.
type ClaudeRecoveryInput struct {
	ProviderID int64 // selected recovery provider identifier
	// ProviderName identifies diagnostic provider names; Prefix labels logs;
	// ProjectID supplies envelope project identity; Model selects the mapped
	// model for retries.
	ProviderName, Prefix, ProjectID, Model string
	// Request supplies the original conversion state; recovery copies the request
	// value and message slice while other nested request values remain shared.
	Request protocolanthropic.ClaudeRequest
	// InitialOptions supplies budget-retry conversion controls, while signature
	// stages retrieve current transform options through the callback before
	// disabling thinking.
	InitialOptions TransformOptions
}

// ClaudeRecoveryOptions supplies recovery policy, retry ownership, and
// diagnostic callbacks for rejected Claude requests.
type ClaudeRecoveryOptions struct {
	// Retry returns a nonnil response on nil error for a rebuilt request;
	// recovery consumes failures and returns the selected successful body open.
	// The callback cleans up any response returned together with an error.
	Retry func([]byte) (*http.Response, error)
	// SignatureEnabled permits signature-content recovery; BudgetEnabled permits
	// budget correction. Both receive the request context and are required when
	// their respective failure conditions are considered.
	SignatureEnabled, BudgetEnabled func(context.Context) bool
	// TransformOptions supplies current signature-stage controls using ctx;
	// recovery marks thinking resolved and clears thinking before serializing
	// each stripped request.
	TransformOptions func(context.Context) TransformOptions
	// LogConfig supplies body-log enablement and byte limit for signature retry
	// diagnostics; required when the signature branch is selected.
	LogConfig func() (bool, int)
	// ErrorDetail extracts permitted diagnostic detail from captured error bytes;
	// required when an eligible recovery error is observed.
	ErrorDetail func([]byte) string
	// ReadErrorBody captures the initial or budget-retry error bytes; this caller
	// performs selected explicit closes and rebuilds bodies afterward.
	ReadErrorBody func(*http.Response) []byte
	// Observe accepts provider recovery diagnostics synchronously; required on
	// selected observation paths and does not own returned response bodies.
	Observe func(RetryObservation)
	// IsBudgetConstraint recognizes eligible non-signature error wording before
	// BudgetEnabled is evaluated; required for the budget recovery selection
	// branch.
	IsBudgetConstraint func(string) bool
	// BudgetTokens supplies the corrected thinking budget; MinMaxTokens selects
	// when output correction applies; MaxTokens supplies the replacement output
	// allowance below that threshold.
	BudgetTokens, MinMaxTokens, MaxTokens int
	// TruncateForLog formats a bounded signature-retry rate-limit body for logs;
	// required only when that retry returns status 429.
	TruncateForLog func([]byte, int) string
	// TruncateString bounds captured signature-retry detail when body logging is
	// enabled, using the byte limit returned by LogConfig.
	TruncateString func(string, int) string
}

// RecoverClaude returns the response selected after eligible signature and
// budget retries. Callers supply a nonnil response/body and callbacks required
// by the chosen branches; successful selected bodies remain open for the
// caller to consume and close.
//
// Signature stages copy the request value and message slice, try thinking
// removal before tool degradation, and skip unchanged or failed
// transformations. Transport errors are observed before trying another stage.
// A non-signature retry response stops degradation and can permit budget
// correction, which skips adaptive thinking and uses initial options. Captured
// failed responses are rebuilt for later readers; this helper does not close
// every replaced initial failure body, so exchange ownership must account for
// consumed bodies.
func RecoverClaude(ctx context.Context, input ClaudeRecoveryInput, resp *http.Response, options ClaudeRecoveryOptions) *http.Response {
	if resp.StatusCode < 400 {
		return resp
	}
	respBody := options.ReadErrorBody(resp)
	claudeReq, projectID, mappedModel, transformOpts, prefix := input.Request, input.ProjectID, input.Model, input.InitialOptions, input.Prefix
	// Select staged degradation for an enabled signature-related 400 so ordinary
	// failures keep their captured body and do not lose tool content.
	if resp.StatusCode == http.StatusBadRequest && IsSignatureRelatedError(respBody) && options.SignatureEnabled(ctx) {
		upstreamMsg := strings.TrimSpace(googlewire.ExtractPlatformMessage(respBody))
		upstreamMsg = logredact.SanitizeUpstreamQueries(upstreamMsg)
		logBody, maxBytes := options.LogConfig()
		upstreamDetail := options.ErrorDetail(respBody)
		options.Observe(RetryObservation{
			ProviderID:         input.ProviderID,
			ProviderName:       input.ProviderName,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "signature_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})

		// Try thinking removal before broader tool conversion because the first
		// stage can recover the request while retaining invocation/result content;
		// each stage starts from the supplied request.

		retryStages := []struct {
			name string // diagnostic recovery stage name
			// selected request degradation operation
			strip func(*protocolanthropic.ClaudeRequest) (bool, error)
		}{
			{name: "thinking-only", strip: protocolanthropic.StripThinkingFromClaudeRequest},
			{name: "thinking+tools", strip: protocolanthropic.StripSignatureSensitiveBlocksFromClaudeRequest},
		}

		// Start each stage from the supplied request, skipping unchanged or failed
		// stripping/serialization so an inapplicable recovery does not replace the
		// selected response.
		for _, stage := range retryStages {
			retryClaudeReq := claudeReq
			// Copy the message slice before stage mutation so assigning rewritten turns
			// does not replace entries in the supplied conversation slice.
			retryClaudeReq.Messages = append([]protocolanthropic.ClaudeMessage(nil), claudeReq.Messages...)

			stripped, stripErr := stage.strip(&retryClaudeReq)
			if stripErr != nil || !stripped {
				continue
			}

			logger.LegacyPrintf("service.antigravity_gateway", "Antigravity provider %d: detected signature-related 400, retrying once (%s)", input.ProviderID, stage.name)

			retryOptions := options.TransformOptions(ctx)
			retryOptions.Thinking = nil
			retryOptions.ThinkingResolved = true
			retryGeminiBody, txErr := TransformClaudeToGeminiWithOptions(&retryClaudeReq, projectID, mappedModel, retryOptions)
			if txErr != nil {
				continue
			}
			retryResult, retryErr := options.Retry(retryGeminiBody)
			if retryErr != nil {
				options.Observe(RetryObservation{
					ProviderID:         input.ProviderID,
					ProviderName:       input.ProviderName,
					UpstreamStatusCode: 0,
					Kind:               "signature_retry_request_error",
					Message:            logredact.SanitizeUpstreamQueries(retryErr.Error()),
				})
				logger.LegacyPrintf("service.antigravity_gateway", "Antigravity provider %d: signature retry request failed (%s): %v", input.ProviderID, stage.name, retryErr)
				continue
			}

			retryResp := retryResult
			if retryResp.StatusCode < 400 {
				// The selected retry succeeded; close the preceding response still owned
				// here before returning this new open body for client conversion.
				_ = resp.Body.Close()
				resp = retryResp
				respBody = nil
				break
			}

			// Capture at most eight KiB from the failed retry and close its network
			// body; read failure retains the prefix for classification and
			// reconstruction.
			retryBody, _ := io.ReadAll(io.LimitReader(retryResp.Body, 8<<10))
			_ = retryResp.Body.Close()
			if retryResp.StatusCode == http.StatusTooManyRequests {
				retryBaseURL := ""
				if retryResp.Request != nil && retryResp.Request.URL != nil {
					retryBaseURL = retryResp.Request.URL.Scheme + "://" + retryResp.Request.URL.Host
				}
				logger.LegacyPrintf("service.antigravity_gateway", "%s status=429 rate_limited base_url=%s retry_stage=%s body=%s", prefix, retryBaseURL, stage.name, options.TruncateForLog(retryBody, 200))
			}
			kind := "signature_retry"
			if strings.TrimSpace(stage.name) != "" {
				kind = "signature_retry_" + strings.ReplaceAll(stage.name, "+", "_")
			}
			retryUpstreamMsg := strings.TrimSpace(googlewire.ExtractPlatformMessage(retryBody))
			retryUpstreamMsg = logredact.SanitizeUpstreamQueries(retryUpstreamMsg)
			retryUpstreamDetail := ""
			if logBody {
				retryUpstreamDetail = options.TruncateString(string(retryBody), maxBytes)
			}
			options.Observe(RetryObservation{
				ProviderID:         input.ProviderID,
				ProviderName:       input.ProviderName,
				UpstreamStatusCode: retryResp.StatusCode,
				UpstreamRequestID:  retryResp.Header.Get("x-request-id"),
				Kind:               kind,
				Message:            retryUpstreamMsg,
				Detail:             retryUpstreamDetail,
			})

			// Stop content degradation when the selected retry leaves signature
			// classification, rebuilding its captured response so later budget policy
			// can inspect that failure.
			if retryResp.StatusCode != http.StatusBadRequest || !IsSignatureRelatedError(retryBody) {
				respBody = retryBody
				resp = &http.Response{
					StatusCode: retryResp.StatusCode,
					Header:     retryResp.Header.Clone(),
					Body:       io.NopCloser(bytes.NewReader(retryBody)),
				}
				break
			}

			// Rebuild the failed signature response from captured bytes before the next
			// stage, because its original network body has already been consumed and
			// closed.
			respBody = retryBody
			resp = &http.Response{
				StatusCode: retryResp.StatusCode,
				Header:     retryResp.Header.Clone(),
				Body:       io.NopCloser(bytes.NewReader(retryBody)),
			}
		}
	}

	// Consider budget correction after signature stages select a non-signature
	// 400, so the two recovery mechanisms act on the current captured failure.
	if resp.StatusCode == http.StatusBadRequest && respBody != nil && !IsSignatureRelatedError(respBody) {
		errMsg := strings.TrimSpace(googlewire.ExtractPlatformMessage(respBody))
		if options.IsBudgetConstraint(errMsg) && options.BudgetEnabled(ctx) {
			options.Observe(RetryObservation{
				ProviderID:         input.ProviderID,
				ProviderName:       input.ProviderName,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				Kind:               "budget_constraint_error",
				Message:            errMsg,
				Detail:             options.ErrorDetail(respBody),
			})

			// Skip explicit budget correction for the supplied adaptive mode, whose
			// budget selection follows model rules; other modes can use the corrected
			// enabled configuration.
			if claudeReq.Thinking == nil || claudeReq.Thinking.Type != "adaptive" {
				retryClaudeReq := claudeReq
				retryClaudeReq.Messages = append([]protocolanthropic.ClaudeMessage(nil), claudeReq.Messages...)
				// Allocate separate thinking controls for the budget retry so overwriting
				// its budget does not mutate the supplied request thinking pointer.
				retryClaudeReq.Thinking = &protocolanthropic.ThinkingConfig{
					Type:         "enabled",
					BudgetTokens: options.BudgetTokens,
				}
				// Raise output below the caller correction threshold so this retry pairs
				// its selected thinking budget with the configured output allowance.
				if retryClaudeReq.MaxTokens < options.MinMaxTokens {
					retryClaudeReq.MaxTokens = options.MaxTokens
				}

				logger.LegacyPrintf("service.antigravity_gateway", "Antigravity provider %d: detected budget_tokens constraint error, retrying with rectified budget (budget_tokens=%d, max_tokens=%d)", input.ProviderID, options.BudgetTokens, options.MaxTokens)

				retryOptions := transformOpts
				retryOptions.Thinking = &bridge.GeminiThinkingConfig{IncludeThoughts: true, ThinkingBudget: options.BudgetTokens, BudgetSet: true}
				retryOptions.ThinkingResolved = true
				retryGeminiBody, txErr := TransformClaudeToGeminiWithOptions(&retryClaudeReq, projectID, mappedModel, retryOptions)
				// If budget serialization or dispatch fails, retain the selected failure
				// because this recovery returns a response and suppresses that secondary
				// error.
				if txErr == nil {
					retryResult, retryErr := options.Retry(retryGeminiBody)
					if retryErr == nil {
						retryResp := retryResult
						if retryResp.StatusCode < 400 {
							// Replace the selected failure after budget retry success, closing
							// its current body before caller ownership moves to the accepted
							// response.
							_ = resp.Body.Close()
							resp = retryResp
							respBody = nil
						} else {
							retryBody := options.ReadErrorBody(retryResp)
							_ = retryResp.Body.Close()
							respBody = retryBody
							resp = &http.Response{
								StatusCode: retryResp.StatusCode,
								Header:     retryResp.Header.Clone(),
								Body:       io.NopCloser(bytes.NewReader(retryBody)),
							}
						}
					} else {
						logger.LegacyPrintf("service.antigravity_gateway", "Antigravity provider %d: budget rectifier retry failed: %v", input.ProviderID, retryErr)
					}
				}
			}
		}
	}

	// Restore selected failure bytes as a readable body before returning, because
	// error capture has consumed the stream used by later gateway error handling.
	if resp.StatusCode >= 400 {
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
	}
	return resp
}
