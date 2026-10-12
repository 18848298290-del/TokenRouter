package identity_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitytestkit "github.com/TokenFlux/TokenRouter/internal/identity/testkit"
)

// TestAuthService_CreateRegisteredUser_RechecksDomainQuotaSwitch checks that disabling quotas after preflight restores whitelist enforcement during creation.
func TestAuthService_CreateRegisteredUser_RechecksDomainQuotaSwitch(t *testing.T) {
	ctx := context.Background()
	settings := map[string]string{
		identity.SettingKeyRegistrationEnabled:                 "true",
		identity.SettingKeyRegistrationEmailSuffixWhitelist:    `["@example.com"]`,
		identity.SettingKeyRegistrationEmailDomainQuotaEnabled: "true",
	}
	repo := &userRepoStub{nextID: 9, domainCounts: map[string]int{"custom.example": 0}}
	cfg := &config.Config{Default: config.DefaultConfig{UserConcurrency: 1}}
	settingService := newAuthSettingsFixture(&settingRepoStub{values: settings}, cfg)
	service := identitytestkit.Auth(
		nil, &identity.AuthDependencies{Users: repo, Options: identitytestkit.AuthOptions(cfg), Settings: authSettingsPort(settingService)},
	)

	require.NoError(t, service.AuthValidateRegistrationEmailQuota(ctx, "first@custom.example"))
	settings[identity.SettingKeyRegistrationEmailDomainQuotaEnabled] = "false"

	err := service.AuthCreateRegisteredUser(ctx, &identity.User{Email: "first@custom.example"}, &identity.AuthRegistrationArtifacts{
		EnforceEmailDomainQuota: true,
	})
	require.ErrorIs(t, err, identity.ErrEmailSuffixNotAllowed)
	require.Empty(t, repo.created)
	require.Empty(t, repo.domainGuardCalls)
}
