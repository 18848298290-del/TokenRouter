package httpapi

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// DingTalk staff lookup depends on organization policy after unionID
// resolution. The fixture records user-ID and staff-profile requests, injects
// failure at either stage, and checks that internal_only propagates the
// failing stage while other policies continue with an empty profile.

// dingTalkStaffClient records lookup order and supplies configured staff
// results and errors.
type dingTalkStaffClient struct {
	identity.DingTalkOAuthClient // unused provider methods fixture
	// userErr is the user-ID lookup failure; staffErr is the profile lookup
	// failure. A userErr stops the second lookup, while staffErr is observed
	// after both calls are recorded.
	userErr, staffErr error
	calls             []string                          // ordered lookup call records
	profile           *identity.DingTalkProfileSnapshot // configured staff profile pointer
}

// TestDingTalkStaffLookupPolicy varies organization policy and the failing
// directory stage. It checks lookup order, internal-only stage errors, and
// empty profiles for tolerated failures under the other supplied policies.
func TestDingTalkStaffLookupPolicy(t *testing.T) {
	for _, policy := range []string{"none", "", "internal_only", "unknown"} {
		for _, failure := range []string{"", "get_user_id", "get_staff_info"} {
			t.Run(policy+"/"+failure, func(t *testing.T) {
				upstreamErr := errors.New("directory unavailable")
				client := &dingTalkStaffClient{profile: &identity.DingTalkProfileSnapshot{Nickname: "tester"}}
				if failure == "get_user_id" {
					client.userErr = upstreamErr
				}
				if failure == "get_staff_info" {
					client.staffErr = upstreamErr
				}
				profile, step, err := loadDingTalkStaff(t.Context(), client, policy, "union-id", "corp-id")
				if failure == "get_user_id" {
					require.Equal(t, []string{"user:union-id"}, client.calls)
				} else {
					require.Equal(t, []string{"user:union-id", "staff:staff-id"}, client.calls)
				}
				if policy == "internal_only" && failure != "" {
					require.ErrorIs(t, err, upstreamErr)
					require.Equal(t, failure, step)
					require.Nil(t, profile)
				} else {
					require.NoError(t, err)
					require.Empty(t, step)
					if failure == "" {
						require.Same(t, client.profile, profile)
					} else {
						require.Equal(t, &identity.DingTalkProfileSnapshot{}, profile)
					}
				}
			})
		}
	}
}

// GetUserIdByUnionId records the unionID lookup and returns the fixture staff
// ID. The configured userErr is returned with that ID, allowing the caller to
// exercise its policy-specific failure path.
func (c *dingTalkStaffClient) GetUserIdByUnionId(_ context.Context, id string) (string, error) {
	c.calls = append(c.calls, "user:"+id)
	return "staff-id", c.userErr
}

// GetStaffInfoByUserId records the supplied staff ID and returns the
// configured profile pointer. Its configured staffErr lets tests distinguish
// failures after successful unionID resolution from first-stage failure.
func (c *dingTalkStaffClient) GetStaffInfoByUserId(_ context.Context, id string) (*identity.DingTalkProfileSnapshot, error) {
	c.calls = append(c.calls, "staff:"+id)
	return c.profile, c.staffErr
}
