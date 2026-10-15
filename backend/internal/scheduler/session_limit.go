package scheduler

import (
	"context"
	"time"
)

// session_limit.go defines cache operations for idle-session registration,
// activity checks, counts, and removal.
// Anthropic idle-session limits reserve provider capacity across requests
// belonging to the same session. Successful or billable partial attempts keep
// their registration until inactivity expires, while unsuccessful attempts
// unregister promptly so failed work does not occupy the remaining idle
// window.

// SessionLimitCache tracks active provider sessions for Anthropic
// OAuth/SetupToken limits. Redis stores last-activity timestamps in
// session_limit:provider:{providerID}, a sorted set keyed by session UUID.
// Registration and count queries prune idle sessions.
type SessionLimitCache interface {
	// RegisterSession refreshes an existing session or admits a new one
	// below maxSessions. It returns false when a new session exceeds
	// capacity and an error on cache failure. sessionUUID identifies the
	// session extracted from metadata.user_id; idleTimeout determines how
	// long a session remains active after its last activity. Empty
	// sessionUUID or nonpositive maxSessions bypasses admission. Redis
	// truncates idleTimeout to whole seconds and uses the default when that
	// converted value is nonpositive.
	RegisterSession(ctx context.Context, providerID int64, sessionUUID string, maxSessions int, idleTimeout time.Duration) (allowed bool, err error)

	// RefreshSession updates activity for an existing session without
	// admitting a new one. Empty session IDs do nothing, and the Redis
	// adapter substitutes its default when the supplied duration truncates
	// to nonpositive seconds.
	RefreshSession(ctx context.Context, providerID int64, sessionUUID string, idleTimeout time.Duration) error

	// UnregisterSession removes a registration immediately. Call it when
	// an upstream attempt ends without serving the session, so failed
	// requests do not occupy max_sessions capacity for the rest of the
	// idle window.
	UnregisterSession(ctx context.Context, providerID int64, sessionUUID string) error

	// GetActiveSessionCount prunes sessions idle for the cache's default
	// timeout and returns the remaining count. Redis failures are returned
	// to callers; the query can change the stored set through pruning.
	GetActiveSessionCount(ctx context.Context, providerID int64) (int, error)

	// GetActiveSessionCountBatch prunes and counts each provider using its
	// positive timeout override or the default. Each selected duration
	// truncates to whole seconds; a positive subsecond override becomes
	// zero here. Failed queries are omitted, successful counts remain
	// available, and the Redis adapter returns a nil error even when
	// pipeline commands fail.
	GetActiveSessionCountBatch(ctx context.Context, providerIDs []int64, idleTimeouts map[int64]time.Duration) (map[int64]int, error)

	// IsSessionActive reports whether the named session lies inside the
	// default idle window. Empty or expired sessions return false; the
	// Redis activity check leaves stored membership unchanged and
	// propagates cache errors.
	IsSessionActive(ctx context.Context, providerID int64, sessionUUID string) (bool, error)
}
