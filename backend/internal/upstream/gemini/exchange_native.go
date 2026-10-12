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

func ExchangeNative(ctx context.Context, options ExchangeOptions) (ExchangeResult, error) {
	requestIDHeader := options.RequestIDHeader
	var resp *http.Response
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
			if options.CountFallback {
				estimated := options.EstimateCount()
				return ExchangeResult{EstimatedTokens: &estimated}, nil
			}
			options.SetError(0, safeErr, "")
			return ExchangeResult{}, options.FinalError("Upstream request failed after retries: " + safeErr)
		}

		// A matching error policy takes precedence over status-based retries.
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
			if options.CountFallback {
				estimated := options.EstimateCount()
				return ExchangeResult{EstimatedTokens: &estimated}, nil
			}
			// Keep the final upstream error body available to the caller for passthrough.
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
