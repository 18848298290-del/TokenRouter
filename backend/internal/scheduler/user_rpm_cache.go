package scheduler

import "context"

// UserRPMCache counts requests per user or user/group pair across the user's API keys.
// Keys are rpm:ug:{userID}:{groupID}:{min} and rpm:u:{userID}:{min}.
// RPMCache owns provider counts under rpm:{providerID}:{min}.
type UserRPMCache interface {
	// IncrementUserGroupRPM atomically increments and returns the current minute's user/group count.
	// It supports group rpm_limit and user-group rpm_override checks.
	IncrementUserGroupRPM(ctx context.Context, userID, groupID int64) (count int, err error)

	// IncrementUserRPM atomically increments and returns the current minute's user count.
	// The user-wide limit is checked after the group limit passes.
	IncrementUserRPM(ctx context.Context, userID int64) (count int, err error)

	// GetUserGroupRPM returns the current minute's user/group request count.
	GetUserGroupRPM(ctx context.Context, userID, groupID int64) (count int, err error)

	// GetUserRPM returns the current minute's user request count.
	GetUserRPM(ctx context.Context, userID int64) (count int, err error)
}
