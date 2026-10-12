package provider

import (
	"context"
	"errors"
	"strings"
	"time"

	"google.golang.org/api/idtoken"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// Google One Tap credentials need a verified signature and application identity
// claims before they can identify a local login subject. The official validator
// checks the token, then the payload check requires a Google issuer, the selected
// audience, future expiry, and a verified nonempty email with a nonempty subject.

// GoogleIDTokenClaims aliases the identity fields extracted from a verified Google credential.
type GoogleIDTokenClaims = identity.GoogleIDTokenClaims

// GoogleIDTokenVerifier aliases the identity credential-verification interface used by HTTP login.
type GoogleIDTokenVerifier = identity.GoogleIDTokenVerifier

// GoogleAPIIDTokenVerifier validates signed credentials through Google's ID-token library.
type GoogleAPIIDTokenVerifier struct{}

// Verify checks the signed credential with Google's validator using the supplied
// audience, then applies local payload requirements with the current time. Library
// or claim-validation errors stop before identity claims are returned.
func (GoogleAPIIDTokenVerifier) Verify(ctx context.Context, credential string, audience string) (*GoogleIDTokenClaims, error) {
	payload, err := idtoken.Validate(ctx, credential, audience)
	if err != nil {
		return nil, err
	}
	return ValidateGoogleIDTokenPayload(payload, audience, time.Now())
}

// ValidateGoogleIDTokenPayload checks application claims in an already
// signature-validated payload and returns trimmed identity profile values. Callers
// perform signature verification first; this helper checks issuer, trimmed
// audience, expiry strictly after now, boolean true email verification, subject,
// and email. Optional non-string profile claims become empty text.
func ValidateGoogleIDTokenPayload(payload *idtoken.Payload, audience string, now time.Time) (*GoogleIDTokenClaims, error) {
	if payload == nil {
		return nil, errors.New("google id token payload is missing")
	}
	if payload.Issuer != "accounts.google.com" && payload.Issuer != "https://accounts.google.com" {
		return nil, errors.New("google id token issuer is invalid")
	}
	if payload.Audience != strings.TrimSpace(audience) {
		return nil, errors.New("google id token audience is invalid")
	}
	// Compare whole Unix seconds; equality is already expired for this local check.
	if payload.Expires <= now.Unix() {
		return nil, errors.New("google id token is expired")
	}

	verified, ok := payload.Claims["email_verified"].(bool)
	if !ok || !verified {
		return nil, errors.New("google verified email is missing")
	}
	claims := &GoogleIDTokenClaims{
		Subject:       strings.TrimSpace(payload.Subject),
		Email:         strings.TrimSpace(GoogleIDTokenStringClaim(payload.Claims, "email")),
		EmailVerified: true,
		Name:          strings.TrimSpace(GoogleIDTokenStringClaim(payload.Claims, "name")),
		GivenName:     strings.TrimSpace(GoogleIDTokenStringClaim(payload.Claims, "given_name")),
		Picture:       strings.TrimSpace(GoogleIDTokenStringClaim(payload.Claims, "picture")),
		Locale:        strings.TrimSpace(GoogleIDTokenStringClaim(payload.Claims, "locale")),
		HostedDomain:  strings.TrimSpace(GoogleIDTokenStringClaim(payload.Claims, "hd")),
	}
	if claims.Subject == "" || claims.Email == "" {
		return nil, errors.New("google id token identity is incomplete")
	}
	return claims, nil
}

// GoogleIDTokenStringClaim reads a string profile claim for identity extraction.
// Absent keys or values of another type yield empty text so optional names, locale,
// and avatar fields can remain unset.
func GoogleIDTokenStringClaim(claims map[string]any, key string) string {
	value, _ := claims[key].(string)
	return value
}
