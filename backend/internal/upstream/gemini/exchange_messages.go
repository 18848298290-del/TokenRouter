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

type ExchangeNotice struct {
	Platform, ProviderName                   string
	ProviderID                               int64
	UpstreamStatusCode                       int
	UpstreamRequestID, Kind, Message, Detail string
}
type ExchangeOptions struct {
	ProviderID                              int64
	ProviderName, Platform, RequestIDHeader string
	MaxRetries                              int
	CountFallback                           bool
	Build                                   func(context.Context) (*http.Request, string, error)
	Do                                      func(*http.Request) (*http.Response, error)
	BuildError                              func(error) error
	FinalError                              func(string) error
	EstimateCount                           func() int
	ReadError                               func(*http.Response) []byte
	CheckPolicy                             func(context.Context, *http.Response) (bool, *http.Response)
	ShouldRetry                             func(int) bool
	OnStatus                                func(context.Context, int, http.Header, []byte)
	Observe                                 func(ExchangeNotice)
	SetError                                func(int, string, string)
	Message, Detail                         func([]byte) string
	Sanitize                                func(string) string
	FilterThinking, FilterTools             func() []byte
	ConvertRetry                            func([]byte) ([]byte, error)
	ReplaceBody                             func([]byte)
}
type ExchangeResult struct {
	Response        *http.Response
	RequestIDHeader string
	EstimatedTokens *int
}

func ExchangeMessages(ctx context.Context, options ExchangeOptions) (ExchangeResult, error) {
	requestIDHeader := options.RequestIDHeader
	var resp *http.Response
	signatureRetryStage := 0
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
			if attempt < options.MaxRetries {
				logger.LegacyPrintf("service.gemini_messages_compat", "Gemini provider %d: upstream request failed, retry %d/%d: %v", options.ProviderID, attempt, options.MaxRetries, err)
				SleepGeminiBackoff(attempt)
				continue
			}
			options.SetError(0, safeErr, "")
			return ExchangeResult{}, options.FinalError("Upstream request failed after retries: " + safeErr)
		}

		// Special-case: signature/thought_signature validation errors are not transient, but may be fixed by
		// downgrading Claude thinking/tool history to plain text (conservative two-stage retry).
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
					// Stage 1: disable thinking + thinking->text
					strippedClaudeBody = options.FilterThinking()
					stageName = "thinking-only"
					signatureRetryStage = 1
				default:
					// Stage 2: additionally downgrade tool_use/tool_result blocks to text
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
					// Consume one retry budget attempt and continue with the updated request payload.
					SleepGeminiBackoff(1)
					continue
				}
			}

			// Restore body for downstream error handling.
			resp = &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     resp.Header.Clone(),
				Body:       io.NopCloser(bytes.NewReader(respBody)),
			}
			break
		}

		// 错误策略优先：匹配则跳过重试直接处理。
		matched, rebuilt := options.CheckPolicy(ctx, resp)
		resp = rebuilt
		if matched {
			break
		}

		if resp.StatusCode >= 400 && options.ShouldRetry(resp.StatusCode) {
			respBody := options.ReadError(resp)
			_ = resp.Body.Close()
			// Don't treat insufficient-scope as transient.
			if resp.StatusCode == http.StatusForbidden && IsGeminiInsufficientScope(resp.Header, respBody) {
				resp = &http.Response{
					StatusCode: resp.StatusCode,
					Header:     resp.Header.Clone(),
					Body:       io.NopCloser(bytes.NewReader(respBody)),
				}
				break
			}
			if resp.StatusCode == http.StatusTooManyRequests {
				// Mark as rate-limited early so concurrent requests avoid this provider.
				options.OnStatus(ctx, resp.StatusCode, resp.Header, respBody)
			}
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
			// Final attempt: surface the upstream error body (mapped below) instead of a generic retry error.
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
