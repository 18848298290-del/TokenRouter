package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/imroc/req/v3"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

// email_oauth.go exchanges GitHub and Google authorization codes and retrieves
// profiles with verified email addresses.
// Email OAuth profiles identify users by provider subject and require verified
// email before local account decisions. GitHub userinfo supplies the numeric
// subject, but its separate email endpoint supplies verification and primary-email
// flags. Google provides subject and verified email in the fetched profile itself.
// Code exchange returns the bearer credential used for those profile reads.
//
// GitHub email selection makes two passes: a nonempty primary verified address
// wins, and otherwise the first nonempty verified address is selected. Unverified
// addresses never satisfy that lookup. The resulting profile keeps the stable
// provider subject separate from its email and suggested display fields so the
// identity flow can enforce account ownership independently.

type (
	// EmailOAuthOptions aliases the identity configuration consumed during code exchange.
	EmailOAuthOptions = identity.EmailOAuthOptions
	// EmailOAuthTokenResponse aliases the identity bearer credential and granted-scope response.
	EmailOAuthTokenResponse = identity.EmailOAuthTokenResponse
	// EmailOAuthProfile aliases the verified identity profile returned to account flows.
	EmailOAuthProfile = identity.EmailOAuthProfile
)

// EmailOAuthClientAdapter implements code exchange and profile retrieval over HTTP.
type EmailOAuthClientAdapter struct{}

// ExchangeEmailOAuthCode posts an authorization-code grant with the configured
// client credentials and backend callback. It requires a successful status, decoded
// JSON, and nonempty access_token before returning the bearer credential to the
// profile-fetching caller.
func ExchangeEmailOAuthCode(ctx context.Context, cfg EmailOAuthOptions, code string) (*EmailOAuthTokenResponse, error) {
	resp, err := req.C().
		R().
		SetContext(ctx).
		SetHeader("Accept", "application/json").
		SetFormData(map[string]string{
			"grant_type":    "authorization_code",
			"client_id":     cfg.ClientID,
			"client_secret": cfg.ClientSecret,
			"code":          code,
			"redirect_uri":  cfg.RedirectURL,
		}).
		Post(cfg.TokenURL)
	if err != nil {
		return nil, err
	}
	// Bound the endpoint body included in failures so callback error handling
	// receives at most 1024 bytes at a UTF-8 boundary from this response.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token endpoint status %d: %s", resp.StatusCode, logredact.TruncateUTF8Value(resp.String(), 1024))
	}
	var tokenResp EmailOAuthTokenResponse
	if err := json.Unmarshal(resp.Bytes(), &tokenResp); err != nil {
		return nil, err
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, errors.New("missing access_token")
	}
	return &tokenResp, nil
}

// FetchEmailOAuthProfile reads userinfo with a nonnil token and interprets the
// body for GitHub or Google. The provider name is normalized after the request;
// unsupported providers fail at interpretation, and HTTP failures stop before
// profile parsing.
func FetchEmailOAuthProfile(ctx context.Context, provider string, cfg EmailOAuthOptions, token *EmailOAuthTokenResponse) (*EmailOAuthProfile, error) {
	resp, err := req.C().
		R().
		SetContext(ctx).
		SetBearerAuthToken(token.AccessToken).
		SetHeader("Accept", "application/json").
		Get(cfg.UserInfoURL)
	if err != nil {
		return nil, err
	}
	// Bound the endpoint body included in failures so callback error handling
	// receives at most 1024 bytes at a UTF-8 boundary from this response.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("userinfo endpoint status %d: %s", resp.StatusCode, logredact.TruncateUTF8Value(resp.String(), 1024))
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "github":
		return ParseGitHubOAuthProfile(ctx, cfg, token, resp.String())
	case "google":
		return ParseGoogleOAuthProfile(resp.String())
	default:
		return nil, errors.New("unsupported oauth provider")
	}
}

// ParseGitHubOAuthProfile combines the userinfo subject with a verified address
// from the configured email endpoint. The subject must be nonempty before the
// separate email request runs. A nonnil token and nonempty EmailsURL are required
// for that request, whose verified result supplies the profile's signup email.
//
// The subject remains the userinfo ID even when the email endpoint selects another
// address, so account lookup can use the stable provider identity. Missing login
// and name values leave a subject-derived username suggestion; display fields
// remain separate from the subject and verified email.
func ParseGitHubOAuthProfile(ctx context.Context, cfg EmailOAuthOptions, token *EmailOAuthTokenResponse, body string) (*EmailOAuthProfile, error) {
	subject := strings.TrimSpace(gjson.Get(body, "id").String())
	if subject == "" {
		return nil, errors.New("github user id is missing")
	}
	emailsURL := strings.TrimSpace(cfg.EmailsURL)
	if emailsURL == "" {
		return nil, errors.New("github verified email is missing")
	}
	email, err := FetchGitHubPrimaryVerifiedEmail(ctx, emailsURL, token.AccessToken)
	if err != nil {
		return nil, err
	}
	if email == "" {
		return nil, errors.New("github verified email is missing")
	}
	login := strings.TrimSpace(gjson.Get(body, "login").String())
	name := strings.TrimSpace(gjson.Get(body, "name").String())
	// Missing display fields can still produce a suggested username from the
	// stable GitHub subject, while the selected verified email remains the signup input.
	return &EmailOAuthProfile{
		Subject:       subject,
		Email:         email,
		EmailVerified: true,
		Username:      identity.OAuthFirstNonEmpty(login, name, "github_"+subject),
		DisplayName:   identity.OAuthFirstNonEmpty(name, login),
		AvatarURL:     strings.TrimSpace(gjson.Get(body, "avatar_url").String()),
		Metadata: map[string]any{
			"login": login,
		},
	}, nil
}

// FetchGitHubPrimaryVerifiedEmail retrieves the authenticated email list and
// selects a usable verified address. It first scans for a nonempty address marked
// both primary and verified, then scans again for any nonempty verified address;
// response order determines the winner within either category.
//
// The second pass handles accounts whose listed primary address cannot satisfy
// verification while another address can. HTTP failures return endpoint errors,
// and an empty or unsuitable list returns the missing-verified-email error so
// local signup cannot proceed from an unverified profile email.
func FetchGitHubPrimaryVerifiedEmail(ctx context.Context, emailsURL string, accessToken string) (string, error) {
	resp, err := req.C().
		R().
		SetContext(ctx).
		SetBearerAuthToken(accessToken).
		SetHeader("Accept", "application/json").
		Get(emailsURL)
	if err != nil {
		return "", err
	}
	// Bound the endpoint body included in failures so callback error handling
	// receives at most 1024 bytes at a UTF-8 boundary from this response.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("github emails endpoint status %d: %s", resp.StatusCode, logredact.TruncateUTF8Value(resp.String(), 1024))
	}
	items := gjson.Parse(resp.String()).Array()
	for _, item := range items {
		if item.Get("primary").Bool() && item.Get("verified").Bool() {
			if email := strings.TrimSpace(item.Get("email").String()); email != "" {
				return email, nil
			}
		}
	}
	// If no usable primary verified address was found, another verified address
	// can still supply signup email; response order resolves multiple eligible addresses.
	for _, item := range items {
		if item.Get("verified").Bool() {
			if email := strings.TrimSpace(item.Get("email").String()); email != "" {
				return email, nil
			}
		}
	}
	return "", errors.New("github verified email is missing")
}

// ParseGoogleOAuthProfile extracts a nonempty subject and a verified nonempty
// email from the userinfo body. Suggested username uses given_name, then name,
// then email; display name and avatar can remain empty in the returned profile.
func ParseGoogleOAuthProfile(body string) (*EmailOAuthProfile, error) {
	subject := strings.TrimSpace(gjson.Get(body, "sub").String())
	email := strings.TrimSpace(gjson.Get(body, "email").String())
	verified := gjson.Get(body, "email_verified").Bool()
	if subject == "" {
		return nil, errors.New("google subject is missing")
	}
	if email == "" || !verified {
		return nil, errors.New("google verified email is missing")
	}
	name := strings.TrimSpace(gjson.Get(body, "name").String())
	// Use the required verified email as the username suggestion when profile
	// names are empty, allowing an accepted identity to retain a display candidate.
	return &EmailOAuthProfile{
		Subject:       subject,
		Email:         email,
		EmailVerified: true,
		Username:      identity.OAuthFirstNonEmpty(strings.TrimSpace(gjson.Get(body, "given_name").String()), name, email),
		DisplayName:   name,
		AvatarURL:     strings.TrimSpace(gjson.Get(body, "picture").String()),
		Metadata: map[string]any{
			"email_verified": true,
		},
	}, nil
}

// ExchangeCode delegates authorization-code exchange using the supplied identity
// options and context. The returned token is ready for profile retrieval when the
// HTTP exchange, JSON decoding, and access-token presence check succeed.
func (EmailOAuthClientAdapter) ExchangeCode(ctx context.Context, c EmailOAuthOptions, code string) (*EmailOAuthTokenResponse, error) {
	return ExchangeEmailOAuthCode(ctx, c, code)
}

// FetchProfile delegates the provider-specific verified-profile lookup with the
// supplied nonnil token. GitHub reads its email-list endpoint after userinfo; Google
// checks the verification claim in the userinfo response itself.
func (EmailOAuthClientAdapter) FetchProfile(ctx context.Context, provider string, c EmailOAuthOptions, t *EmailOAuthTokenResponse) (*EmailOAuthProfile, error) {
	return FetchEmailOAuthProfile(ctx, provider, c, t)
}
