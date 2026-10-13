package identity_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	identityprovider "github.com/TokenFlux/TokenRouter/internal/identity/provider"
)

// TestSyncDingTalkIdentity_UsesCfgAttrKeys_NoopWithNilService enables all
// profile switches with custom keys and an unavailable attribute service. It
// requires Sync to return without panic before the nil directory client can be
// used.
func TestSyncDingTalkIdentity_UsesCfgAttrKeys_NoopWithNilService(t *testing.T) {
	syncer := &identitycore.DingTalkSyncRuntime{Profiles: &identitycore.DingTalkProfileSync{}}

	cfg := identitycore.DingTalkOAuthOptions{
		CorpRestrictionPolicy: "internal_only",
		SyncCorpEmail:         true,
		SyncDisplayName:       true,
		SyncDept:              true,
		// Non-default attribute keys.
		SyncCorpEmailAttrKey:   "custom_email_key",
		SyncDisplayNameAttrKey: "custom_name_key",
		SyncDeptAttrKey:        "custom_dept_key",
	}

	staff := &identityprovider.DingTalkStaffInfo{
		Name:  "张三",
		Email: "zhangsan@example.com",
	}

	// This fixture has no attribute service or observer, so the skip path must
	// finish before department synchronization could use its unconfigured
	// directory client.
	require.NotPanics(t, func() {
		syncer.Sync(context.Background(), cfg, nil, 42, staff, false)
	})
}

// TestSyncDingTalkIdentity_DefaultAttrKeys_NoopWithNilService supplies
// canonical attribute keys and an unavailable attribute service. It checks that
// enabled name and email options still skip safely through the synchronous
// runtime entry.
func TestSyncDingTalkIdentity_DefaultAttrKeys_NoopWithNilService(t *testing.T) {
	syncer := &identitycore.DingTalkSyncRuntime{Profiles: &identitycore.DingTalkProfileSync{}}

	cfg := identitycore.DingTalkOAuthOptions{
		CorpRestrictionPolicy: "internal_only",
		SyncCorpEmail:         true,
		SyncDisplayName:       true,
		SyncDept:              false,
		// Canonical attribute key strings used in this fixture.
		SyncCorpEmailAttrKey:   "dingtalk_email",
		SyncDisplayNameAttrKey: "dingtalk_name",
		SyncDeptAttrKey:        "dingtalk_department",
	}

	staff := &identityprovider.DingTalkStaffInfo{
		Name:  "李四",
		Email: "lisi@corp.com",
	}

	require.NotPanics(t, func() {
		syncer.Sync(context.Background(), cfg, nil, 99, staff, false)
	})
}
