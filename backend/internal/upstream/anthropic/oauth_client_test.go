package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic/oauth"
)

// ClaudeOAuthServiceSuite exercises OAuth HTTP payloads using injected
// synchronous transports.
type ClaudeOAuthServiceSuite struct {
	suite.Suite              // assertion and subtest support
	client      *OAuthClient // injected OAuth HTTP client
}

// requestCapture records the OAuth request inspected by its test assertions.
type requestCapture struct {
	path        string         // observed URL path
	method      string         // observed HTTP method
	cookies     []*http.Cookie // observed request cookies
	body        []byte         // raw request bytes
	bodyJSON    map[string]any // decoded request fields
	contentType string         // observed content type
}

// roundTripFunc adapts a request callback to the HTTP transport interface.
type roundTripFunc func(*http.Request) (*http.Response, error)

// newTestReqClient installs the supplied RoundTripper on a req client for
// synchronous fixtures. Transport behavior remains with rt, allowing handlers
// to inspect payloads without opening a network listener.
func newTestReqClient(rt http.RoundTripper) *req.Client {
	c := req.C()
	c.GetClient().Transport = rt
	return c
}

// TestGetOrganizationUUID injects synchronous success, unauthorized, and
// malformed-response handlers for cookie organization lookup. It checks
// returned UUID or errors and verifies the request path and supplied session
// cookie.
func (s *ClaudeOAuthServiceSuite) TestGetOrganizationUUID() {
	tests := []struct {
		name       string                        // case display name
		handler    http.HandlerFunc              // fixture response handler
		wantErr    bool                          // expected error presence
		errContain string                        // required error substring
		wantUUID   string                        // expected organization identifier
		validate   func(captured requestCapture) // captured request assertions
	}{
		{
			name: "success",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[{"uuid":"org-1"}]`))
			},
			wantUUID: "org-1",
			validate: func(captured requestCapture) {
				require.Equal(s.T(), "/api/organizations", captured.path, "unexpected path")
				require.Len(s.T(), captured.cookies, 1, "expected 1 cookie")
				require.Equal(s.T(), "sessionKey", captured.cookies[0].Name)
				require.Equal(s.T(), "sess", captured.cookies[0].Value)
			},
		},
		{
			name: "non_200_returns_error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("unauthorized"))
			},
			wantErr:    true,
			errContain: "401",
		},
		{
			name: "invalid_json_returns_error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("not-json"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			var captured requestCapture

			rt := newInProcessTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured.path = r.URL.Path
				captured.cookies = r.Cookies()
				tt.handler(w, r)
			}), nil)

			client := NewOAuthClient()
			s.client = client
			s.client.BaseURL = "http://in-process"
			s.client.ClientFactory = func(string) (*req.Client, error) { return newTestReqClient(rt), nil }

			got, err := s.client.GetOrganizationUUID(context.Background(), "sess", "")

			if tt.wantErr {
				require.Error(s.T(), err)
				if tt.errContain != "" {
					require.ErrorContains(s.T(), err, tt.errContain)
				}
				return
			}

			require.NoError(s.T(), err)
			require.Equal(s.T(), tt.wantUUID, got)
			if tt.validate != nil {
				tt.validate(captured)
			}
		})
	}
}

// TestGetAuthorizationCode injects redirect responses with and without an
// authorization code. It checks joined code/state output, missing-code failure,
// and captured organization, client, callback, cookie, and request state
// values.
func (s *ClaudeOAuthServiceSuite) TestGetAuthorizationCode() {
	tests := []struct {
		name     string                        // case display name
		handler  http.HandlerFunc              // fixture response handler
		wantErr  bool                          // expected error presence
		wantCode string                        // expected joined proof
		validate func(captured requestCapture) // captured request assertions
	}{
		{
			name: "parses_redirect_uri",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{
					"redirect_uri": oauth.RedirectURI + "?code=AUTH&state=STATE",
				})
			},
			wantCode: "AUTH#STATE",
			validate: func(captured requestCapture) {
				require.True(s.T(), strings.HasPrefix(captured.path, "/v1/oauth/") && strings.HasSuffix(captured.path, "/authorize"), "unexpected path: %s", captured.path)
				require.Equal(s.T(), http.MethodPost, captured.method, "expected POST")
				require.Len(s.T(), captured.cookies, 1, "expected 1 cookie")
				require.Equal(s.T(), "sess", captured.cookies[0].Value)
				require.Equal(s.T(), "org-1", captured.bodyJSON["organization_uuid"])
				require.Equal(s.T(), oauth.ClientID, captured.bodyJSON["client_id"])
				require.Equal(s.T(), oauth.RedirectURI, captured.bodyJSON["redirect_uri"])
				require.Equal(s.T(), "st", captured.bodyJSON["state"])
			},
		},
		{
			name: "missing_code_returns_error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{
					"redirect_uri": oauth.RedirectURI + "?state=STATE", // no code
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			var captured requestCapture

			rt := newInProcessTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured.path = r.URL.Path
				captured.method = r.Method
				captured.cookies = r.Cookies()
				captured.body, _ = io.ReadAll(r.Body)
				_ = json.Unmarshal(captured.body, &captured.bodyJSON)
				tt.handler(w, r)
			}), nil)

			client := NewOAuthClient()
			s.client = client
			s.client.BaseURL = "http://in-process"
			s.client.ClientFactory = func(string) (*req.Client, error) { return newTestReqClient(rt), nil }

			code, err := s.client.GetAuthorizationCode(context.Background(), "sess", "org-1", oauth.ScopeInference, "cc", "st", "")

			if tt.wantErr {
				require.Error(s.T(), err)
				return
			}

			require.NoError(s.T(), err)
			require.Equal(s.T(), tt.wantCode, code)
			if tt.validate != nil {
				tt.validate(captured)
			}
		})
	}
}

// TestExchangeCodeForToken captures regular and setup-flag exchange payloads
// using local transports. Embedded state reaches JSON, both supplied flag
// values omit expires_in, selected token fields are returned, and an
// unsuccessful status returns an error.
func (s *ClaudeOAuthServiceSuite) TestExchangeCodeForToken() {
	tests := []struct {
		name         string                        // case display name
		handler      http.HandlerFunc              // fixture response handler
		code         string                        // authorization code input
		isSetupToken bool                          // supplied setup-token flag
		wantErr      bool                          // expected error presence
		wantResp     *oauth.TokenResponse          // expected token fields
		validate     func(captured requestCapture) // captured request assertions
	}{
		{
			name: "sends_state_when_embedded",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(oauth.TokenResponse{
					AccessToken:  "at",
					TokenType:    "bearer",
					ExpiresIn:    3600,
					RefreshToken: "rt",
					Scope:        "s",
				})
			},
			code:         "AUTH#STATE2",
			isSetupToken: false,
			wantResp: &oauth.TokenResponse{
				AccessToken:  "at",
				RefreshToken: "rt",
			},
			validate: func(captured requestCapture) {
				require.Equal(s.T(), http.MethodPost, captured.method, "expected POST")
				require.True(s.T(), strings.HasPrefix(captured.contentType, "application/json"), "unexpected content-type")
				require.Equal(s.T(), "AUTH", captured.bodyJSON["code"])
				require.Equal(s.T(), "STATE2", captured.bodyJSON["state"])
				require.Equal(s.T(), oauth.ClientID, captured.bodyJSON["client_id"])
				require.Equal(s.T(), oauth.RedirectURI, captured.bodyJSON["redirect_uri"])
				require.Equal(s.T(), "ver", captured.bodyJSON["code_verifier"])
				// Regular OAuth omits expires_in.
				require.Nil(s.T(), captured.bodyJSON["expires_in"], "regular OAuth should not include expires_in")
			},
		},
		{
			name: "setup_token_omits_expires_in",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(oauth.TokenResponse{
					AccessToken: "at",
					TokenType:   "bearer",
					ExpiresIn:   31536000,
				})
			},
			code:         "AUTH",
			isSetupToken: true,
			wantResp: &oauth.TokenResponse{
				AccessToken: "at",
			},
			validate: func(captured requestCapture) {
				require.Nil(s.T(), captured.bodyJSON["expires_in"], "setup token should not include expires_in")
			},
		},
		{
			name: "non_200_returns_error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("bad request"))
			},
			code:         "AUTH",
			isSetupToken: false,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			var captured requestCapture

			rt := newInProcessTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured.method = r.Method
				captured.contentType = r.Header.Get("Content-Type")
				captured.body, _ = io.ReadAll(r.Body)
				_ = json.Unmarshal(captured.body, &captured.bodyJSON)
				tt.handler(w, r)
			}), nil)

			client := NewOAuthClient()
			s.client = client
			s.client.TokenURL = "http://in-process/token"
			s.client.ClientFactory = func(string) (*req.Client, error) { return newTestReqClient(rt), nil }

			resp, err := s.client.ExchangeCodeForToken(context.Background(), tt.code, "ver", "", "", tt.isSetupToken)

			if tt.wantErr {
				require.Error(s.T(), err)
				return
			}

			require.NoError(s.T(), err)
			require.Equal(s.T(), tt.wantResp.AccessToken, resp.AccessToken)
			require.Equal(s.T(), tt.wantResp.RefreshToken, resp.RefreshToken)
			if tt.validate != nil {
				tt.validate(captured)
			}
		})
	}
}

// TestRefreshToken captures refresh grant requests and returns ordinary,
// rotated, or unauthorized responses. It checks JSON proof fields, returned
// access/refresh values, and the unsuccessful response's error path.
func (s *ClaudeOAuthServiceSuite) TestRefreshToken() {
	tests := []struct {
		name     string                        // case display name
		handler  http.HandlerFunc              // fixture response handler
		wantErr  bool                          // expected error presence
		wantResp *oauth.TokenResponse          // expected token fields
		validate func(captured requestCapture) // captured request assertions
	}{
		{
			name: "sends_json_format",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(oauth.TokenResponse{
					AccessToken:  "new_access_token",
					TokenType:    "bearer",
					ExpiresIn:    28800,
					RefreshToken: "new_refresh_token",
					Scope:        "user:profile user:inference",
				})
			},
			wantResp: &oauth.TokenResponse{
				AccessToken:  "new_access_token",
				RefreshToken: "new_refresh_token",
			},
			validate: func(captured requestCapture) {
				require.Equal(s.T(), http.MethodPost, captured.method, "expected POST")
				require.True(s.T(), strings.HasPrefix(captured.contentType, "application/json"),
					"expected JSON content-type, got: %s", captured.contentType)
				require.Equal(s.T(), "refresh_token", captured.bodyJSON["grant_type"])
				require.Equal(s.T(), "rt", captured.bodyJSON["refresh_token"])
				require.Equal(s.T(), oauth.ClientID, captured.bodyJSON["client_id"])
			},
		},
		{
			name: "returns_new_refresh_token",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(oauth.TokenResponse{
					AccessToken:  "at",
					TokenType:    "bearer",
					ExpiresIn:    28800,
					RefreshToken: "rotated_rt", // fixture returns a rotated token
				})
			},
			wantResp: &oauth.TokenResponse{
				AccessToken:  "at",
				RefreshToken: "rotated_rt",
			},
		},
		{
			name: "non_200_returns_error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			var captured requestCapture

			rt := newInProcessTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured.method = r.Method
				captured.contentType = r.Header.Get("Content-Type")
				captured.body, _ = io.ReadAll(r.Body)
				_ = json.Unmarshal(captured.body, &captured.bodyJSON)
				tt.handler(w, r)
			}), nil)

			client := NewOAuthClient()
			s.client = client
			s.client.TokenURL = "http://in-process/token"
			s.client.ClientFactory = func(string) (*req.Client, error) { return newTestReqClient(rt), nil }

			resp, err := s.client.RefreshToken(context.Background(), "rt", "")

			if tt.wantErr {
				require.Error(s.T(), err)
				return
			}

			require.NoError(s.T(), err)
			require.Equal(s.T(), tt.wantResp.AccessToken, resp.AccessToken)
			require.Equal(s.T(), tt.wantResp.RefreshToken, resp.RefreshToken)
			if tt.validate != nil {
				tt.validate(captured)
			}
		})
	}
}

// TestClaudeOAuthServiceSuite runs the OAuth client suite against injected
// in-process HTTP transports. The suite assertions cover organization lookup,
// redirect extraction, code exchange, and refresh payload/result handling.
func TestClaudeOAuthServiceSuite(t *testing.T) {
	suite.Run(t, new(ClaudeOAuthServiceSuite))
}

// RoundTrip invokes f to produce the request's response or error.
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newInProcessTransport runs handler synchronously and optionally captures
// request bytes. It reads and closes an existing body, restores a readable
// copy, and returns the recorder response for the req client to consume.
// Read and close errors are discarded; a failed read can leave callbacks with
// partial bytes while the synthetic transport still returns no error.
func newInProcessTransport(handler http.HandlerFunc, capture func(r *http.Request, body []byte)) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body []byte
		if r.Body != nil {
			// Capture whatever bytes the read produced, then close the original body
			// before installing a copy. Both callbacks inspect that captured data even
			// when a read or close error was discarded.
			body, _ = io.ReadAll(r.Body)
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if capture != nil {
			capture(r, body)
		}

		rec := httptest.NewRecorder()
		handler(rec, r)
		return rec.Result(), nil
	})
}
