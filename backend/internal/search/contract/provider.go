package contract

import "context"

// Provider performs searches against an external search service.
type Provider interface {
	// Name returns the provider identifier ("brave" or "tavily").
	Name() string
	// Search executes a web search and returns results.
	Search(ctx context.Context, req SearchRequest) (*SearchResponse, error)
}
