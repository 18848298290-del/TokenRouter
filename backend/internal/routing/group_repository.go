package routing

import (
	"context"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// A copied group's settings and source memberships need one transaction so
// failures cannot leave a partial product configuration. Ordinary mutations
// join storage through a shared context, while the optional sort-order lock
// protects automatic position allocation until its creation transaction
// completes.

// Group lookup and uniqueness errors let callers distinguish missing references
// from a saved group-name conflict during administrative writes.
var (
	ErrGroupNotFound = infraerrors.NotFound("GROUP_NOT_FOUND", "group not found")
	ErrGroupExists   = infraerrors.Conflict("GROUP_EXISTS", "group name already exists")
)

// GroupRepository persists group settings and supplies their provider
// associations.
type GroupRepository interface {
	// Create saves the supplied group and populates its persistent identity.
	// Administrative callers pass the mutation context when creation and provider
	// binding need to commit or roll back together.
	Create(ctx context.Context, group *Group) error
	// GetByID reads a disposable group working value with optional management
	// counts. A missing record returns ErrGroupNotFound; PostgreSQL count failures
	// leave zero counts while returning the loaded configuration.
	// GroupAdmin.UpdateGroup mutates this value during validation, so
	// implementations detach retained snapshots before returning it.
	GetByID(ctx context.Context, id int64) (*Group, error)
	// GetByIDLite reads saved routing configuration without fetching derived
	// provider counts. Missing records return ErrGroupNotFound, and callers
	// requiring count data use GetByID or a separate capacity query.
	GetByIDLite(ctx context.Context, id int64) (*Group, error)
	// Update saves the supplied group using its persistent identity and update
	// timestamp. Administrative callers pass the shared mutation context when
	// settings and membership replacement must succeed together.
	Update(ctx context.Context, group *Group) error
	// Delete removes the group identified by its persistent ID through the
	// ordinary repository deletion path. Callers requiring coordinated user and
	// provider association cleanup use DeleteCascade, as group administration
	// does.
	Delete(ctx context.Context, id int64) error
	// DeleteCascade removes a group together with its authorization and provider
	// associations. The repository coordinates cleanup in a transaction so failure
	// cannot leave the group removed while its memberships remain.
	//
	// The returned user-ID slice is reserved for affected-user reporting by
	// repository implementations. GroupAdmin obtains bound Key strings before
	// invoking this operation because successful deletion removes the associations
	// needed to locate authentication snapshots afterward. A missing group returns
	// ErrGroupNotFound.
	DeleteCascade(ctx context.Context, id int64) ([]int64, error)

	// List returns a group page and its pagination metadata.
	List(ctx context.Context, params pagination.PaginationParams) ([]Group, *pagination.PaginationResult, error)
	// ListWithFilters returns a page filtered by status, text, and optional
	// exclusivity. The platform argument is retained for existing callers; the
	// PostgreSQL store does not use it to restrict the group query.
	ListWithFilters(ctx context.Context, params pagination.PaginationParams, platform, status, search string, isExclusive *bool) ([]Group, *pagination.PaginationResult, error)
	// ListActive returns enabled groups in their repository-defined display
	// ordering.
	ListActive(ctx context.Context) ([]Group, error)

	// ExistsByName reports whether a saved group uses the supplied name.
	ExistsByName(ctx context.Context, name string) (bool, error)
	// GetProviderCount reports total and active provider counts for one group.
	GetProviderCount(ctx context.Context, groupID int64) (total int64, active int64, err error)
	// DeleteProviderGroupsByGroupID clears a group's provider memberships and
	// returns the affected count. Pass the group mutation context when clearing
	// and replacement bindings need to be one transaction.
	DeleteProviderGroupsByGroupID(ctx context.Context, groupID int64) (int64, error)
	// GetProviderIDsByGroupIDs returns distinct provider IDs across the supplied
	// groups. An empty source list yields no IDs; administration validates source
	// existence separately before using these IDs for copying.
	GetProviderIDsByGroupIDs(ctx context.Context, groupIDs []int64) ([]int64, error)
	// BindProvidersToGroup adds provider memberships for the supplied group and
	// IDs. Empty ID lists are accepted, and the PostgreSQL participant ignores
	// already-existing bindings; pass the mutation context for coordinated
	// settings writes.
	BindProvidersToGroup(ctx context.Context, groupID int64, providerIDs []int64) error
	// UpdateSortOrders saves supplied display positions as a batch. The PostgreSQL
	// store ignores nonpositive IDs, uses the last value for repeated IDs, and
	// rejects a batch containing a missing group.
	UpdateSortOrders(ctx context.Context, updates []GroupSortOrderUpdate) error
}

// GroupDuplicateRepository recovers keyed copies and commits configuration with
// copied memberships.
type GroupDuplicateRepository interface {
	// FindByDuplicateOperationID reads a saved copy using its duplication
	// identity. Empty IDs and absent operations return nil without an error,
	// allowing callers to distinguish an unrecovered operation from a lookup
	// failure.
	FindByDuplicateOperationID(ctx context.Context, operationID string) (*Group, error)
	// CreateFromSource saves a duplicate group with its source provider
	// memberships and scheduling event. The supplied group receives its persistent
	// identity and derived binding count as the repository performs creation.
	//
	// The group, copied memberships, and scheduling outbox entry share a
	// transaction. A failed binding or outbox insertion rolls that transaction
	// back, while an operation-ID or generated-name conflict is reported as
	// ErrGroupExists so the administrator can recover the prior operation or try
	// another name.
	CreateFromSource(ctx context.Context, group *Group, sourceGroupID int64) error
}

// GroupSortOrderRepository serializes automatic group display-position
// allocation inside a transaction.
type GroupSortOrderRepository interface {
	// LockGroupSortOrder serializes automatic tail reads and creation in the
	// current transaction. Callers acquire it before reading the highest position
	// and keep the same operation context until the new group is saved.
	LockGroupSortOrder(ctx context.Context) error
}

// GroupSortOrderUpdate assigns a saved group its administrative display
// position.
type GroupSortOrderUpdate struct {
	ID        int64 `json:"id"`         // saved group row ID
	SortOrder int   `json:"sort_order"` // administrative display sort position
}
