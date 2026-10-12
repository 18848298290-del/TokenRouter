package routing

import (
	"context"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

var (
	ErrGroupNotFound = infraerrors.NotFound("GROUP_NOT_FOUND", "group not found")
	ErrGroupExists   = infraerrors.Conflict("GROUP_EXISTS", "group name already exists")
)

type GroupRepository interface {
	Create(ctx context.Context, group *Group) error
	GetByID(ctx context.Context, id int64) (*Group, error)
	GetByIDLite(ctx context.Context, id int64) (*Group, error)
	Update(ctx context.Context, group *Group) error
	Delete(ctx context.Context, id int64) error
	DeleteCascade(ctx context.Context, id int64) ([]int64, error)

	List(ctx context.Context, params pagination.PaginationParams) ([]Group, *pagination.PaginationResult, error)
	ListWithFilters(ctx context.Context, params pagination.PaginationParams, platform, status, search string, isExclusive *bool) ([]Group, *pagination.PaginationResult, error)
	ListActive(ctx context.Context) ([]Group, error)

	ExistsByName(ctx context.Context, name string) (bool, error)
	GetProviderCount(ctx context.Context, groupID int64) (total int64, active int64, err error)
	DeleteProviderGroupsByGroupID(ctx context.Context, groupID int64) (int64, error)
	// GetProviderIDsByGroupIDs returns distinct provider IDs across the supplied groups.
	GetProviderIDsByGroupIDs(ctx context.Context, groupIDs []int64) ([]int64, error)
	// BindProvidersToGroup associates providers with the given group.
	BindProvidersToGroup(ctx context.Context, groupID int64, providerIDs []int64) error
	// UpdateSortOrders updates group display positions in a batch.
	UpdateSortOrders(ctx context.Context, updates []GroupSortOrderUpdate) error
}

type GroupDuplicateRepository interface {
	// FindByDuplicateOperationID looks up a committed copy when an idempotent write has an uncertain outcome.
	FindByDuplicateOperationID(ctx context.Context, operationID string) (*Group, error)
	// CreateFromSource atomically saves the group, source provider bindings, and scheduling outbox events.
	CreateFromSource(ctx context.Context, group *Group, sourceGroupID int64) error
}

// GroupSortOrderRepository serializes allocation of the next group display position.
type GroupSortOrderRepository interface {
	LockGroupSortOrder(ctx context.Context) error
}

// GroupSortOrderUpdate assigns a display position to a group.
type GroupSortOrderUpdate struct {
	ID        int64 `json:"id"`
	SortOrder int   `json:"sort_order"`
}
