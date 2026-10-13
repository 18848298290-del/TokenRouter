package identity_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	identityprovider "github.com/TokenFlux/TokenRouter/internal/identity/provider"
)

// TestDingTalkStaffFromClaims_RoundTrip builds upstream claims from one DingTalk
// staff snapshot and reconstructs that profile. It checks the name, email,
// organization-local user ID and first department retained by this representation.
func TestDingTalkStaffFromClaims_RoundTrip(t *testing.T) {
	staff := &identityprovider.DingTalkStaffInfo{UserID: "u3", Name: "王五", Email: "ww@corp.com", DeptIDs: []int64{55}}
	claims := identitycore.DingTalkUpstreamClaims(staff, "uid3", "corpZ")

	recovered := identitycore.DingTalkProfileFromClaims(claims)
	require.Equal(t, "王五", recovered.Name)
	require.Equal(t, "ww@corp.com", recovered.Email)
	require.Equal(t, "u3", recovered.UserID)
	require.Equal(t, []int64{55}, recovered.DeptIDs)
}
