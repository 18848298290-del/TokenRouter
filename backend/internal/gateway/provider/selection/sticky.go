package selection

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// resolveOpenAIGuardianParentProviderID looks up the parent-session hashes stored in request state.
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
	if err != nil || providerID <= 0 {
		return 0
	}
	return providerID
}

func (s *Compatible) openAISessionHashReadOldFallbackEnabled() bool {
	if s == nil {
		return true
	}
	return s.options.ReadLegacySticky
}

func (s *Compatible) openAISessionHashDualWriteOldEnabled() bool {
	if s == nil {
		return true
	}
	return s.options.WriteLegacySticky
}

func (s *Compatible) schedulerSticky() *schedulercore.StickySession {
	var cache schedulercore.StickyCache
	if s != nil {
		cache = s.cache
	}
	return schedulercore.NewStickySession(cache, schedulercore.StickyOptions{Prefix: "openai:", ReadLegacy: s.openAISessionHashReadOldFallbackEnabled(), DualWriteLegacy: s.openAISessionHashDualWriteOldEnabled(), DefaultTTL: openaiStickySessionTTL}, s.stickyStats())
}

func (s *Compatible) getStickySessionProviderID(ctx context.Context, groupID *int64, sessionHash string) (int64, error) {
	if prefetchedGroup, ok := requeststate.PrefetchedStickyGroupIDFromContext(ctx); ok && prefetchedGroup == derefGroupID(groupID) {
		if id, present := requeststate.PrefetchedStickyProviderIDFromContext(ctx); present {
			return id, nil
		}
	}
	return s.schedulerSticky().Get(ctx, derefGroupID(groupID), sessionHash, requeststate.OpenAILegacySessionHashFromContext(ctx))
}

func (s *Compatible) setStickySessionProviderID(ctx context.Context, groupID *int64, sessionHash string, providerID int64, ttl time.Duration) error {
	return s.schedulerSticky().Set(ctx, derefGroupID(groupID), sessionHash, requeststate.OpenAILegacySessionHashFromContext(ctx), providerID, ttl)
}

func (s *Compatible) refreshStickySessionTTL(ctx context.Context, groupID *int64, sessionHash string, ttl time.Duration) error {
	return s.schedulerSticky().Refresh(ctx, derefGroupID(groupID), sessionHash, requeststate.OpenAILegacySessionHashFromContext(ctx), ttl)
}

func (s *Compatible) deleteStickySessionProviderID(ctx context.Context, groupID *int64, sessionHash string) error {
	return s.schedulerSticky().Delete(ctx, derefGroupID(groupID), sessionHash, requeststate.OpenAILegacySessionHashFromContext(ctx))
}
