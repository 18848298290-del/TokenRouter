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

// antigravityRetryLoopParams supplies credentials, transport, and feedback for one retry loop.
type antigravityRetryLoopParams struct {
	userAgent       string // Set by provider probes; ordinary forwarding leaves this empty.
	ctx             context.Context
	prefix          string
	provider        *gatewayprovider.ExecutionProvider
	proxyURL        string
	accessToken     string
	action          string
	body            []byte
	c               *attempt
	httpUpstream    httpclient.UpstreamTransport
	providerRepo    gatewayprovider.ExecutionProviderStore // Stores model cooldowns during smart retry.
	handleError     func(ctx context.Context, prefix string, provider *gatewayprovider.ExecutionProvider, statusCode int, headers http.Header, body []byte, requestedModel string, groupID int64, sessionHash string, isStickySession bool) *provideradapter.AntigravityModelLimitResult
	requestedModel  string // Original requested model used for rate-limit checks.
	isStickySession bool   // Controls cache billing when switching a sticky provider.
	groupID         int64  // Group whose sticky binding is cleared on model throttling.
	sessionHash     string // Session whose sticky binding is cleared on model throttling.
}

// logPrefix formats the provider and optional session for logs.
func logPrefix(sessionID, providerName string) string {
	if sessionID != "" {
		return fmt.Sprintf("[antigravity-Forward] session=%s provider=%s", sessionID, providerName)
	}
	return fmt.Sprintf("[antigravity-Forward] provider=%s", providerName)
}

func (s *Antigravity) shouldFailoverUpstreamError(statusCode int) bool {
	switch statusCode {
	case 401, 403, 429, 529:
		return true
	default:
		return statusCode >= 500
	}
}

// isSingleProviderRetry reports whether single-provider backoff is enabled in context.
func isSingleProviderRetry(ctx context.Context) bool {
	v, _ := requeststate.SingleProviderRetryFromContext(ctx)
	return v
}

func (s *Antigravity) clearStickySession(ctx context.Context, groupID int64, sessionHash string) {
	if s == nil || s.Sticky == nil || strings.TrimSpace(sessionHash) == "" {
		return
	}
	if err := s.Sticky.DeleteSessionProviderID(ctx, groupID, sessionHash); err != nil {
		logging.LegacyPrintf("service.antigravity_gateway", "[antigravity-Forward] sticky_session_clear_failed group_id=%d session=%s err=%v", groupID, sessionHash[:min(8, len(sessionHash))], err)
	}
}

// handleUpstreamError delegates health evaluation and persistence to the provider observer.

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
	if s.Sticky != nil && sessionHash != "" {
		input.ClearSticky = func() { _ = s.Sticky.DeleteSessionProviderID(ctx, groupID, sessionHash) }
	}
	result := observer.Observe(input)
	if value != nil && view != nil {
		value.Record.Extra, value.Record.Credentials = view.Extra, view.Credentials
	}
	return result
}
