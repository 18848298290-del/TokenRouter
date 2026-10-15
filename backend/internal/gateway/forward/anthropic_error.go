package forward

import (
	"context"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// anthropic_error.go handles Anthropic HTTP failures through provider policy,
// retry decisions, and client error output. Anthropic HTTP errors retain their
// source status and body for provider policy even when the client receives a
// different error envelope. Direct handling records activity and diagnostics,
// then selects generic output, failover, or display rules in that order.
// Retry-exhausted handling first gives retry policy the captured body through a
// replacement reader.
//
// Display rules can override status or extracted message after recovery has
// been decided. Unmatched direct HTTP 400 responses retain their raw body;
// other unmatched errors use protocol summaries, while exhausted retries use
// their separate generic summary. Additional body logging does not select
// those client display paths. Error results retain the source status in their
// diagnostic text even when a rule supplies another client-facing status.

// AnthropicError records direct HTTP failure observations and handles the
// selected recovery or client-output path. It can classify partial bytes after
// a body read error. Generic policy writes a 500 response, failover returns its
// signal before commitment, and remaining paths commit before applying display.
//
// A matching display rule supplies status and message fields independently.
// Without one, HTTP 400 writes raw captured bytes and other statuses choose
// built-in summaries. The returned error describes the source status and usable
// summary, while response-resource closure stays with the exchange owner.
func AnthropicError(ctx context.Context, p ErrorPorts, in ErrorInput) (*Result, error) {
	// A completed unsuccessful HTTP exchange still used this provider. Record
	// that activity before parsing the response so body-read failure does not
	// prevent the direct error path's activity observation.
	p.ScheduleActivity()
	body, readErr := p.ReadBody()
	if readErr != nil {
		// A read failure can leave partial body evidence for later classification.
		// Record the read error separately while retaining whatever bytes the
		// adapter supplied for diagnostics and policy.
		p.Log(fmt.Sprintf("[Forward] Failed to fully read upstream error body: Provider=%d(%s) Status=%d err=%v",
			in.ProviderID, in.ProviderName, in.Status, readErr))
	}

	// Keep a bounded routine excerpt even when optional body detail is disabled.
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
		// Bound stored body detail independently of the extracted error summary.
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

	// Health policy can terminate output or request provider recovery before a
	// display rule runs. A display override therefore cannot undo a chosen
	// failover or replace the generic policy rejection.
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

	// Additional body logging follows its diagnostic flag and byte budget.
	// Client display below independently chooses a structured summary, matched
	// rule message, or the direct 400 raw-body path.
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

	// Match display rules against the source status and body, even though the
	// committed client envelope can use another status or message after matching.
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

	// An unmatched 400 retains the upstream's request-validation body. Other
	// statuses use built-in summaries and translate upstream authentication or
	// server failure into gateway errors for the client's protocol.
	var errType, errMsg string
	var statusCode int

	switch in.Status {
	case 400:
		p.Raw(400, body)
		summary := upstreamMsg
		if summary == "" {
			// A missing extracted message leaves the returned diagnostic error with
			// a bounded raw-body excerpt; the separate upstream 400 body has
			// already been submitted through the raw-output path.
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

// AnthropicRetryError handles an HTTP response after upstream retries are
// exhausted. It captures available bytes despite a read error and resets the
// reader before retry health policy, which can select failover or delegate
// generic handling to AnthropicError.
//
// Remaining paths commit and record retry-exhausted diagnostics before applying
// display rules. A missing rule writes the exhausted-retry 502 summary, including
// for source HTTP 400; direct handling has its own raw-400 behavior. The
// exchange adapter owns reader consumption and closure, and this method returns
// the source-status error after selecting the client response.
func AnthropicRetryError(ctx context.Context, p ErrorPorts, in ErrorInput) (*Result, error) {
	// Keep the available error bytes even when the read fails, then reset the
	// consumed reader so retry health policy can inspect that captured response.
	// Later delegated handlers use whichever bytes their adapter reader supplies.
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
		// Retry diagnostics use bounded body evidence beside the extracted summary.
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
