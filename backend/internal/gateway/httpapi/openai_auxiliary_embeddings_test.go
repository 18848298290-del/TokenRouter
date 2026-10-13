package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Embedding aliases and batch content travel through a compatible OpenAI
// endpoint while completion retains separate client and provider model
// identities. These fixtures check endpoint assembly, image/cache usage
// parsing, and retry metadata for access-state and ordinary rate-limit failures
// before client output.

// TestBuildOpenAIEmbeddingsURL supplies bare, versioned, complete, and
// third-party base paths. It checks endpoint construction appends missing
// suffixes without duplicating an existing version prefix or embedding
// endpoint.
func TestBuildOpenAIEmbeddingsURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string // endpoint construction scenario label
		base string // supplied upstream base URL
		want string // expected embedding endpoint URL
	}{
		{"bare domain", "https://api.openai.com", "https://api.openai.com/v1/embeddings"},
		{"bare /v1", "https://api.openai.com/v1", "https://api.openai.com/v1/embeddings"},
		{"already embeddings", "https://api.openai.com/v1/embeddings", "https://api.openai.com/v1/embeddings"},
		{"third-party versioned path", "https://open.bigmodel.cn/api/paas/v4", "https://open.bigmodel.cn/api/paas/v4/embeddings"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, buildOpenAIEmbeddingsURL(tt.base))
		})
	}
}

// TestExtractOpenAIEmbeddingsUsage_ParsesImageInputTokens decodes embedding
// usage with image and cached input details. It checks total input, image
// input, cache read, and zero output tokens for that fixture.
func TestExtractOpenAIEmbeddingsUsage_ParsesImageInputTokens(t *testing.T) {
	body := []byte(`{
		"usage":{
			"prompt_tokens":1340,
			"prompt_tokens_details":{
				"text_tokens":1312,
				"image_tokens":28,
				"cached_tokens":4
			}
		}
	}`)

	usage := openai.ExtractEmbeddingsUsage(body)

	require.Equal(t, 1340, usage.InputTokens)
	require.Equal(t, 28, usage.ImageInputTokens)
	require.Equal(t, 4, usage.CacheReadInputTokens)
	require.Zero(t, usage.OutputTokens)
}

// TestForwardEmbeddings_APIKeyPassthroughRecordsUsageAndBatchInput sends a
// batch under a client alias. It checks credentialed target construction,
// mapped wire model, retained batch/options, and returned client, billing,
// upstream, and usage identities.
func TestForwardEmbeddings_APIKeyPassthroughRecordsUsageAndBatchInput(t *testing.T) {
	reqBody := []byte(`{
		"model":"nowledge-embedding",
		"input":["hello","world"],
		"encoding_format":"float",
		"dimensions":256
	}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"emb-rid"},
		},
		Body: io.NopCloser(strings.NewReader(`{
			"object":"list",
			"data":[
				{"object":"embedding","index":0,"embedding":[0.1,0.2]},
				{"object":"embedding","index":1,"embedding":[0.3,0.4]}
			],
			"model":"jina-embeddings-v5-text-small",
			"usage":{"prompt_tokens":13,"total_tokens":13}
		}`)),
	}}
	svc := newAuxiliaryFixture(auxiliaryFixtureInputs{
		transport: upstream,
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 42,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": "https://api.jina.ai",
				"model_mapping": map[string]any{
					"nowledge-embedding": "jina-embeddings-v5-text-small",
				},
			},
		},
	}

	result, err := svc.ForwardEmbeddings(context.Background(), c, provider, reqBody, "")

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, result)
	require.Equal(t, "emb-rid", result.RequestID)
	require.Equal(t, "nowledge-embedding", result.Model)
	require.Equal(t, "jina-embeddings-v5-text-small", result.BillingModel)
	require.Equal(t, "jina-embeddings-v5-text-small", result.UpstreamModel)
	require.Equal(t, 13, result.Usage.InputTokens)
	require.Equal(t, 0, result.Usage.OutputTokens)
	require.Equal(t, "https://api.jina.ai/v1/embeddings", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer sk-test", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "jina-embeddings-v5-text-small", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, int64(2), gjson.GetBytes(upstream.lastBody, "input.#").Int())
	require.Equal(t, "hello", gjson.GetBytes(upstream.lastBody, "input.0").String())
	require.Equal(t, "world", gjson.GetBytes(upstream.lastBody, "input.1").String())
	require.Equal(t, "float", gjson.GetBytes(upstream.lastBody, "encoding_format").String())
	require.Equal(t, int64(256), gjson.GetBytes(upstream.lastBody, "dimensions").Int())
}

// TestForwardEmbeddings_AccessStateUsesTypedFailover returns a
// deactivated-workspace error from an embedding target. It checks typed
// provider-auth recovery, safe client status/message, retained request ID, and
// unwritten client output.
func TestForwardEmbeddings_AccessStateUsesTypedFailover(t *testing.T) {
	reqBody := []byte(`{"model":"text-embedding-3-small","input":"hello"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(reqBody))

	upstreamBody := []byte(`{"error":{"code":"deactivated_workspace","message":"Workspace is deactivated"}}`)
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusForbidden,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"req_embeddings_access_state"},
		},
		Body: io.NopCloser(bytes.NewReader(upstreamBody)),
	}}
	svc := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 43,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
		},
	}

	result, err := svc.ForwardEmbeddings(context.Background(), c, provider, reqBody, "")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusForbidden, failoverErr.StatusCode)
	require.Equal(t, forwardcore.GatewayFailureStageProviderAuth, failoverErr.Stage)
	require.Equal(t, forwardcore.GatewayFailureScopeProvider, failoverErr.Scope)
	require.Equal(t, forwardcore.OpenAIUpstreamAccessStateReason, failoverErr.Reason)
	require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
	require.Equal(t, http.StatusBadGateway, failoverErr.ClientStatusCode)
	require.Equal(t, "Upstream access is temporarily unavailable, please retry later", failoverErr.ClientMessage)
	require.False(t, failoverErr.RetryableOnSameProvider)
	require.Equal(t, "req_embeddings_access_state", http.Header(failoverErr.ResponseHeaders).Get("x-request-id"))
	require.False(t, c.Writer.Written())
}

// TestForwardEmbeddings_NonAccessFailoverKeepsLegacyShape returns a generic 429
// from embedding execution. It checks retry evidence retains empty typed
// access-state metadata and leaves the client response available for provider
// recovery.
func TestForwardEmbeddings_NonAccessFailoverKeepsLegacyShape(t *testing.T) {
	reqBody := []byte(`{"model":"text-embedding-3-small","input":"hello"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(reqBody))

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}}
	svc := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 44,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
		},
	}

	result, err := svc.ForwardEmbeddings(context.Background(), c, provider, reqBody, "")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Empty(t, failoverErr.Stage)
	require.Empty(t, failoverErr.Scope)
	require.Empty(t, failoverErr.Reason)
	require.Zero(t, failoverErr.ClientStatusCode)
	require.Empty(t, failoverErr.ClientMessage)
	require.Nil(t, failoverErr.ResponseHeaders)
	require.False(t, c.Writer.Written())
}
