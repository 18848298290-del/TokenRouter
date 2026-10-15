package ports

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// readers.go defines lookup interfaces and callback adapters used by usage HTTP
// handlers.
// Usage HTTP handlers need current key ownership, user suggestions and
// persisted request observations alongside usage rows. These reader
// interfaces and function adapters supply those lookups, while the handler
// chooses authorization scope and the owning service performs storage and
// formatting rules.

// KeyReference carries key identity and actor ownership for usage HTTP
// lookups.
type KeyReference struct {
	ID, UserID int64  // ID: key identifier; UserID: acting owner identifier
	Name       string // current key display name
}

// UserReference carries user identity and deletion state for administrator
// filter suggestions.
type UserReference struct {
	ID        int64      // suggested account identifier
	Email     string     // suggested account contact email
	DeletedAt *time.Time // optional account deletion instant
}

// UserListFilters selects account search text and historical deletion
// visibility for usage suggestions.
type UserListFilters struct {
	Search         string // account suggestion search text
	IncludeDeleted bool   // deleted account visibility flag
}

// KeyReader supplies key lookup, ownership selection and administrator
// usage-filter search.
type KeyReader interface {
	// GetByID returns a nonnil key identity and actor owner on success.
	// GetMyAPIKeyDailyUsage reads that successful result for its ownership check;
	// lookup errors stop invalid key filters or unavailable daily usage before
	// usage queries.
	GetByID(context.Context, int64) (*KeyReference, error)
	// VerifyOwnership selects IDs from the supplied list that belong to the
	// queried actor user. User batch handlers query usage only for this returned
	// subset, including an empty successful subset.
	VerifyOwnership(context.Context, int64, []int64) ([]int64, error)
	// SearchAPIKeys returns key suggestions for the supplied actor filter, text
	// and result limit. Administrator usage handlers supply thirty as the limit
	// and expose each returned key's ID, name and owner.
	SearchAPIKeys(context.Context, int64, string, int) ([]KeyReference, error)
}

// UserReader supplies paginated account suggestions for administrator
// usage-filter controls.
type UserReader interface {
	// ListUsers returns account suggestions and pagination totals using the
	// supplied filters and order. Administrator usage search asks for the first
	// thirty email-ordered rows and includes deleted accounts with retained
	// history.
	ListUsers(context.Context, int, int, UserListFilters, string, string) ([]UserReference, int64, error)
}

// UserErrors supplies user-scoped failed-request lists and redacted request
// detail reads.
type UserErrors interface {
	// ListUserErrorRequests returns failed requests visible to the supplied user
	// under filter. The HTTP handler gates error-view settings first, while the
	// owning Ops service applies user scope and response redaction.
	ListUserErrorRequests(context.Context, int64, *ops.OpsErrorLogFilter) (*ops.UserErrorRequestList, error)
	// GetUserErrorRequestDetail returns one failed-request detail visible to the
	// supplied user. The HTTP handler gates error-view settings before this
	// read, and the owning Ops service provides the scoped redacted result.
	GetUserErrorRequestDetail(context.Context, int64, int64) (*ops.UserErrorRequestDetail, error)
}

// Timings supplies persisted gateway-stage observations indexed by local
// request identity.
type Timings interface {
	// LookupRequestTimings returns persisted stage observations indexed by the
	// supplied request identifiers. Administrator enrichment treats a failed
	// lookup or missing identifier as an absent optional timing object for that
	// usage row.
	LookupRequestTimings(context.Context, []string) (map[string]*ops.OpsRequestTiming, error)
}

// Settings supplies ranking configuration and the user failed-request
// visibility decision.
type Settings interface {
	// GetUsageRankingSettings returns the normalized ranking enablement, order,
	// visibility and result limit. The ranking handler applies these values
	// before querying and hides response fields whose normalized visibility
	// flags are false.
	GetUsageRankingSettings(context.Context) (usage.UsageRankingSettings, error)
	// IsUserErrorViewAllowed reports the current server-side permission for user
	// error-request views. User handlers reject reads when this decision is
	// false and require the check before listing or returning error details.
	IsUserErrorViewAllowed(context.Context) bool
}

// KeyQueries implements key reads through callbacks bound by the application.
type KeyQueries struct {
	// Lookup, Ownership and Search are bound before this adapter is used. Its
	// methods invoke these callbacks directly, so each callback required by a
	// handler needs a nonnil binding.
	Lookup    func(context.Context, int64) (*KeyReference, error)               // key identity lookup callback
	Ownership func(context.Context, int64, []int64) ([]int64, error)            // owned key selection callback
	Search    func(context.Context, int64, string, int) ([]KeyReference, error) // key suggestion search callback
}

// UserQueries implements account suggestions through one application-bound
// list callback.
type UserQueries func(context.Context, int, int, UserListFilters, string, string) ([]UserReference, int64, error)

// GetByID invokes Lookup for the requested key identity and forwards its
// result. Bind Lookup before this adapter is used; the owning query
// determines lookup errors and stored ownership data.
func (q KeyQueries) GetByID(ctx context.Context, id int64) (*KeyReference, error) {
	return q.Lookup(ctx, id)
}

// VerifyOwnership invokes Ownership with the actor and submitted key IDs.
// Bind that callback before user batch reads; its returned subset determines
// which IDs the handler can pass to usage storage.
func (q KeyQueries) VerifyOwnership(ctx context.Context, id int64, keys []int64) ([]int64, error) {
	return q.Ownership(ctx, id, keys)
}

// SearchAPIKeys invokes Search with the actor filter, query text and limit.
// Bind Search before administrator suggestion reads; the callback supplies
// matching identities and any storage error.
func (q KeyQueries) SearchAPIKeys(ctx context.Context, id int64, query string, limit int) ([]KeyReference, error) {
	return q.Search(ctx, id, query, limit)
}

// ListUsers invokes the bound account suggestion function with pagination,
// filtering and order. The function adapter must be nonnil before use, and
// the owning query supplies rows, totals and errors.
func (q UserQueries) ListUsers(ctx context.Context, page, size int, f UserListFilters, sort, order string) ([]UserReference, int64, error) {
	return q(ctx, page, size, f, sort, order)
}
