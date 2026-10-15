package googleauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/proxy"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

// service_account.go signs Google service-account assertions and exchanges
// them for access tokens with cache lifetimes.
// Google service accounts exchange an RSA-signed JWT assertion for an access
// token. The exchange reports a shortened cache lifetime to provider
// orchestration, while its HTTP client records dependency timing and uses the
// proxy selected by that caller.

const (
	// CloudPlatformScope selects the assertion permission, and
	// ServiceAccountCacheSkew reserves time before provider caches expire tokens.
	CloudPlatformScope      = "https://www.googleapis.com/auth/cloud-platform"
	ServiceAccountCacheSkew = 5 * time.Minute
)

// NewServiceAccountHTTPClient builds a timed token-exchange client with a
// fifteen-second request timeout. A blank proxy uses the default transport; a
// supplied proxy configures a cloned HTTP transport and returns configuration
// errors.
func NewServiceAccountHTTPClient(proxyURL string) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return timing.InstrumentClient(&http.Client{Timeout: 15 * time.Second}), nil
	}

	_, parsedProxy, err := proxy.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("unexpected default transport type %T", http.DefaultTransport)
	}
	// Clone before replacing proxy behavior so this exchange cannot rewrite the
	// process-wide default transport used by unrelated HTTP clients.
	transport := defaultTransport.Clone()
	transport.Proxy = nil
	if err := proxy.ConfigureTransportProxy(transport, parsedProxy); err != nil {
		return nil, err
	}
	return timing.InstrumentClient(&http.Client{Timeout: 15 * time.Second, Transport: transport}), nil
}

// ExchangeServiceAccountToken signs a one-hour RS256 assertion from a nonnil
// key and exchanges it at key.TokenURI. The key requires a PEM-encoded PKCS1
// or PKCS8 RSA private key; endpoint validation and cache coordination belong
// to callers.
//
// A nonblank access_token on a successful HTTP status is returned as received.
// Nonpositive expires_in uses one hour, and lifetimes exceeding five minutes
// reserve that cache skew; short lifetimes retain their full duration.
func ExchangeServiceAccountToken(ctx context.Context, key *google.ServiceAccountKey, proxyURL string) (string, time.Duration, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":   key.ClientEmail,
		"scope": CloudPlatformScope,
		"aud":   key.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if strings.TrimSpace(key.PrivateKeyID) != "" {
		token.Header["kid"] = key.PrivateKeyID
	}
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(key.PrivateKey))
	if err != nil {
		return "", 0, fmt.Errorf("parse service account private key: %w", err)
	}
	assertion, err := token.SignedString(privateKey)
	if err != nil {
		return "", 0, fmt.Errorf("sign service account assertion: %w", err)
	}

	values := url.Values{}
	values.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	values.Set("assertion", assertion)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, key.TokenURI, strings.NewReader(values.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client, err := NewServiceAccountHTTPClient(proxyURL)
	if err != nil {
		return "", 0, fmt.Errorf("configure service account token proxy: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("service account token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Retain at most one MiB for token and error decoding. Read and JSON errors
	// are discarded, so status and decoded token presence determine this result.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed google.ServiceAccountTokenResponse
	_ = json.Unmarshal(body, &parsed)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Prefer the structured description, then the error label, then the bounded
		// body text so an unrecognized failure still supplies diagnostic content.
		msg := strings.TrimSpace(parsed.ErrorDesc)
		if msg == "" {
			msg = strings.TrimSpace(parsed.Error)
		}
		if msg == "" {
			msg = string(bytes.TrimSpace(body))
		}
		return "", 0, fmt.Errorf("service account token request returned %d: %s", resp.StatusCode, msg)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return "", 0, errors.New("service account token response missing access_token")
	}
	// Convert the server lifetime to a cache duration. Missing or nonpositive
	// lifetimes select one hour before reserving the provider cache refresh
	// margin.
	ttl := time.Duration(parsed.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	// Short tokens keep their positive lifetime; subtracting the refresh margin
	// from them would produce a nonpositive cache duration.
	if ttl > ServiceAccountCacheSkew {
		ttl -= ServiceAccountCacheSkew
	}
	return parsed.AccessToken, ttl, nil
}
