package usageclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usagecontract"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// usageResponseBody counts response-body closes.
type usageResponseBody struct {
	io.Reader
	closed int
}

// Usage endpoints share forwarding rules for already-versioned base URLs.
func TestUpstreamUsageEndpointUsesExistingVersionedBaseURLRules(t *testing.T) {
	tests := map[string]string{
		"https://gateway.example/v1":     "https://gateway.example/v1/usage",
		"https://gateway.example/v4":     "https://gateway.example/v4/usage",
		"https://gateway.example/v1beta": "https://gateway.example/v1beta/usage",
		"https://gateway.example/api":    "https://gateway.example/api/v1/usage",
	}
	for base, want := range tests {
		got, err := UpstreamUsageEndpoint(base, "/v1/usage", httpclient.BuildOpenAIEndpointURL)
		require.NoError(t, err, base)
		require.Equal(t, want, got, base)
	}

	statusTests := map[string]string{
		"https://gateway.example/v1":         "https://gateway.example/api/status",
		"https://gateway.example/subpath/v1": "https://gateway.example/subpath/api/status",
		"https://gateway.example/v4":         "https://gateway.example/api/status",
	}
	for base, want := range statusTests {
		got, err := UpstreamUsageStatusEndpoint(base)
		require.NoError(t, err, base)
		require.Equal(t, want, got, base)
	}
	tokenEndpoint, err := UpstreamUsageRootEndpoint("https://gateway.example/v1", "/api/usage/token")
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example/api/usage/token", tokenEndpoint)
	tokenEndpoint, err = UpstreamUsageTokenEndpoint("https://gateway.example/v1")
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example/api/usage/token/", tokenEndpoint)
	walletEndpoint, err := UpstreamUsageWalletEndpoint("https://gateway.example/v1")
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example/user/balance", walletEndpoint)
	selfEndpoint, err := UpstreamUsageUserSelfEndpoint("https://gateway.example/v1")
	require.NoError(t, err)
	require.Equal(t, "https://gateway.example/api/user/self", selfEndpoint)
}

func (b *usageResponseBody) Close() error {
	b.closed++
	return nil
}

func TestUsageClientResponseLimitsAndClose(t *testing.T) {
	entries := []string{"headers", "bearer"}
	cases := []struct {
		name           string
		size           int
		readError      bool
		nilResponse    bool
		nilBody        bool
		doError        error
		expiredContext bool
		wantError      error
	}{
		{name: "empty"},
		{name: "at limit", size: MaxBodyBytes},
		{name: "over limit", size: MaxBodyBytes + 1, wantError: usageview.ErrUpstreamUsageInvalidResponse},
		{name: "read failure", readError: true, wantError: usageview.ErrUpstreamUsageRequestFailed},
		{name: "nil response", nilResponse: true, wantError: usageview.ErrUpstreamUsageInvalidResponse},
		{name: "nil body", nilBody: true, wantError: usageview.ErrUpstreamUsageInvalidResponse},
		{name: "request failure", doError: io.ErrUnexpectedEOF, wantError: usageview.ErrUpstreamUsageRequestFailed},
		{name: "original context deadline", doError: io.ErrUnexpectedEOF, expiredContext: true, wantError: usageview.ErrUpstreamUsageTimeout},
	}
	for _, entry := range entries {
		for _, tc := range cases {
			t.Run(entry+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				if tc.expiredContext {
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer cancel()
				}
				body := &usageResponseBody{Reader: strings.NewReader(strings.Repeat("x", tc.size))}
				if tc.readError {
					body.Reader = iotest.ErrReader(io.ErrUnexpectedEOF)
				}
				calls := 0
				client := New(&usagecontract.Request{
					Context: func(context.Context) context.Context { return context.Background() },
					ApplyHeaders: func(h http.Header) {
						h.Set("Authorization", "ambient")
						h.Set("api-key", "ambient")
					},
					Do: func(req *http.Request) (*http.Response, error) {
						calls++
						require.Equal(t, "application/json", req.Header.Get("Accept"))
						if entry == "headers" {
							require.Equal(t, "Fixed", req.Header.Get("Authorization"))
							require.Empty(t, req.Header.Get("api-key"))
						} else {
							require.Equal(t, "Bearer token", req.Header.Get("Authorization"))
							require.Equal(t, "7", req.Header.Get("New-Api-User"))
							require.Equal(t, "ambient", req.Header.Get("api-key"))
						}
						if tc.doError != nil || tc.nilResponse {
							return nil, tc.doError
						}
						resp := &http.Response{StatusCode: http.StatusCreated}
						if !tc.nilBody {
							resp.Body = body
						}
						return resp, nil
					},
				})
				var payload []byte
				var status int
				var err error
				if entry == "headers" {
					payload, status, err = client.GetURLWithHeaders(ctx, "https://example.test/usage", map[string]string{"Authorization": "Fixed"})
				} else {
					payload, status, err = client.GetURLWithBearer(ctx, "https://example.test/usage", " token ", " 7 ")
				}
				require.Equal(t, 1, calls)
				if tc.wantError != nil {
					require.ErrorIs(t, err, tc.wantError)
					require.Nil(t, payload)
					require.Zero(t, status)
				} else {
					require.NoError(t, err)
					require.Len(t, payload, tc.size)
					require.Equal(t, http.StatusCreated, status)
				}
				if tc.doError != nil || tc.nilResponse || tc.nilBody {
					require.Zero(t, body.closed)
				} else {
					require.Equal(t, 1, body.closed)
				}
			})
		}
	}
}
