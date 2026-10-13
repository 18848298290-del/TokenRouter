package testkit

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identityprovider "github.com/TokenFlux/TokenRouter/internal/identity/provider"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// Identity fixture settings combine backend mode, authentication runtime
// switches, grants, OAuth, display, and signup-promotion reads over one
// repository. Startup configuration supplies selected OAuth and grant
// defaults, while runtime methods continue to read the retained repository and
// provider-specific resolvers when requested.

// SettingsReaders exposes the module settings readers assembled for
// authentication fixtures.
type SettingsReaders struct {
	*admission.BackendMode                               // panel backend-mode runtime reader
	*identity.RuntimeSettings                            // authentication switch runtime reader
	*identity.GrantSettings                              // signup and binding grant defaults
	*identity.OAuthSettings                              // provider OAuth runtime settings
	*site.DisplaySettings                                // frontend display URL reader
	promotion                 *promotion.RuntimeSettings // signup promotion runtime settings
}

// Settings assembles fixture readers over one repository using selected
// startup defaults. Nil configuration supplies zero grant options and no OAuth
// defaults; the display callback returns an empty frontend URL in that case.
//
// OAuth default structs are copied from configuration and retain any
// references their fields contain. The display URL callback reads the supplied
// configuration when invoked, so it can observe later configuration changes.
// OIDC metadata resolution uses the provider resolver when the OAuth reader
// requests it; the returned readers retain their shared repository.
func Settings(repo settings.Repository, cfg *config.Config) *SettingsReaders {
	var defaults *identity.OAuthSettingsDefaults
	grants := identity.GrantSettingsOptions{}
	if cfg != nil {
		defaults = &identity.OAuthSettingsDefaults{LinuxDo: cfg.LinuxDo, DingTalk: cfg.DingTalk, OIDC: cfg.OIDC, WeChat: cfg.WeChat, GitHubOAuth: cfg.GitHubOAuth, GoogleOAuth: cfg.GoogleOAuth}
		grants.DefaultBalance = cfg.Default.UserBalance
		grants.DefaultConcurrency = cfg.Default.UserConcurrency
	}
	return &SettingsReaders{
		BackendMode:     admission.NewBackendMode(repo, nil),
		RuntimeSettings: identity.NewRuntimeSettings(repo, settings.ErrSettingNotFound),
		GrantSettings:   identity.NewGrantSettings(repo, grants),
		OAuthSettings:   identity.NewOAuthSettings(repo, defaults, identityprovider.ResolveSettingsOIDCMetadata),
		DisplaySettings: site.NewDisplaySettings(repo, func() string {
			if cfg == nil {
				return ""
			}
			return cfg.Server.FrontendURL
		}),
		promotion: promotion.NewRuntimeSettings(repo),
	}
}

// GetDingTalkConnectOAuthConfig returns the registration policy subset of the
// OAuth configuration result. The enabled flag, bypass switch, and
// organization restriction are copied; an OAuth settings error passes through
// with that value.
func (s *SettingsReaders) GetDingTalkConnectOAuthConfig(ctx context.Context) (identity.DingTalkRegistrationPolicy, error) {
	value, err := s.OAuthSettings.GetDingTalkConnectOAuthConfig(ctx)
	return identity.DingTalkRegistrationPolicy{Enabled: value.Enabled, BypassRegistration: value.BypassRegistration, CorpRestrictionPolicy: value.CorpRestrictionPolicy}, err
}

// IsInvitationCodeEnabled returns the signup invitation switch from the
// retained promotion settings reader.
func (s *SettingsReaders) IsInvitationCodeEnabled(ctx context.Context) bool {
	return s.promotion.IsInvitationCodeEnabled(ctx)
}

// IsPromoCodeEnabled returns the signup promotion switch from the retained
// promotion settings reader.
func (s *SettingsReaders) IsPromoCodeEnabled(ctx context.Context) bool {
	return s.promotion.IsPromoCodeEnabled(ctx)
}
