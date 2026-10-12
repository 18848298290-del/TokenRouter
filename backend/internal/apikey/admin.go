package apikey

import (
	"context"
	"fmt"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Administrative group binding can add exclusive permission and update a Key
// together, while public targets require the credential creator's existing
// binding permission. Composite credentials reject this administrative path
// before either binding changes or window resets, and an omitted group with no
// reset returns the loaded record directly.
//
// Selected configuration and explicit usage-reset masks protect counters owned
// by billing from unrelated edits. Exclusive grants delegate transaction
// consistency to GroupGrantMutations; other changes use one Key update.
// Successful mutations invalidate authentication, and selected resets attempt
// separate spending-cache invalidation. Cache notification failure is handled
// by the invalidator or durable delivery rather than changing the successful
// configuration result.

// AdminUpdateAPIKeyGroupIDResult carries the administrative Key view and
// reported exclusive group grant.
type AdminUpdateAPIKeyGroupIDResult struct {
	APIKey *APIKey // loaded or updated credential view
	// AutoGrantedGroupAccess, GrantedGroupID and GrantedGroupName report the
	// exclusive target selected by this use case. They do not independently
	// establish whether that permission already existed before the mutation.
	AutoGrantedGroupAccess bool   // selected exclusive grant occurrence
	GrantedGroupID         *int64 // selected granted group identifier
	GrantedGroupName       string // selected granted group display name
}

// Admin coordinates permitted ordinary-Key binding edits and spending-window
// resets for administration.
type Admin struct {
	Keys   APIKeyRepository // credential lookup and update dependency
	Users  UserRepository   // creator public-group permission reader
	Groups GroupRepository  // administrative target group reader
	// Mutations applies exclusive grants before the Key result is accepted.
	// Callers bind a dependency that supplies the required transaction ownership rules.
	Mutations GroupGrantMutations
	// Invalidator is invoked after successful configuration persistence. Nil
	// skips this direct notification, leaving configured storage triggers and cache expiry.
	Invalidator interface {
		// InvalidateAuthCacheByKey attempts credential-based cache invalidation after
		// a completed administrative mutation. Implementations own any suppressed
		// failure handling and coordinate cache resource lifetime with their callers.
		InvalidateAuthCacheByKey(context.Context, string)
	}
	// RateLimits clears separately cached spending windows after an explicit
	// reset. Nil skips the notification, and returned invalidation errors are suppressed.
	RateLimits interface {
		// InvalidateAPIKeyRateLimit attempts to clear observed spending-window usage
		// for the supplied Key ID. Callers invoke it after a stored reset and
		// permit cache recovery when this optional notification fails.
		InvalidateAPIKeyRateLimit(context.Context, int64) error
	}
}

// GroupGrantMutations applies an exclusive group grant and selected Key fields
// together.
type GroupGrantMutations interface {
	// GrantGroupAndUpdateFields grants creator permission and applies selected
	// Key fields according to the bound storage transaction rules. Caller cache
	// invalidation follows only a successful result from this operation.
	GrantGroupAndUpdateFields(context.Context, *APIKey, APIKeyUpdateFields, int64) error
}

// AdminUpdateAPIKeyGroupID changes an ordinary Key binding and reports an
// exclusive grant when selected. Nil gid returns the loaded record unchanged,
// zero unbinds, and a positive target requires active state; composite Keys
// reject this path.
func (s *Admin) AdminUpdateAPIKeyGroupID(ctx context.Context, id int64, gid *int64) (*AdminUpdateAPIKeyGroupIDResult, error) {
	return s.updateManagedFields(ctx, id, gid, false, true)
}

// UpdateManagedFields selects ordinary Key group changes and an optional
// spending-window reset in one use case. Composite Keys reject even a
// reset-only request; the mutation dependency owns transaction consistency when
// exclusive permission must also be granted.
func (s *Admin) UpdateManagedFields(ctx context.Context, id int64, gid *int64, reset bool) (*AdminUpdateAPIKeyGroupIDResult, error) {
	return s.updateManagedFields(ctx, id, gid, reset, true)
}

// updateManagedFields loads the Key and applies selected binding or
// spending-window reset masks. Negative targets reject, positive targets
// require active state, and public groups require creator binding permission.
// When checkComposite is true, composite records reject before reset or target
// validation; both public mutation methods enable that check.
//
// Exclusive targets select a permission grant using the mutation dependency
// before the configuration result is accepted. A reset selects all three usage
// counters and saved starts while retaining billing-owned fields outside that
// mask. Omitted changes return the loaded result before any write.
// Authentication invalidation follows a successful mutation; optional
// spending-cache invalidation errors are suppressed for reset results.
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
	// Clear counters together with their starts so later billing initializes fresh
	// windows; the dedicated mask leaves total quota and configuration outside this reset.
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
	// Submit exclusive grants and selected Key fields through the same mutation
	// dependency; ordinary binding and reset-only writes use the direct configuration writer.
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
	// The configuration writer has succeeded. Notify authentication and selected
	// spending caches afterward so requests do not reload data from a failed edit.
	if s.Invalidator != nil {
		s.Invalidator.InvalidateAuthCacheByKey(ctx, key.Key)
	}
	if reset && s.RateLimits != nil {
		_ = s.RateLimits.InvalidateAPIKeyRateLimit(ctx, key.ID)
	}
	return out, nil
}

// GetUserAPIKeys returns the creator's credential page and total through
// repository presentation filters. It supplies an empty Key filter with
// requested sort parameters; repository failures are returned before the
// pagination result is read.
func (s *Admin) GetUserAPIKeys(ctx context.Context, userID int64, page, pageSize int, sortBy, sortOrder string) ([]APIKey, int64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder}
	keys, result, err := s.Keys.ListByUserID(ctx, userID, params, APIKeyListFilters{})
	if err != nil {
		return nil, 0, err
	}
	return keys, result.Total, nil
}

// GetGroupAPIKeys returns a page of nondeleted credentials with ordinary or
// composite binding to groupID. It delegates repository ordering and total
// count, which can include managed records for administrative consumers.
func (s *Admin) GetGroupAPIKeys(ctx context.Context, groupID int64, page, pageSize int) ([]APIKey, int64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize}
	keys, result, err := s.Keys.ListByGroupID(ctx, groupID, params)
	if err != nil {
		return nil, 0, err
	}
	return keys, result.Total, nil
}
