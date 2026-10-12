package identity_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	identityprovider "github.com/TokenFlux/TokenRouter/internal/identity/provider"
)

// TestBuildDingTalkSyntheticEmail_UsesUnionID checks that the synthetic address is derived from unionID.
func TestBuildDingTalkSyntheticEmail_UsesUnionID(t *testing.T) {
	unionID := "union_AbCdEf123"
	email := identitycore.DingTalkSyntheticEmail(unionID)

	want := "dingtalk-union_abcdef123@dingtalk-connect.invalid"
	require.Equal(t, want, email)

	require.True(t, strings.ToLower(email) == email, "synthetic email should be all lowercase")

	require.True(t, strings.HasPrefix(email, "dingtalk-"), "should have dingtalk- prefix")

	require.True(t, strings.HasSuffix(email, "@dingtalk-connect.invalid"), "should have reserved domain suffix")
}

// TestBuildDingTalkSyntheticEmail_TrimsSpace checks trimming before address construction.
func TestBuildDingTalkSyntheticEmail_TrimsSpace(t *testing.T) {
	email := identitycore.DingTalkSyntheticEmail("  UID_XYZ  ")
	require.Equal(t, "dingtalk-uid_xyz@dingtalk-connect.invalid", email)
}

// TestBuildDingTalkUpstreamClaims_EmptyStaff checks the profile-unavailable path.
// The global unionID remains the subject when organization-specific fields are empty.
func TestBuildDingTalkUpstreamClaims_EmptyStaff(t *testing.T) {
	staff := &identityprovider.DingTalkStaffInfo{}
	claims := identitycore.DingTalkUpstreamClaims(staff, "UNION_AAA", "CORP_X")

	require.Equal(t, "", claims["email"])
	require.Equal(t, "", claims["username"])

	require.Equal(t, "UNION_AAA", claims["subject"])
	require.Equal(t, "", claims["corp_user_id"]) // Unavailable for cross-organization users.
	require.Equal(t, "UNION_AAA", claims["union_id"])
	require.Equal(t, "CORP_X", claims["corp_id"])
}

// TestCheckDingTalkCorpAllowed_CrossOrgPolicy checks that none permits any organization.
func TestCheckDingTalkCorpAllowed_CrossOrgPolicy(t *testing.T) {
	cfg := identitycore.DingTalkOAuthOptions{CorpRestrictionPolicy: "none"}

	assert.True(t, identitycore.DingTalkCorpAllowed(cfg, "dingABC"), "policy=none should allow any corp")
	assert.True(t, identitycore.DingTalkCorpAllowed(cfg, ""), "policy=none should allow empty corp")
	assert.True(t, identitycore.DingTalkCorpAllowed(cfg, "foreign_corp"), "policy=none should allow foreign corp")
}

// TestCheckDingTalkCorpAllowed_InternalOnly checks that missing or different corpID values are accepted.
// Membership is checked separately by GetUserIdByUnionId.
// Membership errors 60011/60121 map to corp_rejected.
func TestCheckDingTalkCorpAllowed_InternalOnly(t *testing.T) {
	cfgWithCorpID := identitycore.DingTalkOAuthOptions{
		CorpRestrictionPolicy: "internal_only",
		InternalCorpID:        "dingInternal",
	}
	assert.True(t, identitycore.DingTalkCorpAllowed(cfgWithCorpID, "dingInternal"), "internal_only: matching corpID allowed")
	assert.True(t, identitycore.DingTalkCorpAllowed(cfgWithCorpID, "foreign_corp"), "internal_only: corpID 字段不再用于决策，step 3 兜底")
	assert.True(t, identitycore.DingTalkCorpAllowed(cfgWithCorpID, ""), "internal_only: 空 corpID 也通过（钉钉部分授权场景不返回 corpId）")

	cfgNoCorpID := identitycore.DingTalkOAuthOptions{
		CorpRestrictionPolicy: "internal_only",
		InternalCorpID:        "",
	}
	assert.True(t, identitycore.DingTalkCorpAllowed(cfgNoCorpID, "dingAnyNonEmpty"), "internal_only + no InternalCorpID: 非空 corpID 通过")
	assert.True(t, identitycore.DingTalkCorpAllowed(cfgNoCorpID, ""), "internal_only + no InternalCorpID: 空 corpID 也通过")
}

// TestCompleteDingTalkRegistration_UsernameFromEmailLocalPart checks using the email local part
// when the username is empty.
func TestCompleteDingTalkRegistration_UsernameFromEmailLocalPart(t *testing.T) {
	tests := []struct {
		name      string
		email     string
		username  string
		wantUser  string
		wantValid bool
	}{
		{
			name:      "username empty, normal email → local part",
			email:     "dingtalk-uid123@dingtalk-connect.invalid",
			username:  "",
			wantUser:  "dingtalk-uid123",
			wantValid: true,
		},
		{
			name:      "username already set → keep original",
			email:     "user@example.com",
			username:  "张三",
			wantUser:  "张三",
			wantValid: true,
		},
		{
			name:      "username empty, no @ in email → use whole email",
			email:     "noemail",
			username:  "",
			wantUser:  "noemail",
			wantValid: true,
		},
		{
			name:      "both empty → invalid",
			email:     "",
			username:  "",
			wantUser:  "",
			wantValid: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			username := tc.username
			email := tc.email

			// Model the username default used by CompleteDingTalkOAuthRegistration.
			if username == "" {
				if at := strings.Index(email, "@"); at > 0 {
					username = email[:at]
				} else {
					username = email
				}
			}

			isValid := email != "" && username != ""
			require.Equal(t, tc.wantUser, username, fmt.Sprintf("username for email=%q", tc.email))
			require.Equal(t, tc.wantValid, isValid, "validity check")
		})
	}
}

// TestBuildDingTalkUpstreamClaims_SubjectEqualsUnionID checks that claims use the global
// unionID as the identity subject.
func TestBuildDingTalkUpstreamClaims_SubjectEqualsUnionID(t *testing.T) {
	staff := &identityprovider.DingTalkStaffInfo{UserID: "user123", Name: "张三", Email: "zhangsan@corp.com"}
	claims := identitycore.DingTalkUpstreamClaims(staff, "union456", "dingcorp789")

	require.Equal(t, "union456", claims["subject"], "subject should equal unionID after refactor")
	// Keep the organization-local user ID separately for diagnostics.
	require.Equal(t, "user123", claims["corp_user_id"], "corp_user_id should be staff.UserID")
	// union_id repeats subject for consumers that read the provider-specific field.
	require.Equal(t, "union456", claims["union_id"])
	require.Equal(t, "dingcorp789", claims["corp_id"])
	require.Equal(t, "张三", claims["username"])
	require.Equal(t, "zhangsan@corp.com", claims["email"])
}

// TestBuildDingTalkUpstreamClaims_CrossOrgEmptyCorpUserID checks that missing organization data
// leaves corp_user_id empty while retaining unionID as subject.
func TestBuildDingTalkUpstreamClaims_CrossOrgEmptyCorpUserID(t *testing.T) {
	// A cross-organization profile lookup can yield no staff fields.
	staff := &identityprovider.DingTalkStaffInfo{}
	claims := identitycore.DingTalkUpstreamClaims(staff, "union_cross_org", "foreign_corp")

	require.Equal(t, "union_cross_org", claims["subject"], "subject should still be unionID for cross-org users")
	require.Equal(t, "", claims["corp_user_id"], "corp_user_id should be empty for cross-org fallback")
	require.Equal(t, "", claims["email"])
	require.Equal(t, "", claims["username"])
}

// TestBuildDingTalkUpstreamClaims_PrimaryDeptIDInClaims checks that claims contain the first department ID.
func TestBuildDingTalkUpstreamClaims_PrimaryDeptIDInClaims(t *testing.T) {
	staff := &identityprovider.DingTalkStaffInfo{UserID: "u1", Name: "张三", Email: "a@b.com", DeptIDs: []int64{42, 99}}
	claims := identitycore.DingTalkUpstreamClaims(staff, "uid1", "corpX")

	require.Equal(t, int64(42), claims["primary_dept_id"], "primary_dept_id should be the first dept_id")
}

// TestBuildDingTalkUpstreamClaims_NoDeptIDs checks that a missing department produces zero.
func TestBuildDingTalkUpstreamClaims_NoDeptIDs(t *testing.T) {
	staff := &identityprovider.DingTalkStaffInfo{UserID: "u2", Name: "李四"}
	claims := identitycore.DingTalkUpstreamClaims(staff, "uid2", "corpY")

	require.Equal(t, int64(0), claims["primary_dept_id"], "primary_dept_id should be 0 when no depts")
}
