package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Provider deletion and manual recovery can finish some writes before a later
// operation fails. Administration therefore exposes the storage error while
// leaving completed shadow deletions or recovery steps in place, and callers
// can reload the record to inspect its remaining state. List queries pass
// filtering and sorting to storage before returning provider records.
//
// Recovery completes storage cleanup calls before removing the process
// scheduling block. The supplied store or its outer transaction owner controls
// when those writes become durable. Deletion removes shadows before their
// parent so a surviving shadow cannot retain a deleted credential owner. Quota
// reset delegates its combined write to billing and rejects shadows, whose spending
// belongs to the parent credential identity.

var (
	// Administration reuses billing's missing-provider error and defines distinct
	// input and proxy-state errors so callers can classify management failures.
	ErrProviderNotFound      = billing.ErrProviderNotFound
	ErrProviderNilInput      = apperror.BadRequest("PROVIDER_NIL_INPUT", "provider input cannot be nil")
	ErrProviderNotInFallback = apperror.BadRequest("PROVIDER_NOT_IN_FALLBACK", "provider is not in proxy fallback state")
)

// ListProviders returns the requested filtered page and its matching total from
// storage.
func (s *Admin) ListProviders(ctx context.Context, page, pageSize int, platform, providerType, status, search string, groupID int64, privacyMode string, sortBy, sortOrder string) ([]Record, int64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder}
	providers, result, err := s.providerRepo.ListWithFilters(ctx, params, platform, providerType, status, search, groupID, privacyMode)
	if err != nil {
		return nil, 0, err
	}
	return providers, result.Total, nil
}

// ListProvidersForSchedulerScoreFilter reads every matching provider for
// administrator score diagnostics. A nil Admin or missing store returns nil
// records successfully, allowing optional diagnostics to skip unavailable
// administration.
func (s *Admin) ListProvidersForSchedulerScoreFilter(ctx context.Context, platform, providerType, status, search string, groupID int64, privacyMode string) ([]Record, error) {
	if s == nil || s.providerRepo == nil {
		return nil, nil
	}
	return s.providerRepo.ListAllWithFilters(ctx, platform, providerType, status, search, groupID, privacyMode)
}

// ListSchedulableProvidersForAdvancedSchedulerScore reads schedulable members
// of a positive group ID. Missing administration or a nil/nonpositive group
// skips the query; a blank trimmed platform selects the supported platform
// list.
func (s *Admin) ListSchedulableProvidersForAdvancedSchedulerScore(ctx context.Context, groupID *int64, platform string) ([]Record, error) {
	if s == nil || s.providerRepo == nil || groupID == nil || *groupID <= 0 {
		return nil, nil
	}
	platform = strings.TrimSpace(platform)
	if platform != "" {
		return s.providerRepo.ListSchedulableByGroupIDAndPlatform(ctx, *groupID, platform)
	}
	return s.providerRepo.ListSchedulableByGroupIDAndPlatforms(ctx, *groupID, []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok, PlatformQoder, PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformJev})
}

// GetProvider reads one provider record through the administrative store.
func (s *Admin) GetProvider(ctx context.Context, id int64) (*Record, error) {
	return s.providerRepo.GetByID(ctx, id)
}

// GetProvidersByIDs reads the requested provider IDs through one storage call.
// Empty input returns an allocated empty slice; lookup failures are wrapped
// with context while retaining the underlying error.
func (s *Admin) GetProvidersByIDs(ctx context.Context, ids []int64) ([]*Record, error) {
	if len(ids) == 0 {
		return []*Record{}, nil
	}

	providers, err := s.providerRepo.GetByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to get providers by IDs: %w", err)
	}

	return providers, nil
}

// DeleteProvider removes a provider after deleting its credential shadows. A
// failed shadow lookup prevents all deletion, and a failed shadow deletion
// stops the cascade before the parent is removed.
//
// Each deletion is a separate storage call. Production storage commits locally
// opened transactions, while an already transaction-bound store leaves
// completion to its outer owner. Earlier child removals can remain applied
// after a later failure. The parent deletion error is returned directly.
func (s *Admin) DeleteProvider(ctx context.Context, id int64) error {
	// Remove each credential dependent before its owner. If a child deletion fails,
	// retain the parent so remaining shadows can still resolve its credentials.
	shadows, err := s.providerRepo.ListShadowsByParent(ctx, id)
	if err != nil {
		return fmt.Errorf("list spark shadows for cascade delete: %w", err)
	}
	for _, shadow := range shadows {
		if err := s.providerRepo.Delete(ctx, shadow.ID); err != nil {
			return fmt.Errorf("cascade delete spark shadow %d: %w", shadow.ID, err)
		}
	}
	return s.providerRepo.Delete(ctx, id)
}

// ClearProviderError clears error status, global rate limits, Antigravity
// scopes, model limits, and temporary unschedulability in that order. It stops
// at the first storage error. The updates are separate calls whose transaction
// completion belongs to the supplied store, so earlier steps can remain applied
// after a later failure.
//
// The optional runtime scheduling block is removed after every storage cleanup
// succeeds. Keeping that step last prevents an incomplete reset from releasing
// the process block. The final lookup can fail after cleanup has completed.
func (s *Admin) ClearProviderError(ctx context.Context, id int64) (*Record, error) {
	// Finish the separate storage cleanup calls before releasing the process
	// scheduling block. A later error stops this sequence while earlier calls
	// remain governed by the store's transaction ownership.
	if err := s.providerRepo.ClearError(ctx, id); err != nil {
		return nil, err
	}
	if err := s.providerRepo.ClearRateLimit(ctx, id); err != nil {
		return nil, err
	}
	if err := s.providerRepo.ClearAntigravityQuotaScopes(ctx, id); err != nil {
		return nil, err
	}
	if err := s.providerRepo.ClearModelRateLimits(ctx, id); err != nil {
		return nil, err
	}
	if err := s.providerRepo.ClearTempUnschedulable(ctx, id); err != nil {
		return nil, err
	}
	if s.options.RuntimeBlocker != nil {
		s.options.RuntimeBlocker.ClearProviderSchedulingBlock(id)
	}
	return s.providerRepo.GetByID(ctx, id)
}

// SetProviderSchedulable writes the administrator's scheduling switch and
// reloads the provider. A read failure follows a successful write call, whose
// transaction completion belongs to storage; callers can retry the lookup to
// display its result.
func (s *Admin) SetProviderSchedulable(ctx context.Context, id int64, schedulable bool) (*Record, error) {
	if err := s.providerRepo.SetSchedulable(ctx, id, schedulable); err != nil {
		return nil, err
	}
	updated, err := s.providerRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// ResetProviderQuota delegates quota and cooldown reset to the injected billing
// writer. Credential shadows are rejected because their parent owns the quota;
// lookup errors stop the operation before billing is called.
func (s *Admin) ResetProviderQuota(ctx context.Context, id int64) error {
	provider, err := s.providerRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}

	if provider.IsCredentialShadow() {
		return apperror.New(apperror.CategoryBadRequest, "SPARK_SHADOW_NO_QUOTA_RESET",
			"cannot reset quota for a spark shadow provider; manage it on the parent provider")
	}
	return s.options.Quotas.ResetQuotaUsedAndClearRateLimitCooldown(ctx, id)
}
