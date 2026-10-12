package openaiattempt

import (
	"context"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// SessionPorts shares provider session counters; the text loop owns the current attempt lifecycle.
type SessionPorts struct {
	New              func() *scheduler.SessionAttempts
	StickyProviderID func(context.Context, *int64, string) int64
	Track            func(*scheduler.SessionAttempts, *gatewayadapter.ExecutionProvider, string)
	IncrementRPM     func(context.Context, int64) error
}

// PrepareRetry requests cache billing on sticky-provider switches or upstream instruction; completion applies
// it to native usage.
func (b *responsesAttemptBridge) PrepareRetry(failure *text.AttemptFailure, sameProvider bool) {
	if failure == nil {
		return
	}
	if failover.NeedForceCacheBilling(b.hasBoundSession, failure.Policy, sameProvider) {
		b.forceCacheBilling = true
		ctx := requeststate.WithForceCacheBilling(b.Context())
		b.c.Request = b.c.Request.WithContext(ctx)
		b.selectionCtx = ctx
	}
}

func (b *responsesAttemptBridge) Begin() {
	if b.binding().sessions.StickyProviderID != nil {
		id := b.binding().sessions.StickyProviderID(b.Context(), b.apiKey.GroupID, b.sessionHash)
		b.hasBoundSession = id > 0
		if b.apiKey.GroupID != nil {
			b.c.Request = b.c.Request.WithContext(requeststate.WithPrefetchedStickySession(b.Context(), id, *b.apiKey.GroupID))
			b.selectionCtx = b.c.Request.Context()
		}
	}
	if b.binding().sessions.New != nil {
		b.sessionAttempts = b.binding().sessions.New()
	}
}

// Finish retains idle sessions for served results and unregisters other attempts.
func (b *responsesAttemptBridge) Finish(served bool) {
	if b.sessionAttempts != nil {
		b.sessionAttempts.Finish(scheduler.AttemptOutcome{Served: served})
	}
	if !served || b.provider == nil || b.result == nil || !b.provider.View().IsAnthropicOAuthOrSetupToken() || gatewayadapter.ExecutionRuntimeConfig(b.provider).GetBaseRPM() <= 0 || b.binding().sessions.IncrementRPM == nil {
		return
	}
	if err := b.binding().sessions.IncrementRPM(b.Context(), b.provider.Record.ID); err != nil {
		b.reqLog.Warn("gateway.rpm_increment_failed", zap.Int64("provider_id", b.provider.Record.ID), zap.Error(err))
	}
}
