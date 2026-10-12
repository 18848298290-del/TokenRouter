package identity_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	identityprovider "github.com/TokenFlux/TokenRouter/internal/identity/provider"
)

// TestSyncDingTalkIdentity_UsesCfgAttrKeys_NoopWithNilService checks custom keys with an unavailable attribute service.
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

	// An unavailable attribute service is logged and skipped.
	require.NotPanics(t, func() {
		syncer.Sync(context.Background(), cfg, nil, 42, staff, false)
	})
}

// TestSyncDingTalkIdentity_DefaultAttrKeys_NoopWithNilService checks default keys with an unavailable attribute service.
func TestSyncDingTalkIdentity_DefaultAttrKeys_NoopWithNilService(t *testing.T) {
	syncer := &identitycore.DingTalkSyncRuntime{Profiles: &identitycore.DingTalkProfileSync{}}

	cfg := identitycore.DingTalkOAuthOptions{
		CorpRestrictionPolicy: "internal_only",
		SyncCorpEmail:         true,
		SyncDisplayName:       true,
		SyncDept:              false,
		// Default keys supplied by the settings reader.
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
