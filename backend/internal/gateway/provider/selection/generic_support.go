package selection

import (
	"context"
	"time"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
)

const stickySessionTTL = time.Hour // Sticky-session lifetime.

// providerWithLoad pairs a provider with its load snapshot for selection.
type providerWithLoad struct {
	provider *gatewayprovider.ExecutionProvider
	loadInfo *schedulercore.ProviderLoadInfo
}

func shortSessionHash(sessionHash string) string {
	if sessionHash == "" {
		return ""
	}
	if len(sessionHash) <= 8 {
		return sessionHash
	}
	return sessionHash[:8]
}

// derefGroupID returns the group ID or zero for nil.
func derefGroupID(groupID *int64) int64 {
	if groupID == nil {
		return 0
	}
	return *groupID
}

func prefetchedStickyProviderIDFromContext(ctx context.Context, groupID *int64) int64 {
	prefetchedGroupID, ok := requeststate.PrefetchedStickyGroupIDFromContext(ctx)
	if !ok || prefetchedGroupID != derefGroupID(groupID) {
		return 0
	}
	if providerID, ok := requeststate.PrefetchedStickyProviderIDFromContext(ctx); ok && providerID > 0 {
		return providerID
	}
	return 0
}

// shouldClearStickySession reports whether provider unschedulability or a model cooldown invalidates a binding.
// A nil provider returns false; callers handle missing records separately.
func shouldClearStickySession(provider *gatewayprovider.ExecutionProvider, requestedModel string) bool {
	if provider == nil {
		return false
	}
	if !provider.View().IsSchedulable() {
		return true
	}
	if remaining := gatewayprovider.ExecutionModelPolicy(provider).LimitRemaining(context.Background(), requestedModel); remaining > 0 {
		return true
	}
	return false
}

// BindStickySession sets session -> provider binding with standard TTL.
func (s *Generic) BindStickySession(ctx context.Context, groupID *int64, sessionHash string, providerID int64) error {
	if sessionHash == "" || providerID <= 0 || s.cache == nil {
		return nil
	}
	return s.cache.SetSessionProviderID(ctx, derefGroupID(groupID), sessionHash, providerID, stickySessionTTL)
}

// GetCachedSessionProviderID retrieves the provider ID bound to a sticky session.
// Missing session input or cache returns zero without error; cache errors are returned with zero.
func (s *Generic) GetCachedSessionProviderID(ctx context.Context, groupID *int64, sessionHash string) (int64, error) {
	if sessionHash == "" || s.cache == nil {
		return 0, nil
	}
	providerID, err := s.cache.GetSessionProviderID(ctx, derefGroupID(groupID), sessionHash)
	if err != nil {
		return 0, err
	}
	return providerID, nil
}
