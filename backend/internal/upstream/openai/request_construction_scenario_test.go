package openai

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// request_construction_scenario_test.go compares authenticated construction in
// request_headers.go, images_request.go, and alpha_search_headers.go. The same
// authentication values exercise each caller's header ordering, wire output,
// and early failures before the endpoint-specific callbacks can run.

// requestConstructionBuilder describes one actual request entry and its expected policy.
type requestConstructionBuilder struct {
	name        string   // named request entry
	steps       []string // ordered header mutations
	contentType string   // content before overrides
	accept      string   // endpoint response format
	host        string   // outgoing host property
	session     string   // session before overrides

	// build invokes the production request entry under examination.
	build func(context.Context, []byte, AlphaSearchRequestOptions) (*http.Request, error)
}

// TestAuthenticatedRequestBuilders_HeadersAndContext supplies shared authentication
// values to all four request entries. It checks wire output, callback order,
// transport context, and independent ownership of copied header values.
func TestAuthenticatedRequestBuilders_HeadersAndContext(t *testing.T) {
	for _, builder := range requestConstructionBuilders() {
		t.Run(builder.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(upstream.WithHTTPUpstreamProfile(t.Context(), upstream.HTTPUpstreamProfileGrok))
			defer cancel()
			body := []byte(`{"model":"fixture-model","input":"hello"}`)
			authHeaders := http.Header{
				"Authorization":      {"Bearer fixture"},
				"X-Order":            {"auth-first", "auth-second"},
				"X-Multi":            {"first", "second"},
				"X-Nil":              nil,
				"X-Empty":            {},
				"Content-Type":       {"auth/type"},
				"Session_id":         {"auth-session"},
				"Conversation_id":    {"auth-conversation"},
				"Openai-Beta":        {"auth-beta"},
				"X-Codex-Turn-State": {"auth-turn"},
				"X-Responses-Lite":   {"auth-lite"},
			}
			authSnapshot := authHeaders.Clone()
			authCalls := 0
			options := AlphaSearchRequestOptions{
				ResponsesRequestOptions: ResponsesRequestOptions{
					URL: "https://upstream.example/v1/endpoint?shared=base",
					Authenticate: func(got context.Context) (http.Header, error) {
						authCalls++
						require.Same(t, ctx, got)
						require.Equal(t, upstream.HTTPUpstreamProfileGrok, upstream.HTTPUpstreamProfileFromContext(got))
						return authHeaders, nil
					},
					ProviderHeaders: func(got context.Context, headers http.Header) error {
						require.Same(t, ctx, got)
						headers.Add("X-Order", "provider")
						headers.Set("Chatgpt-Account-Id", "fixture-account")
						return nil
					},
					ForwardHeaders: func() http.Header {
						return http.Header{"X-Order": {"forwarded"}, "Content-Type": {"client/type"}, "X-Blocked": {"blocked"}}
					},
					AllowHeader: func(key string) bool {
						return key == "x-order" || key == "content-type"
					},
					UsesCodex:     func() bool { return true },
					IsCompact:     func() bool { return false },
					ForceCodexCLI: func() bool { return false },
					GuardTurnState: func(headers http.Header) {
						headers.Add("X-Order", "guard")
						headers.Del("X-Codex-Turn-State")
					},
					MessagesBridge: func([]byte) bool { return false },
					Originator:     func() string { return CodexDefaultOriginator },
					APIKeyID:       func() int64 { return 17 },
					IsolateSession: func(keyID int64, session string) string {
						return fmt.Sprintf("isolated:%d:%s", keyID, session)
					},
					ApplyUserAgent: func(req *http.Request) {
						require.Equal(t, builder.host, req.Host)
						require.Equal(t, upstream.HTTPUpstreamProfileOpenAI, upstream.HTTPUpstreamProfileFromContext(req.Context()))
						req.Header.Add("X-Order", "user-agent")
						req.Header.Set("User-Agent", CodexCanonicalUserAgent())
					},
					ApplyProviderIdentity: func(headers http.Header) {
						headers.Add("X-Order", "identity")
					},
					ApplyFingerprint: func(headers http.Header) {
						require.Equal(t, "isolated:17:prompt-id", headers.Get("Session_id"))
						headers.Add("X-Order", "fingerprint")
						headers.Set("Session_id", "fingerprint-session")
					},
					OverrideHeaders: func(headers http.Header) {
						require.Equal(t, builder.contentType, headers.Get("Content-Type"))
						require.Equal(t, builder.accept, headers.Get("Accept"))
						require.Equal(t, builder.session, headers.Get("Session_id"))
						headers.Add("X-Order", "overrides")
						headers.Set("Content-Type", "override/type")
						headers.Set("X-Codex-Turn-State", "override-turn")
					},
					OpenCodeSession: func(headers http.Header) { headers.Add("X-Order", "opencode") },
					BetaFeatures:    func(headers http.Header) { headers.Add("X-Order", "beta") },
					RoutingHint: func(headers http.Header, gotBody []byte) {
						require.Equal(t, body, gotBody)
						headers.Add("X-Order", "routing")
					},
					Diagnostics: func(headers http.Header, gotBody []byte) {
						require.Equal(t, body, gotBody)
						headers.Add("X-Order", "diagnostics")
					},
				},
				Query: func() url.Values { return url.Values{"shared": {"incoming-first", "incoming-second"}} },
				OAuth: func() bool { return true },
				InboundHeader: func(key string) string {
					if key == "Version" {
						return CodexCanonicalClientVersion()
					}
					return "fixture-metadata"
				},
				IdentityWithKey: func(headers http.Header, keyID int64) {
					require.Equal(t, int64(17), keyID)
					headers.Add("X-Order", "identity-key")
				},
				ResponsesLiteHeader: "X-Responses-Lite",
			}

			req, err := builder.build(ctx, body, options)
			require.NoError(t, err)
			require.Equal(t, 1, authCalls)
			require.Equal(t, builder.steps, req.Header.Values("X-Order"))
			require.Equal(t, authSnapshot, authHeaders)
			require.NotContains(t, req.Header, "X-Nil")
			require.NotContains(t, req.Header, "X-Empty")
			require.NotContains(t, req.Header, "X-Blocked")
			require.Equal(t, int64(len(body)), req.ContentLength)
			require.Equal(t, upstream.HTTPUpstreamProfileOpenAI, upstream.HTTPUpstreamProfileFromContext(req.Context()))

			if builder.name == "alpha-search" {
				require.Equal(t, []string{"base", "incoming-first", "incoming-second"}, req.URL.Query()["shared"])
				for _, key := range []string{"OpenAI-Beta", "Session_id", "Conversation_id", "X-Codex-Turn-State", "X-Responses-Lite"} {
					require.Empty(t, req.Header.Get(key))
				}
			} else {
				require.Equal(t, "shared=base", req.URL.RawQuery)
				require.Equal(t, "override-turn", req.Header.Get("X-Codex-Turn-State"))
			}
			if builder.name == "pat-responses" {
				require.Equal(t, "responses=experimental", req.Header.Get("OpenAI-Beta"))
				require.Equal(t, "isolated:17:search-id", req.Header.Get("Conversation_id"))
			}

			replay, err := req.GetBody()
			require.NoError(t, err)
			replayedBody, err := io.ReadAll(replay)
			require.NoError(t, err)
			require.NoError(t, replay.Close())
			require.Equal(t, body, replayedBody)
			var wire bytes.Buffer
			require.NoError(t, req.Write(&wire))
			written, err := http.ReadRequest(bufio.NewReader(&wire))
			require.NoError(t, err)
			require.Equal(t, http.MethodPost, written.Method)
			require.Equal(t, builder.host, written.Host)
			require.Equal(t, req.URL.RequestURI(), written.RequestURI)
			require.Equal(t, "override/type", written.Header.Get("Content-Type"))
			require.Equal(t, "Bearer fixture", written.Header.Get("Authorization"))
			require.Equal(t, []string{"first", "second"}, written.Header.Values("X-Multi"))
			writtenBody, err := io.ReadAll(written.Body)
			require.NoError(t, err)
			require.NoError(t, written.Body.Close())
			require.Equal(t, body, writtenBody)

			authHeaders["X-Multi"][0] = "changed-source"
			require.Equal(t, []string{"first", "second"}, req.Header.Values("X-Multi"))
			req.Header["X-Multi"][1] = "changed-request"
			require.Equal(t, []string{"changed-source", "second"}, authHeaders.Values("X-Multi"))
			cancel()
			require.ErrorIs(t, req.Context().Err(), context.Canceled)
		})
	}
}

// TestAuthenticatedRequestBuilders_FailureOrder combines malformed URLs and
// authentication failures at each entry. Request construction must fail before
// authentication, and callback failures retain their cause and existing wrapper.
func TestAuthenticatedRequestBuilders_FailureOrder(t *testing.T) {
	for _, builder := range requestConstructionBuilders() {
		for _, failure := range []string{"invalid-url", "nil-context", "authentication"} {
			t.Run(builder.name+"/"+failure, func(t *testing.T) {
				ctx := t.Context()
				authCalls := 0
				queryCalls := 0
				authErr := errors.New("fixture authentication failure")
				options := AlphaSearchRequestOptions{
					ResponsesRequestOptions: ResponsesRequestOptions{
						URL: "https://upstream.example/v1/endpoint",
						Authenticate: func(got context.Context) (http.Header, error) {
							authCalls++
							require.Same(t, ctx, got)
							return nil, authErr
						},
					},
					Query: func() url.Values {
						queryCalls++
						return nil
					},
				}
				switch failure {
				case "invalid-url":
					options.URL = "http://[::1"
				case "nil-context":
					ctx = nil
				}

				req, err := builder.build(ctx, nil, options)
				require.Nil(t, req)
				require.Error(t, err)
				switch failure {
				case "invalid-url":
					var parseErr *url.Error
					require.ErrorAs(t, err, &parseErr)
					require.NotErrorIs(t, err, authErr)
					require.Zero(t, authCalls)
					require.Zero(t, queryCalls)
					if builder.name == "alpha-search" {
						require.EqualError(t, err, "parse alpha search URL: "+parseErr.Error())
					} else {
						require.Same(t, parseErr, err)
					}
				case "nil-context":
					require.EqualError(t, err, "net/http: nil Context")
					require.Zero(t, authCalls)
				case "authentication":
					require.Equal(t, 1, authCalls)
					require.ErrorIs(t, err, authErr)
					require.EqualError(t, err, "build openai authentication headers: "+authErr.Error())
				}
				if failure != "invalid-url" && builder.name == "alpha-search" {
					require.Equal(t, 1, queryCalls)
				}
			})
		}
	}
}

// TestAuthenticatedRequestBuilders_EmptyAuthenticationHeaders supplies nil and
// empty authentication maps with empty bodies. Each entry still produces a
// replayable POST and invokes authentication once with the supplied context.
func TestAuthenticatedRequestBuilders_EmptyAuthenticationHeaders(t *testing.T) {
	for _, builder := range requestConstructionBuilders() {
		for index, authHeaders := range []http.Header{nil, {}} {
			t.Run(fmt.Sprintf("%s/%d", builder.name, index), func(t *testing.T) {
				ctx := t.Context()
				authCalls := 0
				options := AlphaSearchRequestOptions{
					ResponsesRequestOptions: ResponsesRequestOptions{
						URL: "https://upstream.example/v1/endpoint",
						Authenticate: func(got context.Context) (http.Header, error) {
							authCalls++
							require.Same(t, ctx, got)
							return authHeaders, nil
						},
						ProviderHeaders:       func(context.Context, http.Header) error { return nil },
						ForwardHeaders:        func() http.Header { return nil },
						UsesCodex:             func() bool { return false },
						IsCompact:             func() bool { return false },
						ForceCodexCLI:         func() bool { return false },
						GuardTurnState:        func(http.Header) {},
						Originator:            func() string { return CodexDefaultOriginator },
						APIKeyID:              func() int64 { return 17 },
						IsolateSession:        func(_ int64, session string) string { return session },
						ApplyUserAgent:        func(*http.Request) {},
						ApplyProviderIdentity: func(http.Header) {},
						ApplyFingerprint:      func(http.Header) {},
						OverrideHeaders:       func(http.Header) {},
						OpenCodeSession:       func(http.Header) {},
						BetaFeatures:          func(http.Header) {},
						RoutingHint:           func(http.Header, []byte) {},
						Diagnostics:           func(http.Header, []byte) {},
					},
					Query:               func() url.Values { return nil },
					OAuth:               func() bool { return false },
					InboundHeader:       func(string) string { return "" },
					IdentityWithKey:     func(http.Header, int64) {},
					ResponsesLiteHeader: "X-Responses-Lite",
				}

				req, err := builder.build(ctx, nil, options)
				require.NoError(t, err)
				require.Equal(t, 1, authCalls)
				require.Equal(t, http.MethodPost, req.Method)
				require.Equal(t, int64(0), req.ContentLength)
				require.Equal(t, http.NoBody, req.Body)
				require.NotNil(t, req.Header)
				require.Equal(t, upstream.HTTPUpstreamProfileOpenAI, upstream.HTTPUpstreamProfileFromContext(req.Context()))
				replay, err := req.GetBody()
				require.NoError(t, err)
				require.Equal(t, http.NoBody, replay)
			})
		}
	}
}

// requestConstructionBuilders binds the four entries to their expected header
// phases. Each closure invokes the production builder with its own body and
// content-type arguments so the shared assertions observe caller behavior.
func requestConstructionBuilders() []requestConstructionBuilder {
	return []requestConstructionBuilder{
		{
			name: "responses",
			build: func(ctx context.Context, body []byte, options AlphaSearchRequestOptions) (*http.Request, error) {
				return BuildResponsesRequest(ctx, body, "prompt-id", options.ResponsesRequestOptions)
			},
			steps:       []string{"auth-first", "auth-second", "provider", "forwarded", "guard", "user-agent", "identity", "fingerprint", "overrides", "opencode", "beta", "routing", "diagnostics"},
			contentType: "auth/type",
			accept:      "text/event-stream",
			host:        "chatgpt.com",
			session:     "fingerprint-session",
		},
		{
			name: "images",
			build: func(ctx context.Context, body []byte, options AlphaSearchRequestOptions) (*http.Request, error) {
				return BuildImagesRequest(ctx, body, "multipart/form-data; boundary=fixture", options.ResponsesRequestOptions)
			},
			steps:       []string{"auth-first", "auth-second", "forwarded", "user-agent", "overrides"},
			contentType: "multipart/form-data; boundary=fixture",
			host:        "upstream.example",
			session:     "auth-session",
		},
		{
			name: "alpha-search",
			build: func(ctx context.Context, body []byte, options AlphaSearchRequestOptions) (*http.Request, error) {
				return BuildAlphaSearchRequest(ctx, body, options)
			},
			steps:       []string{"auth-first", "auth-second", "provider", "identity", "user-agent", "overrides"},
			contentType: "application/json",
			accept:      "application/json",
			host:        "chatgpt.com",
			session:     "auth-session",
		},
		{
			name: "pat-responses",
			build: func(ctx context.Context, body []byte, options AlphaSearchRequestOptions) (*http.Request, error) {
				return BuildAlphaSearchResponsesRequest(ctx, []byte(`{"id":" search-id "}`), body, options)
			},
			steps:       []string{"auth-first", "auth-second", "provider", "user-agent", "identity-key", "overrides"},
			contentType: "application/json",
			accept:      "text/event-stream",
			host:        "chatgpt.com",
			session:     "isolated:17:search-id",
		},
	}
}
