package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// openai_response_timeout.go selects first-output timeout budgets and
// constructs timeout failover evidence.
// Native OpenAI streaming attempts can bound the wait for first semantic
// output across header and stream phases. The base timeout enables that
// budget, high reasoning efforts may override it, and timeout failures carry an
// upstream 504 observation plus replay permission for staged output.

// FirstOutputTimeout selects the first-output budget from configured seconds.
// Nil output, unconfigured options or a nonpositive base disables it; trimmed
// high, xhigh and max efforts use a positive high-effort override when
// configured.
func (p *OpenAIResponseOutput) FirstOutputTimeout(reasoningEffort string) time.Duration {
	if p == nil || !p.Options.Configured || p.Options.OpenAIFirstOutputTimeoutSeconds <= 0 {
		return 0
	}
	seconds := p.Options.OpenAIFirstOutputTimeoutSeconds
	switch strings.ToLower(strings.TrimSpace(reasoningEffort)) {
	case "high", "xhigh", "max":
		if override := p.Options.OpenAIHighEffortFirstOutputTimeoutSeconds; override > 0 {
			seconds = override
		}
	}
	// Stored timeout values are whole seconds for either phase.
	return time.Duration(seconds) * time.Second
}

// FirstOutputFailure records the timed-out phase and returns a 504 failover
// signal. Callers supply a selected provider and the attempt start time after
// the first-output budget expires; an available health observer receives the
// same model timeout. Headers are cloned, and SafeToFailoverAfterWrite
// supports the reader's staged-output recovery path.
func (p *OpenAIResponseOutput) FirstOutputFailure(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	startTime time.Time,
	originalModel string,
	reasoningEffort string,
	timeout time.Duration,
	phase string,
	responseHeaders http.Header,
) *forwardcore.UpstreamFailoverError {
	elapsed := time.Since(startTime)
	logging.LegacyPrintf(
		"service.openai_gateway",
		"OpenAI first output timeout: provider=%d model=%s effort=%s phase=%s elapsed=%s limit=%s",
		provider.Record.ID, originalModel, reasoningEffort, phase, elapsed, timeout,
	)
	requestID := strings.TrimSpace(responseHeaders.Get("x-request-id"))
	// Store elapsed and timeout diagnostics in milliseconds for Ops duration comparisons.
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
		Platform: provider.Record.Platform, ProviderID: provider.Record.ID, ProviderName: provider.Record.Name,
		UpstreamStatusCode: http.StatusGatewayTimeout, UpstreamRequestID: requestID,
		Kind: "first_output_timeout", Message: "OpenAI upstream produced no semantic output before the deadline",
		Detail: fmt.Sprintf("phase=%s elapsed_ms=%d timeout_ms=%d", phase, elapsed.Milliseconds(), timeout.Milliseconds()),
	})
	if p.Observer != nil {
		p.Observer.Core.HandleStreamTimeout(ctx, gatewayprovider.ExecutionRecord(provider), originalModel)
	}
	return &forwardcore.UpstreamFailoverError{
		StatusCode:      http.StatusGatewayTimeout,
		ResponseBody:    []byte(`{"error":{"type":"first_output_timeout","message":"Upstream produced no output before the deadline"}}`),
		ResponseHeaders: responseHeaders.Clone(), SafeToFailoverAfterWrite: true,
	}
}
