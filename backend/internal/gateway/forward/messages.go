package forward

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Messages prepares one provider exchange and handles errors and observed partial usage.
func Messages(ctx context.Context, p MessagePorts, in MessageInput, parsed *requeststate.ParsedRequest) (*Result, error) {
	startTime := time.Now()
	if parsed == nil {
		return nil, fmt.Errorf("parse request: empty request")
	}

	if in.ProviderPresent && p.ShouldEmulate(ctx, parsed.GroupID, parsed.Body.Bytes()) {
		return p.Emulate(ctx, parsed)
	}

	if in.ProviderPresent && in.Passthrough {
		passthroughBody := parsed.Body.Bytes()
		passthroughModel := parsed.Model
		if passthroughModel != "" {
			if mappedModel := p.MappedModel(passthroughModel); mappedModel != passthroughModel {
				passthroughBody = p.ReplaceModel(passthroughBody, mappedModel)
				p.Log(fmt.Sprintf("Passthrough model mapping: %s -> %s (provider: %s)", parsed.Model, mappedModel, in.ProviderName))
				passthroughModel = mappedModel
			}
		}
		return p.Passthrough(ctx, PassthroughInput{Body: passthroughBody, Parsed: parsed, RequestModel: passthroughModel, OriginalModel: parsed.Model, Stream: parsed.Stream, StartedAt: startTime})
	}

	if in.ProviderPresent && in.Bedrock {
		return p.Bedrock(ctx, parsed, startTime)
	}

	done, err := p.Begin()
	if err != nil {
		return nil, err
	}
	defer done()
	if in.Platform == "anthropic" && in.HTTPPresent {
		if err := p.Beta(ctx, parsed.Model); err != nil {
			return nil, err
		}
	}

	body := parsed.Body.Bytes()
	replaceBody := func(next []byte) error {
		if err := parsed.ReplaceBody(next); err != nil {
			return fmt.Errorf("rewrite request body: %w", err)
		}
		body = parsed.Body.Bytes()
		return nil
	}
	reqModel := parsed.Model
	reqStream := parsed.Stream
	originalModel := reqModel

	if in.HTTPPresent {
		p.DebugOriginal(body, reqModel, reqStream)
	}

	// Apply provider mapping before platform normalization, matching scheduling and quota checks.
	providerMappedModel := p.ProviderMappedModel(reqModel)
	if providerMappedModel != reqModel {
		if err := replaceBody(p.ReplaceModel(body, providerMappedModel)); err != nil {
			return nil, err
		}
		reqModel = providerMappedModel
		parsed.Model = providerMappedModel
		p.Log(fmt.Sprintf("Model mapping applied: %s -> %s (provider: %s, source=provider)", originalModel, providerMappedModel, in.ProviderName))
	}

	// Recognized Claude Code clients supply their own system prompts, cache breakpoints, and identity headers.
	// Skip OAuth mimicry for those clients so replacing a long system prompt does not
	// shorten its cacheable prefix or discard its cache strategy.
	// Other OAuth clients receive the configured system and metadata rewrites below.

	isClaudeCode := p.IsClaudeCode(ctx, body, parsed.MetadataUserID)

	shouldMimicClaudeCode := in.OAuth && !isClaudeCode

	if shouldMimicClaudeCode {
		systemRewritten := false
		systemPromptInjectionEnabled, systemPrompt, systemPromptBlocks := p.SystemSettings(ctx)
		if systemPromptInjectionEnabled {
			if err := replaceBody(p.RewriteSystem(body, parsed, systemPrompt, systemPromptBlocks)); err != nil {
				return nil, err
			}
			systemRewritten = true
		}

		// Keep injected system cache markers after a rewrite; otherwise strip client system markers.
		// CacheLimit later enforces the total marker limit in either case.
		normalizeOpts := NormalizeOptions{StripSystemCacheControl: !systemRewritten}
		if metadata := p.Metadata(ctx, parsed); metadata != "" {
			normalizeOpts.InjectMetadata = true
			normalizeOpts.MetadataUserID = metadata
		}

		normalizedBody := p.NormalizeOAuth(body, normalizeOpts)
		if err := replaceBody(normalizedBody); err != nil {
			return nil, err
		}

		if err := replaceBody(p.RewriteCache(ctx, body)); err != nil {
			return nil, err
		}
		if next, found := p.RewriteTools(body); found {
			if err := replaceBody(next); err != nil {
				return nil, err
			}
			p.BindTools()
		} else if err := replaceBody(p.ToolsLast(body)); err != nil {
			return nil, err
		}
	}

	if next, ok := p.NormalizeDateline(ctx, body); ok {
		if err := replaceBody(next); err != nil {
			return nil, err
		}
	}

	if err := replaceBody(p.CacheLimit(body)); err != nil {
		return nil, err
	}

	mappedModel := p.PlatformModel(reqModel)
	if mappedModel != reqModel {
		if err := replaceBody(p.ReplaceModel(body, mappedModel)); err != nil {
			return nil, err
		}
		reqModel = mappedModel
		parsed.Model = mappedModel
		p.Log(fmt.Sprintf("Platform model normalization applied: %s -> %s (provider: %s)", providerMappedModel, mappedModel, in.ProviderName))
	}

	if p.InjectTTL(ctx) {
		if err := replaceBody(p.CacheTTL(body)); err != nil {
			return nil, err
		}
	}

	if err := p.Credential(ctx); err != nil {
		return nil, err
	}
	p.Transport()

	if err := replaceBody(protocolanthropic.StripEmptyTextBlocks(body)); err != nil {
		return nil, err
	}

	if err := replaceBody(p.FilterSearchHistory(body, reqModel)); err != nil {
		return nil, err
	}

	if err := replaceBody(p.FilterThinking(body, reqModel)); err != nil {
		return nil, err
	}
	if p.PassbackThinking(reqModel) {
		if rewritten, applied := p.NormalizeThinking(body, reqModel); applied {
			if err := replaceBody(rewritten); err != nil {
				return nil, err
			}
			p.Log(fmt.Sprintf("Provider %d: rewrote thinking.type for %s", in.ProviderID, reqModel))
		}
	}

	var resp *ExchangeResponse
	lastWireBody := body
	var earlyResult *Result
	stopped := false
	var streamResult *StreamOutcome
	writerSizeBeforeStream := 0
	before := func(ctx context.Context, response *ExchangeResponse, wire []byte) (bool, error) {
		resp = response
		lastWireBody = wire
		early := func(result *Result, err error) (bool, error) { earlyResult = result; return true, err }
		if resp.StatusCode >= 400 && ShouldRetry(in.OAuth, resp.StatusCode) {
			if ShouldFailover(resp.StatusCode) {
				respBody, _ := p.ReadErrorBody()
				p.ResetErrorBody(respBody)

				p.Log(fmt.Sprintf("[Forward] Upstream error (retry exhausted, failover): Provider=%d(%s) Status=%d RequestID=%s Body=%s",
					in.ProviderID, in.ProviderName, resp.StatusCode, resp.RequestID, p.Truncate(string(respBody), 1000)))

				decision := p.Health(ctx, "retry", resp.StatusCode, resp.Headers, respBody, reqModel)
				if decision.Generic {
					return early(p.HandleError(ctx, reqModel, false))
				}
				p.Observe(Notice{
					Platform:           in.Platform,
					ProviderID:         in.ProviderID,
					ProviderName:       in.ProviderName,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  resp.RequestID,
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
			return early(p.HandleError(ctx, reqModel, true))
		}

		if resp.StatusCode >= 400 && ShouldFailover(resp.StatusCode) {
			respBody, _ := p.ReadErrorBody()
			p.ResetErrorBody(respBody)

			p.Log(fmt.Sprintf("[Forward] Upstream error (failover): Provider=%d(%s) Status=%d RequestID=%s Body=%s",
				in.ProviderID, in.ProviderName, resp.StatusCode, resp.RequestID, p.Truncate(string(respBody), 1000)))

			decision := p.Health(ctx, "failover", resp.StatusCode, resp.Headers, respBody, reqModel)
			if decision.Generic {
				return early(p.HandleError(ctx, reqModel, false))
			}
			p.Observe(Notice{
				Platform:           in.Platform,
				ProviderID:         in.ProviderID,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.RequestID,
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
			if resp.StatusCode == 400 && in.FailoverOn400 {
				respBody, readErr := p.ReadErrorBody()
				if readErr != nil {
					return early(p.HandleError(ctx, reqModel, false))
				}
				p.ResetErrorBody(respBody)

				if p.Failover400(respBody) {
					upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(respBody))
					upstreamMsg = p.Sanitize(upstreamMsg)
					upstreamDetail := ""
					if in.LogErrorBody {
						maxBytes := in.LogErrorBodyMaxBytes
						if maxBytes <= 0 {
							maxBytes = 2048
						}
						upstreamDetail = p.Truncate(string(respBody), maxBytes)
					}
					p.Observe(Notice{
						Platform:           in.Platform,
						ProviderID:         in.ProviderID,
						ProviderName:       in.ProviderName,
						UpstreamStatusCode: resp.StatusCode,
						UpstreamRequestID:  resp.RequestID,
						Kind:               "failover_on_400",
						Message:            upstreamMsg,
						Detail:             upstreamDetail,
					})

					if in.LogErrorBody {
						p.Log(fmt.Sprintf(
							"Provider %d: 400 error, attempting failover: %s",
							in.ProviderID,
							p.TruncateBytes(respBody, in.LogErrorBodyMaxBytes),
						))
					} else {
						p.Log(fmt.Sprintf("Provider %d: 400 error, attempting failover", in.ProviderID))
					}
					decision := p.Health(ctx, "failover", resp.StatusCode, resp.Headers, respBody, reqModel)
					if decision.Generic {
						return early(p.HandleError(ctx, reqModel, false))
					}
					return true, p.FailoverError(resp.StatusCode, respBody, decision.RetrySameProvider)
				}
			}
			return early(p.HandleError(ctx, reqModel, false))
		}

		if !bytes.Equal(lastWireBody, body) {
			if err := replaceBody(lastWireBody); err != nil {
				return true, err
			}
		}

		return false, nil
	}
	attempt, err := p.Execute(ctx, MessageExecution{Model: reqModel, OriginalModel: originalModel, Stream: reqStream, Mimic: shouldMimicClaudeCode, StartedAt: startTime, Body: body, ReplaceBody: replaceBody, Hooks: MessageHooks{
		Before: func(ctx context.Context, response *ExchangeResponse, wire []byte) (bool, error) {
			var err error
			stopped, err = before(ctx, response, wire)
			return stopped, err
		},
		Wire: func(wire []byte) { lastWireBody = wire },
		Accepted: func() {
			if parsed.OnUpstreamAccepted != nil {
				parsed.OnUpstreamAccepted()
			}
		},
		BeforeStream: func() { writerSizeBeforeStream = p.Size() },
		Stream: func(result *StreamOutcome) {
			if result != nil {
				streamResult = result
			}
		},
	}})

	if stopped {
		return earlyResult, err
	}
	if reqStream {
		if err != nil {
			if raw, ok := p.StreamError(err); ok {
				body := []byte(raw)
				semanticStatus := 403
				var semanticDecision ErrorDecision
				if p.Size() == writerSizeBeforeStream && gjson.GetBytes(body, "error.type").String() == "overloaded_error" {
					semanticStatus = 529
					semanticDecision = p.Health(ctx, "failover_synthetic", semanticStatus, resp.Headers, body, reqModel)
				}
				upstreamMsg := p.Sanitize(strings.TrimSpace(upstream.ExtractErrorMessage(body)))

				upstreamDetail := ""
				if in.LogErrorBody {
					maxBytes := in.LogErrorBodyMaxBytes
					if maxBytes <= 0 {
						maxBytes = 2048
					}
					upstreamDetail = p.Truncate(raw, maxBytes)
				}

				p.Observe(Notice{
					Platform:           in.Platform,
					ProviderID:         in.ProviderID,
					ProviderName:       in.ProviderName,
					UpstreamStatusCode: semanticStatus,
					UpstreamRequestID:  resp.RequestID,
					Kind:               "stream_error",
					Message:            upstreamMsg,
					Detail:             upstreamDetail,
				})

				p.Log(fmt.Sprintf(
					"[Forward] SSE error event in stream: Provider=%d(%s) RequestID=%s Body=%s",
					in.ProviderID, in.ProviderName, resp.RequestID,
					p.Truncate(raw, 1000),
				))

				decision := semanticDecision
				if semanticStatus != 529 {
					decision = p.Health(ctx, "volatile", 403, resp.Headers, body, reqModel)
					if p.Size() == writerSizeBeforeStream {
						decision = p.Health(ctx, "persist", 403, resp.Headers, body, reqModel)
					}
				}
				if decision.Generic && !p.Written() {
					p.GenericError()
					return nil, fmt.Errorf("upstream SSE error not in custom error codes")
				}
				if !p.Written() && decision.Failover {
					return nil, p.FailoverError(semanticStatus, body, decision.RetrySameProvider)
				}
				return nil, err
			}
			requestSpeed := gjson.GetBytes(lastWireBody, "speed").String()
			if partial := PartialUsage(resp, streamResult, originalModel, mappedModel, startTime, requestSpeed, p.IsFailover(err)); partial != nil {
				partial.UpstreamResponseModel = attempt.UpstreamResponseModel
				return partial, err
			}
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	usage := &attempt.Usage
	firstTokenMs := attempt.FirstTokenMs
	clientDisconnect := attempt.ClientDisconnect
	if resp == nil {
		return nil, err
	}
	return &Result{
		RequestID:                   resp.RequestID,
		UpstreamHeaders:             resp.Headers,
		Usage:                       *usage,
		Model:                       originalModel,
		UpstreamModel:               mappedModel,
		UpstreamResponseServiceTier: p.ServiceTier(),
		UpstreamResponseModel:       attempt.UpstreamResponseModel,
		Stream:                      reqStream,
		Duration:                    time.Since(startTime),
		FirstTokenMs:                firstTokenMs,
		ClientDisconnect:            clientDisconnect,
	}, nil
}
