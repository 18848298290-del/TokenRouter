package apikey

import (
	"context"
	"fmt"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// AdminUpdateAPIKeyGroupIDResult is the result of AdminUpdateAPIKeyGroupID.
type AdminUpdateAPIKeyGroupIDResult struct {
	APIKey                 *APIKey
	AutoGrantedGroupAccess bool   // true if a new exclusive group permission was auto-added
	GrantedGroupID         *int64 // the group ID that was auto-granted
	GrantedGroupName       string // the group name that was auto-granted
}

// Admin manages key group bindings and usage resets using user and group repositories.
type Admin struct {
	Keys        APIKeyRepository
	Users       UserRepository
	Groups      GroupRepository
	Mutations   GroupGrantMutations
	Invalidator interface{ InvalidateAuthCacheByKey(context.Context, string) }
	RateLimits  interface {
		InvalidateAPIKeyRateLimit(context.Context, int64) error
	}
}

type GroupGrantMutations interface {
	GrantGroupAndUpdateFields(context.Context, *APIKey, APIKeyUpdateFields, int64) error
}

// AdminUpdateAPIKeyGroupID validates and updates the key's group binding.
// A nil gid leaves the binding unchanged; zero unbinds it; a positive value selects a group.
// Exclusive groups are granted to the user together with the key update.
func (s *Admin) AdminUpdateAPIKeyGroupID(ctx context.Context, id int64, gid *int64) (*AdminUpdateAPIKeyGroupIDResult, error) {
	return s.updateManagedFields(ctx, id, gid, false, true)
}

// UpdateManagedFields commits group changes and usage resets in one write or transaction.
func (s *Admin) UpdateManagedFields(ctx context.Context, id int64, gid *int64, reset bool) (*AdminUpdateAPIKeyGroupIDResult, error) {
	return s.updateManagedFields(ctx, id, gid, reset, true)
}

func (s *Admin) updateManagedFields(ctx context.Context, id int64, gid *int64, reset, checkComposite bool) (*AdminUpdateAPIKeyGroupIDResult, error) {
	key, err := s.Keys.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if checkComposite && key.IsComposite {
		return nil, ErrCompositeKeyGroupConflict
	}
	out := &AdminUpdateAPIKeyGroupIDResult{APIKey: key}
	var fields APIKeyUpdateFields
	var grant *int64
	if gid != nil {
		if *gid < 0 {
			return nil, infraerrors.BadRequest("INVALID_GROUP_ID", "group_id must be non-negative")
		}
		fields.GroupID = true
		if *gid == 0 {
			key.GroupID = nil
			key.Group = nil
		} else {
			group, err := s.Groups.GetByID(ctx, *gid)
			if err != nil {
				return nil, err
			}
			if group.Status != StatusActive {
				return nil, infraerrors.BadRequest("GROUP_NOT_ACTIVE", "target group is not active")
			}
			if !group.IsExclusive {
				if s.Users == nil {
					return nil, infraerrors.InternalServer("USER_REPOSITORY_UNAVAILABLE", "user repository unavailable")
				}
				user, err := s.Users.GetByID(ctx, key.UserID)
				if err != nil {
					return nil, fmt.Errorf("get user: %w", err)
				}
				if !user.CanBindGroup(group.ID, group.IsExclusive) {
					return nil, ErrGroupNotAllowed
				}
			}
			value := *gid
			key.GroupID = &value
			key.Group = group
			if group.IsExclusive {
				grant = &value
				out.AutoGrantedGroupAccess = true
				out.GrantedGroupID = &value
				out.GrantedGroupName = group.Name
			}
		}
	}
	if reset {
		fields.RateLimitUsage = true
		key.Usage5h = 0
		key.Usage1d = 0
		key.Usage7d = 0
		key.Window5hStart = nil
		key.Window1dStart = nil
		key.Window7dStart = nil
	}
	if !fields.GroupID && !fields.RateLimitUsage {
		return out, nil
	}
	if grant != nil {
		if err := s.Mutations.GrantGroupAndUpdateFields(ctx, key, fields, *grant); err != nil {
			return nil, err
		}
	} else if err := s.Keys.Update(ctx, key, fields); err != nil {
		if reset && gid == nil {
			return nil, fmt.Errorf("reset api key rate limit usage: %w", err)
		}
		return nil, fmt.Errorf("update api key: %w", err)
	}
	// Invalidate caches after the write commits. Database triggers enqueue outbox events.
	if s.Invalidator != nil {
		s.Invalidator.InvalidateAuthCacheByKey(ctx, key.Key)
	}
	if reset && s.RateLimits != nil {
		_ = s.RateLimits.InvalidateAPIKeyRateLimit(ctx, key.ID)
	}
	return out, nil
}

func (s *Admin) GetUserAPIKeys(ctx context.Context, userID int64, page, pageSize int, sortBy, sortOrder string) ([]APIKey, int64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder}
	keys, result, err := s.Keys.ListByUserID(ctx, userID, params, APIKeyListFilters{})
	if err != nil {
		return nil, 0, err
	}
	return keys, result.Total, nil
}

func (s *Admin) GetGroupAPIKeys(ctx context.Context, groupID int64, page, pageSize int) ([]APIKey, int64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize}
	keys, result, err := s.Keys.ListByGroupID(ctx, groupID, params)
	if err != nil {
		return nil, 0, err
	}
	return keys, result.Total, nil
}
