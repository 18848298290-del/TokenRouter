package middleware

// These tests cover model selection, request rewriting, and response restoration in
// gateway/httpapi/api_key_composite.go and gateway/httpapi/api_key_model_mapping.go.

import (
	"bytes"
	"compress/gzip"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/apikey/testkit"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

func TestResolveCompositeAPIKeyRequestJSON(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gPt/vendor/model","messages":[]}`))
	c.Request.Header.Set("Content-Type", "application/json")

	apiKeyService := testkit.NewService(nil, nil, nil, nil, nil, nil, nil)
	apiKeyService.Start()
	selected, err := resolveCompositeAPIKeyRequest(c, apiKeyService, compositeMiddlewareTestKey())
	require.NoError(t, err)
	require.NotNil(t, selected.GroupID)
	require.Equal(t, int64(7), *selected.GroupID)
	// Subsequent admission checks use the selected composite-key group's protocol policy.
	require.NotNil(t, selected.Group)
	require.True(t, selected.Group.AllowsClientProtocol(protocol.ProtocolOpenAIResponses))
	require.False(t, selected.Group.AllowsClientProtocol(protocol.ProtocolAnthropicMessages))
	body, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Equal(t, "vendor/model", gjson.GetBytes(body, "model").String())
	clientModel, actualModel, ok := gatewayhttp.GetCompositeModelFromContext(c)
	require.True(t, ok)
	require.Equal(t, "gPt/vendor/model", clientModel)
	require.Equal(t, "vendor/model", actualModel)
}

func TestResolveCompositeAPIKeyRequestMultipart(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "GPT/gpt-image-1"))
	file, err := writer.CreateFormFile("image", "input.png")
	require.NoError(t, err)
	_, err = file.Write([]byte("image-data"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	apiKeyService := testkit.NewService(nil, nil, nil, nil, nil, nil, nil)
	apiKeyService.Start()
	_, err = resolveCompositeAPIKeyRequest(c, apiKeyService, compositeMiddlewareTestKey())
	require.NoError(t, err)
	require.NoError(t, c.Request.ParseMultipartForm(1024))
	require.Equal(t, "gpt-image-1", c.Request.FormValue("model"))
	fileHeader := c.Request.MultipartForm.File["image"][0]
	opened, err := fileHeader.Open()
	require.NoError(t, err)
	// Close the multipart file before the test ends and check the close error.
	defer func() {
		require.NoError(t, opened.Close())
	}()
	content, err := io.ReadAll(opened)
	require.NoError(t, err)
	require.Equal(t, []byte("image-data"), content)
}

func TestResolveCompositeAPIKeyRequestGeminiURL(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/GPT/vendor/model:generateContent", bytes.NewBufferString(`{}`))
	c.Params = gin.Params{{Key: "modelAction", Value: "/GPT/vendor/model:generateContent"}}
	apiKeyService := testkit.NewService(nil, nil, nil, nil, nil, nil, nil)
	apiKeyService.Start()
	selected, err := resolveCompositeAPIKeyRequest(c, apiKeyService, compositeMiddlewareTestKey())
	require.NoError(t, err)
	require.Equal(t, int64(7), *selected.GroupID)
	require.Equal(t, "/vendor/model:generateContent", c.Param("modelAction"))
}

func TestResolveCompositeAPIKeyRequestAdditionalModels(t *testing.T) {
	apiKeyService := testkit.NewService(nil, nil, nil, nil, nil, nil, nil)
	apiKeyService.Start()

	t.Run("rewrites additional model from selected group", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(
			`{"model":"GPT/gpt-5","tools":[{"type":"image_generation","model":"gpt/gpt-image-2"}]}`,
		))
		c.Request.Header.Set("Content-Type", "application/json")

		_, err := resolveCompositeAPIKeyRequest(c, apiKeyService, compositeMiddlewareMultiGroupTestKey())
		require.NoError(t, err)
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, "gpt-5", gjson.GetBytes(body, "model").String())
		require.Equal(t, "gpt-image-2", gjson.GetBytes(body, "tools.0.model").String())
	})

	t.Run("rejects additional model from another group", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(
			`{"model":"GPT/gpt-5","tools":[{"type":"image_generation","model":"Claude/claude-image"}]}`,
		))
		c.Request.Header.Set("Content-Type", "application/json")

		_, err := resolveCompositeAPIKeyRequest(c, apiKeyService, compositeMiddlewareMultiGroupTestKey())
		require.ErrorIs(t, err, apikey.ErrCompositeKeyUnsupported)
	})
}

func TestResolveCompositeAPIKeyRequestSpecialEndpoints(t *testing.T) {
	apiKeyService := testkit.NewService(nil, nil, nil, nil, nil, nil, nil)
	apiKeyService.Start()

	listContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	listContext.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	selected, err := resolveCompositeAPIKeyRequest(listContext, apiKeyService, compositeMiddlewareTestKey())
	require.NoError(t, err)
	require.Nil(t, selected.GroupID)
	_, marked := listContext.Get(gatewayhttp.CompositeKeyNoGroupContextKey)
	require.True(t, marked)
	require.False(t, isCompositeKeyBillingBypassEndpoint(http.MethodGet, "/v1/models"))
	require.False(t, isCompositeKeyBillingBypassEndpoint(http.MethodGet, "/v1/images/batches/models"))

	videoContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	videoContext.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/video-123/content", nil)
	selected, err = resolveCompositeAPIKeyRequest(videoContext, apiKeyService, compositeMiddlewareTestKey())
	require.NoError(t, err)
	require.Nil(t, selected.GroupID)
	require.True(t, isCompositeKeyBillingBypassEndpoint(http.MethodGet, "/v1/videos/video-123/content"))
	require.True(t, isCompositeKeyBillingBypassEndpoint(http.MethodGet, "/videos/video-123"))
	require.True(t, isCompositeKeyBillingBypassEndpoint(http.MethodGet, "/videos/video-123/content"))

	antigravityUsageContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	antigravityUsageContext.Request = httptest.NewRequest(http.MethodGet, "/antigravity/v1/usage", nil)
	selected, err = resolveCompositeAPIKeyRequest(antigravityUsageContext, apiKeyService, compositeMiddlewareTestKey())
	require.NoError(t, err)
	require.Nil(t, selected.GroupID)

	realtimeContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	realtimeContext.Request = httptest.NewRequest(http.MethodPost, "/v1/live", nil)
	_, err = resolveCompositeAPIKeyRequest(realtimeContext, apiKeyService, compositeMiddlewareTestKey())
	require.ErrorIs(t, err, apikey.ErrCompositeKeyUnsupported)

	require.True(t, isCompositeKeyUnsupportedEndpoint(
		http.MethodGet,
		"/backend-api/codex/call_123",
		"/backend-api/codex/:call_id",
	))
	require.False(t, isCompositeKeyUnsupportedEndpoint(
		http.MethodPost,
		"/backend-api/codex/responses",
		"/backend-api/codex/responses",
	))
}

func TestReplaceCompositeResponseModel(t *testing.T) {
	response := []byte("data: {\"id\":\"gpt-5\",\"model\":\"gpt-5\",\"modelVersion\":\"gpt-5\",\"model_version\":\"gpt-5\",\"name\":\"models/gpt-5\",\"text\":\"gpt-5\"}\n\n")

	// Restore protocol model fields while leaving identical text in response content unchanged.
	rewritten := replaceCompositeResponseModel(response, "gpt-5", "GPT/gpt-5")
	require.Contains(t, string(rewritten), `"id":"GPT/gpt-5"`)
	require.Contains(t, string(rewritten), `"model":"GPT/gpt-5"`)
	require.Contains(t, string(rewritten), `"modelVersion":"GPT/gpt-5"`)
	require.Contains(t, string(rewritten), `"model_version":"GPT/gpt-5"`)
	require.Contains(t, string(rewritten), `"name":"models/GPT/gpt-5"`)
	require.Contains(t, string(rewritten), `"text":"gpt-5"`)

	// JSON-escape special characters in model aliases.
	escaped := replaceCompositeResponseModel([]byte(`{"model":"safe-target","text":"safe-target"}`), "safe-target", `review"alias`)
	require.JSONEq(t, `{"model":"review\"alias","text":"safe-target"}`, string(escaped))
}

func TestAbortCompositeKeyErrorPreservesProtocolShape(t *testing.T) {
	openAIRecorder := httptest.NewRecorder()
	openAIContext, _ := gin.CreateTestContext(openAIRecorder)
	openAIContext.Request = httptest.NewRequest(http.MethodPost, "/v1/live", nil)
	abortCompositeKeyError(openAIContext, apikey.ErrCompositeKeyUnsupported)
	require.Equal(t, http.StatusBadRequest, openAIRecorder.Code)
	require.Equal(t, "COMPOSITE_KEY_ENDPOINT_UNSUPPORTED", gjson.Get(openAIRecorder.Body.String(), "error.code").String())

	anthropicRecorder := httptest.NewRecorder()
	anthropicContext, _ := gin.CreateTestContext(anthropicRecorder)
	anthropicContext.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	abortCompositeKeyError(anthropicContext, apikey.ErrCompositeKeyPrefixRequired)
	require.Equal(t, http.StatusBadRequest, anthropicRecorder.Code)
	require.Equal(t, "error", gjson.Get(anthropicRecorder.Body.String(), "type").String())
	require.Equal(t, "COMPOSITE_KEY_MODEL_PREFIX_REQUIRED", gjson.Get(anthropicRecorder.Body.String(), "error.code").String())
}

func TestApplyAPIKeyModelRedirectRewritesJSONAndRestoresMetadataOnly(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex-auto-review"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	applyAPIKeyModelRedirect(c, &apikey.APIKey{ModelMapping: map[string]string{
		"codex-auto-review": "gpt-5.6-luna",
	}})

	body, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-luna", gjson.GetBytes(body, "model").String())
	require.Equal(t, "codex-auto-review", c.Request.Context().Value(telemetry.ClientModel))

	_, err = c.Writer.Write([]byte(`{"model":"upstream-luna","output_text":"upstream-luna"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"codex-auto-review","output_text":"upstream-luna"}`, recorder.Body.String())
}

func TestApplyAPIKeyModelRedirectDiscoversStreamingUpstreamModel(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"review"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	applyAPIKeyModelRedirect(c, &apikey.APIKey{ModelMapping: map[string]string{"review": "key-target"}})

	_, err := c.Writer.WriteString("data: {\"type\":\"response.completed\",\"response\":{\"model\":\"upstream-target\",\"output_text\":\"upstream-target\"}}\n\n")
	require.NoError(t, err)
	require.Contains(t, recorder.Body.String(), `"model":"review"`)
	require.Contains(t, recorder.Body.String(), `"output_text":"upstream-target"`)
}

func TestApplyAPIKeyModelRedirectRewritesCompressedJSON(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err := writer.Write([]byte(`{"model":"codex-auto-review","tools":[{"model":"tool-alias"}]}`))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressed.Bytes()))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Content-Encoding", "gzip")
	applyAPIKeyModelRedirect(c, &apikey.APIKey{ModelMapping: map[string]string{
		"codex-auto-review": "gpt-5.6-luna",
		"tool-alias":        "tool-target",
	}})

	body, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Empty(t, c.Request.Header.Get("Content-Encoding"))
	require.Equal(t, "gpt-5.6-luna", gjson.GetBytes(body, "model").String())
	require.Equal(t, "tool-target", gjson.GetBytes(body, "tools.0.model").String())
}

func TestApplyAPIKeyModelRedirectRewritesMultipartModel(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "image-alias"))
	require.NoError(t, writer.WriteField("prompt", "keep image-alias in text"))
	require.NoError(t, writer.Close())

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	applyAPIKeyModelRedirect(c, &apikey.APIKey{ModelMapping: map[string]string{"image-alias": "gpt-image-1"}})

	require.NoError(t, c.Request.ParseMultipartForm(1<<20))
	require.Equal(t, "gpt-image-1", c.Request.FormValue("model"))
	require.Equal(t, "keep image-alias in text", c.Request.FormValue("prompt"))
}

func TestApplyAPIKeyModelRedirectRewritesGeminiURLModel(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-alias:generateContent", nil)
	c.Params = gin.Params{{Key: "modelAction", Value: "/gemini-alias:generateContent"}}

	applyAPIKeyModelRedirect(c, &apikey.APIKey{ModelMapping: map[string]string{"gemini-alias": "gemini-3.1-pro-preview"}})

	require.Equal(t, "/gemini-3.1-pro-preview:generateContent", c.Param("modelAction"))
}

func TestApplyAPIKeyModelRedirectRewritesAdditionalToolModelsWithoutMainMatch(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"main-model","tools":[{"type":"namespace","model":"tool-alias"}]}`,
	))
	c.Request.Header.Set("Content-Type", "application/json")

	applyAPIKeyModelRedirect(c, &apikey.APIKey{ModelMapping: map[string]string{"tool-alias": "tool-target"}})
	body, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Equal(t, "main-model", gjson.GetBytes(body, "model").String())
	require.Equal(t, "tool-target", gjson.GetBytes(body, "tools.0.model").String())
}

func TestApplyAPIKeyModelRedirectComposesWithCompositeResponseRestore(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"review"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	gatewayhttp.SetCompositeModelContext(c, "GPT/review", "review")

	applyAPIKeyModelRedirect(c, &apikey.APIKey{ModelMapping: map[string]string{"review": "gpt-5.6-luna"}})
	_, err := c.Writer.Write([]byte(`{"model":"gpt-5.6-luna"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"GPT/review"}`, recorder.Body.String())
}

// resolveCompositeAPIKeyRequest resolves a composite key's model and group through the gateway.
func resolveCompositeAPIKeyRequest(c *gin.Context, keys *apikey.APIKeyService, key *apikey.APIKey) (*apikey.APIKey, error) {
	if key == nil || !key.IsComposite {
		return key, nil
	}
	value, err := gatewayhttp.ResolveCompositeAPIKeyRequest(c, keys, apikey.CopyAPIKey(key))
	return apikey.CopyAPIKey(value), err
}

func replaceCompositeResponseModel(data []byte, actualModel, clientModel string) []byte {
	return gatewayhttp.ReplaceCompositeResponseModel(data, actualModel, clientModel)
}

func isCompositeKeyBillingBypassEndpoint(method, path string) bool {
	return gatewayhttp.IsCompositeKeyBillingBypassEndpoint(method, path)
}

func isCompositeKeyUnsupportedEndpoint(method, path, routePath string) bool {
	return gatewayhttp.IsCompositeKeyUnsupportedEndpoint(method, path, routePath)
}

func abortCompositeKeyError(c *gin.Context, err error) { gatewayhttp.AbortCompositeKeyError(c, err) }

func compositeMiddlewareTestKey() *apikey.APIKey {
	group := &routing.Group{
		ID: 7, Name: "OpenAI", Status: billing.StatusActive, IsExclusive: true,
		AllowedProtocols: []protocol.ProtocolID{
			protocol.ProtocolOpenAIResponses,
			protocol.ProtocolOpenAIChatCompletions,
		},
	}
	return &apikey.APIKey{
		ID: 1, UserID: 2, IsComposite: true, User: &identity.User{ID: 2, Status: billing.StatusActive},
		CompositeGroups: []apikey.APIKeyCompositeGroup{{GroupID: 7, Prefix: "GPT", NormalizedPrefix: "gpt", Group: group}},
	}
}

// compositeMiddlewareMultiGroupTestKey supplies two groups to test rejection of cross-group models in one request.
func compositeMiddlewareMultiGroupTestKey() *apikey.APIKey {
	key := compositeMiddlewareTestKey()
	group := &routing.Group{ID: 8, Name: "Claude", Status: billing.StatusActive, IsExclusive: true}
	key.CompositeGroups = append(key.CompositeGroups, apikey.APIKeyCompositeGroup{
		GroupID: 8, Prefix: "Claude", NormalizedPrefix: "claude", Group: group,
	})
	return key
}

func applyAPIKeyModelRedirect(c *gin.Context, apiKey *apikey.APIKey) {
	gatewayhttp.ApplyAPIKeyModelRedirect(c, apikey.CopyAPIKey(apiKey))
}
