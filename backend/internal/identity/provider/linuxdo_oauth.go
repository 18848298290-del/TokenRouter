package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/imroc/req/v3"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// LinuxDo code exchange accepts JSON and form-encoded token replies, while
// userinfo can vary in where it places identity fields. Configured gjson paths
// precede common field names, and the subject is checked before it can participate
// in the local synthetic email or username. Profile suggestions remain separate
// from that stable provider subject.
//
// Token exchange retains failure status and trimmed response text for callback handling.
// Successful parsing requires access_token, and an omitted token type becomes
// Bearer at exchange time. Userinfo construction accepts Bearer alone and rejects
// whitespace inside the credential. A missing userinfo email uses a subject-derived
// synthetic address; callers decide account binding and registration from the
// resulting subject and profile.

type (
	// LinuxDoTokenResponse aliases token fields consumed by the identity OAuth flow.
	LinuxDoTokenResponse = identity.LinuxDoTokenResponse
	// LinuxDoTokenExchangeError aliases provider failure details retained for callback handling.
	LinuxDoTokenExchangeError = identity.LinuxDoTokenExchangeError
)

// LinuxDoClient exchanges authorization codes and retrieves LinuxDo profiles.
type LinuxDoClient struct{}

// LinuxDoOptions aliases the resolved LinuxDo authorization settings supplied by identity.
type LinuxDoOptions = identity.LinuxDoOAuthOptions

// ExchangeCode delegates code exchange with the callback and optional PKCE
// verifier supplied by the authorization flow. Authentication method and token
// parsing follow the effective LinuxDo options; failures return the underlying
// exchange error.
func (LinuxDoClient) ExchangeCode(ctx context.Context, o LinuxDoOptions, code, redirect, verifier string) (*LinuxDoTokenResponse, error) {
	return LinuxDoExchangeCode(ctx, o, code, redirect, verifier)
}

// FetchUserInfo delegates profile retrieval with a nonnil token and the supplied
// LinuxDo options. Successful output includes the stable subject and profile
// suggestions; a missing email can become the subject-derived synthetic address.
func (LinuxDoClient) FetchUserInfo(ctx context.Context, o LinuxDoOptions, t *LinuxDoTokenResponse) (string, string, string, string, string, error) {
	return LinuxDoFetchUserInfo(ctx, o, t)
}

// LinuxDoExchangeCode submits a code grant with a thirty-second request budget.
// The caller supplies the callback and optional PKCE verifier; empty authentication
// method uses client_secret_post, while basic and none select the other supported
// credential forms. Failure responses retain status and body in an exchange error.
func LinuxDoExchangeCode(
	ctx context.Context,
	cfg LinuxDoOptions,
	code string,
	redirectURI string,
	codeVerifier string,
) (*LinuxDoTokenResponse, error) {
	client := req.C().SetTimeout(30 * time.Second)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", cfg.ClientID)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	if strings.TrimSpace(codeVerifier) != "" {
		form.Set("code_verifier", codeVerifier)
	}

	r := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/json")

	// Select one configured authentication form so a basic-auth endpoint receives
	// the secret in its header, while post-auth endpoints receive it in the grant body.
	switch strings.ToLower(strings.TrimSpace(cfg.TokenAuthMethod)) {
	case "", "client_secret_post":
		form.Set("client_secret", cfg.ClientSecret)
	case "client_secret_basic":
		r.SetBasicAuth(cfg.ClientID, cfg.ClientSecret)
	case "none":
	default:
		return nil, fmt.Errorf("unsupported token_auth_method: %s", cfg.TokenAuthMethod)
	}

	resp, err := r.SetFormDataFromValues(form).Post(cfg.TokenURL)
	if err != nil {
		return nil, fmt.Errorf("request token: %w", err)
	}
	body := strings.TrimSpace(resp.String())
	if !resp.IsSuccessState() {
		providerErr, providerDesc := ParseOAuthProviderError(body)
		return nil, &LinuxDoTokenExchangeError{
			StatusCode:          resp.StatusCode,
			ProviderError:       providerErr,
			ProviderDescription: providerDesc,
			Body:                body,
		}
	}

	tokenResp, ok := ParseLinuxDoTokenResponse(body)
	if !ok || strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, &LinuxDoTokenExchangeError{
			StatusCode: resp.StatusCode,
			Body:       body,
		}
	}
	if strings.TrimSpace(tokenResp.TokenType) == "" {
		// Accept providers that omit the type by selecting the same Bearer scheme
		// supported by profile fetching; supplied unsupported types still fail at that request.
		tokenResp.TokenType = "Bearer"
	}
	return tokenResp, nil
}

// LinuxDoFetchUserInfo retrieves a profile using a nonnil bearer token and a
// thirty-second request budget. BuildBearerAuthorization rejects unsupported token
// types and embedded whitespace, then LinuxDoParseUserInfo supplies the validated
// subject and display candidates from a successful response.
func LinuxDoFetchUserInfo(
	ctx context.Context,
	cfg LinuxDoOptions,
	token *LinuxDoTokenResponse,
) (email string, username string, subject string, displayName string, avatarURL string, err error) {
	client := req.C().SetTimeout(30 * time.Second)
	authorization, err := BuildBearerAuthorization(token.TokenType, token.AccessToken)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("invalid token for userinfo request: %w", err)
	}

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/json").
		SetHeader("Authorization", authorization).
		Get(cfg.UserInfoURL)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("request userinfo: %w", err)
	}
	if !resp.IsSuccessState() {
		return "", "", "", "", "", fmt.Errorf("userinfo status=%d", resp.StatusCode)
	}

	return LinuxDoParseUserInfo(resp.String(), cfg)
}

// LinuxDoParseUserInfo returns a stable subject with email and suggested profile
// fields from provider JSON. Configured paths take precedence over ordered common
// field names, so deployment-specific response shapes can select their values
// before the defaults are tried. A missing or unsafe subject fails the profile.
//
// The checked subject is also used to build a synthetic email when userinfo omits
// email, keeping the login identity usable by the local user record. Missing
// username becomes linuxdo_ plus subject, and missing display name uses that
// username. These display replacements remain separate from subject identity.
func LinuxDoParseUserInfo(body string, cfg LinuxDoOptions) (email string, username string, subject string, displayName string, avatarURL string, err error) {
	// Resolve configurable fields before common shapes so this login uses the
	// administrator-selected email, username, and subject when those values are present.
	email = identity.OAuthFirstNonEmpty(
		GetGJSON(body, cfg.UserInfoEmailPath),
		GetGJSON(body, "email"),
		GetGJSON(body, "user.email"),
		GetGJSON(body, "data.email"),
		GetGJSON(body, "attributes.email"),
	)
	username = identity.OAuthFirstNonEmpty(
		GetGJSON(body, cfg.UserInfoUsernamePath),
		GetGJSON(body, "username"),
		GetGJSON(body, "preferred_username"),
		GetGJSON(body, "name"),
		GetGJSON(body, "user.username"),
		GetGJSON(body, "user.name"),
	)
	subject = identity.OAuthFirstNonEmpty(
		GetGJSON(body, cfg.UserInfoIDPath),
		GetGJSON(body, "sub"),
		GetGJSON(body, "id"),
		GetGJSON(body, "user_id"),
		GetGJSON(body, "uid"),
		GetGJSON(body, "user.id"),
	)

	// Derive display suggestions independently from the stable subject fields;
	// a provider can supply name and avatar through different userinfo response shapes.
	displayName = identity.OAuthFirstNonEmpty(
		GetGJSON(body, "name"),
		GetGJSON(body, "nickname"),
		GetGJSON(body, "display_name"),
		GetGJSON(body, "user.name"),
		GetGJSON(body, "user.username"),
		username,
	)
	avatarURL = identity.OAuthFirstNonEmpty(
		GetGJSON(body, "avatar_url"),
		GetGJSON(body, "avatar"),
		GetGJSON(body, "picture"),
		GetGJSON(body, "profile_image_url"),
		GetGJSON(body, "user.avatar"),
		GetGJSON(body, "user.avatar_url"),
	)

	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "", "", "", "", "", errors.New("userinfo missing id field")
	}
	if !identity.OAuthIsSafeLinuxDoSubject(subject) {
		return "", "", "", "", "", errors.New("userinfo returned invalid id field")
	}

	email = strings.TrimSpace(email)
	if email == "" {
		// The checked subject determines a synthetic address when no email candidate
		// is present, allowing local user storage to retain a stable account email value.
		email = identity.OAuthLinuxDoSyntheticEmail(subject)
	}

	username = strings.TrimSpace(username)
	if username == "" {
		// Give accepted subjects a username suggestion even when userinfo lacks one,
		// so the local account flow receives a usable display candidate for signup.
		username = "linuxdo_" + subject
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		// Use the resolved username after name candidates are absent so returned
		// profile suggestions still contain display text for the identity flow.
		displayName = username
	}
	avatarURL = strings.TrimSpace(avatarURL)

	return email, username, subject, displayName, avatarURL, nil
}

// ParseOAuthProviderError extracts a provider error code and description from
// JSON paths, then tries form-encoded fields when neither JSON selection is
// nonempty. Each representation has ordered field candidates, and one selected
// JSON value is sufficient to keep that result.
//
// GetGJSON can stringify a non-string JSON value, so an object under error can
// become the selected error text before nested paths are considered. Empty input
// or an unparseable form returns empty strings; callers retain the trimmed response
// text separately in their token-exchange errors.
func ParseOAuthProviderError(body string) (providerErr string, providerDesc string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", ""
	}

	providerErr = identity.OAuthFirstNonEmpty(
		GetGJSON(body, "error"),
		GetGJSON(body, "code"),
		GetGJSON(body, "error.code"),
	)
	providerDesc = identity.OAuthFirstNonEmpty(
		GetGJSON(body, "error_description"),
		GetGJSON(body, "error.message"),
		GetGJSON(body, "message"),
		GetGJSON(body, "detail"),
	)

	if providerErr != "" || providerDesc != "" {
		return providerErr, providerDesc
	}

	// Try the form representation after JSON supplies no usable fields, keeping
	// OAuth endpoints that return query-encoded bodies readable by the same caller.
	values, err := url.ParseQuery(body)
	if err != nil {
		return "", ""
	}
	providerErr = identity.OAuthFirstNonEmpty(values.Get("error"), values.Get("code"))
	providerDesc = identity.OAuthFirstNonEmpty(values.Get("error_description"), values.Get("error_message"), values.Get("message"))
	return providerErr, providerDesc
}

// ParseLinuxDoTokenResponse decodes JSON or form-encoded token fields while
// requiring a nonempty access_token in either representation. JSON token presence
// selects that representation first, and the returned token type remains as decoded
// for the exchange caller to supply its default.
//
// Form decoding is attempted when JSON yields no access token. A malformed form
// or absent token returns false. An absent or invalid decimal expires_in becomes
// zero in the form result; the JSON result uses gjson's integer conversion.
// Profile retrieval later validates the token type before constructing Authorization.
func ParseLinuxDoTokenResponse(body string) (*LinuxDoTokenResponse, bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, false
	}

	accessToken := strings.TrimSpace(GetGJSON(body, "access_token"))
	if accessToken != "" {
		tokenType := strings.TrimSpace(GetGJSON(body, "token_type"))
		refreshToken := strings.TrimSpace(GetGJSON(body, "refresh_token"))
		scope := strings.TrimSpace(GetGJSON(body, "scope"))
		expiresIn := gjson.Get(body, "expires_in").Int()
		return &LinuxDoTokenResponse{
			AccessToken:  accessToken,
			TokenType:    tokenType,
			ExpiresIn:    expiresIn,
			RefreshToken: refreshToken,
			Scope:        scope,
		}, true
	}

	// Try the form representation after JSON supplies no usable fields, keeping
	// OAuth endpoints that return query-encoded bodies readable by the same caller.
	values, err := url.ParseQuery(body)
	if err != nil {
		return nil, false
	}
	accessToken = strings.TrimSpace(values.Get("access_token"))
	if accessToken == "" {
		return nil, false
	}
	expiresIn := int64(0)
	if raw := strings.TrimSpace(values.Get("expires_in")); raw != "" {
		// Malformed lifetime text leaves zero while the access token stays usable;
		// LinuxDoFetchUserInfo uses the token type and access credential from this result.
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			expiresIn = v
		}
	}
	return &LinuxDoTokenResponse{
		AccessToken:  accessToken,
		TokenType:    strings.TrimSpace(values.Get("token_type")),
		ExpiresIn:    expiresIn,
		RefreshToken: strings.TrimSpace(values.Get("refresh_token")),
		Scope:        strings.TrimSpace(values.Get("scope")),
	}, true
}

// GetGJSON reads a trimmed gjson path and returns the selected value's string
// representation. An empty path or absent value returns empty text, allowing
// provider parsers to continue through their ordered candidate paths.
func GetGJSON(body string, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	res := gjson.Get(body, path)
	if !res.Exists() {
		return ""
	}
	return res.String()
}

// BuildBearerAuthorization builds a normalized Bearer header from a supported
// token type and trimmed access token. An empty type defaults to Bearer; other
// types, missing token text, and remaining space, tab, or line breaks fail.
func BuildBearerAuthorization(tokenType, accessToken string) (string, error) {
	tokenType = strings.TrimSpace(tokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}
	if !strings.EqualFold(tokenType, "Bearer") {
		return "", fmt.Errorf("unsupported token_type: %s", tokenType)
	}

	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return "", errors.New("missing access_token")
	}
	if strings.ContainsAny(accessToken, " \t\r\n") {
		return "", errors.New("access_token contains whitespace")
	}
	// Normalize accepted case variants to one header scheme so userinfo receives
	// a consistent Authorization value after token type and credential validation.
	return "Bearer " + accessToken, nil
}
