package scheduler

import (
	"context"
	"time"
)

// SessionLimitCache tracks active provider sessions for Anthropic OAuth/SetupToken limits.
// Redis stores last-activity timestamps in session_limit:provider:{providerID},
// a sorted set keyed by session UUID. Idle sessions are pruned by cache operations.
type SessionLimitCache interface {
	// RegisterSession refreshes an existing session or admits a new one below maxSessions.
	// It returns false when a new session exceeds capacity and an error on cache failure.
	// sessionUUID identifies the session extracted from metadata.user_id; idleTimeout
	// determines how long a session remains active after its last activity. Empty sessionUUID
	// or nonpositive maxSessions bypasses admission; nonpositive idleTimeout uses the default.
	RegisterSession(ctx context.Context, providerID int64, sessionUUID string, maxSessions int, idleTimeout time.Duration) (allowed bool, err error)

	// RefreshSession updates an existing session's activity timestamp.
	RefreshSession(ctx context.Context, providerID int64, sessionUUID string, idleTimeout time.Duration) error

	// UnregisterSession removes a registration immediately. Call it when an upstream
	// attempt ends without serving the session, so failed requests do not occupy
	// max_sessions capacity for the rest of the idle window.
	UnregisterSession(ctx context.Context, providerID int64, sessionUUID string) error

	// GetActiveSessionCount returns the count within the cache's default idle timeout.
	GetActiveSessionCount(ctx context.Context, providerID int64) (int, error)

	// GetActiveSessionCountBatch returns counts by provider ID, omitting failed queries.
	// Positive idleTimeouts entries override the default timeout per provider; nil, missing,
	// or nonpositive entries use the default.
	GetActiveSessionCountBatch(ctx context.Context, providerIDs []int64, idleTimeouts map[int64]time.Duration) (map[int64]int, error)

	// IsSessionActive reports whether a session is within the default idle timeout.
	IsSessionActive(ctx context.Context, providerID int64, sessionUUID string) (bool, error)
}
