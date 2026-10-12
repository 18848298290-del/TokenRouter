package search

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/search/contract"
)

const (
	ProviderTypeBrave  = contract.ProviderTypeBrave
	ProviderTypeTavily = contract.ProviderTypeTavily
	defaultMaxResults  = contract.DefaultMaxResults
)

type (
	SearchRequest  = contract.SearchRequest
	SearchResponse = contract.SearchResponse
	SearchResult   = contract.SearchResult
	ProviderConfig = contract.ProviderConfig
	Provider       = contract.Provider
)

type QuotaState interface {
	Increment(context.Context, string, time.Duration) (int64, error)
	Decrement(context.Context, string) error
	Usage(context.Context, string) (int64, error)
	Reset(context.Context, string) error
	MarkProxy(context.Context, int64, time.Duration) error
	ProxyAvailable(context.Context, int64) bool
}
type Executor interface {
	Search(context.Context, ProviderConfig, SearchRequest) (*SearchResponse, error)
	IsProxyError(error) bool
	CloseIdle()
}

// CloneProviderConfigs copies the slice and pointer fields so callers can mutate their own snapshots.
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

func CloneInt64(v *int64) *int64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}
