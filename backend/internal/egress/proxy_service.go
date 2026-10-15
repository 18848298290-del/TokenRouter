package egress

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// proxy_service.go defines proxy repository operations for administration,
// provider bindings, and expiration maintenance.
// Proxy administration uses one repository for connection records, provider
// associations, and expiration sweeps. Repository methods supply counts and
// optional filtering for the admin service, while connection changes and
// expiration handling can coordinate provider snapshot invalidation and
// scheduler publication through the storage adapter.

// Proxy errors distinguish a missing record from an administration request to
// delete a proxy still referenced by providers.
var (
	ErrProxyNotFound = apperror.NotFound("PROXY_NOT_FOUND", "proxy not found")
	ErrProxyInUse    = apperror.Conflict("PROXY_IN_USE", "proxy is in use by providers")
)

// ProxyRepository persists connection records and supplies provider association
// and expiration maintenance operations.
type ProxyRepository interface {
	// Create persists proxy and assigns its stored identity and timestamps after
	// successful creation. The caller supplies validated values and retains
	// ownership of the mutable input used to receive those fields.
	Create(ctx context.Context, proxy *Proxy) error
	// GetByID returns the selected persisted proxy or ErrProxyNotFound when its ID
	// is absent. The caller owns the returned model, while optional values can
	// follow the adapter's sharing rules.
	GetByID(ctx context.Context, id int64) (*Proxy, error)
	// ListByIDs returns stored records matching the selected IDs in repository
	// query order. Empty input returns an empty slice; duplicate input IDs do not
	// request repeated copies from the storage query.
	ListByIDs(ctx context.Context, ids []int64) ([]Proxy, error)
	// Update replaces the persisted values identified by proxy.ID and coordinates
	// connection-dependent provider changes. Success copies stored metadata to the
	// supplied model; the storage adapter owns or joins its write transaction.
	Update(ctx context.Context, proxy *Proxy) error
	// Delete removes the selected proxy record and succeeds when that ID is absent
	// in the PostgreSQL adapter. Bound-provider rejection belongs to ProxyAdmin;
	// callers needing that guard use the administrative deletion path.
	Delete(ctx context.Context, id int64) error

	// List returns a proxy page and total using the supplied pagination settings.
	List(ctx context.Context, params pagination.PaginationParams) ([]Proxy, *pagination.PaginationResult, error)
	// ListWithFilters returns a proxy page and count using protocol, status, and
	// name search filters. Empty filter values omit those predicates; count and row
	// retrieval may observe different concurrent database changes.
	ListWithFilters(ctx context.Context, params pagination.PaginationParams, protocol, status, search string) ([]Proxy, *pagination.PaginationResult, error)
	// ListWithFiltersAndProviderCount returns a proxy page and total augmented
	// with bound-provider counts. Provider-count ordering is applied across all
	// matching proxies before page selection; other orderings follow stored proxy
	// fields.
	ListWithFiltersAndProviderCount(ctx context.Context, params pagination.PaginationParams, protocol, status, search string) ([]ProxyWithProviderCount, *pagination.PaginationResult, error)
	// ListActive returns proxies whose persisted status is active in repository
	// query order. Expiration timestamps are not evaluated here; maintenance
	// changes the stored status separately when a proxy expires.
	ListActive(ctx context.Context) ([]Proxy, error)
	// ListActiveWithProviderCount returns active-status proxies with bound-provider
	// counts in decreasing creation-time order. Time-based expiration is separate
	// from this filter, and proxy and count reads can observe concurrent database
	// changes.
	ListActiveWithProviderCount(ctx context.Context) ([]ProxyWithProviderCount, error)

	// ExistsByHostPortAuth reports whether host, port, username, and password
	// match any stored proxy. Protocol is excluded from this duplicate check, and
	// empty authentication text also matches stored null values.
	ExistsByHostPortAuth(ctx context.Context, host string, port int, username, password string) (bool, error)
	// CountProvidersByProxyID returns the number of nondeleted providers bound to
	// proxyID.
	CountProvidersByProxyID(ctx context.Context, proxyID int64) (int64, error)
	// ListProviderSummariesByProxyID returns administrator summaries for
	// nondeleted providers bound to proxyID.
	ListProviderSummariesByProxyID(ctx context.Context, proxyID int64) ([]ProxyProviderSummary, error)

	// SweepExpiredProxies marks expired active proxies and applies configured
	// provider fallback at now. It returns the reassigned-provider count; a later
	// error can occur after earlier per-proxy transactions have committed.
	SweepExpiredProxies(ctx context.Context, now time.Time) (changed int64, err error)
	// ListAllForFallback returns all repository-visible proxy statuses for backup-
	// chain resolution. Inactive and expired records remain in that snapshot; the
	// caller supplies the observation time and applies fallback eligibility checks.
	ListAllForFallback(ctx context.Context) ([]Proxy, error)
	// CountExpired returns the number of proxies carrying the stored expired
	// status.
	CountExpired(ctx context.Context) (int64, error)
	// CountExpiringSoon counts active proxies whose future expiration lies within
	// their configured warning days from now. The current instant is excluded and
	// the warning endpoint included; zero days admits no future expiration.
	CountExpiringSoon(ctx context.Context, now time.Time) (int64, error)
}
