package apikey

import (
	"context"
)

type accessSnapshotContextKey struct{}

// WithAccessSnapshot stores the authenticated access snapshot in the context.
// The struct and TeamID are copied so request policy overrides stay local.
func WithAccessSnapshot(ctx context.Context, access AccessSnapshot) context.Context {
	access.TeamID = clonePointer(access.TeamID)
	return context.WithValue(ctx, accessSnapshotContextKey{}, access)
}

func AccessSnapshotFromContext(ctx context.Context) (AccessSnapshot, bool) {
	if ctx == nil {
		return AccessSnapshot{}, false
	}
	access, ok := ctx.Value(accessSnapshotContextKey{}).(AccessSnapshot)
	access.TeamID = clonePointer(access.TeamID)
	return access, ok
}

// WithFastModePolicy sets the Fast policy on a copy of this request's access snapshot.
func WithFastModePolicy(ctx context.Context, policy string) context.Context {
	access, _ := AccessSnapshotFromContext(ctx)
	access.fastModePolicy = policy
	return WithAccessSnapshot(ctx, access)
}

func (a AccessSnapshot) FastModePolicy() string { return a.fastModePolicy }

// WithRuntimeAPIKey publishes a reauthorized key view for this request when the key ID matches.
func WithRuntimeAPIKey(ctx context.Context, key *APIKey) context.Context {
	access, ok := AccessSnapshotFromContext(ctx)
	if !ok || key == nil || access.KeyID != key.ID {
		return ctx
	}
	access.key = key
	if key.User != nil {
		access.PayerUserID = key.User.ID
	}
	return WithAccessSnapshot(ctx, access)
}
