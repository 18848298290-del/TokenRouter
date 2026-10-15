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

// count_tokens.go prepares and forwards token-count requests and handles
// unsupported endpoints and signature repair. Count input can need the same
// selected model and OAuth cache/tool normalization as generation, while
// unsupported count endpoints have a handled 404 outcome that lets the client
// use its own estimate. API-key passthrough takes its separate path before
// platform unsupported checks and requires the acquired token to have the exact
// apikey kind.
//
// The ordinary count path keeps logical bytes separate from its first wire
// representation. A recognized signature failure permits one rebuilt request;
// build or transport failure retains the first captured metadata and body for
// final handling. Only an HTTP status below 400 can select the wire body
// republished to parsed state, and response reading must succeed before that
// publication. This keeps later request processing tied to accepted input rather
// than an unaccepted repair proposal.

// CountTokens prepares a count request, writes its protocol result, or returns
// a retry/error signal. Nil parsed input writes 400 and returns an error; handled
// unsupported-platform or endpoint outcomes write 404 and return nil. A successful
// send requires nonnil response metadata and the adapter owns response-body closure.
//
// Model and normalization updates reparse the shared request body. Signature
// repair is attempted once from logical bytes; failed repair building or sending
// retains the first captured response, while a returned repair response becomes the current
// response even if its HTTP status is another error. Accepted wire bytes are
// published only after a status below 400 and a successful body read. Size-limit reader
// errors keep the reader's dedicated output instead of adding a generic count error.
func CountTokens(ctx context.Context, p CountPorts, in MessageInput, parsed *requeststate.ParsedRequest) error {
	if parsed == nil {
		p.CountError(400, "invalid_request_error", "Request body is empty")
		return fmt.Errorf("parse request: empty request")
	}

	// Passthrough keeps mapped bytes local while BuildCount receives the
	// resolved provider model. The shared parsed request retains its input
	// representation through this early delegation.
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

	// This selected Bedrock path has no count exchange. Write the handled 404
	// before returning nil so the outer handler does not add another error envelope.
	if in.ProviderPresent && in.Bedrock {
		p.CountError(404, "not_found_error", "count_tokens endpoint is not supported for Bedrock")
		return nil
	}

	// These platform paths expose count unavailability as a handled 404 so
	// clients can use their local estimate. Nil reports that response handling
	// is finished, not that an upstream count succeeded.
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

	// Count the selected provider's resolved model rather than a client alias.
	// Reparse changed bytes and publish the corresponding model so later views
	// consult the same normalized request that will be built.
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

	// Count and Messages preparation share empty-text normalization. Apply it
	// before OAuth cache and tool rewrites so later count preparation reads
	// the same normalized content representation.
	if err := replaceBody(protocolanthropic.StripEmptyTextBlocks(body)); err != nil {
		return err
	}

	isClaudeCodeCT := p.IsCountClaudeCode(ctx, parsed.MetadataUserID)
	shouldMimicClaudeCode := in.OAuth && !isClaudeCodeCT

	// Recognized Claude Code count input retains its existing client
	// preparation. Other OAuth input applies system-cache stripping and cache
	// policy before selecting rewritten tools or the tools-last breakpoint.
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
	// Keep the serialized first request separate from logical input. A repair
	// rebuilds from the logical body and can publish its own wire representation
	// only after an upstream response accepts it.
	acceptedWireBody := wireBody

	resp, err := p.SendCount(ctx, false)
	if err != nil {
		p.SetError(0, p.Sanitize(err.Error()), "")
		p.CountError(502, "upstream_error", "Request failed")
		return fmt.Errorf("upstream request failed: %w", err)
	}

	respBody, err := p.ReadCount()
	if err != nil {
		// Limit errors already use the reader's dedicated count response.
		// Other read failures need the generic envelope selected here.
		if !p.IsTooLarge(err) {
			p.CountError(502, "upstream_error", "Failed to read response")
		}
		return err
	}

	// A recognized signature failure permits one provider-local repair. Wire
	// bytes can contain derived attribution or signatures, so rebuild from
	// filtered logical input to keep those fields consistent with the repair.
	if resp.StatusCode == 400 && p.RectifyCount(ctx, respBody, reqModel) {
		p.Log(fmt.Sprintf("Provider %d: detected thinking block signature error on count_tokens, retrying with filtered thinking blocks", in.ProviderID))

		// Repair is optional after the original response has been captured.
		// A repair build or transport error retains that earlier metadata and
		// body so final policy still has the original upstream failure to classify.
		filteredBody := p.FilterCountRetry(body, reqModel)
		retryWireBody, buildErr := p.BuildCount(ctx, filteredBody, reqModel, shouldMimicClaudeCode, false)
		if buildErr == nil {
			retryResp, retryErr := p.SendCount(ctx, false)
			if retryErr == nil {
				if retryResp.StatusCode < 400 {
					// A successful retry status selects its rebuilt wire input for later
					// publication. The following body read must also succeed before parsed
					// state is updated from this candidate.
					acceptedWireBody = retryWireBody
				}
				resp = retryResp
				respBody, err = p.ReadCount()
				if err != nil {
					// The retry reader owns size-limit output too; add generic output
					// only for another read failure before returning the error.
					if !p.IsTooLarge(err) {
						p.CountError(502, "upstream_error", "Failed to read response")
					}
					return err
				}
			}
		}
	}

	if resp.StatusCode < 400 && !bytes.Equal(acceptedWireBody, body) {
		// Publish the wire representation associated with the accepted response
		// after its body was read. Later parsed-body consumers then observe that
		// accepted request rather than an unaccepted signature-repair candidate.
		if err := replaceBody(acceptedWireBody); err != nil {
			return err
		}
	}

	if resp.StatusCode >= 400 {
		upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(respBody))
		upstreamMsg = p.Sanitize(upstreamMsg)
		// Endpoint unavailability is handled before health classification. A
		// completed 404 response lets the client estimate locally and ends this
		// attempt without returning another output error to its caller.
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
			// Store bounded response detail independently of the extracted summary.
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

		// Keep the extracted upstream message in diagnostics while count output
		// uses fixed summaries. Rate-limit and overload statuses select their
		// specific client explanations after provider recovery has been decided.
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

// CountPassthrough builds and sends a count request with an exact apikey token
// and no client mimicry. It writes the protocol response on ordinary success or
// handled endpoint unavailability, while provider policy can return a failover signal.
// The adapter reads and closes each response body before final handling; a response
// limit error retains its dedicated output rather than adding another generic error.
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
		// Preserve the reader's dedicated limit response. Other read failures
		// still need the generic count error before the caller returns.
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
			// Store bounded response detail independently of the extracted summary.
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
