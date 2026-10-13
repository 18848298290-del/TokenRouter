package googleauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

// TestServiceAccountSignedExchange receives a locally signed assertion and
// checks its issuer, audience, key ID, scope, and one-hour claims. The
// returned access token carries the expected fifty-five-minute cache lifetime.
func TestServiceAccountSignedExchange(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	var tokenURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "urn:ietf:params:oauth:grant-type:jwt-bearer", r.Form.Get("grant_type"))
		token, err := jwt.Parse(r.Form.Get("assertion"), func(token *jwt.Token) (any, error) {
			require.Equal(t, "fixture-kid", token.Header["kid"])
			return &key.PublicKey, nil
		}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithAudience(tokenURL), jwt.WithIssuer("fixture@example.invalid"))
		require.NoError(t, err)
		require.True(t, token.Valid)
		claims, ok := token.Claims.(jwt.MapClaims)
		require.True(t, ok)
		require.Equal(t, CloudPlatformScope, claims["scope"])
		expiry, err := claims.GetExpirationTime()
		require.NoError(t, err)
		issued, err := claims.GetIssuedAt()
		require.NoError(t, err)
		require.Equal(t, time.Hour, expiry.Sub(issued.Time))
		_, _ = io.WriteString(w, `{"access_token":"fixture-token","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer server.Close()
	tokenURL = server.URL
	secret := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	got, ttl, err := ExchangeServiceAccountToken(context.Background(), &google.ServiceAccountKey{ClientEmail: "fixture@example.invalid", PrivateKeyID: "fixture-kid", PrivateKey: string(secret), TokenURI: server.URL}, "")
	require.NoError(t, err)
	require.Equal(t, "fixture-token", got)
	require.Equal(t, 55*time.Minute, ttl)
}

// TestVertexServiceAccountHTTPClientRecordsDependency sends a request with a
// timing collector through the service-account HTTP client. Closing its
// successful response leaves an HTTP dependency duration in the formatted
// timing header.
func TestVertexServiceAccountHTTPClientRecordsDependency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := NewServiceAccountHTTPClient("")
	require.NoError(t, err)
	collector := timing.New(time.Now())
	ctx := timing.WithCollector(context.Background(), collector)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	response, err := client.Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Contains(t, collector.HeaderValue(time.Now(), "bypass"), "dep_http;dur=")
}
