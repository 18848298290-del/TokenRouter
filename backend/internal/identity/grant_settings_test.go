package identity_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// TestSettingService_GetAuthSourceDefaultSettings_ParsesValuesAndDefaults
// reads a mixture of saved provider values and missing keys. It checks
// numeric/plan parsing, absent-provider defaults, independent action switches
// and the exact third-party email-policy flag.
func TestSettingService_GetAuthSourceDefaultSettings_ParsesValuesAndDefaults(t *testing.T) {
	repo := &authSourceDefaultsRepoStub{
		values: map[string]string{
			identity.SettingKeyAuthSourceDefaultEmailBalance:            "12.5",
			identity.SettingKeyAuthSourceDefaultEmailConcurrency:        "7",
			identity.SettingKeyAuthSourceDefaultEmailSubscriptions:      `[{"plan_id":11}]`,
			identity.SettingKeyAuthSourceDefaultEmailGrantOnSignup:      "false",
			identity.SettingKeyAuthSourceDefaultLinuxDoGrantOnFirstBind: "true",
			identity.SettingKeyForceEmailOnThirdPartySignup:             "true",
		},
	}
	svc := identity.NewGrantSettings(repo, identity.GrantSettingsOptions{})

	got, err := svc.GetAuthSourceDefaultSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 12.5, got.Email.Balance)
	require.Equal(t, 7, got.Email.Concurrency)
	require.Equal(t, []identity.DefaultSubscriptionSetting{{PlanID: 11}}, got.Email.Subscriptions)
	require.False(t, got.Email.GrantOnSignup)
	require.False(t, got.Email.GrantOnFirstBind)
	require.Equal(t, 0.0, got.LinuxDo.Balance)
	require.Equal(t, 5, got.LinuxDo.Concurrency)
	require.Equal(t, []identity.DefaultSubscriptionSetting{}, got.LinuxDo.Subscriptions)
	require.False(t, got.LinuxDo.GrantOnSignup)
	require.True(t, got.LinuxDo.GrantOnFirstBind)
	require.Equal(t, 5, got.OIDC.Concurrency)
	require.Equal(t, 5, got.WeChat.Concurrency)
	require.False(t, got.OIDC.GrantOnSignup)
	require.False(t, got.WeChat.GrantOnSignup)
	require.True(t, got.ForceEmailOnThirdPartySignup)
}

// TestSettingService_UpdateAuthSourceDefaultSettings_PersistsAllKeys submits
// several provider grant configurations to the settings writer. It checks
// selected flat values, eight-decimal balance encoding, action flags and the
// preserved WeChat plan list in stored JSON.
func TestSettingService_UpdateAuthSourceDefaultSettings_PersistsAllKeys(t *testing.T) {
	repo := &authSourceDefaultsRepoStub{}
	svc := identity.NewGrantSettings(repo, identity.GrantSettingsOptions{})

	err := svc.UpdateAuthSourceDefaultSettings(context.Background(), &identity.AuthSourceDefaultSettings{
		Email: identity.ProviderDefaultGrantSettings{
			Balance:          1.25,
			Concurrency:      3,
			Subscriptions:    []identity.DefaultSubscriptionSetting{{PlanID: 21}},
			GrantOnSignup:    false,
			GrantOnFirstBind: true,
		},
		LinuxDo: identity.ProviderDefaultGrantSettings{
			Balance:          2,
			Concurrency:      4,
			Subscriptions:    []identity.DefaultSubscriptionSetting{{PlanID: 22}},
			GrantOnSignup:    true,
			GrantOnFirstBind: false,
		},
		OIDC: identity.ProviderDefaultGrantSettings{
			Balance:          3,
			Concurrency:      5,
			Subscriptions:    []identity.DefaultSubscriptionSetting{{PlanID: 23}},
			GrantOnSignup:    true,
			GrantOnFirstBind: true,
		},
		WeChat: identity.ProviderDefaultGrantSettings{
			Balance:          4,
			Concurrency:      6,
			Subscriptions:    []identity.DefaultSubscriptionSetting{{PlanID: 24}},
			GrantOnSignup:    false,
			GrantOnFirstBind: false,
		},
		ForceEmailOnThirdPartySignup: true,
	})
	require.NoError(t, err)
	require.Equal(t, "1.25000000", repo.updates[identity.SettingKeyAuthSourceDefaultEmailBalance])
	require.Equal(t, "3", repo.updates[identity.SettingKeyAuthSourceDefaultEmailConcurrency])
	require.Equal(t, "false", repo.updates[identity.SettingKeyAuthSourceDefaultEmailGrantOnSignup])
	require.Equal(t, "true", repo.updates[identity.SettingKeyAuthSourceDefaultEmailGrantOnFirstBind])
	require.Equal(t, "true", repo.updates[identity.SettingKeyForceEmailOnThirdPartySignup])

	var got []identity.DefaultSubscriptionSetting
	require.NoError(t, json.Unmarshal([]byte(repo.updates[identity.SettingKeyAuthSourceDefaultWeChatSubscriptions]), &got))
	require.Equal(t, []identity.DefaultSubscriptionSetting{{PlanID: 24}}, got)
}
