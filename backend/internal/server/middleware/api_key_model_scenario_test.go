package middleware

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

// A composite prefix selects a group before key aliases redirect the internal
// model, and response metadata still needs the client's requested spelling.
// These scenarios exercise that sequence across
// gateway/httpapi/api_key_composite.go and
// gateway/httpapi/api_key_model_mapping.go using JSON, multipart, Gemini
// parameters, compressed bodies, and response chunks.

// TestResolveCompositeAPIKeyRequestJSON supplies a mixed-case composite prefix
// followed by a slash-containing model name. Selection retains the group's
// protocol policy and separates the client's full name from the rewritten JSON
// model.
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

// TestResolveCompositeAPIKeyRequestMultipart selects a group from an image-edit
// multipart model field. The rewritten form keeps the image part and its bytes
// while removing the composite prefix from the model.
func TestResolveCompositeAPIKeyRequestMultipart(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "GPT/gpt-image-1"))
	file, err := writer.CreateFormFile("image", "input.png")
	require.NoError(t, err)
	_, err = file.Write([]byte("image-data"))
	require.NoError(t, err)
	// Finish the multipart closing boundary before routing the fixture body. The
	// downstream parser needs that complete stream to recover model and image
	// parts.
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
	// The small image part stays memory-backed under this parse limit. Close its
	// opened file interface after reading and check the exposed Close result
	// before the test ends.
	defer func() {
		require.NoError(t, opened.Close())
	}()
	content, err := io.ReadAll(opened)
	require.NoError(t, err)
	require.Equal(t, []byte("image-data"), content)
}

// TestResolveCompositeAPIKeyRequestGeminiURL supplies a composite prefix in a
// Gemini model-action parameter. The selected key binds the requested group and
// the route parameter retains the model's remaining path and action.
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

// TestResolveCompositeAPIKeyRequestAdditionalModels includes a tool model
// alongside the primary composite model. Matching group prefixes are rewritten,
// while a tool selecting another group returns the unsupported-composite error.
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

// TestResolveCompositeAPIKeyRequestSpecialEndpoints exercises model lists,
// existing video reads, usage, and realtime paths. It protects no-group marking
// separately from consumption bypass and rejects the represented realtime
// connection cases.
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

// TestReplaceCompositeResponseModel places the same model text in protocol
// metadata and response content. Restoration changes recognized metadata
// patterns, keeps the content text, and JSON-escapes an alias containing a
// quote.
func TestReplaceCompositeResponseModel(t *testing.T) {
	response := []byte("data: {\"id\":\"gpt-5\",\"model\":\"gpt-5\",\"modelVersion\":\"gpt-5\",\"model_version\":\"gpt-5\",\"name\":\"models/gpt-5\",\"text\":\"gpt-5\"}\n\n")

	// The same target appears in metadata and generated text. Restore recognized
	// protocol fields so returning the client's alias does not rewrite that
	// content text.
	rewritten := replaceCompositeResponseModel(response, "gpt-5", "GPT/gpt-5")
	require.Contains(t, string(rewritten), `"id":"GPT/gpt-5"`)
	require.Contains(t, string(rewritten), `"model":"GPT/gpt-5"`)
	require.Contains(t, string(rewritten), `"modelVersion":"GPT/gpt-5"`)
	require.Contains(t, string(rewritten), `"model_version":"GPT/gpt-5"`)
	require.Contains(t, string(rewritten), `"name":"models/GPT/gpt-5"`)
	require.Contains(t, string(rewritten), `"text":"gpt-5"`)

	// Include a quote inside the client alias. The restored metadata needs JSON
	// escaping so the complete output remains decodable while content text stays
	// unchanged.
	escaped := replaceCompositeResponseModel([]byte(`{"model":"safe-target","text":"safe-target"}`), "safe-target", `review"alias`)
	require.JSONEq(t, `{"model":"review\"alias","text":"safe-target"}`, string(escaped))
}

// TestAbortCompositeKeyErrorPreservesProtocolShape reports composite errors
// through OpenAI and Messages request paths. The responses retain their
// protocol-specific envelope and the composite error code selected for each
// case.
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

// TestApplyAPIKeyModelRedirectRewritesJSONAndRestoresMetadataOnly redirects the
// primary JSON alias and writes a response using another upstream model. Client
// metadata returns to the requested alias while identical response content
// remains unchanged.
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

// TestApplyAPIKeyModelRedirectDiscoversStreamingUpstreamModel writes a complete
// response event with a discovered upstream model. The wrapped writer restores
// the client alias in metadata and retains the event's output text.
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

// TestApplyAPIKeyModelRedirectRewritesCompressedJSON supplies a gzip body with
// primary and tool aliases. The restored reader contains both targets and the
// compression header is removed after decoding.
func TestApplyAPIKeyModelRedirectRewritesCompressedJSON(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err := writer.Write([]byte(`{"model":"codex-auto-review","tools":[{"model":"tool-alias"}]}`))
	require.NoError(t, err)
	// Finish gzip output before routing the captured bytes. Closing writes the
	// compressed stream trailer that the downstream body decoder needs to validate
	// its input.
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

// TestApplyAPIKeyModelRedirectRewritesMultipartModel uses an image alias also
// present in a multipart prompt. The form model is redirected while prompt
// content remains exactly as supplied.
func TestApplyAPIKeyModelRedirectRewritesMultipartModel(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "image-alias"))
	require.NoError(t, writer.WriteField("prompt", "keep image-alias in text"))
	// Finish the multipart boundary before supplying its content type and bytes to
	// the request. The subsequent parser needs a complete form stream for model
	// and prompt fields.
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

// TestApplyAPIKeyModelRedirectRewritesGeminiURLModel maps a Gemini model alias
// through route parameters. The action suffix remains attached after the
// parameter switches to the configured target model.
func TestApplyAPIKeyModelRedirectRewritesGeminiURLModel(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-alias:generateContent", nil)
	c.Params = gin.Params{{Key: "modelAction", Value: "/gemini-alias:generateContent"}}

	applyAPIKeyModelRedirect(c, &apikey.APIKey{ModelMapping: map[string]string{"gemini-alias": "gemini-3.1-pro-preview"}})

	require.Equal(t, "/gemini-3.1-pro-preview:generateContent", c.Param("modelAction"))
}

// TestApplyAPIKeyModelRedirectRewritesAdditionalToolModelsWithoutMainMatch
// supplies a tool alias with an unmapped primary model. The primary text
// remains and the independent tool field still receives its target.
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

// TestApplyAPIKeyModelRedirectComposesWithCompositeResponseRestore seeds the
// composite client model before key alias redirection. The response restores
// the full prefixed client spelling after the internal target is written.
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

// resolveCompositeAPIKeyRequest delegates composite selection with CopyAPIKey
// views at input and output. Nil and ordinary keys return their supplied
// pointer unchanged; copying still shares other key references, including
// mapping data, which fixtures treat as read-only during the request.
func resolveCompositeAPIKeyRequest(c *gin.Context, keys *apikey.APIKeyService, key *apikey.APIKey) (*apikey.APIKey, error) {
	if key == nil || !key.IsComposite {
		return key, nil
	}
	value, err := gatewayhttp.ResolveCompositeAPIKeyRequest(c, keys, apikey.CopyAPIKey(key))
	return apikey.CopyAPIKey(value), err
}

// replaceCompositeResponseModel delegates known model-metadata byte replacement
// to the gateway implementation. Callers supply complete recognized patterns in
// the chunk and use the returned bytes; arbitrary content text is outside those
// supported metadata patterns.
func replaceCompositeResponseModel(data []byte, actualModel, clientModel string) []byte {
	return gatewayhttp.ReplaceCompositeResponseModel(data, actualModel, clientModel)
}

// isCompositeKeyBillingBypassEndpoint delegates consumption-bypass
// classification for the supplied method and path. Existing usage,
// batch-management, and video reads may qualify, while model-list selection
// alone does not establish this bypass.
func isCompositeKeyBillingBypassEndpoint(method, path string) bool {
	return gatewayhttp.IsCompositeKeyBillingBypassEndpoint(method, path)
}

// isCompositeKeyUnsupportedEndpoint delegates composite realtime-path
// classification to the gateway. The matched route template accompanies method
// and path so sideband GET can be distinguished from ordinary endpoints sharing
// the Codex prefix.
func isCompositeKeyUnsupportedEndpoint(method, path, routePath string) bool {
	return gatewayhttp.IsCompositeKeyUnsupportedEndpoint(method, path, routePath)
}

// abortCompositeKeyError delegates status, code, and protocol-envelope writing
// for a composite error. Supply the request path for protocol selection; the
// gateway helper writes its response and aborts Gin's handler chain.
func abortCompositeKeyError(c *gin.Context, err error) { gatewayhttp.AbortCompositeKeyError(c, err) }

// compositeMiddlewareTestKey constructs a fresh composite key with one active
// exclusive OpenAI group. Its GPT prefix and allowed Responses/Chat protocols
// support selection assertions; this fixture calls selection directly rather
// than complete authentication.
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

// compositeMiddlewareMultiGroupTestKey appends an active Claude group to a
// fresh GPT fixture. The returned key supplies independent prefixes so a
// request can demonstrate rejection of tool models from another selected group.
func compositeMiddlewareMultiGroupTestKey() *apikey.APIKey {
	key := compositeMiddlewareTestKey()
	group := &routing.Group{ID: 8, Name: "Claude", Status: billing.StatusActive, IsExclusive: true}
	key.CompositeGroups = append(key.CompositeGroups, apikey.APIKeyCompositeGroup{
		GroupID: 8, Prefix: "Claude", NormalizedPrefix: "claude", Group: group,
	})
	return key
}

// applyAPIKeyModelRedirect delegates key-alias rewriting using CopyAPIKey,
// which retains shared mapping references. Call it after composite context
// selection and keep fixture mappings unchanged while request and response
// rewriting read them.
func applyAPIKeyModelRedirect(c *gin.Context, apiKey *apikey.APIKey) {
	gatewayhttp.ApplyAPIKeyModelRedirect(c, apikey.CopyAPIKey(apiKey))
}
