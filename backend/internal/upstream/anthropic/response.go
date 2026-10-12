package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

type ResponseOptions struct {
	StreamOptions
	ReadBody            func(io.Reader) ([]byte, error)
	InvalidJSON         func(context.Context, *http.Response, []byte, error) error
	PreserveContentType bool
	ForceCacheBilling   bool
}

func NonStreamResponse(ctx context.Context, resp *http.Response, c *upstream.OutputContext, options ResponseOptions, originalModel, mappedModel string) (*upstream.TokenUsage, error) {
	if options.UpdateWindow != nil {
		options.UpdateWindow(ctx, resp.Header)
	}

	body, err := options.ReadBody(resp.Body)
	if err != nil {
		return nil, err
	}

	var response struct {
		Usage upstream.TokenUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return nil, options.InvalidJSON(ctx, resp, body, err)
		}
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if options.Observe != nil {
		options.Observe(wire.ObserveMessage(string(body)))
	}
	// Read the nested five-minute and one-hour cache creation counters.
	cc5m := gjson.GetBytes(body, "usage.cache_creation.ephemeral_5m_input_tokens")
	cc1h := gjson.GetBytes(body, "usage.cache_creation.ephemeral_1h_input_tokens")
	if cc5m.Exists() || cc1h.Exists() {
		response.Usage.CacheCreation5mTokens = int(cc5m.Int())
		response.Usage.CacheCreation1hTokens = int(cc1h.Int())
	}

	// Accept Kimi cached_tokens when the Anthropic cache-read counter is zero.
	if response.Usage.CacheReadInputTokens == 0 {
		cachedTokens := gjson.GetBytes(body, "usage.cached_tokens").Int()
		if cachedTokens > 0 {
			response.Usage.CacheReadInputTokens = int(cachedTokens)
			if newBody, err := sjson.SetBytes(body, "usage.cache_read_input_tokens", cachedTokens); err == nil {
				body = newBody
			}
		}
	}

	// Use the resolved cache TTL billing override for both internal usage and
	// the client response. The OverrideCache callback resolves provider/global precedence.
	if overrideTarget, ok := options.override(ctx); ok {
		if ApplyCacheTTLOverride(&response.Usage, overrideTarget) {
			// Keep client-visible cache categories consistent with billing usage.
			if newBody, err := sjson.SetBytes(body, "usage.cache_creation.ephemeral_5m_input_tokens", response.Usage.CacheCreation5mTokens); err == nil {
				body = newBody
			}
			if newBody, err := sjson.SetBytes(body, "usage.cache_creation.ephemeral_1h_input_tokens", response.Usage.CacheCreation1hTokens); err == nil {
				body = newBody
			}
		}
	}

	// Restore the requested model name after provider mapping.
	if originalModel != mappedModel {
		body = ReplaceModelInResponseBody(body, mappedModel, originalModel)
	}

	if options.WriteHeaders != nil {
		options.WriteHeaders(c.Writer.Header(), resp.Header)
	}

	contentType := "application/json"
	if options.PreserveContentType {
		if upstreamType := resp.Header.Get("Content-Type"); upstreamType != "" {
			contentType = upstreamType
		}
	}

	body = RestoreToolNamesInBytes(body, options.ToolNames)

	observation := wire.ObserveMessage(string(body))
	c.NextEvent(observation.Semantic, true)
	c.Data(resp.StatusCode, contentType, body)

	return &response.Usage, nil
}

func NonStreamResponsePassthrough(
	ctx context.Context,
	resp *http.Response,
	c *upstream.OutputContext,
	options ResponseOptions,
) (*upstream.TokenUsage, error) {
	if options.UpdateWindow != nil {
		options.UpdateWindow(ctx, resp.Header)
	}

	body, err := options.ReadBody(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		var raw json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, options.InvalidJSON(ctx, resp, body, err)
		}
	}

	if options.Observe != nil {
		options.Observe(wire.ObserveMessage(string(body)))
	}
	usage := wire.ParseClaudeUsageFromResponseBody(body)
	if options.ForceCacheBilling && usage.InputTokens > 0 {
		body, err = ClassifyResponseInputAsCacheRead(body, usage)
		if err != nil {
			return nil, err
		}
	}

	if options.WriteHeaders != nil {
		options.WriteHeaders(c.Writer.Header(), resp.Header)
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	body = RestoreToolNamesInBytes(body, options.ToolNames)
	observation := wire.ObserveMessage(string(body))
	c.NextEvent(observation.Semantic, true)
	c.Data(resp.StatusCode, contentType, body)
	return usage, nil
}

// ReplaceModelInResponseBody replaces model only when it equals fromModel.
// A JSON path update leaves unrelated response fields unchanged.
func ReplaceModelInResponseBody(body []byte, fromModel, toModel string) []byte {
	if m := gjson.GetBytes(body, "model"); m.Exists() && m.Str == fromModel {
		newBody, err := sjson.SetBytes(body, "model", toModel)
		if err != nil {
			return body
		}
		return newBody
	}
	return body
}

// ClassifyResponseInputAsCacheRead moves input tokens into the response cache-read count.
// The supplied usage is read without mutation.
func ClassifyResponseInputAsCacheRead(body []byte, usage *upstream.TokenUsage) ([]byte, error) {
	classified, err := sjson.SetBytes(body, "usage.input_tokens", 0)
	if err != nil {
		return nil, fmt.Errorf("classify forced cache billing input tokens: %w", err)
	}
	classified, err = sjson.SetBytes(classified, "usage.cache_read_input_tokens", usage.CacheReadInputTokens+usage.InputTokens)
	if err != nil {
		return nil, fmt.Errorf("classify forced cache billing cache read tokens: %w", err)
	}
	return classified, nil
}
