package apikey

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// ResolveRuntimeGroup rechecks payer and group access and returns a key copy bound to the target group.
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
	resolved := CopyAPIKey(key)
	resolved.GroupID = &id
	resolved.Group = group
	payerCopy := *payer
	resolved.User = &payerCopy
	s.KeyRefreshFallbackUserGroupRPMOverride(ctx, resolved, id)
	return resolved, nil
}
