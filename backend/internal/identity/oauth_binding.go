package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

// oauth_binding.go signs and verifies local account identifiers carried
// through OAuth binding redirects.
// OAuth binding cookies carry a decimal local user ID and a signature so the
// callback can recover its target after a browser redirect. The ID remains
// readable, while HMAC-SHA256 detects changes to the exact decimal payload using
// the configured secret. Signing accepts positive IDs and a nonblank secret;
// verification checks the signature before parsing and validating the ID.
//
// The cookie format contains no expiry or provider information. HTTP handlers
// set its lifetime and use the recovered ID to load the target user, while the
// authentication flow separately checks user state and binding intent. Reusing
// the same secret across signing and verification is therefore required for the
// redirect to identify the intended local account.

// OAuthBindingSigner signs user IDs for binding cookies using HMAC.
type OAuthBindingSigner struct {
	secret string /* shared cookie signing secret */
}

// NewOAuthBindingSigner retains the secret used by both cookie operations.
// Sign and Verify trim it when invoked and report an error when it is blank,
// so construction itself does not validate configuration.
func NewOAuthBindingSigner(secret string) OAuthBindingSigner {
	return OAuthBindingSigner{secret: secret}
}

// Sign encodes a positive local user ID into a signed binding cookie.
// The configured secret must remain available to Verify after the redirect;
// blank secrets and nonpositive IDs return an error.
func (s OAuthBindingSigner) Sign(id int64) (string, error) {
	return BuildOAuthBindUserCookieValue(id, s.secret)
}

// Verify authenticates a binding cookie and returns its positive local user ID.
// The caller still loads and checks that user for the current binding flow;
// malformed values or signature mismatches return an error.
func (s OAuthBindingSigner) Verify(value string) (int64, error) {
	return ParseOAuthBindUserCookieValue(value, s.secret)
}

// BuildOAuthBindUserCookieValue returns the decimal user ID followed by a dot
// and its raw URL-base64 HMAC signature. A positive userID and a nonblank trimmed
// secret are required before the cookie can be constructed.
//
// The signature covers the decimal payload exactly, allowing the callback to
// detect tampering while recovering the local account ID. The value carries no
// expiration timestamp, so HTTP cookie lifetime and current-user checks remain
// responsibilities of the binding flow that transports it.
func BuildOAuthBindUserCookieValue(userID int64, secret string) (string, error) {
	secret = strings.TrimSpace(secret)
	if userID <= 0 || secret == "" {
		return "", errors.New("invalid oauth bind cookie input")
	}
	payload := strconv.FormatInt(userID, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	// HMAC accepts the complete decimal payload and reports no write error, so
	// signature construction can disregard the write result for this in-memory hash.
	_, _ = mac.Write([]byte(payload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + signature, nil
}

// ParseOAuthBindUserCookieValue verifies a binding cookie and returns its local
// account ID. It trims the complete value and secret, requires a dot-separated
// payload and signature, and rejects a missing secret before computing the MAC.
//
// Comparing the expected raw URL-base64 signature authenticates the exact
// payload before decimal parsing. A malformed or nonpositive ID is rejected even
// after signature verification. The parser checks no timestamp or account state;
// the binding handler supplies cookie lifetime and loads the recovered user.
func ParseOAuthBindUserCookieValue(value string, secret string) (int64, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return 0, errors.New("missing oauth bind cookie secret")
	}
	payload, signature, ok := strings.Cut(strings.TrimSpace(value), ".")
	if !ok || payload == "" || signature == "" {
		return 0, errors.New("invalid oauth bind cookie")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	// HMAC accepts the complete decimal payload and reports no write error, so
	// signature construction can disregard the write result for this in-memory hash.
	_, _ = mac.Write([]byte(payload))
	expectedSignature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(signature), []byte(expectedSignature)) {
		return 0, errors.New("invalid oauth bind cookie signature")
	}
	userID, err := strconv.ParseInt(payload, 10, 64)
	if err != nil || userID <= 0 {
		return 0, errors.New("invalid oauth bind cookie user")
	}
	return userID, nil
}
