package session

import (
	"context"
	"errors"
	"time"
)

// ErrReasoningContentNotFound indicates a cache miss for a reasoning item ID.
var ErrReasoningContentNotFound = errors.New("reasoning content not found")

// OpenAIWSSessionPreemptionCache is an optional GatewayCache capability. The
// production Redis cache implements all operations atomically; cache stubs do
// not need to implement it for ordinary gateway tests.
type OpenAIWSSessionPreemptionCache interface {
	ClaimOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, owner []byte, ttl time.Duration) ([]byte, error)
	CompareAndRefreshOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, expected []byte, ttl time.Duration) (bool, error)
	CompareAndDeleteOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, expected []byte) (bool, error)
}

// CyberSessionBlockStore reads and writes cyber session blocks.
// AdaptCyberSessionBlockStore discovers this optional capability; caches lacking both supported interfaces disable blocking.
type CyberSessionBlockStore interface {
	SetCyberSessionBlocked(ctx context.Context, scopeKey string, keys []string, ttl time.Duration) error
	IsCyberSessionScopeActive(ctx context.Context, scopeKey string) (bool, error)
	FindCyberSessionBlocked(ctx context.Context, keys []string) (string, error)
}

// GatewayCache stores sticky provider bindings and first-owner group records.
type GatewayCache interface {
	// GetSessionProviderID returns the provider bound to the sticky session.
	GetSessionProviderID(ctx context.Context, groupID int64, sessionHash string) (int64, error)
	// SetSessionProviderID binds a sticky session to a provider for the supplied TTL.
	SetSessionProviderID(ctx context.Context, groupID int64, sessionHash string, providerID int64, ttl time.Duration) error
	// RefreshSessionTTL extends the sticky binding lifetime.
	RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error
	// DeleteSessionProviderID removes a binding, for example when its provider becomes unavailable.
	DeleteSessionProviderID(ctx context.Context, groupID int64, sessionHash string) error
	// SetSessionOwnerGroupID records the first owner group and returns true when inserted.
	SetSessionOwnerGroupID(ctx context.Context, userID int64, source, sessionHash string, groupID int64, ttl time.Duration) (bool, error)
	// GetSessionOwnerGroupID returns the session's first owner group.
	GetSessionOwnerGroupID(ctx context.Context, userID int64, source, sessionHash string) (int64, error)
	// RefreshSessionOwnerTTL extends the ownership record lifetime.
	RefreshSessionOwnerTTL(ctx context.Context, userID int64, source, sessionHash string, ttl time.Duration) error
}

// ReasoningContentCache optionally supplies reasoning text for Responses-to-Chat conversion.
type ReasoningContentCache interface {
	SetReasoningContent(ctx context.Context, itemID string, content string, ttl time.Duration) error
	GetReasoningContent(ctx context.Context, itemID string) (string, error)
}

// GrokVideoBillingCache stores creation-time price snapshots for asynchronous videos.
// Shared billing claims prevent duplicate charges from polling across instances.
type GrokVideoBillingCache interface {
	SetGrokVideoPendingBilling(ctx context.Context, key string, payload []byte, ttl time.Duration) error
	GetGrokVideoPendingBilling(ctx context.Context, key string) ([]byte, error)
	ClaimGrokVideoBilled(ctx context.Context, key string, ttl time.Duration) (bool, error)
	ReleaseGrokVideoBilled(ctx context.Context, key string) error
}
