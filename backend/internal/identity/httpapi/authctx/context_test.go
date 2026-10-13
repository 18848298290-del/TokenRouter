package authctx

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// Authentication readers use a private record when present and typed Gin
// compatibility fields otherwise. These cases overwrite compatibility values
// after recording a principal and separate the API Key actor from the payer to
// check which values remain observable.

// TestPrincipalOwnsLegacyProjection overwrites public Gin user and role values
// after SetPrincipal records a JWT identity. It checks that principal,
// concurrency, and role readers still return the recorded account.
func TestPrincipalOwnsLegacyProjection(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	principal := identity.Principal{UserID: 7, Role: "user", SessionID: "session-1", CredentialKind: "jwt"}
	SetPrincipal(c, principal, 3, "test@example.invalid")
	c.Set(ContextKeyUser, AuthSubject{UserID: 99, Concurrency: 999})
	c.Set(ContextKeyUserRole, "admin")
	got, ok := GetPrincipal(c)
	require.True(t, ok)
	require.Equal(t, principal, got)
	subject, ok := GetAuthSubjectFromContext(c)
	require.True(t, ok)
	require.Equal(t, AuthSubject{UserID: 7, Concurrency: 3}, subject)
	role, ok := GetUserRoleFromContext(c)
	require.True(t, ok)
	require.Equal(t, "user", role)
}

// TestAPIKeyPrincipalKeepsActorSeparateFromPayer records an API Key caller
// after installing a different paying user. It checks caller identity
// separately from the retained payer ID and administrator role.
func TestAPIKeyPrincipalKeepsActorSeparateFromPayer(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	c.Set(ContextKeyUser, AuthSubject{UserID: 11, Concurrency: 4})
	c.Set(ContextKeyUserRole, "admin")
	SetAuthenticatedPrincipal(c, identity.Principal{UserID: 22, CredentialKind: "api_key"})
	p, ok := GetPrincipal(c)
	require.True(t, ok)
	require.Equal(t, int64(22), p.UserID)
	require.Empty(t, p.Role)
	require.Empty(t, p.SessionID)
	subject, ok := GetAuthSubjectFromContext(c)
	require.True(t, ok)
	require.Equal(t, int64(11), subject.UserID)
	role, ok := GetUserRoleFromContext(c)
	require.True(t, ok)
	require.Equal(t, "admin", role, "旧网关展示投影仍按原付款用户提供")
}

// TestLegacyOnlyContextRemainsReadable supplies compatibility user and role
// fields with no private record. It checks that those values remain readable
// while GetPrincipal reports no authenticated principal.
func TestLegacyOnlyContextRemainsReadable(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	c.Set(ContextKeyUser, AuthSubject{UserID: 9})
	c.Set(ContextKeyUserRole, "user")
	_, ok := GetPrincipal(c)
	require.False(t, ok)
	subject, ok := GetAuthSubjectFromContext(c)
	require.True(t, ok)
	require.Equal(t, int64(9), subject.UserID)
	role, ok := GetUserRoleFromContext(c)
	require.True(t, ok)
	require.Equal(t, "user", role)
}

// TestAuthSubjectHelpers_RoundTrip stores a typed account and administrator
// role directly in Gin. It checks that the compatibility readers return the
// user ID, concurrency allowance, and role.
func TestAuthSubjectHelpers_RoundTrip(t *testing.T) {
	c := &gin.Context{}
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1, Concurrency: 2})
	c.Set(string(ContextKeyUserRole), "admin")

	sub, ok := GetAuthSubjectFromContext(c)
	require.True(t, ok)
	require.Equal(t, int64(1), sub.UserID)
	require.Equal(t, 2, sub.Concurrency)

	role, ok := GetUserRoleFromContext(c)
	require.True(t, ok)
	require.Equal(t, "admin", role)
}
