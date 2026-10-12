package forward

import (
	"context"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// AnthropicError records diagnostics, applies provider health policy, and writes an error when failover is not selected.
func AnthropicError(ctx context.Context, p ErrorPorts, in ErrorInput) (*Result, error) {
	// Failed HTTP responses still count as provider activity.
	p.ScheduleActivity()
	body, readErr := p.ReadBody()
	if readErr != nil {
		// A failed read may leave a partial body; log the read error alongside classification diagnostics.
		p.Log(fmt.Sprintf("[Forward] Failed to fully read upstream error body: Provider=%d(%s) Status=%d err=%v",
			in.ProviderID, in.ProviderName, in.Status, readErr))
	}

	p.Log(fmt.Sprintf("[Forward] Upstream error (non-retryable): Provider=%d(%s) Status=%d RequestID=%s Body=%s",
		in.ProviderID, in.ProviderName, in.Status, in.RequestID, p.Truncate(string(body), 1000)))

	upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(body))
	upstreamMsg = p.Sanitize(upstreamMsg)

	p.ScopeDiagnostic(upstreamMsg, in.Status, in.RequestID)

	upstreamDetail := ""
	if in.LogBody {
		maxBytes := in.LogBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = p.Truncate(string(body), maxBytes)
	}
	p.SetError(in.Status, upstreamMsg, upstreamDetail)
	p.Observe(Notice{
		Platform:           in.Platform,
		ProviderID:         in.ProviderID,
		UpstreamStatusCode: in.Status,
		UpstreamRequestID:  in.RequestID,
		Kind:               "http_error",
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})

	// Provider health policy chooses generic output or failover before display rules run.
	decision := p.Health(ctx, in.Status, in.RequestedModels)
	if decision.Generic {
		p.Commit()
		p.Message(500, "upstream_error", "Upstream gateway error")
		return nil, fmt.Errorf("upstream error: %d (not in custom error codes)", in.Status)
	}
	if decision.Failover {
		return nil, p.Failover(in.Status, body, decision.RetrySameProvider)
	}

	p.Commit()

	// Optional body diagnostics stay in logs rather than the client error message.
	if in.LogBody {
		p.Log(fmt.Sprintf(
			"Upstream error %d (provider=%d platform=%s type=%s): %s",
			in.Status,
			in.ProviderID,
			in.Platform,
			in.ProviderType,
			p.TruncateBytes(body, in.LogBodyMaxBytes),
		))
	}

	// Display rules also apply when the error cannot fail over.
	if status, errType, errMsg, matched := applyAnthropicDisplayRule(
		p,
		in.Platform,
		in.Status,
		body,
		502,
		"upstream_error",
		"Upstream request failed",
	); matched {
		p.Message(status, errType, errMsg)

		summary := upstreamMsg
		if summary == "" {
			summary = errMsg
		}
		if summary == "" {
			return nil, fmt.Errorf("upstream error: %d (passthrough rule matched)", in.Status)
		}
		return nil, fmt.Errorf("upstream error: %d (passthrough rule matched) message=%s", in.Status, summary)
	}

	// Pass through HTTP 400 bodies; use safe built-in messages for other unmatched errors.
	var errType, errMsg string
	var statusCode int

	switch in.Status {
	case 400:
		p.Raw(400, body)
		summary := upstreamMsg
		if summary == "" {
			summary = p.TruncateBytes(body, 512)
		}
		if summary == "" {
			return nil, fmt.Errorf("upstream error: %d", in.Status)
		}
		return nil, fmt.Errorf("upstream error: %d message=%s", in.Status, summary)
	case 401:
		statusCode = 502
		errType = "upstream_error"
		errMsg = "Upstream authentication failed, please contact administrator"
	case 403:
		statusCode = 502
		errType = "upstream_error"
		errMsg = "Upstream access forbidden, please contact administrator"
	case 429:
		statusCode = 429
		errType = "rate_limit_error"
		errMsg = "Upstream rate limit exceeded, please retry later"
	case 529:
		statusCode = 503
		errType = "overloaded_error"
		errMsg = "Upstream service overloaded, please retry later"
	case 500, 502, 503, 504:
		statusCode = 502
		errType = "upstream_error"
		errMsg = "Upstream service temporarily unavailable"
	default:
		statusCode = 502
		errType = "upstream_error"
		errMsg = "Upstream request failed"
	}

	p.Message(statusCode, errType, errMsg)

	if upstreamMsg == "" {
		return nil, fmt.Errorf("upstream error: %d", in.Status)
	}
	return nil, fmt.Errorf("upstream error: %d message=%s", in.Status, upstreamMsg)
}

// AnthropicRetryError applies health and display policy after upstream retries fail.
func AnthropicRetryError(ctx context.Context, p ErrorPorts, in ErrorInput) (*Result, error) {
	respBody, _ := p.ReadBody()
	p.ResetBody(respBody)

	decision := p.RetryHealth(ctx, in.RequestedModels)
	if decision.Generic {
		return AnthropicError(ctx, p, in)
	}
	if decision.Failover {
		return nil, p.Failover(in.Status, respBody, decision.RetrySameProvider)
	}
	p.Commit()

	upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(respBody))
	upstreamMsg = p.Sanitize(upstreamMsg)

	p.ScopeDiagnostic(upstreamMsg, in.Status, in.RequestID)

	upstreamDetail := ""
	if in.LogBody {
		maxBytes := in.LogBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = p.Truncate(string(respBody), maxBytes)
	}
	p.SetError(in.Status, upstreamMsg, upstreamDetail)
	p.Observe(Notice{
		Platform:           in.Platform,
		ProviderID:         in.ProviderID,
		UpstreamStatusCode: in.Status,
		UpstreamRequestID:  in.RequestID,
		Kind:               "retry_exhausted",
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})

	if in.LogBody {
		p.Log(fmt.Sprintf(
			"Upstream error %d retries_exhausted (provider=%d platform=%s type=%s): %s",
			in.Status,
			in.ProviderID,
			in.Platform,
			in.ProviderType,
			p.TruncateBytes(respBody, in.LogBodyMaxBytes),
		))
	}

	if status, errType, errMsg, matched := applyAnthropicDisplayRule(
		p,
		in.Platform,
		in.Status,
		respBody,
		502,
		"upstream_error",
		"Upstream request failed after retries",
	); matched {
		p.Message(status, errType, errMsg)

		summary := upstreamMsg
		if summary == "" {
			summary = errMsg
		}
		if summary == "" {
			return nil, fmt.Errorf("upstream error: %d (retries exhausted, passthrough rule matched)", in.Status)
		}
		return nil, fmt.Errorf("upstream error: %d (retries exhausted, passthrough rule matched) message=%s", in.Status, summary)
	}

	p.Message(502, "upstream_error", "Upstream request failed after retries")

	if upstreamMsg == "" {
		return nil, fmt.Errorf("upstream error: %d (retries exhausted)", in.Status)
	}
	return nil, fmt.Errorf("upstream error: %d (retries exhausted) message=%s", in.Status, upstreamMsg)
}
