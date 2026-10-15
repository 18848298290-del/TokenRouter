package search

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/search/contract"
)

// ports.go defines search storage and execution dependencies and copies
// supplier configurations for Manager ownership.
// Supplier configuration is copied before a Manager retains it, including its
// optional subscription and expiration pointers. Quota storage records
// attempted supplier calls and proxy failure markers, while execution returns
// the shared result shape consumed by gateway search emulation.

// These aliases keep supplier names and the default result count consistent
// with the shared search request definitions.
const (
	ProviderTypeBrave  = contract.ProviderTypeBrave  // Brave supplier identifier
	ProviderTypeTavily = contract.ProviderTypeTavily // Tavily supplier identifier
	defaultMaxResults  = contract.DefaultMaxResults  // default requested result count
)

type (
	// SearchRequest is the shared query, count, and proxy override value.
	SearchRequest = contract.SearchRequest
	// SearchResponse is the shared supplier result and submitted query value.
	SearchResponse = contract.SearchResponse
	// SearchResult is the shared link, title, snippet, and age value.
	SearchResult = contract.SearchResult
	// ProviderConfig is the shared supplier credential, quota, and proxy
	// configuration.
	ProviderConfig = contract.ProviderConfig
	// Provider is the supplier search interface implemented by HTTP adapters.
	Provider = contract.Provider
)

// QuotaState stores supplier call counters and temporary configured-proxy
// failure markers.
type QuotaState interface {
	// Increment requests a supplier-call reservation and returns the resulting
	// count. The Redis adapter applies ttl when the count becomes one or lacks
	// expiration; client retries can repeat execution after a failed reply, and an
	// error leaves Manager uncertain whether reservation occurred.
	Increment(context.Context, string, time.Duration) (int64, error)
	// Decrement requests compensation for a confirmed supplier reservation using
	// the cleanup context. Manager invokes it once for a failed or over-limit
	// attempt; storage clients can retry internally, and returned errors leave the
	// counter's restoration unconfirmed.
	Decrement(context.Context, string) error
	// Usage reads the current supplier counter for display and weighting. A
	// missing counter yields zero; other errors are available to consumers,
	// although Manager's weighting ignores them.
	Usage(context.Context, string) (int64, error)
	// Reset removes the supplier counter selected by its literal type string.
	// Managers use this operation for administrator resets, and a missing counter
	// can be removed successfully.
	Reset(context.Context, string) error
	// MarkProxy stores a nonempty failure marker for a configured proxy ID with
	// ttl. Manager treats storage errors as reporting failures and can still
	// proceed to another supplier.
	MarkProxy(context.Context, int64, time.Duration) error
	// ProxyAvailable reports whether the configured proxy's marker permits an
	// attempt. The Redis adapter permits attempts when reading fails or returns an
	// empty value, so this check can fail open.
	ProxyAvailable(context.Context, int64) bool
}

// Executor performs supplier HTTP searches and classifies failures for Manager
// retry decisions.
type Executor interface {
	// Search executes cfg with req and returns the normalized response. A nonempty
	// request proxy overrides the supplier proxy; the supplied context controls
	// the HTTP call and its cancellation.
	Search(context.Context, ProviderConfig, SearchRequest) (*SearchResponse, error)
	// IsProxyError reports whether Manager should treat a failure as
	// connectivity-related. The HTTP adapter uses typed network errors and message
	// matching, which can also identify direct-connection failures.
	IsProxyError(error) bool
	// CloseIdle closes currently idle supplier connections during Manager
	// retirement. Active calls retain their existing exchanges, and retired
	// Managers call this operation again when each search finishes.
	CloseIdle()
}

// CloneProviderConfigs copies the candidate slice and both optional timestamp
// pointers. Nil input returns nil, and callers can mutate the copied elements
// and pointed-to timestamps independently of the input.
func CloneProviderConfigs(in []ProviderConfig) []ProviderConfig {
	if in == nil {
		return nil
	}
	out := make([]ProviderConfig, len(in))
	copy(out, in)
	for i := range out {
		out[i].SubscribedAt = CloneInt64(out[i].SubscribedAt)
		out[i].ExpiresAt = CloneInt64(out[i].ExpiresAt)
	}
	return out
}

// CloneInt64 returns a new pointed-to integer for a nonnil input. The returned
// pointer can be changed independently; nil continues to represent an absent
// optional value in configuration snapshots.
func CloneInt64(v *int64) *int64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}
