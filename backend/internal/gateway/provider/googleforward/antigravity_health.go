package googleforward

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// Antigravity retry feedback can change the provider model window and remove a
// sticky session binding. The adapter passes request thinking and failure
// evidence to the shared observer, then copies updated credential and extra
// references back into the selected execution record.

// antigravityRetryLoopParams binds selected-provider inputs and callbacks for
// one retry loop.
type antigravityRetryLoopParams struct {
	// ctx, provider, c, httpUpstream, providerRepo, and handleError are
	// borrowed while the shared retry service runs. body is its current
	// prepared request, and credentials identify the selected provider
	// exchange.
	userAgent string          // optional explicit upstream user-agent
	ctx       context.Context // current retry request context
	prefix    string          // provider attempt log prefix
	// selected execution provider state
	provider    *gatewayprovider.ExecutionProvider
	proxyURL    string   // selected transport proxy URL
	accessToken string   // resolved OAuth access token
	action      string   // selected upstream action name
	body        []byte   // prepared v1internal request bytes
	c           *attempt // current attempt output adapter
	// selected HTTP exchange transport
	httpUpstream httpclient.UpstreamTransport
	// model cooldown persistence adapter
	providerRepo gatewayprovider.ExecutionProviderStore
	// provider error observation callback
	handleError     func(ctx context.Context, prefix string, provider *gatewayprovider.ExecutionProvider, statusCode int, headers http.Header, body []byte, requestedModel string, groupID int64, sessionHash string, isStickySession bool) *provideradapter.AntigravityModelLimitResult
	requestedModel  string // original request model identifier
	isStickySession bool   // sticky provider selection flag
	// groupID and sessionHash identify the binding removed during model
	// throttling; empty sessionHash leaves deletion to a caller that has
	// the binding context.
	groupID     int64  // sticky binding group identifier
	sessionHash string // sticky binding session discriminator
}

// logPrefix formats Antigravity logs with provider name and optional client
// session.
func logPrefix(sessionID, providerName string) string {
	if sessionID != "" {
		return fmt.Sprintf("[antigravity-Forward] session=%s provider=%s", sessionID, providerName)
	}
	return fmt.Sprintf("[antigravity-Forward] provider=%s", providerName)
}

// shouldFailoverUpstreamError recognizes authentication, quota, and server
// statuses for provider switching.
func (s *Antigravity) shouldFailoverUpstreamError(statusCode int) bool {
	switch statusCode {
	case 401, 403, 429, 529:
		return true
	default:
		return statusCode >= 500
	}
}

// isSingleProviderRetry reads the context flag enabling single-provider retry
// behavior.
func isSingleProviderRetry(ctx context.Context) bool {
	v, _ := requeststate.SingleProviderRetryFromContext(ctx)
	return v
}

// clearStickySession removes the supplied group/session provider binding when a
// cache and session exist. Nil runtime, missing cache, or blank session skips
// deletion; failed deletion is logged for diagnostics.
func (s *Antigravity) clearStickySession(ctx context.Context, groupID int64, sessionHash string) {
	if s == nil || s.Sticky == nil || strings.TrimSpace(sessionHash) == "" {
		return
	}
	// Remove the selected session binding before later routing reuses its
	// provider ID; deletion failure is logged and the surrounding retry
	// handling continues.
	if err := s.Sticky.DeleteSessionProviderID(ctx, groupID, sessionHash); err != nil {
		logging.LegacyPrintf("service.antigravity_gateway", "[antigravity-Forward] sticky_session_clear_failed group_id=%d session=%s err=%v", groupID, sessionHash[:min(8, len(sessionHash))], err)
	}
}

// handleUpstreamError delegates provider health with current thinking and error
// evidence. A nonempty session with an installed cache supplies sticky
// deletion, and observed credential/extra references are copied back to the
// execution record.
func (s *Antigravity) handleUpstreamError(ctx context.Context, prefix string, value *gatewayprovider.ExecutionProvider, status int, headers http.Header, body []byte, model string, groupID int64, sessionHash string, sticky bool) *provideradapter.AntigravityModelLimitResult {
	view := gatewayprovider.ExecutionRecord(value)
	observer := s.Errors
	input := provideradapter.AntigravityErrorInput{
		Context:          ctx,
		Provider:         view,
		Prefix:           prefix,
		Status:           status,
		Headers:          headers,
		Body:             body,
		RequestedModel:   model,
		Thinking:         requeststate.HealthThinking(ctx),
		OtherObservation: gatewayprovider.HealthObservationFromContext(ctx, status, headers, body, nil),
		Sticky:           sticky,
	}
	// Give this observer the current binding identity. Cache deletion
	// errors are discarded by this callback, allowing model-health
	// observation to return its result even when binding removal fails.
	if s.Sticky != nil && sessionHash != "" {
		input.ClearSticky = func() { _ = s.Sticky.DeleteSessionProviderID(ctx, groupID, sessionHash) }
	}
	result := observer.Observe(input)
	if value != nil && view != nil {
		value.Record.Extra, value.Record.Credentials = view.Extra, view.Credentials
	}
	return result
}
