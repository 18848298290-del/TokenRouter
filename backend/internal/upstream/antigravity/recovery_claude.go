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

type ClaudeRecoveryInput struct {
	ProviderID                             int64
	ProviderName, Prefix, ProjectID, Model string
	Request                                protocolanthropic.ClaudeRequest
	InitialOptions                         TransformOptions
}
type ClaudeRecoveryOptions struct {
	Retry                                 func([]byte) (*http.Response, error)
	SignatureEnabled, BudgetEnabled       func(context.Context) bool
	TransformOptions                      func(context.Context) TransformOptions
	LogConfig                             func() (bool, int)
	ErrorDetail                           func([]byte) string
	ReadErrorBody                         func(*http.Response) []byte
	Observe                               func(RetryObservation)
	IsBudgetConstraint                    func(string) bool
	BudgetTokens, MinMaxTokens, MaxTokens int
	TruncateForLog                        func([]byte, int) string
	TruncateString                        func(string, int) string
}

func RecoverClaude(ctx context.Context, input ClaudeRecoveryInput, resp *http.Response, options ClaudeRecoveryOptions) *http.Response {
	if resp.StatusCode < 400 {
		return resp
	}
	respBody := options.ReadErrorBody(resp)
	claudeReq, projectID, mappedModel, transformOpts, prefix := input.Request, input.ProjectID, input.Model, input.InitialOptions, input.Prefix
	// A signature-related 400 can be retried with less signature-sensitive content.
	// Invalid historical thinking signatures may fail v1internal validation.
	// Each recovery stage rebuilds the request before another attempt.
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

		// Try thinking recovery before degrading tool blocks:
		// 1) Disable top-level thinking and convert thinking blocks to text.
		// 2) If a signature-related 400 persists, also convert tool blocks to text.

		retryStages := []struct {
			name  string
			strip func(*protocolanthropic.ClaudeRequest) (bool, error)
		}{
			{name: "thinking-only", strip: protocolanthropic.StripThinkingFromClaudeRequest},
			{name: "thinking+tools", strip: protocolanthropic.StripSignatureSensitiveBlocksFromClaudeRequest},
		}

		for _, stage := range retryStages {
			retryClaudeReq := claudeReq
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
				_ = resp.Body.Close()
				resp = retryResp
				respBody = nil
				break
			}

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

			// Stop degrading content once the error is no longer signature-related.
			if retryResp.StatusCode != http.StatusBadRequest || !IsSignatureRelatedError(retryBody) {
				respBody = retryBody
				resp = &http.Response{
					StatusCode: retryResp.StatusCode,
					Header:     retryResp.Header.Clone(),
					Body:       io.NopCloser(bytes.NewReader(retryBody)),
				}
				break
			}

			// Preserve this failed response for the next recovery stage.
			respBody = retryBody
			resp = &http.Response{
				StatusCode: retryResp.StatusCode,
				Header:     retryResp.Header.Clone(),
				Body:       io.NopCloser(bytes.NewReader(retryBody)),
			}
		}
	}

	// Retry a recognized budget constraint with corrected thinking limits.
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

			// Adaptive thinking does not use an explicit budget correction.
			if claudeReq.Thinking == nil || claudeReq.Thinking.Type != "adaptive" {
				retryClaudeReq := claudeReq
				retryClaudeReq.Messages = append([]protocolanthropic.ClaudeMessage(nil), claudeReq.Messages...)
				// Allocate a separate config so the original request retains its thinking settings.
				retryClaudeReq.Thinking = &protocolanthropic.ThinkingConfig{
					Type:         "enabled",
					BudgetTokens: options.BudgetTokens,
				}
				if retryClaudeReq.MaxTokens < options.MinMaxTokens {
					retryClaudeReq.MaxTokens = options.MaxTokens
				}

				logger.LegacyPrintf("service.antigravity_gateway", "Antigravity provider %d: detected budget_tokens constraint error, retrying with rectified budget (budget_tokens=%d, max_tokens=%d)", input.ProviderID, options.BudgetTokens, options.MaxTokens)

				retryOptions := transformOpts
				retryOptions.Thinking = &bridge.GeminiThinkingConfig{IncludeThoughts: true, ThinkingBudget: options.BudgetTokens, BudgetSet: true}
				retryOptions.ThinkingResolved = true
				retryGeminiBody, txErr := TransformClaudeToGeminiWithOptions(&retryClaudeReq, projectID, mappedModel, retryOptions)
				if txErr == nil {
					retryResult, retryErr := options.Retry(retryGeminiBody)
					if retryErr == nil {
						retryResp := retryResult
						if retryResp.StatusCode < 400 {
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

	if resp.StatusCode >= 400 {
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
	}
	return resp
}
