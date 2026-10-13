package scheduler

import "context"

// User RPM limits aggregate requests across API keys, with distinct counters
// for a user and each user-group pair. RPMAdmission checks the effective group
// allowance before incrementing the user-wide counter, so a group rejection
// does not also consume the user allowance.

// UserRPMCache counts requests per user or user/group pair across the user's
// API keys. Keys are rpm:ug:{userID}:{groupID}:{min} and rpm:u:{userID}:{min}.
// RPMCache owns provider counts under rpm:{providerID}:{min}.
type UserRPMCache interface {
	// IncrementUserGroupRPM records a request in the user-group minute
	// bucket and returns its updated count. The caller compares the count
	// with the group limit or user override; rejected requests remain
	// counted.
	IncrementUserGroupRPM(ctx context.Context, userID, groupID int64) (count int, err error)

	// IncrementUserRPM records a request in the user-wide minute bucket
	// and returns its updated count. RPMAdmission invokes it after group
	// admission, and callers retain the increment even when the user limit
	// rejects the request.
	IncrementUserRPM(ctx context.Context, userID int64) (count int, err error)

	// GetUserGroupRPM reads the user-group count for the current Redis-
	// clock minute. Missing keys return zero; reads do not increment the
	// counter or apply the configured allowance.
	GetUserGroupRPM(ctx context.Context, userID, groupID int64) (count int, err error)

	// GetUserRPM reads the user-wide count for the current Redis-clock
	// minute. Missing keys return zero; storage errors are returned to the
	// consumer that requested the observation.
	GetUserRPM(ctx context.Context, userID int64) (count int, err error)
}
