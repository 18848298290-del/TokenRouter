package openaiforward

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// RawChatEndpoint identifies the upstream endpoint in results from all three compatible entry points.
const RawChatEndpoint = "/v1/chat/completions"

func RunRawChat(ctx context.Context, body []byte, defaultMappedModel string, p RawChatPorts) (*Result, error) {
	profile := p.Profile()
	startTime := time.Now()

	originalModel := gjson.GetBytes(body, "model").String()
	if originalModel == "" {
		p.Error(http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	clientStream := gjson.GetBytes(body, "stream").Bool()

	// Resolve billing and upstream models using the Chat entry point's rules.
	billingModel := p.BillingModel(originalModel, defaultMappedModel)
	upstreamModel := p.UpstreamModel(billingModel)
	p.ObserveModel(upstreamModel)
	grokCacheIdentity := ""
	if profile.Grok {
		// Derive identity before image bridging or body rewriting so it uses the stable client session prefix.
		grokCacheIdentity = p.GrokCacheIdentity(body, "", upstreamModel)
	}
	upstreamBody := body
	if upstreamModel != originalModel {
		upstreamBody = p.ReplaceModel(body, upstreamModel)
	}
	upstreamBody, err := p.PrepareModel(ctx, upstreamBody)
	if err != nil {
		return nil, err
	}

	updatedBody, policyErr := p.FastRaw(ctx, upstreamModel, upstreamBody)
	if policyErr != nil {
		return nil, policyErr
	}
	upstreamBody = updatedBody
	// Keep outgoing and observed tiers separate; billing selects between them by credential type.
	serviceTier := p.ServiceTier(upstreamBody)
	if profile.Grok {
		strippedBody, stripErr := p.StripViewImage(upstreamBody)
		if stripErr != nil {
			return nil, fmt.Errorf("strip redundant Grok Chat view_image tool: %w", stripErr)
		}
		upstreamBody = strippedBody
	}

	// The image bridge uses these credentials to obtain Grok Build descriptions for Composer image input.
	token, tokenKind, err := p.RawCredential(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("provider %d missing %s credential", profile.ID, tokenKind)
	}

	var bridgeUsage protocolopenai.ForwardUsage
	if profile.Grok {
		bridgedBody, usage, bridged, bridgeErr := p.BridgeImages(ctx, upstreamBody, token)
		if bridgeErr != nil {
			return nil, bridgeErr
		}
		if bridged {
			upstreamBody = bridgedBody
			protocolopenai.AddForwardUsage(&bridgeUsage, usage)
		}
	}

	if clientStream {
		var usageErr error
		upstreamBody, usageErr = protocolopenai.EnsureOpenAIChatStreamUsage(upstreamBody)
		if usageErr != nil {
			return nil, fmt.Errorf("enable stream usage: %w", usageErr)
		}
	}
	if profile.Grok {
		upstreamBody, err = p.StripGrokCacheKey(upstreamBody)
		if err != nil {
			return nil, fmt.Errorf("remove Responses-only Grok prompt cache key: %w", err)
		}
		upstreamBody, err = p.GrokEffort(upstreamBody, upstreamModel)
		if err != nil {
			return nil, fmt.Errorf("normalize Grok chat reasoning effort: %w", err)
		}
	}
	upstreamBody = p.OllamaBody(upstreamBody)

	// Record effort after applying platform parameter conversions.
	reasoningEffort := p.EffectiveEffort(upstreamBody, body, upstreamModel, billingModel, originalModel)
	reasoningEffort = p.ThinkingFallback(reasoningEffort, upstreamBody, billingModel)

	p.Debug("openai chat_completions raw: forwarding without protocol conversion",
		zap.Int64("provider_id", profile.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)

	// Use the shared Chat transport for upstream request construction and sending.
	targetURL, err := p.RawTarget()
	if err != nil {
		return nil, err
	}
	p.Endpoint(RawChatEndpoint)
	customUA := p.UserAgent()
	if customUA == "" && profile.GrokOAuth {
		customUA = p.GrokUserAgent()
	}
	resp, err := p.SendRaw(ctx, targetURL, upstreamBody, clientStream, token, customUA, grokCacheIdentity)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := p.ReadUpstreamError(resp)
		if profile.Grok {
			decision := p.GrokDecision(ctx, resp, respBody, upstreamModel)
			kind := "http_error"
			if decision.Failover {
				kind = "failover"
			}
			p.ObserveGrokError(resp, upstreamMsg, kind)
			if decision.Generic {
				return p.ChatErrorResponse(resp, billingModel)
			}
			if kind == "failover" {
				return nil, p.GrokFailover(resp, respBody, p.GrokRetry(resp.StatusCode, respBody), decision.RetrySame)
			}
			return p.ChatErrorResponse(resp, billingModel)
		}
		if foErr := p.FailoverHTTP(ctx, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		return p.ChatErrorResponse(resp, billingModel)
	}

	if profile.Grok {
		p.UpdateGrokUsage(ctx, upstreamModel, resp.Header, resp.StatusCode)
	}

	output := upstream.NewDeferredOutputContext(p.Sink())
	options := p.RawOptions(resp, billingModel, upstreamModel, serviceTier)
	var observed *openai.CompatResponseResult
	var forwardErr error
	if clientStream {
		observed, forwardErr = openai.ReadRawChatStreaming(output, resp, options, originalModel, upstreamModel, reasoningEffort, startTime, len(body))
	} else {
		observed, forwardErr = openai.ReadRawChatBuffered(output, resp, options, originalModel, upstreamModel, reasoningEffort, startTime)
	}
	result := FromCompatResult(observed, billingModel)
	if result != nil {
		protocolopenai.AddForwardUsage(&result.Usage, bridgeUsage)
		result.UpstreamEndpoint = RawChatEndpoint
	}
	return result, forwardErr
}
