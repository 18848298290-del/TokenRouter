package provider

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// TestBuildBearerAuthorization covers omitted and lowercase Bearer types plus
// unsupported type and embedded token whitespace. Accepted inputs produce the
// canonical header, and both invalid credential forms return errors.
func TestBuildBearerAuthorization(t *testing.T) {
	auth, err := BuildBearerAuthorization("", "token123")
	require.NoError(t, err)
	require.Equal(t, "Bearer token123", auth)

	auth, err = BuildBearerAuthorization("bearer", "token123")
	require.NoError(t, err)
	require.Equal(t, "Bearer token123", auth)

	_, err = BuildBearerAuthorization("MAC", "token123")
	require.Error(t, err)

	_, err = BuildBearerAuthorization("Bearer", "token 123")
	require.Error(t, err)
}

// TestLinuxDoParseUserInfoParsesIDAndUsername supplies profile names and a numeric
// identifier with no email. Parsing retains the subject, username, display name,
// and avatar while deriving the expected synthetic address from that subject.
func TestLinuxDoParseUserInfoParsesIDAndUsername(t *testing.T) {
	cfg := LinuxDoOptions{
		UserInfoURL: "https://connect.linux.do/api/user",
	}

	email, username, subject, displayName, avatarURL, err := LinuxDoParseUserInfo(`{"id":123,"username":"alice","name":"Alice","avatar_url":"https://cdn.example/avatar.png"}`, cfg)
	require.NoError(t, err)
	require.Equal(t, "123", subject)
	require.Equal(t, "alice", username)
	require.Equal(t, "linuxdo-123@linuxdo-connect.invalid", email)
	require.Equal(t, "Alice", displayName)
	require.Equal(t, "https://cdn.example/avatar.png", avatarURL)
}

// TestLinuxDoParseUserInfoDefaultsUsername supplies a subject without any optional
// profile fields. The parser derives the synthetic email, username, and display
// name while leaving the absent avatar empty.
func TestLinuxDoParseUserInfoDefaultsUsername(t *testing.T) {
	cfg := LinuxDoOptions{
		UserInfoURL: "https://connect.linux.do/api/user",
	}

	email, username, subject, displayName, avatarURL, err := LinuxDoParseUserInfo(`{"id":"123"}`, cfg)
	require.NoError(t, err)
	require.Equal(t, "123", subject)
	require.Equal(t, "linuxdo_123", username)
	require.Equal(t, "linuxdo-123@linuxdo-connect.invalid", email)
	require.Equal(t, "linuxdo_123", displayName)
	require.Equal(t, "", avatarURL)
}

// TestLinuxDoParseUserInfoRejectsUnsafeSubject supplies an email-like identifier
// and an identifier beyond the allowed subject length. Both supplied profiles
// return errors; the overlong input is constructed from the published subject limit.
func TestLinuxDoParseUserInfoRejectsUnsafeSubject(t *testing.T) {
	cfg := LinuxDoOptions{
		UserInfoURL: "https://connect.linux.do/api/user",
	}

	_, _, _, _, _, err := LinuxDoParseUserInfo(`{"id":"123@456"}`, cfg)
	require.Error(t, err)

	tooLong := strings.Repeat("a", identity.OAuthLinuxDoMaxSubjectLength+1)
	_, _, _, _, _, err = LinuxDoParseUserInfo(`{"id":"`+tooLong+`"}`, cfg)
	require.Error(t, err)
}

// TestParseOAuthProviderErrorJSON supplies a JSON OAuth failure with code and
// description fields. The parser returns those two values for token-exchange
// errors retained by the calling authorization flow.
func TestParseOAuthProviderErrorJSON(t *testing.T) {
	code, desc := ParseOAuthProviderError(`{"error":"invalid_client","error_description":"bad secret"}`)
	require.Equal(t, "invalid_client", code)
	require.Equal(t, "bad secret", desc)
}

// TestParseOAuthProviderErrorForm supplies a form-encoded OAuth failure with a
// space represented by a plus sign. The parser returns the code and decoded
// description after the JSON path candidates are empty.
func TestParseOAuthProviderErrorForm(t *testing.T) {
	code, desc := ParseOAuthProviderError("error=invalid_request&error_description=Missing+code_verifier")
	require.Equal(t, "invalid_request", code)
	require.Equal(t, "Missing code_verifier", desc)
}

// TestParseLinuxDoTokenResponseJSON supplies a successful JSON credential
// response. Parsing accepts it and retains access token, token type, lifetime,
// and granted scope for the subsequent profile request.
func TestParseLinuxDoTokenResponseJSON(t *testing.T) {
	token, ok := ParseLinuxDoTokenResponse(`{"access_token":"t1","token_type":"Bearer","expires_in":3600,"scope":"user"}`)
	require.True(t, ok)
	require.Equal(t, "t1", token.AccessToken)
	require.Equal(t, "Bearer", token.TokenType)
	require.Equal(t, int64(3600), token.ExpiresIn)
	require.Equal(t, "user", token.Scope)
}

// TestParseLinuxDoTokenResponseForm supplies token fields as form-encoded text.
// Parsing accepts the credential and retains the lowercase token type and decimal
// lifetime for the exchange caller to interpret.
func TestParseLinuxDoTokenResponseForm(t *testing.T) {
	token, ok := ParseLinuxDoTokenResponse("access_token=t2&token_type=bearer&expires_in=60")
	require.True(t, ok)
	require.Equal(t, "t2", token.AccessToken)
	require.Equal(t, "bearer", token.TokenType)
	require.Equal(t, int64(60), token.ExpiresIn)
}
