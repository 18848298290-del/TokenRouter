package selection

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// sticky.go adapts compatible session affinity to current and legacy hashes
// and resolves Guardian parent bindings.
// OpenAI-compatible session bindings use a group-scoped openai namespace and
// can read or write a legacy hash during migration. Guardian review requests
// resolve their parent's affinity within the same group while preserving the
// parent binding during later child selection.
//
// A matching request prefetch can supply the provider ID directly, including a
// captured miss. Otherwise scheduler.StickySession applies primary and legacy
// lookup rules and counts migration observations. Updates, expiration
// refreshes, and deletions pass both request hashes to that shared
// implementation, which owns write ordering and the shorter legacy lifetime.
// This adapter keeps the current request's hash state with the selector's
// configured migration options.

// resolveOpenAIGuardianParentProviderID returns the optional provider
// associated with a guardian parent's session affinity. Missing affinity or
// cache, failed reads, and nonpositive results return zero so ordinary
// candidate selection remains available.
//
// The lookup uses CurrentSessionHash from the affinity value and installs its
// matching LegacySessionHash in a derived context before calling the shared
// sticky reader. That pairing keeps migration reads on the parent's identity
// rather than the child request's legacy hash. The reader can also reuse a
// matching group prefetch when present.
func (s *Compatible) resolveOpenAIGuardianParentProviderID(ctx context.Context, groupID *int64) int64 {
	if s == nil || s.cache == nil {
		return 0
	}
	affinity, ok := requeststate.GuardianParentAffinityFromContext(ctx)
	if !ok {
		return 0
	}
	lookupCtx := requeststate.WithOpenAILegacySessionHash(ctx, affinity.LegacySessionHash)
	providerID, err := s.getStickySessionProviderID(lookupCtx, groupID, affinity.CurrentSessionHash)
	// Parent affinity is optional selection information. A failed lookup supplies
	// no preferred parent provider and leaves ordinary candidate selection available.
	if err != nil || providerID <= 0 {
		return 0
	}
	return providerID
}

// openAISessionHashReadOldFallbackEnabled reports the configured legacy-hash
// lookup option for this selector. A nil selector returns true, matching the
// default migration setting used while constructing optional sticky helpers.
func (s *Compatible) openAISessionHashReadOldFallbackEnabled() bool {
	if s == nil {
		return true
	}
	return s.options.ReadLegacySticky
}

// openAISessionHashDualWriteOldEnabled reports the configured legacy-hash write
// option for this selector. A nil selector returns true, matching startup
// defaults for sticky helpers constructed without a compatible selector.
func (s *Compatible) openAISessionHashDualWriteOldEnabled() bool {
	if s == nil {
		return true
	}
	return s.options.WriteLegacySticky
}

// schedulerSticky binds the selector's cache, migration options, and counters
// to the openai session namespace. It creates a wrapper per call while
// retaining the constructed selector's shared cache and statistics.
func (s *Compatible) schedulerSticky() *schedulercore.StickySession {
	var cache schedulercore.StickyCache
	if s != nil {
		cache = s.cache
	}
	return schedulercore.NewStickySession(cache, schedulercore.StickyOptions{Prefix: "openai:", ReadLegacy: s.openAISessionHashReadOldFallbackEnabled(), DualWriteLegacy: s.openAISessionHashDualWriteOldEnabled(), DefaultTTL: openaiStickySessionTTL}, s.stickyStats())
}

// getStickySessionProviderID returns the binding for the current request's
// group and hashes. A matching prefetch wins, including zero for a captured
// miss; otherwise StickySession performs configured primary and legacy reads.
func (s *Compatible) getStickySessionProviderID(ctx context.Context, groupID *int64, sessionHash string) (int64, error) {
	if prefetchedGroup, ok := requeststate.PrefetchedStickyGroupIDFromContext(ctx); ok && prefetchedGroup == derefGroupID(groupID) {
		if id, present := requeststate.PrefetchedStickyProviderIDFromContext(ctx); present {
			return id, nil
		}
	}
	return s.schedulerSticky().Get(ctx, derefGroupID(groupID), sessionHash, requeststate.OpenAILegacySessionHashFromContext(ctx))
}

// setStickySessionProviderID delegates group-scoped binding writes using the
// request's current and legacy hashes. StickySession writes the primary key
// first and returns write errors; configured legacy writes use its bounded
// lifetime.
func (s *Compatible) setStickySessionProviderID(ctx context.Context, groupID *int64, sessionHash string, providerID int64, ttl time.Duration) error {
	return s.schedulerSticky().Set(ctx, derefGroupID(groupID), sessionHash, requeststate.OpenAILegacySessionHashFromContext(ctx), providerID, ttl)
}

// refreshStickySessionTTL delegates expiration refresh for the request's
// primary and configured legacy bindings. The primary refresh error is
// returned, while StickySession treats a legacy refresh failure as an optional
// migration observation.
func (s *Compatible) refreshStickySessionTTL(ctx context.Context, groupID *int64, sessionHash string, ttl time.Duration) error {
	return s.schedulerSticky().Refresh(ctx, derefGroupID(groupID), sessionHash, requeststate.OpenAILegacySessionHashFromContext(ctx), ttl)
}

// deleteStickySessionProviderID delegates deletion of the request's primary and
// configured legacy bindings. The primary deletion error is returned, while
// StickySession tolerates legacy deletion failure so primary cleanup can
// finish.
func (s *Compatible) deleteStickySessionProviderID(ctx context.Context, groupID *int64, sessionHash string) error {
	return s.schedulerSticky().Delete(ctx, derefGroupID(groupID), sessionHash, requeststate.OpenAILegacySessionHashFromContext(ctx))
}
