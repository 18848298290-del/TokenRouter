package forward

import (
	"context"
	"fmt"
	"time"

	"github.com/tidwall/gjson"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// APIKeyPassthrough handles a selected provider response and partial usage; upstream performs the exchange and stream reading.
func APIKeyPassthrough(ctx context.Context, p PassthroughPorts, in MessageInput, input APIKeyInput) (*Result, error) {
	done, err := p.Begin()
	if err != nil {
		return nil, err
	}
	defer done()
	err = p.Credential(ctx)
	if err != nil {
		return nil, err
	}
	if tokenType := p.TokenKind(); tokenType != "apikey" {
		return nil, fmt.Errorf("anthropic api key passthrough requires apikey token, got: %s", tokenType)
	}

	p.ResolveProxy()

	p.Log(fmt.Sprintf("[Anthropic 自动透传] 命中 API Key 透传分支: provider=%d name=%s model=%s stream=%v",
		in.ProviderID, in.ProviderName, input.RequestModel, input.RequestStream))

	p.MarkPassthrough()

	input.Body = protocolanthropic.StripEmptyTextBlocks(input.Body)
	// Filter search history using the already mapped model to avoid unsupported server-tool blocks.
	input.Body = p.FilterSearchHistory(input.Body, input.RequestModel)
	if input.Parsed != nil {
		// Keep the parsed request synchronized with wire changes so successful usage hashes use the current body.
		if err := input.Parsed.ReplaceBody(input.Body); err != nil {
			return nil, err
		}
	}

	var resp *ExchangeResponse
	lastWireBody := input.Body
	var earlyResult *Result
	stopped := false
	var streamResult *StreamOutcome
	before := func(ctx context.Context, response *ExchangeResponse, wire []byte) (bool, error) {
		resp = response
		lastWireBody = wire
		early := func(result *Result, err error) (bool, error) { earlyResult = result; return true, err }
		if resp.StatusCode >= 400 && ShouldRetry(in.OAuth, resp.StatusCode) {
			if ShouldFailover(resp.StatusCode) {
				respBody, _ := p.ReadErrorBody()
				p.ResetErrorBody(respBody)

				p.Log(fmt.Sprintf("[Anthropic Passthrough] Upstream error (retry exhausted, failover): Provider=%d(%s) Status=%d RequestID=%s Body=%s",
					in.ProviderID, in.ProviderName, resp.StatusCode, resp.RequestID, p.Truncate(string(respBody), 1000)))

				decision := p.Health(ctx, "retry", resp.StatusCode, resp.Headers, respBody, input.RequestModel)
				if decision.Generic {
					return early(p.HandleError(ctx, input.RequestModel, false))
				}
				p.Observe(Notice{
					Platform:           in.Platform,
					ProviderID:         in.ProviderID,
					ProviderName:       in.ProviderName,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  resp.RequestID,
					Passthrough:        true,
					Kind:               "retry_exhausted_failover",
					Message:            upstream.ExtractErrorMessage(respBody),
					Detail: func() string {
						if in.LogErrorBody {
							return p.Truncate(string(respBody), in.LogErrorBodyMaxBytes)
						}
						return ""
					}(),
				})
				return true, p.FailoverError(resp.StatusCode, respBody, decision.RetrySameProvider)
			}
			return early(p.HandleError(ctx, input.RequestModel, true))
		}

		if resp.StatusCode >= 400 && ShouldFailover(resp.StatusCode) {
			respBody, _ := p.ReadErrorBody()
			p.ResetErrorBody(respBody)

			p.Log(fmt.Sprintf("[Anthropic Passthrough] Upstream error (failover): Provider=%d(%s) Status=%d RequestID=%s Body=%s",
				in.ProviderID, in.ProviderName, resp.StatusCode, resp.RequestID, p.Truncate(string(respBody), 1000)))

			decision := p.Health(ctx, "failover", resp.StatusCode, resp.Headers, respBody, input.RequestModel)
			if decision.Generic {
				return early(p.HandleError(ctx, input.RequestModel, false))
			}
			p.Observe(Notice{
				Platform:           in.Platform,
				ProviderID:         in.ProviderID,
				ProviderName:       in.ProviderName,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.RequestID,
				Passthrough:        true,
				Kind:               "failover",
				Message:            upstream.ExtractErrorMessage(respBody),
				Detail: func() string {
					if in.LogErrorBody {
						return p.Truncate(string(respBody), in.LogErrorBodyMaxBytes)
					}
					return ""
				}(),
			})
			return true, p.FailoverError(resp.StatusCode, respBody, decision.RetrySameProvider)
		}

		if resp.StatusCode >= 400 {
			return early(p.HandleError(ctx, input.RequestModel, false))
		}

		return false, nil
	}
	attempt, err := p.ExecutePassthrough(ctx, &input, MessageHooks{
		Before: func(ctx context.Context, response *ExchangeResponse, wire []byte) (bool, error) {
			var err error
			stopped, err = before(ctx, response, wire)
			return stopped, err
		},
		Wire: func(wire []byte) { lastWireBody = wire },
		Stream: func(result *StreamOutcome) {
			if result != nil {
				streamResult = result
			}
		},
	})

	if stopped {
		return earlyResult, err
	}
	if err != nil {
		if input.RequestStream {
			requestSpeed := gjson.GetBytes(lastWireBody, "speed").String()
			if partial := PartialUsage(resp, streamResult, input.OriginalModel, input.RequestModel, input.StartTime, requestSpeed, p.IsFailover(err)); partial != nil {
				return partial, err
			}
		}
		return nil, err
	}
	usage := &attempt.Usage
	firstTokenMs := attempt.FirstTokenMs
	clientDisconnect := attempt.ClientDisconnect
	return &Result{
		RequestID:                   resp.RequestID,
		UpstreamHeaders:             resp.Headers,
		Usage:                       *usage,
		Model:                       input.OriginalModel,
		UpstreamModel:               input.RequestModel,
		UpstreamResponseServiceTier: p.ServiceTier(),
		UpstreamResponseModel:       attempt.UpstreamResponseModel,
		Stream:                      input.RequestStream,
		Duration:                    time.Since(input.StartTime),
		FirstTokenMs:                firstTokenMs,
		ClientDisconnect:            clientDisconnect,
	}, nil
}
