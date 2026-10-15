package contract

import "context"

// provider.go defines the shared search interface implemented by Brave and
// Tavily adapters.
// Brave and Tavily return the same query and result shapes even though their
// HTTP requests use different authentication fields. A Provider uses an already
// configured client, so proxy selection happens in the executor before this
// interface is called.

// Provider executes one supplier's search using its configured HTTP client.
type Provider interface {
	// Name returns the supplier identifier used to report successful searches.
	Name() string
	// Search sends req through the provider's configured client and returns
	// normalized results. The context bounds the HTTP exchange, and nonpositive
	// MaxResults selects the shared default result count.
	Search(ctx context.Context, req SearchRequest) (*SearchResponse, error)
}
