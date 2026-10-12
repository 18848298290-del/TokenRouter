package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Gemini's test target selects four authentication routes while using one event
// sink shape. These cases exercise gemini_test_target.go with API-key, AI Studio
// OAuth, Code Assist and Vertex records, checking route paths, credentials,
// request-local User-Agent selection and the resulting event order.

// geminiTestTransportFixture retains a probe request and its consumed payload
// for assertions.
type geminiTestTransportFixture struct {
	// request retains the caller pointer after DoWithTLS consumes its body.
	// Assertions inspect headers and URL rather than reading that body again.
	request *http.Request
	body    string // consumed request JSON text
}

// geminiTestSinkFixture collects the typed events emitted by one serial probe.
type geminiTestSinkFixture struct {
	events []provider.TestEvent /* ordered probe event values */
}

// TestGeminiProviderTestNativeCredentialRoutes supplies four credential kinds to
// the same test-target API. It checks route URL and headers, prompt payload and
// event order, including an automatic UA that leaves the shared executor unchanged.
func TestGeminiProviderTestNativeCredentialRoutes(t *testing.T) {
	for _, test := range []struct {
		name    string // subtest display label
		kind    string // provider credential type
		project string // stored project selection
		path    string // expected route URL path
		bearer  string // expected bearer authorization header
		apiKey  string // expected API-key header value
	}{
		{name: "api_key", kind: provider.ProviderTypeAPIKey, path: "/v1beta/models/gemini-2.5-flash:streamGenerateContent", apiKey: "test-key"},
		{name: "oauth_studio", kind: provider.ProviderTypeOAuth, path: "/v1beta/models/gemini-2.5-flash:streamGenerateContent", bearer: "Bearer test-token"},
		{name: "oauth_codeassist", kind: provider.ProviderTypeOAuth, project: "test-project", path: "/v1internal:streamGenerateContent", bearer: "Bearer test-token"},
		{name: "service_account", kind: provider.ProviderTypeServiceAccount, project: "test-project", path: "/v1/projects/test-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:streamGenerateContent", bearer: "Bearer vertex-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := &provider.Record{ID: 7, Platform: provider.PlatformGemini, Type: test.kind, Credentials: map[string]any{"api_key": "test-key", "access_token": "test-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": test.project}}
			transport := &geminiTestTransportFixture{}
			executor := &GeminiProviderTest{Transport: transport, Tokens: &provider.GeminiTokenSource{Options: provider.GeminiTokenOptions{Vertex: func(context.Context, *provider.Record) (string, error) { return "vertex-token", nil }}}, ValidateURL: func(raw string) (string, error) { return raw, nil }}
			sink := &geminiTestSinkFixture{}
			err := executor.Target(value).Execute(t.Context(), provider.PreparedTestRequest{TestRequest: provider.TestRequest{ProviderID: value.ID, Model: "gemini-2.5-flash", Prompt: "hello", Automatic: true, UserAgent: "fixture-agent"}, TestType: provider.ProviderTestTypeText}, sink)
			require.NoError(t, err)
			require.NotNil(t, transport.request)
			require.Equal(t, test.path, transport.request.URL.Path)
			require.Equal(t, "sse", transport.request.URL.Query().Get("alt"))
			require.Equal(t, test.bearer, transport.request.Header.Get("Authorization"))
			require.Equal(t, test.apiKey, transport.request.Header.Get("x-goog-api-key"))
			require.Equal(t, "fixture-agent", transport.request.Header.Get("User-Agent"))
			require.Contains(t, transport.body, "hello")
			require.Len(t, sink.events, 3)
			require.Equal(t, []string{"test_start", "content", "test_complete"}, []string{sink.events[0].Type, sink.events[1].Type, sink.events[2].Type})
			require.Equal(t, "", executor.UserAgent)
		})
	}
}

// DoWithTLS consumes and saves the probe body before returning a fixed Gemini SSE.
// Read failures are returned to the caller. The retained request pointer supports
// header assertions, and the caller owns the response body.
func (f *geminiTestTransportFixture) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	f.request = req
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	f.body = string(body)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n\ndata: [DONE]\n\n"))}, nil
}

// Begin accepts test-run startup with no additional fixture state or output.
func (*geminiTestSinkFixture) Begin(context.Context, bool) error { return nil }

// Emit appends the supplied event value for the test's ordered event assertions.
func (f *geminiTestSinkFixture) Emit(_ context.Context, event provider.TestEvent) error {
	f.events = append(f.events, event)
	return nil
}
