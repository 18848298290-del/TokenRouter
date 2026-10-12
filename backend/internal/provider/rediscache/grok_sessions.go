package rediscache

import (
	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/infra/redis/session"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Grok authorization sessions use a shared OAuth JSON namespace with a
// separate consumption marker and a thirty-minute lifetime. The adapter binds
// Redis to the provider's session store, whose local storage handles an
// absent backend or failed remote writes and whose cleanup lifecycle is
// controlled by application assembly.

// NewGrokSessionStore binds Grok authorization to the OAuth Redis namespace
// and GrokSessionTTL. A nil client selects the core's memory backend; the
// returned store borrows a nonnil client, and application assembly owns
// cleanup Start/Stop and client shutdown.
func NewGrokSessionStore(rdb *redis.Client) *provider.GrokSessionStore {
	if rdb == nil {
		return provider.NewGrokSessionStore(nil)
	}
	return provider.NewGrokSessionStore(session.New(rdb, "oauth:session:xai", provider.GrokSessionTTL))
}
