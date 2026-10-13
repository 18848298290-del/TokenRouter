package vertex

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

// TestBuildVertexGeminiURL builds a streamed Gemini request in a regional
// project. It checks the exact Google publisher resource and SSE query so the
// action and transport format remain together.
func TestBuildVertexGeminiURL(t *testing.T) {
	got, err := BuildVertexGeminiURL("my-project", "us-central1", "gemini-3-pro", "streamGenerateContent", true)
	require.NoError(t, err)
	require.Equal(t, "https://us-central1-aiplatform.googleapis.com/v1/projects/my-project/locations/us-central1/publishers/google/models/gemini-3-pro:streamGenerateContent?alt=sse", got)
}

// TestBuildVertexGeminiURLUsesGlobalEndpointHost requests streaming generation
// in the global location. It checks the common API hostname while retaining
// global in the location resource and the SSE query.
func TestBuildVertexGeminiURLUsesGlobalEndpointHost(t *testing.T) {
	got, err := BuildVertexGeminiURL("my-project", "global", "gemini-3-flash-preview", "streamGenerateContent", true)
	require.NoError(t, err)
	require.Equal(t, "https://aiplatform.googleapis.com/v1/projects/my-project/locations/global/publishers/google/models/gemini-3-flash-preview:streamGenerateContent?alt=sse", got)
}

// TestBuildVertexAnthropicURL builds a regional Claude prediction URL with an
// at-sign date. It checks the exact Anthropic publisher path and nonstreaming
// action, including the literal model separator.
func TestBuildVertexAnthropicURL(t *testing.T) {
	got, err := BuildVertexAnthropicURL("my-project", "us-east5", "claude-sonnet-4-5@20250929", false)
	require.NoError(t, err)
	require.Equal(t, "https://us-east5-aiplatform.googleapis.com/v1/projects/my-project/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-5@20250929:rawPredict", got)
}

// TestBuildVertexAnthropicURLUsesGlobalEndpointHost builds a global streaming
// Claude prediction URL. It checks the common API hostname, global resource
// location, dated model identity, and streamRawPredict action together.
func TestBuildVertexAnthropicURLUsesGlobalEndpointHost(t *testing.T) {
	got, err := BuildVertexAnthropicURL("my-project", "global", "claude-haiku-4-5@20251001", true)
	require.NoError(t, err)
	require.Equal(t, "https://aiplatform.googleapis.com/v1/projects/my-project/locations/global/publishers/anthropic/models/claude-haiku-4-5@20251001:streamRawPredict", got)
}

// TestNormalizeVertexAnthropicModelID supplies hyphen-dated, at-sign-dated,
// and undated Claude names. It checks date separator conversion, retention of
// existing date syntax, and passthrough of the undated name.
func TestNormalizeVertexAnthropicModelID(t *testing.T) {
	require.Equal(t, "claude-sonnet-4-5@20250929", NormalizeVertexAnthropicModelID("claude-sonnet-4-5-20250929"))
	require.Equal(t, "claude-sonnet-4-5@20250929", NormalizeVertexAnthropicModelID("claude-sonnet-4-5@20250929"))
	require.Equal(t, "claude-sonnet-4-6", NormalizeVertexAnthropicModelID("claude-sonnet-4-6"))
}

// TestBuildVertexAnthropicRequestBody converts a Claude request containing its
// source model and version. It checks removal of model, insertion of
// AnthropicVersion, and retention of max_tokens and user text.
func TestBuildVertexAnthropicRequestBody(t *testing.T) {
	got, err := BuildVertexAnthropicRequestBody([]byte(`{"model":"claude-sonnet-4-5","anthropic_version":"2023-06-01","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	require.Equal(t, "", gjson.GetBytes(got, "model").String())
	require.Equal(t, AnthropicVersion, gjson.GetBytes(got, "anthropic_version").String())
	require.Equal(t, int64(64), gjson.GetBytes(got, "max_tokens").Int())
	require.Equal(t, "hi", gjson.GetBytes(got, "messages.0.content").String())
}

// TestBuildVertexGeminiURLRejectsInvalidLocation supplies a location
// containing a slash and extra path text. It checks a location validation
// error before that input can become part of the generated endpoint.
func TestBuildVertexGeminiURLRejectsInvalidLocation(t *testing.T) {
	_, err := BuildVertexGeminiURL("my-project", "us-central1/path", "gemini-3-pro", "generateContent", false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid vertex location")
}

// TestExchangeVertexServiceAccountTokenUsesProxy exchanges a locally generated
// RSA key through an HTTP proxy fixture. It checks the Google token
// destination, JWT grant form, returned access token, and cache duration.
func TestExchangeVertexServiceAccountTokenUsesProxy(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	// Buffer the proxy observation because the handler runs during
	// ExchangeServiceAccountToken and the assertion reads it only after that call
	// returns.
	seenProxyRequest := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenProxyRequest <- r.URL.String()
		require.Equal(t, "oauth2.googleapis.com", r.URL.Host)
		require.Equal(t, "/token", r.URL.Path)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "urn:ietf:params:oauth:grant-type:jwt-bearer", r.PostForm.Get("grant_type"))

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(google.ServiceAccountTokenResponse{
			AccessToken: "proxied-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		}))
	}))
	defer proxy.Close()

	key := &google.ServiceAccountKey{
		ClientEmail: "svc@example.iam.gserviceaccount.com",
		PrivateKey:  string(pemBytes),
		TokenURI:    "http://oauth2.googleapis.com/token",
	}

	token, ttl, err := ExchangeServiceAccountToken(contextWithTestTimeout(t), key, proxy.URL)
	require.NoError(t, err)
	require.Equal(t, "proxied-token", token)
	require.Equal(t, 55*time.Minute, ttl)

	select {
	case got := <-seenProxyRequest:
		require.Equal(t, "http://oauth2.googleapis.com/token", got)
	default:
		t.Fatal("token request did not reach proxy")
	}
}

// contextWithTestTimeout returns a five-second background context for the
// proxy exchange test. It registers cancellation with test cleanup so the
// context resources are released when the test finishes.
func contextWithTestTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
