package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

const openAIInvalidFunctionParametersBody = `{"error":{` +
	`"message":"Invalid schema for function 'automation_update': expected an object.",` +
	`"type":"invalid_request_error",` +
	`"param":"input[8].tools[1].tools[2].parameters",` +
	`"code":"invalid_function_parameters"}}`

type panicOnReadCloser struct{}

func TestOpenAIHandleErrorResponse_NoRuleKeepsDefault(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := newResponseOutputForTest(OpenAIResponseOptions{})
	respBody := []byte(`{"error":{"message":"Invalid schema for field messages"}}`)
	resp := &http.Response{
		StatusCode: http.StatusUnprocessableEntity,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header:     http.Header{},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 12, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	_, err := svc.ResponseError(context.Background(), resp, c, provider, nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusBadGateway, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errField["type"])
	assert.Equal(t, "Upstream request failed", errField["message"])
}

func TestOpenAIHandleErrorResponse_InvalidRequest400PassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := newResponseOutputForTest(OpenAIResponseOptions{})
	respBody := []byte(`{"error":{"message":"Invalid property name in input arguments","type":"invalid_request_error","param":"input[35].arguments","code":"property_name_above_max_length"}}`)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header: http.Header{
			"Content-Type": {"application/json; charset=utf-8"},
			"X-Request-Id": {"req_invalid_arguments"},
		},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 12, Name: "openai", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	_, err := svc.ResponseError(context.Background(), resp, c, provider, nil)

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, string(respBody), rec.Body.String())
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	require.True(t, IsResponseCommitted(c))
	require.Equal(t, http.StatusBadRequest, c.MustGet(OpsUpstreamStatusCodeKey))
}

func TestOpenAIHandleErrorResponse_TransientInvalidRequest400KeepsGatewayError(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := newResponseOutputForTest(OpenAIResponseOptions{})
	respBody := []byte(`{"error":{"message":"An error occurred while processing your request. You can retry your request, or contact us through our help center at help.openai.com if the error persists. Please include the request ID req_123 in your message.","type":"invalid_request_error"}}`)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header:     http.Header{},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 12, Name: "openai", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	_, err := svc.ResponseError(context.Background(), resp, c, provider, nil)

	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "Upstream request failed")
}

func TestOpenAIHandleErrorResponse_OtherInvalidRequest400PassesThroughDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := newResponseOutputForTest(OpenAIResponseOptions{})
	respBody := []byte(`{"error":{"message":"Unknown parameter: input[0].namespace","type":"invalid_request_error","param":"input[0].namespace","code":"unknown_parameter"}}`)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header:     http.Header{},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 12, Name: "openai", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	_, err := svc.ResponseError(context.Background(), resp, c, provider, nil)

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Equal(t, "unknown_parameter", gjson.Get(rec.Body.String(), "error.code").String())
	require.Equal(t, "input[0].namespace", gjson.Get(rec.Body.String(), "error.param").String())
	require.Equal(t, "Unknown parameter: input[0].namespace", gjson.Get(rec.Body.String(), "error.message").String())
}

func TestOpenAIHandleCompatErrorResponse_InvalidRequest400PreservesDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	svc := newResponseOutputForTest(OpenAIResponseOptions{})
	respBody := []byte(`{"error":{"message":"Invalid property name in input arguments","type":"invalid_request_error","param":"input[35].arguments","code":"property_name_above_max_length"}}`)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header:     http.Header{},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 12, Name: "openai", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}

	_, err := svc.CompatError(resp, c, provider, WriteForwardChatError, WriteForwardChatErrorBody)

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, string(respBody), rec.Body.String())
	require.Contains(t, rec.Body.String(), "property_name_above_max_length")
	require.Contains(t, rec.Body.String(), "input[35].arguments")
}

func TestOpenAIHandleCompatMessagesErrorResponse_InvalidRequest400PreservesDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := newResponseOutputForTest(OpenAIResponseOptions{})
	respBody := []byte(`{"error":{"message":"Invalid property name in input arguments","type":"invalid_request_error","param":"input[35].arguments","code":"property_name_above_max_length"}}`)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header:     http.Header{},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 12, Name: "openai", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}

	_, err := svc.CompatError(resp, c, provider, WriteForwardAnthropicError, WriteForwardAnthropicErrorBody)

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, `{"type":"error","error":{"message":"Invalid property name in input arguments","type":"invalid_request_error","param":"input[35].arguments","code":"property_name_above_max_length"}}`, rec.Body.String())
}

func TestOpenAIHandleErrorResponse_ContextWindow502KeepsMessageWithoutFailover(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	svc := newResponseOutputForTest(OpenAIResponseOptions{})
	respBody := []byte(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"upstream_error","code":null}}`)
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header:     http.Header{},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 14, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	_, err := svc.ResponseError(context.Background(), resp, c, provider, nil)
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	assert.Equal(t, http.StatusBadGateway, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errField["type"])
	assert.Equal(t, "Your input exceeds the context window of this model. Please adjust your input and try again.", errField["message"])
}

func TestOpenAIHandleErrorResponse_AppliesRuleFor422(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	ruleSvc := gatewaytestkit.ErrorRules([]*errorpolicy.ErrorPassthroughRule{gatewaytestkit.NonFailoverRule(http.StatusUnprocessableEntity, "invalid schema", http.StatusTeapot, "OpenAI上游失败")})
	BindErrorPassthroughService(c, ruleSvc)

	svc := newResponseOutputForTest(OpenAIResponseOptions{})
	respBody := []byte(`{"error":{"message":"Invalid schema for field messages"}}`)
	resp := &http.Response{
		StatusCode: http.StatusUnprocessableEntity,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header:     http.Header{},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	_, err := svc.ResponseError(context.Background(), resp, c, provider, nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusTeapot, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errField["type"])
	assert.Equal(t, "OpenAI上游失败", errField["message"])
}

func TestOpenAIHandleErrorResponse_SetsResponseCommitted(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := newResponseOutputForTest(OpenAIResponseOptions{})
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"message":"rate limit exceeded"}}`))),
		Header:     http.Header{},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 101, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	_, err := svc.ResponseError(context.Background(), resp, c, provider, nil)
	require.Error(t, err)
	assert.True(t, IsResponseCommitted(c), "OpenAI non-failover path must mark response committed")
}

func newOpenAIUpstreamClientErrorResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newOpenAIUpstreamClientErrorTestProvider() *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Name: "acct"}}
}

// newOpenAIUpstreamErrorTestContext creates an upstream error fixture.
func newOpenAIUpstreamErrorTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	return newOpenAIUpstreamClientErrorTestContext()
}

func newOpenAIUpstreamErrorResponse(statusCode int, body string) *http.Response {
	return newOpenAIUpstreamClientErrorResponse(statusCode, body)
}

func newOpenAIUpstreamErrorTestProvider() *gatewayprovider.ExecutionProvider {
	return newOpenAIUpstreamClientErrorTestProvider()
}

func TestHandleErrorResponse_Deterministic400IsNotRewrappedAs502(t *testing.T) {
	c, recorder := newOpenAIUpstreamClientErrorTestContext()
	svc := newResponseOutputForTest(OpenAIResponseOptions{})

	_, err := svc.ResponseError(
		context.Background(),
		newOpenAIUpstreamClientErrorResponse(http.StatusBadRequest, openAIInvalidFunctionParametersBody),
		c, newOpenAIUpstreamClientErrorTestProvider(), nil,
	)

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
	require.Equal(t, "invalid_function_parameters", gjson.Get(recorder.Body.String(), "error.code").String())
	require.Equal(t, "input[8].tools[1].tools[2].parameters", gjson.Get(recorder.Body.String(), "error.param").String())
	require.Contains(t, gjson.Get(recorder.Body.String(), "error.message").String(), "automation_update")

	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
}

func TestHandleErrorResponse_Deterministic400MatchesCompatSibling(t *testing.T) {
	svc := newResponseOutputForTest(OpenAIResponseOptions{})
	nativeCtx, nativeRecorder := newOpenAIUpstreamClientErrorTestContext()
	_, nativeErr := svc.ResponseError(
		context.Background(),
		newOpenAIUpstreamClientErrorResponse(http.StatusBadRequest, openAIInvalidFunctionParametersBody),
		nativeCtx, newOpenAIUpstreamClientErrorTestProvider(), nil,
	)
	require.Error(t, nativeErr)

	compatCtx, _ := newOpenAIUpstreamClientErrorTestContext()
	var compatStatus int
	var compatType, compatMessage string
	writeError := func(_ *gin.Context, statusCode int, errType, message string) {
		compatStatus, compatType, compatMessage = statusCode, errType, message
	}
	_, compatErr := svc.CompatError(
		newOpenAIUpstreamClientErrorResponse(http.StatusBadRequest, openAIInvalidFunctionParametersBody),
		compatCtx, newOpenAIUpstreamClientErrorTestProvider(), writeError, WriteForwardChatErrorBody,
	)
	require.Error(t, compatErr)
	require.Equal(t, compatStatus, nativeRecorder.Code)
	require.Equal(t, compatType, gjson.Get(nativeRecorder.Body.String(), "error.type").String())
	require.Equal(t, compatMessage, gjson.Get(nativeRecorder.Body.String(), "error.message").String())
}

func TestHandleErrorResponse_Transient400KeepsGenericGatewayError(t *testing.T) {
	c, recorder := newOpenAIUpstreamClientErrorTestContext()
	svc := newResponseOutputForTest(OpenAIResponseOptions{})
	body := `{"error":{"message":"An error occurred while processing your request. You can retry your request.","type":"invalid_request_error"}}`

	_, err := svc.ResponseError(
		context.Background(),
		newOpenAIUpstreamClientErrorResponse(http.StatusBadRequest, body),
		c, newOpenAIUpstreamClientErrorTestProvider(), nil,
	)

	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.Equal(t, "upstream_error", gjson.Get(recorder.Body.String(), "error.type").String())
}

func TestHandleErrorResponse_PoolRetryable400StillFailsOver(t *testing.T) {
	c, recorder := newOpenAIUpstreamClientErrorTestContext()
	svc := newResponseOutputForTest(OpenAIResponseOptions{})
	provider := newOpenAIUpstreamClientErrorTestProvider()
	provider.Record.Type = capability.ProviderTypeAPIKey
	provider.Record.Credentials = map[string]any{
		"pool_mode":                    true,
		"pool_mode_retry_status_codes": []any{float64(http.StatusBadRequest)},
	}

	_, err := svc.ResponseError(
		context.Background(),
		newOpenAIUpstreamClientErrorResponse(http.StatusBadRequest, openAIInvalidFunctionParametersBody),
		c, provider, nil,
	)

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadRequest, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameProvider)
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())
}

// TestHandleErrorResponse_NonDeterministicStatusesKeepGeneric502 checks generic 502 handling for default
// statuses such as 404, 422, and 5xx.
func TestHandleErrorResponse_NonDeterministicStatusesKeepGeneric502(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		body       string
		wantStatus int
		wantType   string
		wantMsg    string
	}{
		// A 404 or 405 can indicate an operator base_url error, so it is not exposed as a client request error.
		{
			"not_found", http.StatusNotFound, `{"error":{"message":"Unknown request URL"}}`,
			http.StatusBadGateway, "upstream_error", "Upstream request failed",
		},
		{
			"unprocessable", http.StatusUnprocessableEntity, `{"error":{"message":"Invalid schema for field messages"}}`,
			http.StatusBadGateway, "upstream_error", "Upstream request failed",
		},
		// Credential and billing errors from 401, 402, and 403 are mapped to client-facing statuses.
		// Free-form 403 text cannot establish a durable access-state failure; a structured code is required.
		{
			"unauthorized", http.StatusUnauthorized, `{"error":{"message":"Incorrect API key provided: sk-abc"}}`,
			http.StatusBadGateway, "upstream_error", "Upstream authentication failed, please contact administrator",
		},
		{
			"forbidden", http.StatusForbidden, `{"error":{"message":"Your provider is deactivated"}}`,
			http.StatusBadGateway, "upstream_error", "Upstream access forbidden, please contact administrator",
		},
		// Rate limiting has its own status mapping.
		{
			"rate_limited", http.StatusTooManyRequests, `{"error":{"message":"Rate limit reached"}}`,
			http.StatusTooManyRequests, "rate_limit_error", "Upstream rate limit exceeded, please retry later",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newOpenAIUpstreamErrorTestContext(t)
			svc := newResponseOutputForTest(OpenAIResponseOptions{Configured: true})

			_, err := svc.ResponseError(
				context.Background(),
				newOpenAIUpstreamErrorResponse(tc.statusCode, tc.body),
				c, newOpenAIUpstreamErrorTestProvider(), nil,
			)
			require.Error(t, err)
			if tc.name == "forbidden" {
				var failoverErr *forwardcore.UpstreamFailoverError
				require.False(t, errors.As(err, &failoverErr))
			}
			require.Equal(t, tc.wantStatus, rec.Code)
			require.Equal(t, tc.wantType, gjson.Get(rec.Body.String(), "error.type").String())
			require.Equal(t, tc.wantMsg, gjson.Get(rec.Body.String(), "error.message").String())
		})
	}
}

// TestHandleErrorResponse_PassthroughRuleStillWinsOver400Branch checks that administrator passthrough rules
// precede deterministic 400 handling.
func TestHandleErrorResponse_PassthroughRuleStillWinsOver400Branch(t *testing.T) {
	c, rec := newOpenAIUpstreamErrorTestContext(t)
	ruleSvc := gatewaytestkit.ErrorRules([]*errorpolicy.ErrorPassthroughRule{
		gatewaytestkit.NonFailoverRule(http.StatusBadRequest, "automation_update", http.StatusTeapot, "自定义文案"),
	})
	BindErrorPassthroughService(c, ruleSvc)
	svc := newResponseOutputForTest(OpenAIResponseOptions{})

	_, err := svc.ResponseError(
		context.Background(),
		newOpenAIUpstreamClientErrorResponse(http.StatusBadRequest, openAIInvalidFunctionParametersBody),
		c, newOpenAIUpstreamClientErrorTestProvider(), nil,
	)

	require.Error(t, err)
	require.Equal(t, http.StatusTeapot, rec.Code)
	require.Equal(t, "自定义文案", gjson.Get(rec.Body.String(), "error.message").String())
}

func TestOpenAIUpstreamErrorBodyReadLimitForConfig_RespectsDiagnosticLimit(t *testing.T) {
	output := &OpenAIResponseOutput{Options: OpenAIResponseOptions{
		LogUpstreamErrorBody:         true,
		LogUpstreamErrorBodyMaxBytes: int(512<<10) + 1024,
	}}

	require.Equal(t, int64(output.Options.LogUpstreamErrorBodyMaxBytes), output.errorBodyReadLimit())
}

func (panicOnReadCloser) Read(_ []byte) (int, error) {
	panic("response body should not be reread")
}

func (panicOnReadCloser) Close() error { return nil }

func TestOpenAIGatewayService_HandleFailoverSideEffects_DoesNotRereadResponseBody(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 88,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{},
		Body:       panicOnReadCloser{},
	}

	require.NotPanics(t, func() {
		svc.Output.ApplyHTTPFailure(context.Background(), resp, provider, []byte(`{"error":{"type":"rate_limit_error","message":"rate limited"}}`))
	})

	require.False(t, svc.Output.Health.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
	require.True(t, provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, provider.View(), nil, nil))
}

func TestLogOpenAIInstructionsRequiredDebug_LogsRequestDetails(t *testing.T) {
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses?trace=1", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "curl/8.0")
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("OpenAI-Beta", "assistants=v2")

	body := []byte(`{"model":"gpt-5.1-codex","stream":false,"prompt_cache_key":"pc-abc","access_token":"secret-token","input":[{"type":"text","text":"hello"}]}`)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1001, Name: "codex max套餐"}}

	LogOpenAIInstructionsRequiredDebug(
		context.Background(),
		c,
		provider,
		http.StatusBadRequest,
		"Instructions are required",
		body,
		[]byte(`{"error":{"message":"Instructions are required","type":"invalid_request_error","param":"instructions","code":"missing_required_parameter"}}`),
	)

	require.True(t, logSink.ContainsMessageAtLevel("OpenAI 上游返回 Instructions are required，已记录请求详情用于排查", "warn"))
	require.True(t, logSink.ContainsFieldValue("request_user_agent", "curl/8.0"))
	require.True(t, logSink.ContainsFieldValue("request_model", "gpt-5.1-codex"))
	require.True(t, logSink.ContainsFieldValue("request_query", "trace=1"))
	require.True(t, logSink.ContainsFieldValue("provider_name", "codex max套餐"))
	require.True(t, logSink.ContainsFieldValue("request_headers", "openai-beta"))
	require.True(t, logSink.ContainsFieldValue("request_body_size", ""))
	require.False(t, logSink.ContainsFieldValue("request_body_preview", ""))
}

func TestLogOpenAIInstructionsRequiredDebug_NonTargetErrorSkipped(t *testing.T) {
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "curl/8.0")
	body := []byte(`{"model":"gpt-5.1-codex","stream":false}`)

	LogOpenAIInstructionsRequiredDebug(
		context.Background(),
		c,
		&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1001}},
		http.StatusForbidden,
		"forbidden",
		body,
		[]byte(`{"error":{"message":"forbidden"}}`),
	)

	require.False(t, logSink.ContainsMessage("OpenAI 上游返回 Instructions are required，已记录请求详情用于排查"))
}

func TestIsOpenAITransientProcessingError(t *testing.T) {
	require.True(t, openai.IsOpenAITransientProcessingError(
		http.StatusBadRequest,
		"An error occurred while processing your request.",
		nil,
	))

	require.True(t, openai.IsOpenAITransientProcessingError(
		http.StatusBadRequest,
		"",
		[]byte(`{"error":{"message":"An error occurred while processing your request. You can retry your request, or contact us through our help center at help.openai.com if the error persists. Please include the request ID req_123 in your message."}}`),
	))

	require.True(t, openai.IsOpenAITransientProcessingError(
		http.StatusBadRequest,
		"The selected model is at capacity. Please try again later.",
		nil,
	))

	require.True(t, openai.IsOpenAITransientProcessingError(
		http.StatusBadRequest,
		"",
		[]byte(`{"error":{"code":"server_is_overloaded","message":"Please retry later.","type":"invalid_request_error"}}`),
	))

	require.True(t, openai.IsOpenAITransientProcessingError(
		http.StatusServiceUnavailable,
		"",
		[]byte(`{"error":{"code":"slow_down","message":"Please retry later."}}`),
	))

	require.True(t, openai.IsOpenAITransientProcessingError(
		http.StatusServiceUnavailable,
		"",
		[]byte(`{"error":{"message":"Our servers are currently overloaded. Please try again later."}}`),
	))

	require.True(t, openai.IsOpenAITransientProcessingError(
		http.StatusServiceUnavailable,
		"Server is overloaded. Please try again later.",
		nil,
	))

	require.True(t, openai.IsOpenAITransientProcessingError(
		http.StatusBadGateway,
		"",
		[]byte(`{"error":{"message":"Our servers are currently overloaded. Please try again later."}}`),
	))

	require.True(t, openai.IsOpenAITransientProcessingError(
		http.StatusBadRequest,
		"",
		[]byte(`{"error":{"message":"An error occurred while processing your request. You can retry your request, or contact us through our help center at help.openai.com if the error persists. Please include the request ID req_123 in your message."}}`),
	))

	require.False(t, openai.IsOpenAITransientProcessingError(
		http.StatusBadRequest,
		"Missing required parameter: 'instructions'",
		[]byte(`{"error":{"message":"Missing required parameter: 'instructions'"}}`),
	))
}

func TestIsOpenAIContextWindowError(t *testing.T) {
	require.True(t, openai.IsOpenAIContextWindowError(
		"",
		[]byte(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"upstream_error","code":null}}`),
	))
	require.True(t, openai.IsOpenAIContextWindowError(
		"maximum context length exceeded",
		nil,
	))
	require.True(t, openai.IsOpenAIContextWindowError(
		"",
		[]byte(`maximum context length exceeded`),
	))
	require.False(t, openai.IsOpenAIContextWindowError(
		"context canceled",
		nil,
	))
	require.False(t, openai.IsOpenAIContextWindowError(
		"upstream unavailable",
		[]byte(`{"error":{"message":"upstream unavailable","code":"upstream_error"},"echo":"context_length_exceeded maximum context length"}`),
	))
}

func TestOpenAITransientAndCapacityClassificationIgnoresEchoedJSON(t *testing.T) {
	body := []byte(`{"error":{"message":"upstream unavailable","code":"upstream_error"},"echo":"server is overloaded; selected model is at capacity"}`)

	require.False(t, openai.IsOpenAITransientProcessingError(http.StatusBadRequest, "upstream unavailable", body))
	require.False(t, openai.IsOpenAIRequestScopedCapacityShed("upstream unavailable", body))

	plainText := []byte(`server is overloaded; please retry later`)
	require.True(t, openai.IsOpenAITransientProcessingError(http.StatusServiceUnavailable, "", plainText))
	require.True(t, openai.IsOpenAIRequestScopedCapacityShed("", plainText))
}

func TestShouldFailoverOpenAIUpstreamResponseContextWindow502(t *testing.T) {
	body := []byte(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"upstream_error","code":null}}`)

	require.False(t, gatewayprovider.ShouldFailoverOpenAIResponse(http.StatusBadGateway, "", body))
	require.True(t, gatewayprovider.ShouldFailoverOpenAIResponse(http.StatusBadGateway, "temporary upstream outage", []byte(`{"error":{"message":"temporary upstream outage"}}`)))
	require.True(t, gatewayprovider.ShouldFailoverOpenAIResponse(
		http.StatusBadGateway,
		"temporary upstream outage",
		[]byte(`{"error":{"message":"temporary upstream outage"},"echo":"context_length_exceeded"}`),
	))
}

func TestOpenAIShouldFailoverUpstreamResponse_CyberWarningDoesNotFailover(t *testing.T) {
	body := []byte(`{"error":{"message":"This request has been flagged for potentially high-risk cyber activity."}}`)

	require.False(t,
		gatewayprovider.ShouldFailoverOpenAIResponse(http.StatusUnauthorized, "This request has been flagged for potentially high-risk cyber activity.", body),
	)
	require.True(t,
		gatewayprovider.ShouldFailoverOpenAIResponse(http.StatusUnauthorized, "User account is not active", []byte(`{"code":"USER_INACTIVE","message":"User account is not active"}`)),
	)
}

func TestOpenAIHandleErrorResponse_CyberWarningPassesThroughMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	svc := newResponsesFixture(responsesFixtureInputs{})
	message := "This request has been flagged for potentially high-risk cyber activity."
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"` + message + `"}}`)),
		Header:     http.Header{},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Name: "openai", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	_, err := svc.Output.ResponseError(context.Background(), resp, c, provider, []byte(`{"model":"gpt-5"}`))

	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), message)
	warning, ok := forwardcore.WarningFromError(err)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, warning.StatusCode)
	require.Contains(t, warning.Message, "high-risk cyber")
}
