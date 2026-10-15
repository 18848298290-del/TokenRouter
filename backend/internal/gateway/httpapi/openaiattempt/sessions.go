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

// sessions.go tracks attempt sessions and publishes cache-billing decisions
// during retries.
// A sticky-bound request changing providers can require cache-aware billing for
// the final native usage. The attempt bridge records that decision in its own
// state and the HTTP request context, using the shared failover predicate to
// combine prior affinity, same-provider retry, and upstream billing
// instructions.
//
// Session tracking is created at request Begin and can register successive
// provider attempts before Finish decides retention from the served outcome.
// Optional callbacks provide sticky prefetch and session counters. Successful
// Anthropic OAuth/setup-token results with configured base RPM also increment
// the provider's soft counter; that effect follows session finishing and
// reports failure diagnostically, while the caller controls Finish invocation
// count.

// SessionPorts binds sticky lookup, attempt registration, and provider RPM
// observations.
type SessionPorts struct {
	// New, StickyProviderID, Track, and IncrementRPM bind shared selection
	// services to this request. Begin creates request-local tracking; later
	// lifecycle callbacks use those registrations and publish only eligible
	// completion observations.
	New              func() *scheduler.SessionAttempts                                           // request-local session tracker constructor
	StickyProviderID func(context.Context, *int64, string) int64                                 // current group-affinity provider reader
	Track            func(*scheduler.SessionAttempts, *gatewayadapter.ExecutionProvider, string) // selected provider-session registration callback
	IncrementRPM     func(context.Context, int64) error                                          // provider soft-RPM observation callback
}

// PrepareRetry records cache-billing intent for the next provider execution.
// A nil failure leaves request state unchanged. The shared predicate requests
// billing adjustment after switching away from a bound sticky provider or when
// the upstream failure explicitly requests it.
//
// When selected, the adjustment is stored on the bridge and request context,
// then selectionCtx is replaced with that same context. Later selection and
// native completion therefore see the decision made before the retry rather
// than an unrelated request's affinity state.
func (b *responsesAttemptBridge) PrepareRetry(failure *text.AttemptFailure, sameProvider bool) {
	if failure == nil {
		return
	}
	// A switch can lose the bound provider's cache benefit, and an upstream
	// failure can request this treatment independently. Publish the
	// selected decision before the next attempt.
	if failover.NeedForceCacheBilling(b.hasBoundSession, failure.Policy, sameProvider) {
		b.forceCacheBilling = true
		ctx := requeststate.WithForceCacheBilling(b.Context())
		b.c.Request = b.c.Request.WithContext(ctx)
		b.selectionCtx = ctx
	}
}

// Begin initializes sticky-prefetch and optional session tracking for this
// request. A configured sticky reader uses the current group and session;
// group-bound results are published to request and selection contexts before
// the optional SessionAttempts object is constructed.
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

// Finish completes session retention and optionally increments an eligible
// provider's RPM. Callers invoke it once at request completion. SessionAttempts
// tolerates repeated finishing, but the Anthropic soft-RPM callback can run
// again on another eligible invocation; its failure is logged after session
// finishing.
func (b *responsesAttemptBridge) Finish(served bool) {
	if b.sessionAttempts != nil {
		// Resolve registration retention before the soft-RPM update so
		// a failed diagnostic counter write cannot leave this request's
		// session attempts unfinished.
		b.sessionAttempts.Finish(scheduler.AttemptOutcome{Served: served})
	}
	if !served || b.provider == nil || b.result == nil || !b.provider.View().IsAnthropicOAuthOrSetupToken() || gatewayadapter.ExecutionRuntimeConfig(b.provider).GetBaseRPM() <= 0 || b.binding().sessions.IncrementRPM == nil {
		return
	}
	// RPM is a post-serve soft observation for eligible Anthropic
	// credentials. A failed increment is logged after completion and does
	// not reopen provider execution.
	if err := b.binding().sessions.IncrementRPM(b.Context(), b.provider.Record.ID); err != nil {
		b.reqLog.Warn("gateway.rpm_increment_failed", zap.Int64("provider_id", b.provider.Record.ID), zap.Error(err))
	}
}
