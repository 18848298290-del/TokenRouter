package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"strings"
)

type OIDCOAuthOptions struct {
	Enabled                 bool   `mapstructure:"enabled"`
	ProviderName            string `mapstructure:"provider_name"` // Display name, such as "Keycloak".
	ClientID                string `mapstructure:"client_id"`
	ClientSecret            string `mapstructure:"client_secret"`
	IssuerURL               string `mapstructure:"issuer_url"`
	DiscoveryURL            string `mapstructure:"discovery_url"`
	AuthorizeURL            string `mapstructure:"authorize_url"`
	TokenURL                string `mapstructure:"token_url"`
	UserInfoURL             string `mapstructure:"userinfo_url"`
	JWKSURL                 string `mapstructure:"jwks_url"`
	Scopes                  string `mapstructure:"scopes"`                // Defaults to "openid email profile".
	RedirectURL             string `mapstructure:"redirect_url"`          // Backend callback URL registered with the provider.
	FrontendRedirectURL     string `mapstructure:"frontend_redirect_url"` // Frontend token callback route; defaults to /auth/oidc/callback.
	TokenAuthMethod         string `mapstructure:"token_auth_method"`     // client_secret_post / client_secret_basic / none
	UsePKCE                 bool   `mapstructure:"use_pkce"`
	ValidateIDToken         bool   `mapstructure:"validate_id_token"`
	UsePKCEExplicit         bool   `mapstructure:"-" yaml:"-"`
	ValidateIDTokenExplicit bool   `mapstructure:"-" yaml:"-"`
	AllowedSigningAlgs      string `mapstructure:"allowed_signing_algs"`   // Defaults to "RS256,ES256,PS256".
	ClockSkewSeconds        int    `mapstructure:"clock_skew_seconds"`     // Defaults to 120 seconds.
	RequireEmailVerified    bool   `mapstructure:"require_email_verified"` // Defaults to false.

	// Optional gjson paths for fields in the userinfo response.
	// Empty paths use a set of common field names.
	UserInfoEmailPath    string `mapstructure:"userinfo_email_path"`
	UserInfoIDPath       string `mapstructure:"userinfo_id_path"`
	UserInfoUsernamePath string `mapstructure:"userinfo_username_path"`
}

type OIDCTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
}

type OIDCTokenExchangeError struct {
	StatusCode          int
	ProviderError       string
	ProviderDescription string
	Body                string
}

type OIDCUserInfoClaims struct {
	Email         string
	Username      string
	Subject       string
	EmailVerified *bool
	DisplayName   string
	AvatarURL     string
}

// OIDCVerifiedClaims contains identity and profile claims verified by the SDK.
type OIDCVerifiedClaims struct {
	Issuer, Subject, Email, PreferredUsername, Name string
	EmailVerified                                   *bool
}
type OIDCOAuthClient interface {
	ExchangeCode(context.Context, OIDCOAuthOptions, string, string, string) (*OIDCTokenResponse, error)
	FetchUserInfo(context.Context, OIDCOAuthOptions, *OIDCTokenResponse) (*OIDCUserInfoClaims, error)
	ValidateIDToken(context.Context, OIDCOAuthOptions, string, string) (*OIDCVerifiedClaims, error)
}

func (e *OIDCTokenExchangeError) Error() string {
	if e == nil {
		return ""
	}
	parts := []string{fmt.Sprintf("token exchange status=%d", e.StatusCode)}
	if strings.TrimSpace(e.ProviderError) != "" {
		parts = append(parts, "error="+strings.TrimSpace(e.ProviderError))
	}
	if strings.TrimSpace(e.ProviderDescription) != "" {
		parts = append(parts, "error_description="+strings.TrimSpace(e.ProviderDescription))
	}
	return strings.Join(parts, " ")
}

func OIDCIdentityKey(issuer, subject string) string {
	issuer = strings.TrimSpace(strings.ToLower(issuer))
	subject = strings.TrimSpace(subject)
	return issuer + "\x1f" + subject
}

func OIDCSyntheticEmailFromIdentityKey(identityKey string) string {
	identityKey = strings.TrimSpace(identityKey)
	if identityKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(identityKey))
	return "oidc-" + hex.EncodeToString(sum[:16]) + OIDCConnectSyntheticEmailDomain
}

func OIDCFallbackUsername(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "oidc_user"
	}
	sum := sha256.Sum256([]byte(subject))
	return "oidc_" + hex.EncodeToString(sum[:])[:12]
}

func PrepareOIDCChoice(
	identity PendingAuthIdentityKey,
	suggestedEmail string,
	resolvedEmail string,
	redirectTo string,
	browserSessionKey string,
	upstreamClaims map[string]any,
	compatEmail string,
	compatEmailUser *User,
	forceEmailOnSignup bool,
) OAuthPendingDraft {
	suggestionEmail := strings.TrimSpace(suggestedEmail)
	canonicalEmail := strings.TrimSpace(resolvedEmail)
	if suggestionEmail == "" {
		suggestionEmail = canonicalEmail
	}

	completionResponse := map[string]any{
		"step":                      OAuthPendingChoiceStep,
		"adoption_required":         true,
		"redirect":                  strings.TrimSpace(redirectTo),
		"email":                     suggestionEmail,
		"resolved_email":            canonicalEmail,
		"existing_account_email":    "",
		"existing_account_bindable": false,
		"create_account_allowed":    true,
		"force_email_on_signup":     forceEmailOnSignup,
		"choice_reason":             "third_party_signup",
	}
	if strings.TrimSpace(compatEmail) != "" {
		completionResponse["compat_email"] = strings.TrimSpace(compatEmail)
	}
	if compatEmailUser != nil {
		completionResponse["email"] = strings.TrimSpace(compatEmailUser.Email)
		completionResponse["existing_account_email"] = strings.TrimSpace(compatEmailUser.Email)
		completionResponse["existing_account_bindable"] = true
		completionResponse["choice_reason"] = "compat_email_match"
	}
	if forceEmailOnSignup && compatEmailUser == nil {
		completionResponse["choice_reason"] = "force_email_on_signup"
	}

	resolvedChoiceEmail := suggestionEmail
	if compatEmailUser != nil {
		resolvedChoiceEmail = strings.TrimSpace(compatEmailUser.Email)
	}
	var targetUserID *int64
	if compatEmailUser != nil && compatEmailUser.ID > 0 {
		targetUserID = &compatEmailUser.ID
	}

	return OAuthPendingDraft{
		Intent:                 OAuthIntentLogin,
		Identity:               identity,
		TargetUserID:           targetUserID,
		ResolvedEmail:          resolvedChoiceEmail,
		RedirectTo:             redirectTo,
		BrowserSessionKey:      browserSessionKey,
		UpstreamIdentityClaims: upstreamClaims,
		CompletionResponse:     completionResponse,
	}
}

func OIDCVerifiedEmailIdentity(identity PendingAuthIdentityKey, compatEmail, username string, upstreamClaims map[string]any) EmailOAuthIdentityInput {
	verifiedEmail := strings.TrimSpace(strings.ToLower(compatEmail))
	upstreamMetadata := make(map[string]any, len(upstreamClaims)+1)
	maps.Copy(upstreamMetadata, upstreamClaims)
	if syntheticEmail := PendingSessionStringValue(upstreamClaims, "email"); syntheticEmail != "" && !strings.EqualFold(syntheticEmail, verifiedEmail) {
		upstreamMetadata["synthetic_email"] = syntheticEmail
	}
	upstreamMetadata["email"] = verifiedEmail
	input := EmailOAuthIdentityInput{
		ProviderType:     strings.TrimSpace(identity.ProviderType),
		ProviderKey:      strings.TrimSpace(identity.ProviderKey),
		ProviderSubject:  strings.TrimSpace(identity.ProviderSubject),
		Email:            verifiedEmail,
		EmailVerified:    true,
		Username:         strings.TrimSpace(username),
		DisplayName:      PendingSessionStringValue(upstreamClaims, "suggested_display_name"),
		AvatarURL:        PendingSessionStringValue(upstreamClaims, "suggested_avatar_url"),
		UpstreamMetadata: upstreamMetadata,
	}
	return input
}
