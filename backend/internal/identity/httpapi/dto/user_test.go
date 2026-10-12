package dto

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

func TestUserFromServiceShallow_MapsDeletedAt(t *testing.T) {
	ts := time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC)

	deleted := UserFromServiceShallow(&identity.User{ID: 1, Email: "d@test.com", DeletedAt: &ts})
	require.NotNil(t, deleted.DeletedAt)
	require.Equal(t, ts, *deleted.DeletedAt)

	active := UserFromServiceShallow(&identity.User{ID: 2, Email: "a@test.com"})
	require.Nil(t, active.DeletedAt, "active user must have nil DeletedAt")
}

func TestUserFromServiceAdmin_MapsActivityTimestamps(t *testing.T) {
	t.Parallel()

	lastLoginAt := time.Date(2026, time.April, 20, 10, 0, 0, 0, time.UTC)
	lastActiveAt := lastLoginAt.Add(15 * time.Minute)
	lastUsedAt := lastLoginAt.Add(45 * time.Minute)

	out := UserFromServiceAdmin(&identity.User{
		ID:           42,
		Email:        "admin@example.com",
		Username:     "admin",
		Role:         identity.RoleAdmin,
		Status:       billing.StatusActive,
		LastActiveAt: &lastActiveAt,
		LastUsedAt:   &lastUsedAt,
	})

	require.NotNil(t, out)
	require.NotNil(t, out.LastActiveAt)
	require.NotNil(t, out.LastUsedAt)
	require.WithinDuration(t, lastActiveAt, *out.LastActiveAt, time.Second)
	require.WithinDuration(t, lastUsedAt, *out.LastUsedAt, time.Second)
}

// UserFromServiceShallow copies the user into an HTTP response.
func UserFromServiceShallow(v *identity.User) *User[json.RawMessage] {
	return UserFromIdentityShallow[json.RawMessage](identity.CopyUser(v))
}

// UserFromServiceAdmin copies the user into an administrator response.
func UserFromServiceAdmin(v *identity.User) *AdminUser[json.RawMessage] {
	return AdminUserFromIdentity[json.RawMessage](identity.CopyUser(v), nil)
}
