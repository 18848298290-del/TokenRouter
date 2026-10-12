package provider

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/option"
)

type googleFixtureTransport struct{ target *url.URL }

func TestValidateGoogleIDTokenPayload(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	validPayload := func() *idtoken.Payload {
		return &idtoken.Payload{
			Issuer:   "https://accounts.google.com",
			Audience: "google-web-client",
			Expires:  now.Add(time.Minute).Unix(),
			Subject:  "google-subject",
			Claims: map[string]any{
				"email":          "user@example.com",
				"email_verified": true,
				"name":           "Example User",
			},
		}
	}

	claims, err := ValidateGoogleIDTokenPayload(validPayload(), "google-web-client", now)
	require.NoError(t, err)
	require.Equal(t, "google-subject", claims.Subject)
	require.Equal(t, "user@example.com", claims.Email)
	require.True(t, claims.EmailVerified)

	tests := []struct {
		name   string
		mutate func(*idtoken.Payload)
	}{
		{name: "错误 audience", mutate: func(payload *idtoken.Payload) { payload.Audience = "other-client" }},
		{name: "错误 issuer", mutate: func(payload *idtoken.Payload) { payload.Issuer = "https://issuer.example" }},
		{name: "token 已过期", mutate: func(payload *idtoken.Payload) { payload.Expires = now.Unix() }},
		{name: "邮箱未验证", mutate: func(payload *idtoken.Payload) { payload.Claims["email_verified"] = false }},
		{name: "缺少主体", mutate: func(payload *idtoken.Payload) { payload.Subject = "" }},
		{name: "缺少邮箱", mutate: func(payload *idtoken.Payload) { payload.Claims["email"] = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := validPayload()
			tt.mutate(payload)
			_, err := ValidateGoogleIDTokenPayload(payload, "google-web-client", now)
			require.Error(t, err)
		})
	}
}

// TestGoogleOfficialValidatorWithLocalJWKS validates local RSA signatures through the Google library.
func TestGoogleOfficialValidatorWithLocalJWKS(t *testing.T) {
	ctx := context.Background()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, e)
	encode := base64.RawURLEncoding.EncodeToString
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=60")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test", "n": encode(key.N.Bytes()), "e": encode(big.NewInt(int64(key.E)).Bytes())}}})
	}))
	defer endpoint.Close()
	target, e := url.Parse(endpoint.URL)
	require.NoError(t, e)
	validator, e := idtoken.NewValidator(ctx, option.WithHTTPClient(&http.Client{Transport: googleFixtureTransport{target}}))
	require.NoError(t, e)
	sign := func(claims map[string]any) string {
		header, e := json.Marshal(map[string]any{"alg": "RS256", "kid": "test", "typ": "JWT"})
		require.NoError(t, e)
		payload, e := json.Marshal(claims)
		require.NoError(t, e)
		signed := encode(header) + "." + encode(payload)
		digest := sha256.Sum256([]byte(signed))
		signature, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		require.NoError(t, e)
		return signed + "." + encode(signature)
	}
	for _, sample := range []struct {
		name            string
		change          func(map[string]any)
		tamper, success bool
	}{
		{name: "valid", success: true},
		{name: "audience", change: func(c map[string]any) { c["aud"] = "another-client" }},
		{name: "expired", change: func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }},
		{name: "issuer", change: func(c map[string]any) { c["iss"] = "https://wrong.example" }},
		{name: "verified-type", change: func(c map[string]any) { c["email_verified"] = "true" }},
		{name: "subject", change: func(c map[string]any) { c["sub"] = "" }},
		{name: "signature", tamper: true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			claims := map[string]any{"iss": "https://accounts.google.com", "aud": "test-client", "sub": "local-subject", "email": "local@example.com", "email_verified": true, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()}
			if sample.change != nil {
				sample.change(claims)
			}
			token := sign(claims)
			if sample.tamper {
				parts := strings.Split(token, ".")
				signature, err := base64.RawURLEncoding.DecodeString(parts[2])
				require.NoError(t, err)
				signature[len(signature)-1] ^= 1
				parts[2] = encode(signature)
				token = strings.Join(parts, ".")
			} // Re-encode the modified signature in JWT format.
			payload, e := validator.Validate(ctx, token, "test-client")
			if e == nil {
				_, e = ValidateGoogleIDTokenPayload(payload, "test-client", time.Now())
			}
			if sample.success {
				require.NoError(t, e)
			} else {
				require.Error(t, e)
			}
		})
	}
}

// RoundTrip redirects public-key requests to the test JWKS server.
func (t googleFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.URL = t.target
	return http.DefaultTransport.RoundTrip(copy)
}
