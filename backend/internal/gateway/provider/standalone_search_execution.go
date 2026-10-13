package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// Standalone Grok search performs one Responses exchange using stored
// credentials and provider transport parameters. Its caller owns provider
// selection and retry policy: this executor classifies credential, transport,
// read, and selected HTTP failures into failover errors and closes every
// response body after a successful transport return.

// SearchTransport sends Grok search exchanges through provider-isolated
// upstream transport.
type SearchTransport interface {
	// Do exchanges the supplied request using proxy URL, provider ID, and concurrency.
	// Successful calls supply a nonnil response and body for Execute to read and
	// close. Transport errors return to its failover classification.
	Do(*http.Request, string, int64, int) (*http.Response, error)
}

// GrokSearchExecutor binds one-exchange transport and optional default
// endpoint resolution for search.
type GrokSearchExecutor struct {
	// Transport performs the provider-isolated exchange. Execute rejects a missing
	// transport before reading provider credentials or resolving an upstream URL.
	Transport SearchTransport
	// DefaultBaseURL is read after credentials and validator selection succeed.
	// Provider URL resolution uses it as the default while retaining configured pins.
	DefaultBaseURL func() string
}

// responsesURL selects a validated Grok Responses URL using the provider
// address and optional dynamic default. Validator selection precedes default
// lookup, and provider URL resolution retains configured address precedence
// before building the endpoint.
func (s *GrokSearchExecutor) responsesURL(value *provider.Record) (string, error) {
	validator, err := provideradapter.GrokBaseURLValidator(value, xai.ValidateBaseURL)
	if err != nil {
		return "", err
	}
	base := provideradapter.GrokProviderBaseURL(value)
	// The dynamic default is consulted only after this provider passes validator
	// selection; the eventual Responses URL still uses the selected endpoint policy.
	if s.DefaultBaseURL != nil {
		base = provideradapter.GrokProviderBaseURLOr(value, s.DefaultBaseURL())
	}
	return xai.BuildResponsesURLWithValidator(base, validator)
}

// Execute sends one Grok search request using stored credentials, provider
// headers, proxy identity, and concurrency. Successful transport returns have
// their response bodies closed, and at most four MiB are retained. Missing JSON model fields use
// the static default when patching succeeds; credential, transport, read, and
// selected HTTP failures return failover errors for the caller's loop.
func (s *GrokSearchExecutor) Execute(ctx context.Context, value *provider.Record, body []byte) ([]byte, error) {
	if s == nil || s.Transport == nil {
		return nil, errors.New("http upstream not configured")
	}
	if value == nil || !value.IsGrok() {
		return nil, errors.New("grok provider required")
	}
	token, err := provider.GrokStoredAccessToken(value)
	if err != nil {
		// Preserve the failure stage in a stable failover reason so the caller can
		// attribute this failed exchange before deciding whether another provider is eligible.
		return nil, &forwardcore.UpstreamFailoverError{
			StatusCode: http.StatusUnauthorized,
			Reason:     forwardcore.GatewayFailureReason("grok_search_token"),
		}
	}
	targetURL, err := s.responsesURL(value)
	if err != nil {
		return nil, err
	}
	// Standalone search requests can omit model. Patch valid JSON with the static
	// search default; patch errors retain the caller body for normal upstream validation.
	if json.Valid(body) && strings.TrimSpace(gjson.GetBytes(body, "model").String()) == "" {
		if patched, patchErr := sjson.SetBytes(body, "model", xai.DefaultTextModel); patchErr == nil {
			body = patched
		}
	}
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build grok responses request: %w", err)
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+token)
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "application/json")
	upstreamReq.Header.Set("User-Agent", xai.DefaultGrokUpstreamUserAgent())
	xai.ApplyCLIHeaders(upstreamReq.Header)
	provideradapter.ApplyProviderHeaderOverrides(value, upstreamReq.Header)
	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}
	resp, err := s.Transport.Do(upstreamReq, proxyURL, value.ID, value.Concurrency)
	if err != nil {
		// Preserve the failure stage in a stable failover reason so the caller can
		// attribute this failed exchange before deciding whether another provider is eligible.
		return nil, &forwardcore.UpstreamFailoverError{
			StatusCode: http.StatusBadGateway,
			Reason:     forwardcore.GatewayFailureReason("grok_search_transport"),
		}
	}
	// This successful transport result is owned by the executor. Close its body
	// on each exit so parsed success and HTTP errors release shared transport resources;
	// close errors leave the selected exchange result unchanged.
	defer func() { _ = resp.Body.Close() }()
	// Limit response bytes retained for standalone source parsing; an oversized body
	// returns its four-MiB prefix, with parsing left to the caller of this exchange.
	respBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		// A body read failure leaves no parsed search response to return. Report its
		// stable read-stage reason while the deferred close releases the successful transport result.
		return nil, &forwardcore.UpstreamFailoverError{
			StatusCode: http.StatusBadGateway,
			Reason:     forwardcore.GatewayFailureReason("grok_search_read"),
		}
	}
	if resp.StatusCode >= http.StatusBadRequest {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusPaymentRequired ||
			resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode >= http.StatusInternalServerError {
			return nil, &forwardcore.UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: respBytes}
		}
		message := string(respBytes)
		// Cap error diagnostics in bytes before constructing the returned message.
		if len(message) > 200 {
			message = message[:200]
		}
		return nil, fmt.Errorf("grok upstream %d: %s", resp.StatusCode, message)
	}
	return respBytes, nil
}
