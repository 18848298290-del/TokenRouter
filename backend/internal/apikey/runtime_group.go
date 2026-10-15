package apikey

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// runtime_group.go resolves an authorized runtime group and prepares its API
// key view.
// A runtime fallback target needs the Key's composite scope, an active group,
// and the payer's binding permission before requests can use it. Successful
// resolution returns a copied key view and reloads the payer's RPM override for
// the selected group.
//
// The composite binding restriction is checked before loading the target. An
// inactive or missing group rejects selection, and the payer is resolved
// before group permission is tested. After acceptance, the key and payer views
// are copied and the old group RPM override is cleared before attempting its
// replacement lookup. This keeps the earlier request view available while the
// selected target uses its own admission settings.

// ResolveRuntimeGroup returns a copied Key bound to an active group the payer
// can use. Composite keys restrict the target to their saved bindings; ordinary
// keys still require payer permission. Missing group dependencies reject
// selection, and repository failures are returned to the caller. Funding and
// protocol admission follow this resolution at the gateway. The returned view
// refreshes the group-specific RPM override while retaining other shallow
// references described by CopyAPIKey.
func (s *APIKeyService) ResolveRuntimeGroup(ctx context.Context, key *APIKey, id int64) (*APIKey, error) {
	if !key.AllowsRuntimeGroup(id) || s == nil || s.groupRepo == nil {
		return nil, apperror.Forbidden("GROUP_NOT_ALLOWED", "invalid fallback group")
	}
	group, err := s.groupRepo.GetByIDLite(ctx, id)
	if err != nil {
		return nil, err
	}
	if group == nil || !group.IsActive() {
		return nil, apperror.Forbidden("GROUP_DISABLED", "fallback group is unavailable")
	}
	payer, err := s.KeyBillingUserForAPIKey(ctx, key)
	if err != nil {
		return nil, err
	}
	if !s.KeyCanUserBindGroupInternal(payer, group) {
		return nil, ErrGroupNotAllowed
	}
	// Apply the selected group to a copy so the previous request view retains
	// its binding when a caller compares or retries the attempted fallback.
	resolved := CopyAPIKey(key)
	resolved.GroupID = &id
	resolved.Group = group
	payerCopy := *payer
	resolved.User = &payerCopy
	// The prior RPM override belongs to another group. Reload it for this
	// payer and target so admission does not reuse the prior group's override.
	s.KeyRefreshFallbackUserGroupRPMOverride(ctx, resolved, id)
	return resolved, nil
}
