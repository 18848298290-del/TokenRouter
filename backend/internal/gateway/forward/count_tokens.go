package forward

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// CountTokens prepares and sends a count request, allowing one thinking-signature repair retry.
func CountTokens(ctx context.Context, p CountPorts, in MessageInput, parsed *requeststate.ParsedRequest) error {
	if parsed == nil {
		p.CountError(400, "invalid_request_error", "Request body is empty")
		return fmt.Errorf("parse request: empty request")
	}

	if in.ProviderPresent && in.Passthrough {
		passthroughBody := parsed.Body.Bytes()
		mappedModel := parsed.Model
		if reqModel := parsed.Model; reqModel != "" {
			if resolvedModel := p.ResolveModel(ctx, reqModel); resolvedModel != "" {
				mappedModel = resolvedModel
			}
			if mappedModel != reqModel {
				passthroughBody = p.ReplaceModel(passthroughBody, mappedModel)
				p.Log(fmt.Sprintf("CountTokens passthrough model mapping: %s -> %s (provider: %s)", reqModel, mappedModel, in.ProviderName))
			}
		}
		return CountPassthrough(ctx, p, in, passthroughBody, mappedModel)
	}

	// This Bedrock path does not support count_tokens.
	if in.ProviderPresent && in.Bedrock {
		p.CountError(404, "not_found_error", "count_tokens endpoint is not supported for Bedrock")
		return nil
	}

	// Return 404 for Antigravity and Qoder so clients can estimate tokens locally.
	// The response is complete, so return nil to the handler.
	if in.Platform == "antigravity" || in.Platform == "qoder" {
		p.CountError(404, "not_found_error", "count_tokens endpoint is not supported for this platform")
		return nil
	}

	body := parsed.Body.Bytes()
	replaceBody := func(next []byte) error {
		if err := parsed.ReplaceBody(next); err != nil {
			return fmt.Errorf("rewrite count_tokens body: %w", err)
		}
		body = parsed.Body.Bytes()
		return nil
	}
	reqModel := parsed.Model

	// Use the same provider mapping and platform normalization order as Messages and scheduling.
	if reqModel != "" {
		upstreamModel := p.ResolveModel(ctx, reqModel)
		if upstreamModel != "" && upstreamModel != reqModel {
			originalReqModel := reqModel
			if err := replaceBody(p.ReplaceModel(body, upstreamModel)); err != nil {
				return err
			}
			reqModel = upstreamModel
			parsed.Model = upstreamModel
			p.Log(fmt.Sprintf("CountTokens final model applied: %s -> %s (provider: %s)", originalReqModel, upstreamModel, in.ProviderName))
		}
	}

	if err := replaceBody(protocolanthropic.StripEmptyTextBlocks(body)); err != nil {
		return err
	}

	isClaudeCodeCT := p.IsCountClaudeCode(ctx, parsed.MetadataUserID)
	shouldMimicClaudeCode := in.OAuth && !isClaudeCodeCT

	if shouldMimicClaudeCode {
		normalizeOpts := NormalizeOptions{StripSystemCacheControl: true}
		normalizedBody := p.NormalizeOAuth(body, normalizeOpts)
		if err := replaceBody(normalizedBody); err != nil {
			return err
		}

		if err := replaceBody(p.RewriteCache(ctx, body)); err != nil {
			return err
		}
		if next, found := p.RewriteTools(body); found {
			if err := replaceBody(next); err != nil {
				return err
			}
		} else if err := replaceBody(p.ToolsLast(body)); err != nil {
			return err
		}
	}

	err := p.Credential(ctx)
	if err != nil {
		p.CountError(502, "upstream_error", "Failed to get access token")
		return err
	}

	wireBody, err := p.BuildCount(ctx, body, reqModel, shouldMimicClaudeCode, false)
	if err != nil {
		p.CountError(500, "api_error", "Failed to build request")
		return err
	}
	// Retain the first wire body; a signature retry is rebuilt from the unsigned logical body.
	acceptedWireBody := wireBody

	resp, err := p.SendCount(ctx, false)
	if err != nil {
		p.SetError(0, p.Sanitize(err.Error()), "")
		p.CountError(502, "upstream_error", "Request failed")
		return fmt.Errorf("upstream request failed: %w", err)
	}

	respBody, err := p.ReadCount()
	if err != nil {
		if !p.IsTooLarge(err) {
			p.CountError(502, "upstream_error", "Failed to read response")
		}
		return err
	}

	// A recognized HTTP 400 signature failure permits one retry with filtered thinking blocks.
	if resp.StatusCode == 400 && p.RectifyCount(ctx, respBody, reqModel) {
		p.Log(fmt.Sprintf("Provider %d: detected thinking block signature error on count_tokens, retrying with filtered thinking blocks", in.ProviderID))

		filteredBody := p.FilterCountRetry(body, reqModel)
		retryWireBody, buildErr := p.BuildCount(ctx, filteredBody, reqModel, shouldMimicClaudeCode, false)
		if buildErr == nil {
			retryResp, retryErr := p.SendCount(ctx, false)
			if retryErr == nil {
				if retryResp.StatusCode < 400 {
					// Adopt the retry wire body only after a successful response.
					acceptedWireBody = retryWireBody
				}
				resp = retryResp
				respBody, err = p.ReadCount()
				if err != nil {
					if !p.IsTooLarge(err) {
						p.CountError(502, "upstream_error", "Failed to read response")
					}
					return err
				}
			}
		}
	}

	if resp.StatusCode < 400 && !bytes.Equal(acceptedWireBody, body) {
		// Publish the accepted wire body for later request processing.
		if err := replaceBody(acceptedWireBody); err != nil {
			return err
		}
	}

	if resp.StatusCode >= 400 {
		upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(respBody))
		upstreamMsg = p.Sanitize(upstreamMsg)
		if p.UnsupportedCount(resp.StatusCode, respBody) {
			p.CountError(404, "not_found_error", "count_tokens endpoint is not supported by upstream")
			return nil
		}
		decision := p.CountHealth(ctx, resp.StatusCode, resp.Headers, respBody, reqModel)
		if decision.Generic {
			p.CountError(500, "upstream_error", "Upstream gateway error")
			return fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
		}
		if decision.Failover {
			return p.CountFailover(resp.StatusCode, resp.Headers, respBody, decision.RetrySameProvider)
		}
		upstreamDetail := ""
		if in.LogErrorBody {
			maxBytes := in.LogErrorBodyMaxBytes
			if maxBytes <= 0 {
				maxBytes = 2048
			}
			upstreamDetail = p.Truncate(string(respBody), maxBytes)
		}
		p.SetError(resp.StatusCode, upstreamMsg, upstreamDetail)

		// Optional error-body diagnostics stay in logs.
		if in.LogErrorBody {
			p.Log(fmt.Sprintf(
				"count_tokens upstream error %d (provider=%d platform=%s type=%s): %s",
				resp.StatusCode,
				in.ProviderID,
				in.Platform,
				in.ProviderType,
				p.TruncateBytes(respBody, in.LogErrorBodyMaxBytes),
			))
		}

		// Use a safe summary for the client error response.
		errMsg := "Upstream request failed"
		switch resp.StatusCode {
		case 429:
			errMsg = "Rate limit exceeded"
		case 529:
			errMsg = "Service overloaded"
		}
		p.CountError(resp.StatusCode, "upstream_error", errMsg)
		if upstreamMsg == "" {
			return fmt.Errorf("upstream error: %d", resp.StatusCode)
		}
		return fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	p.CountSuccess(resp.StatusCode, resp.Headers, respBody, false)
	return nil
}

// CountPassthrough sends an API-key count request and handles its response.
func CountPassthrough(ctx context.Context, p CountPorts, in MessageInput, body []byte, mappedModel string) error {
	err := p.Credential(ctx)
	if err != nil {
		p.CountError(502, "upstream_error", "Failed to get access token")
		return err
	}
	if tokenType := p.TokenKind(); tokenType != "apikey" {
		p.CountError(502, "upstream_error", "Invalid provider token type")
		return fmt.Errorf("anthropic api key passthrough requires apikey token, got: %s", tokenType)
	}

	_, err = p.BuildCount(ctx, body, mappedModel, false, true)
	if err != nil {
		p.CountError(500, "api_error", "Failed to build request")
		return err
	}

	resp, err := p.SendCount(ctx, true)
	if err != nil {
		p.SetError(0, p.Sanitize(err.Error()), "")
		p.Observe(Notice{
			Platform:           in.Platform,
			ProviderID:         in.ProviderID,
			ProviderName:       in.ProviderName,
			UpstreamStatusCode: 0,
			UpstreamURL:        p.CountURL(),
			Passthrough:        true,
			Kind:               "request_error",
			Message:            p.Sanitize(err.Error()),
		})
		p.CountError(502, "upstream_error", "Request failed")
		return fmt.Errorf("upstream request failed: %w", err)
	}

	respBody, err := p.ReadCount()
	if err != nil {
		if !p.IsTooLarge(err) {
			p.CountError(502, "upstream_error", "Failed to read response")
		}
		return err
	}

	if resp.StatusCode >= 400 {
		upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(respBody))
		upstreamMsg = p.Sanitize(upstreamMsg)

		// A confirmed unsupported count endpoint returns 404 and nil so clients can estimate locally.
		// Other 404 failures, such as an incorrect base URL, follow upstream error policy.
		if p.UnsupportedCount(resp.StatusCode, respBody) {
			p.Log(fmt.Sprintf(
				"[count_tokens] Upstream does not support count_tokens (404), returning 404: provider=%d name=%s msg=%s",
				in.ProviderID, in.ProviderName, p.Truncate(upstreamMsg, 512)))
			p.CountError(404, "not_found_error", "count_tokens endpoint is not supported by upstream")
			return nil
		}
		decision := p.CountHealth(ctx, resp.StatusCode, resp.Headers, respBody, mappedModel)
		if decision.Generic {
			p.CountError(500, "upstream_error", "Upstream gateway error")
			return fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
		}
		if decision.Failover {
			return p.CountFailover(resp.StatusCode, resp.Headers, respBody, decision.RetrySameProvider)
		}

		upstreamDetail := ""
		if in.LogErrorBody {
			maxBytes := in.LogErrorBodyMaxBytes
			if maxBytes <= 0 {
				maxBytes = 2048
			}
			upstreamDetail = p.Truncate(string(respBody), maxBytes)
		}
		p.SetError(resp.StatusCode, upstreamMsg, upstreamDetail)
		p.Observe(Notice{
			Platform:           in.Platform,
			ProviderID:         in.ProviderID,
			ProviderName:       in.ProviderName,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.RequestID,
			UpstreamURL:        p.CountURL(),
			Passthrough:        true,
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})

		errMsg := "Upstream request failed"
		switch resp.StatusCode {
		case 429:
			errMsg = "Rate limit exceeded"
		case 529:
			errMsg = "Service overloaded"
		}
		p.CountError(resp.StatusCode, "upstream_error", errMsg)
		if upstreamMsg == "" {
			return fmt.Errorf("upstream error: %d", resp.StatusCode)
		}
		return fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	p.CountSuccess(resp.StatusCode, resp.Headers, respBody, true)
	return nil
}
