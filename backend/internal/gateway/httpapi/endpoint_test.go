package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestNormalizeInboundEndpoint(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		// Direct canonical paths.
		{"/v1/messages", EndpointMessages},
		{"/v1/chat/completions", EndpointChatCompletions},
		{"/v1/embeddings", EndpointEmbeddings},
		{"/v1/alpha/search", EndpointAlphaSearch},
		{"/v1/responses", EndpointResponses},
		{"/v1/responses/input_tokens", EndpointResponsesInputTokens},
		{"/v1/responses/compact", EndpointResponsesCompact},
		{"/v1/responses/compact/detail", EndpointResponsesCompact},
		{"/v1/images/generations", EndpointImagesGenerations},
		{"/v1/images/edits", EndpointImagesEdits},
		{"/v1/videos/generations", EndpointVideosGenerations},
		{"/v1/videos/edits", EndpointVideosEdits},
		{"/v1/videos/extensions", EndpointVideosExtensions},
		{"/v1/videos/req_123", EndpointVideos},
		{"/v1beta/models", EndpointGeminiModels},

		// Platform-prefixed Responses root paths.
		{"/antigravity/v1/messages", EndpointMessages},
		{"/openai/v1/responses", EndpointResponses},
		{"/openai/v1/images/generations", EndpointImagesGenerations},
		{"/openai/v1/images/edits", EndpointImagesEdits},
		{"/antigravity/v1beta/models/gemini:generateContent", EndpointGeminiModels},

		// Platform-prefixed compact paths have a separate inbound endpoint.
		{"/openai/v1/responses/compact", EndpointResponsesCompact},
		{"/openai/v1/responses/compact/detail", EndpointResponsesCompact},

		// Bare aliases distinguish the root from compact endpoints.
		{"/responses", EndpointResponses},
		{"/responses/input_tokens", EndpointResponsesInputTokens},
		{"/responses/compact", EndpointResponsesCompact},
		{"/responses/compact/detail", EndpointResponsesCompact},
		{"/alpha/search", EndpointAlphaSearch},

		// Codex aliases distinguish the root from compact endpoints.
		{"/backend-api/codex/responses", EndpointResponses},
		{"/backend-api/codex/responses/input_tokens", EndpointResponsesInputTokens},
		{"/backend-api/codex/responses/compact", EndpointResponsesCompact},
		{"/backend-api/codex/responses/compact/detail", EndpointResponsesCompact},
		{"/backend-api/codex/alpha/search", EndpointAlphaSearch},

		// Unknown prefixes ending in a Responses path must not be accepted as known bare aliases.
		{"/foo/responses", "/foo/responses"},
		{"/foo/responses/compact", "/foo/responses/compact"},

		// Unknown path is returned as-is.
		{"/v1/embeddings", "/v1/embeddings"},
		{"", ""},
		{"  /v1/messages  ", EndpointMessages},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			require.Equal(t, tt.want, NormalizeInboundEndpoint(tt.path))
		})
	}
}

func TestDeriveUpstreamEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		inbound  string
		rawPath  string
		platform string
		want     string
	}{
		// Anthropic.
		{"anthropic messages", EndpointMessages, "/v1/messages", capability.PlatformAnthropic, EndpointMessages},

		// Gemini.
		{"gemini models", EndpointGeminiModels, "/v1beta/models/gemini:gen", capability.PlatformGemini, EndpointGeminiModels},

		// OpenAI Responses roots.
		{"openai responses root", EndpointResponses, "/v1/responses", capability.PlatformOpenAI, EndpointResponses},
		{"openai responses input tokens", EndpointResponsesInputTokens, "/v1/responses/input_tokens", capability.PlatformOpenAI, EndpointResponsesInputTokens},

		// Keep compact subresource suffixes from the raw path in the upstream endpoint.
		{"openai responses compact", EndpointResponsesCompact, "/openai/v1/responses/compact", capability.PlatformOpenAI, "/v1/responses/compact"},
		{"openai responses nested", EndpointResponsesCompact, "/openai/v1/responses/compact/detail", capability.PlatformOpenAI, "/v1/responses/compact/detail"},
		{"openai bare responses compact", EndpointResponsesCompact, "/responses/compact", capability.PlatformOpenAI, "/v1/responses/compact"},
		{"openai bare responses compact detail", EndpointResponsesCompact, "/responses/compact/detail", capability.PlatformOpenAI, "/v1/responses/compact/detail"},
		{"openai codex direct responses compact", EndpointResponsesCompact, "/backend-api/codex/responses/compact", capability.PlatformOpenAI, "/v1/responses/compact"},
		{"openai codex direct responses compact detail", EndpointResponsesCompact, "/backend-api/codex/responses/compact/detail", capability.PlatformOpenAI, "/v1/responses/compact/detail"},

		// Bare root aliases normalize to the Responses endpoint.
		{"openai bare responses", EndpointResponses, "/responses", capability.PlatformOpenAI, EndpointResponses},
		{"openai codex direct responses", EndpointResponses, "/backend-api/codex/responses", capability.PlatformOpenAI, EndpointResponses},

		// Use the canonical compact suffix when only the inbound endpoint identifies compaction.
		{"openai responses compact inbound only, unrelated raw path", EndpointResponsesCompact, "/v1/messages", capability.PlatformOpenAI, EndpointResponsesCompact},

		{"openai from messages", EndpointMessages, "/v1/messages", capability.PlatformOpenAI, EndpointResponses},
		{"openai from completions", EndpointChatCompletions, "/v1/chat/completions", capability.PlatformOpenAI, EndpointResponses},
		{"openai embeddings", EndpointEmbeddings, "/v1/embeddings", capability.PlatformOpenAI, EndpointEmbeddings},
		{"openai alpha search", EndpointAlphaSearch, "/backend-api/codex/alpha/search", capability.PlatformOpenAI, EndpointAlphaSearch},
		{"openai image generations", EndpointImagesGenerations, "/v1/images/generations", capability.PlatformOpenAI, EndpointImagesGenerations},
		{"openai image edits", EndpointImagesEdits, "/openai/v1/images/edits", capability.PlatformOpenAI, EndpointImagesEdits},
		{"grok chat defaults to responses without runtime result", EndpointChatCompletions, "/v1/chat/completions", capability.PlatformGrok, EndpointResponses},
		{"grok responses", EndpointResponses, "/v1/responses", capability.PlatformGrok, EndpointResponses},
		{"grok video generations", EndpointVideosGenerations, "/v1/videos/generations", capability.PlatformGrok, EndpointVideosGenerations},
		{"grok video edits", EndpointVideosEdits, "/videos/edits", capability.PlatformGrok, EndpointVideosEdits},
		{"grok video extensions", EndpointVideosExtensions, "/videos/extensions", capability.PlatformGrok, EndpointVideosExtensions},
		{"grok video status", EndpointVideos, "/videos/req_123", capability.PlatformGrok, EndpointVideos},

		// Antigravity chooses Claude or Gemini from the inbound endpoint.
		{"antigravity claude", EndpointMessages, "/antigravity/v1/messages", capability.PlatformAntigravity, EndpointMessages},
		{"antigravity gemini", EndpointGeminiModels, "/antigravity/v1beta/models", capability.PlatformAntigravity, EndpointGeminiModels},

		// Unknown platforms retain the inbound endpoint.
		{"unknown platform", "/v1/embeddings", "/v1/embeddings", "unknown", "/v1/embeddings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, DeriveUpstreamEndpoint(tt.inbound, tt.rawPath, tt.platform))
		})
	}
}

func TestGetUpstreamEndpointPrefersRuntimeOverride(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, EndpointChatCompletions, nil)
	c.Set(ctxKeyInboundEndpoint, EndpointChatCompletions)

	SetActualUpstreamEndpoint(c, EndpointAntigravityGenerateContent)
	require.Equal(t, EndpointAntigravityGenerateContent, GetUpstreamEndpoint(c, capability.PlatformAntigravity))

	SetActualUpstreamEndpoint(c, "")
	require.Equal(t, EndpointMessages, GetUpstreamEndpoint(c, capability.PlatformAntigravity))
}

func TestGetUpstreamEndpointUsesOpenAIRuntimeOverride(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
	c.Set(ctxKeyInboundEndpoint, EndpointResponses)
	SetActualOpenAIUpstreamEndpoint(c, EndpointChatCompletions)
	require.Equal(t, EndpointChatCompletions, GetUpstreamEndpoint(c, capability.PlatformOpenAI))
}

func TestResponsesSubpathSuffix(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"/v1/responses", ""},
		{"/v1/responses/", ""},
		{"/v1/responses/compact", "/compact"},
		{"/openai/v1/responses/compact/detail", "/compact/detail"},
		{"/responses", ""},
		{"/responses/compact", "/compact"},
		{"/responses/compact/detail", "/compact/detail"},
		{"/backend-api/codex/responses", ""},
		{"/backend-api/codex/responses/compact", "/compact"},
		{"/backend-api/codex/responses/compact/detail", "/compact/detail"},
		{"/v1/messages", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			require.Equal(t, tt.want, responsesSubpathSuffix(tt.raw))
		})
	}
}

func TestInboundEndpointMiddleware(t *testing.T) {
	router := gin.New()
	router.Use(InboundEndpointMiddleware())

	var captured string
	router.POST("/v1/messages", func(c *gin.Context) {
		captured = GetInboundEndpoint(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, EndpointMessages, captured)
}

func TestGetInboundEndpoint_FallbackWithoutMiddleware(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/antigravity/v1/messages", nil)

	// Without middleware, normalize the request URL.
	got := GetInboundEndpoint(c)
	require.Equal(t, EndpointMessages, got)
}

// TestInboundEndpointMiddleware_WildcardRoutes checks normalization using the actual request URL.
// FullPath returns the route pattern, which would misclassify compact requests as the Responses root.
func TestInboundEndpointMiddleware_WildcardRoutes(t *testing.T) {
	tests := []struct {
		name        string
		routePath   string
		requestPath string
		want        string
	}{
		{
			name:        "v1 responses wildcard route, compact request",
			routePath:   "/v1/responses/*subpath",
			requestPath: "/v1/responses/compact",
			want:        EndpointResponsesCompact,
		},
		{
			name:        "bare responses wildcard route, compact request",
			routePath:   "/responses/*subpath",
			requestPath: "/responses/compact",
			want:        EndpointResponsesCompact,
		},
		{
			name:        "codex direct wildcard route, compact request",
			routePath:   "/backend-api/codex/responses/*subpath",
			requestPath: "/backend-api/codex/responses/compact",
			want:        EndpointResponsesCompact,
		},
		{
			name:        "v1 responses wildcard route, non-compact subpath request",
			routePath:   "/v1/responses/*subpath",
			requestPath: "/v1/responses/foo",
			want:        EndpointResponses,
		},
		{
			name:        "bare responses wildcard route, non-compact subpath request",
			routePath:   "/responses/*subpath",
			requestPath: "/responses/foo",
			want:        EndpointResponses,
		},
		{
			name:        "codex direct wildcard route, non-compact subpath request",
			routePath:   "/backend-api/codex/responses/*subpath",
			requestPath: "/backend-api/codex/responses/foo",
			want:        EndpointResponses,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(InboundEndpointMiddleware())

			var captured string
			router.POST(tt.routePath, func(c *gin.Context) {
				captured = GetInboundEndpoint(c)
				c.Status(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodPost, tt.requestPath, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, tt.want, captured)
		})
	}
}

// TestInboundEndpointMiddleware_GeminiWildcardRoute checks that the request URL
// on a Gemini wildcard route normalizes to EndpointGeminiModels.
func TestInboundEndpointMiddleware_GeminiWildcardRoute(t *testing.T) {
	router := gin.New()
	router.Use(InboundEndpointMiddleware())

	var captured string
	router.POST("/v1beta/models/*modelAction", func(c *gin.Context) {
		captured = GetInboundEndpoint(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, EndpointGeminiModels, captured)
}

// TestGetInboundEndpoint_FallbackWildcardRouteWithoutMiddleware checks request-URL
// normalization for compact requests when middleware has not saved an endpoint.
func TestGetInboundEndpoint_FallbackWildcardRouteWithoutMiddleware(t *testing.T) {
	router := gin.New()
	// Leave InboundEndpointMiddleware unregistered to exercise on-demand normalization.

	var captured string
	router.POST("/v1/responses/*subpath", func(c *gin.Context) {
		// FullPath is the route pattern even when the request selects a specific subpath.
		require.Equal(t, "/v1/responses/*subpath", c.FullPath())
		captured = GetInboundEndpoint(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, EndpointResponsesCompact, captured)
}

func TestGetUpstreamEndpoint_FullFlow(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses/compact", nil)

	// Simulate middleware.
	c.Set(ctxKeyInboundEndpoint, NormalizeInboundEndpoint(c.Request.URL.Path))

	got := GetUpstreamEndpoint(c, capability.PlatformOpenAI)
	require.Equal(t, "/v1/responses/compact", got)
}

// TestOpenAIUpstreamEndpoint_ViaGetUpstreamEndpoint checks endpoint derivation
// for OpenAI requests through the shared context helper.

func TestOpenAIUpstreamEndpoint_ViaGetUpstreamEndpoint(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "responses root maps to responses upstream",
			path: "/v1/responses",
			want: EndpointResponses,
		},
		{
			name: "responses compact keeps compact suffix",
			path: "/openai/v1/responses/compact",
			want: "/v1/responses/compact",
		},
		{
			name: "responses nested suffix preserved",
			path: "/openai/v1/responses/compact/detail",
			want: "/v1/responses/compact/detail",
		},
		{
			name: "non responses path uses platform fallback",
			path: "/v1/messages",
			want: EndpointResponses,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, nil)

			got := GetUpstreamEndpoint(c, capability.PlatformOpenAI)
			require.Equal(t, tt.want, got)
		})
	}
}
