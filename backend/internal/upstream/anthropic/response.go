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

// Buffered Messages responses carry cache counts used both for billing and
// client-visible usage. Ordinary processing resolves compatible cached_tokens
// aliases and cache TTL classification, then restores a matching mapped model
// and tool aliases before sending the body.
//
// Passthrough uses the compatible usage parser and can present ordinary input
// as cache-read tokens in the downstream JSON. Its returned usage keeps the
// parsed upstream counters so billing retains its own conversion point. Raw
// response observations precede presentation edits in both paths. Body reads
// and JSON validation finish before output, while header and content-type
// policy come from the options. These readers borrow the response body; the
// executor owns closing it, and OutputContext retains errors from its
// convenience writes.

// ResponseOptions combines stream observations with buffered body reading,
// validation, and response-presentation choices for Messages readers.
type ResponseOptions struct {
	StreamOptions // shared stream observation options
	// ReadBody reads the buffered response under the caller's configured
	// limits. Readers require this callback; closing resp.Body remains the
	// executor or direct caller's responsibility.
	ReadBody func(io.Reader) ([]byte, error)
	// InvalidJSON classifies malformed successful response bodies. Readers
	// require it on that error path and return its result before any body is
	// sent to the output.
	InvalidJSON func(context.Context, *http.Response, []byte, error) error
	// PreserveContentType permits the ordinary reader to use a nonempty
	// upstream Content-Type. Passthrough independently prefers the trimmed
	// upstream value, defaulting it when absent.
	PreserveContentType bool
	// ForceCacheBilling asks the passthrough buffered reader to present
	// positive input as cache reads in its JSON. Returned local usage retains
	// the parsed upstream counters.
	ForceCacheBilling bool
}

// NonStreamResponse reads and emits a buffered Messages response and returns
// its parsed usage. Callers provide ReadBody and InvalidJSON, serialize the
// output context, and close resp.Body after processing. Malformed successful
// responses use InvalidJSON; other malformed JSON returns a parse error.
//
// Raw observations run before cache, model, and tool presentation edits.
// Positive cached_tokens fills a zero cache-read count, resolved TTL overrides
// update detail buckets, and a mapped model is replaced only at the matching
// model path. Header policy and PreserveContentType choose the output metadata.
// OutputContext.Data retains sink failures in that context; this function
// returns the read or parse result alongside the accumulated usage.
func NonStreamResponse(ctx context.Context, resp *http.Response, c *upstream.OutputContext, options ResponseOptions, originalModel, mappedModel string) (*upstream.TokenUsage, error) {
	if options.UpdateWindow != nil {
		options.UpdateWindow(ctx, resp.Header)
	}

	body, err := options.ReadBody(resp.Body)
	if err != nil {
		return nil, err
	}

	var response struct {
		Usage upstream.TokenUsage `json:"usage"` // decoded upstream token usage
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
	// Buffered JSON keeps TTL detail under cache_creation rather than the flat
	// usage tags. Read both categories when either appears so billing sees
	// their paired breakdown.
	cc5m := gjson.GetBytes(body, "usage.cache_creation.ephemeral_5m_input_tokens")
	cc1h := gjson.GetBytes(body, "usage.cache_creation.ephemeral_1h_input_tokens")
	if cc5m.Exists() || cc1h.Exists() {
		response.Usage.CacheCreation5mTokens = int(cc5m.Int())
		response.Usage.CacheCreation1hTokens = int(cc1h.Int())
	}

	// Compatible responses can name cache reads cached_tokens. A positive alias
	// fills an empty canonical count before billing and client JSON use the
	// selected observation.
	if response.Usage.CacheReadInputTokens == 0 {
		cachedTokens := gjson.GetBytes(body, "usage.cached_tokens").Int()
		if cachedTokens > 0 {
			response.Usage.CacheReadInputTokens = int(cachedTokens)
			// Keep the local alias count even if its client JSON path update
			// fails. Successful editing exposes the same selected cache-read
			// count to the downstream consumer.
			if newBody, err := sjson.SetBytes(body, "usage.cache_read_input_tokens", cachedTokens); err == nil {
				body = newBody
			}
		}
	}

	// The callback resolves current TTL classification. Apply that category to
	// local usage first, then write changed detail buckets so client
	// presentation can follow the selected billing counts.
	if overrideTarget, ok := options.override(ctx); ok {
		if ApplyCacheTTLOverride(&response.Usage, overrideTarget) {
			// Each TTL path edit is best effort after local reclassification. A
			// failed edit retains its prior client bytes while later successful
			// edits can still reflect available categories.
			if newBody, err := sjson.SetBytes(body, "usage.cache_creation.ephemeral_5m_input_tokens", response.Usage.CacheCreation5mTokens); err == nil {
				body = newBody
			}
			if newBody, err := sjson.SetBytes(body, "usage.cache_creation.ephemeral_1h_input_tokens", response.Usage.CacheCreation1hTokens); err == nil {
				body = newBody
			}
		}
	}

	// Provider mapping can change the response's model identity. Restore the
	// client name only at a matching model path so other text containing that
	// name retains its own content.
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

// NonStreamResponsePassthrough emits a buffered API-key response and returns
// compatible-provider usage parsed from its body. Callers provide ReadBody and
// InvalidJSON and retain responsibility for closing the response. Successful
// HTTP responses must contain valid JSON before observations and output
// proceed.
//
// ForceCacheBilling can move input tokens into the downstream cache-read field,
// while the returned usage keeps its parsed counters. Upstream content type is
// trimmed and defaults to application/json, and tool aliases are restored
// before emission. OutputContext.Data retains sink errors separately from this
// function's read, validation, and presentation-update errors.
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
	// Forced cache presentation belongs to downstream JSON. Keep the parsed
	// usage value unchanged here so completion can apply its own billing
	// conversion once to the upstream counters.
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

// ReplaceModelInResponseBody updates model when its gjson Str value equals
// fromModel. A missing field keeps body; nonstring field values have an empty
// Str slot, so an empty fromModel can match them. A failed JSON update retains
// the supplied bytes for downstream output.
func ReplaceModelInResponseBody(body []byte, fromModel, toModel string) []byte {
	if m := gjson.GetBytes(body, "model"); m.Exists() && m.Str == fromModel {
		newBody, err := sjson.SetBytes(body, "model", toModel)
		if err != nil {
			// A failed model path update leaves the response bytes available
			// for output. The caller can still deliver the upstream model field
			// while other response processing continues.
			return body
		}
		return newBody
	}
	return body
}

// ClassifyResponseInputAsCacheRead returns JSON presenting ordinary input as
// cache-read usage. Callers supply nonnil usage corresponding to body; the
// helper reads those counters without mutation and returns an error if either
// JSON update fails.
//
// The destination cache-read count adds the supplied cache-read and input
// counts, while the returned input field is zero. For input five and cache
// reads seven, the response presents zero and twelve while usage retains five
// and seven. No partially classified bytes are returned after a later path
// failure, leaving the caller to handle that failed presentation as one
// operation.
func ClassifyResponseInputAsCacheRead(body []byte, usage *upstream.TokenUsage) ([]byte, error) {
	// The downstream cache-read value includes the parsed input count. Apply
	// both path updates to the candidate body and return it only after both
	// succeed, keeping local usage untouched.
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
