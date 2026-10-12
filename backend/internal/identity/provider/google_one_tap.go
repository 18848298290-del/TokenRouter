package provider

import (
	"context"
	"errors"
	"strings"
	"time"

	"google.golang.org/api/idtoken"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

type GoogleIDTokenClaims = identity.GoogleIDTokenClaims

type GoogleIDTokenVerifier = identity.GoogleIDTokenVerifier

type GoogleAPIIDTokenVerifier struct{}

// Verify uses the Google validator for signature and standard claims, then checks required identity fields.
func (GoogleAPIIDTokenVerifier) Verify(ctx context.Context, credential string, audience string) (*GoogleIDTokenClaims, error) {
	payload, err := idtoken.Validate(ctx, credential, audience)
	if err != nil {
		return nil, err
	}
	return ValidateGoogleIDTokenPayload(payload, audience, time.Now())
}

// ValidateGoogleIDTokenPayload checks application-required claims after signature validation.
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

func GoogleIDTokenStringClaim(claims map[string]any, key string) string {
	value, _ := claims[key].(string)
	return value
}
