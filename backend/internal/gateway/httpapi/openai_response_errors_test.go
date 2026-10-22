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

// openAIInvalidFunctionParametersBody supplies the fixed automation schema rejection used
// to compare deterministic native/compatible error output and provider-pool recovery.
const openAIInvalidFunctionParametersBody = `{"error":{` +
	`"message":"Invalid schema for function 'automation_update': expected an object.",` +
	`"type":"invalid_request_error",` +
	`"param":"input[8].tools[1].tools[2].parameters",` +
	`"code":"invalid_function_parameters"}}`

// panicOnReadCloser rejects any body reread in captured-error health fixtures.
type panicOnReadCloser struct{}

// TestOpenAIHandleErrorResponse_NoRuleKeepsDefault supplies an upstream HTTP
// 422 without a presentation rule. It checks a returned error and the default
// JSON 502 upstream-error type and fixed message.
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

// TestOpenAIHandleErrorResponse_InvalidRequest400PassesThrough supplies a
// structured parameter rejection with upstream JSON headers. It checks exact
// error JSON, HTTP 400, content type and committed request/status
// observations.
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

// TestOpenAIHandleErrorResponse_TransientInvalidRequest400KeepsGatewayError
// supplies temporary-processing wording with invalid_request_error type. It
// checks final HTTP 502 and generic gateway text rather than presenting this
// fixture as deterministic input rejection.
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

// TestOpenAIHandleErrorResponse_OtherInvalidRequest400PassesThroughDetails
// supplies an unknown namespace parameter. It checks HTTP 400 with retained
// type, code, parameter and message fields in client output.
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

// TestOpenAIHandleCompatErrorResponse_InvalidRequest400PreservesDetails
// invokes Chat-compatible handling for a structured argument rejection. It
// checks HTTP 400 and the exact JSON body including code and parameter
// identity.
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

// TestOpenAIHandleCompatMessagesErrorResponse_InvalidRequest400PreservesDetails
// invokes Messages-compatible handling for the same structured parameter
// rejection. It checks HTTP 400 and the Anthropic envelope retaining the
// nested error fields.
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

// TestOpenAIHandleErrorResponse_ContextWindow502KeepsMessageWithoutFailover
// wraps context-window text in HTTP 502. It checks no recovery wrapper,
// retained upstream_error type and the exact context-window message in the
// decoded client envelope.
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

// TestOpenAIHandleErrorResponse_AppliesRuleFor422 matches a configured schema
// rule against an upstream HTTP 422. It checks the overridden HTTP 418,
// upstream_error category and configured client message.
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

// TestOpenAIHandleErrorResponse_SetsResponseCommitted handles the fixture
// final HTTP 429 rejection. It checks a returned error and the committed
// response marker used by outer error handling.
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

// newOpenAIUpstreamClientErrorResponse builds an HTTP error fixture with the
// supplied status and body text. It returns a JSON header and readable body;
// callers keep this response live during handling and retain body cleanup.
func newOpenAIUpstreamClientErrorResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// newOpenAIUpstreamClientErrorTestProvider creates the OpenAI OAuth target
// used by HTTP error fixtures.
func newOpenAIUpstreamClientErrorTestProvider() *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Name: "acct"}}
}

// newOpenAIUpstreamErrorTestContext delegates creation of a live Gin request
// and response recorder.
func newOpenAIUpstreamErrorTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	return newOpenAIUpstreamClientErrorTestContext()
}

// newOpenAIUpstreamErrorResponse returns a readable JSON error fixture with
// the supplied status and body. Callers keep it live through handling and
// retain response-body cleanup, as with the underlying fixture constructor.
func newOpenAIUpstreamErrorResponse(statusCode int, body string) *http.Response {
	return newOpenAIUpstreamClientErrorResponse(statusCode, body)
}

// newOpenAIUpstreamErrorTestProvider delegates creation of the HTTP error
// fixture provider.
func newOpenAIUpstreamErrorTestProvider() *gatewayprovider.ExecutionProvider {
	return newOpenAIUpstreamClientErrorTestProvider()
}

// TestHandleErrorResponse_Deterministic400IsNotRewrappedAs502 supplies the
// automation parameter-schema error fixture. It checks HTTP 400, retained
// classification/parameter/message and absence of a typed recovery error.
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

// TestHandleErrorResponse_Deterministic400MatchesCompatSibling handles the
// same parameter-schema fixture through native and compatible output. It
// checks native JSON status, type and message against values captured by the
// compatible writer.
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

// TestHandleErrorResponse_Transient400KeepsGenericGatewayError supplies
// temporary-processing HTTP 400 wording. It checks a returned error with
// generic HTTP 502 and upstream_error classification for that fixture.
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

// TestHandleErrorResponse_PoolRetryable400StillFailsOver admits HTTP 400 in
// the API-key pool retry settings. It checks typed same-provider recovery with
// the original status and no client commitment or body.
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

// TestHandleErrorResponse_NonDeterministicStatusesKeepGeneric502 enumerates
// URL, schema, authentication, access and rate-limit rejections. It checks
// each listed final status, category and message, including no credential
// recovery for free-form forbidden wording.
func TestHandleErrorResponse_NonDeterministicStatusesKeepGeneric502(t *testing.T) {
	cases := []struct {
		name       string // fixture case name
		statusCode int    // upstream HTTP failure status
		body       string // upstream error JSON payload
		wantStatus int    // expected client HTTP status
		wantType   string // expected client error category
		wantMsg    string // expected client error message
	}{
		// An unknown upstream URL maps to a gateway error in this fixture. Its
		// routing rejection does not enter deterministic input-error presentation.
		{
			"not_found", http.StatusNotFound, `{"error":{"message":"Unknown request URL"}}`,
			http.StatusBadGateway, "upstream_error", "Upstream request failed",
		},
		{
			"unprocessable", http.StatusUnprocessableEntity, `{"error":{"message":"Invalid schema for field messages"}}`,
			http.StatusBadGateway, "upstream_error", "Upstream request failed",
		},
		// Authentication and access failures use gateway statuses in this table.
		// Free-form forbidden wording alone is insufficient for the structured
		// access-state classifier used by these fixtures.
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

// TestHandleErrorResponse_PassthroughRuleStillWinsOver400Branch matches the
// schema error against an administrator rule. It checks HTTP 418 and
// configured text before the default deterministic-400 presentation can apply.
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

// TestOpenAIUpstreamErrorBodyReadLimitForConfig_RespectsDiagnosticLimit
// enables a diagnostic excerpt larger than the default error capture. It
// checks the capture limit expands to that configured byte quantity.
func TestOpenAIUpstreamErrorBodyReadLimitForConfig_RespectsDiagnosticLimit(t *testing.T) {
	output := &OpenAIResponseOutput{Options: OpenAIResponseOptions{
		LogUpstreamErrorBody:         true,
		LogUpstreamErrorBodyMaxBytes: int(512<<10) + 1024,
	}}

	require.Equal(t, int64(output.Options.LogUpstreamErrorBodyMaxBytes), output.errorBodyReadLimit())
}

// Read panics to expose any attempt to reread captured upstream error bytes.
func (panicOnReadCloser) Read(_ []byte) (int, error) {
	panic("response body should not be reread")
}

// Close satisfies fixture response cleanup without changing the panic-on-read
// behavior.
func (panicOnReadCloser) Close() error { return nil }

// TestOpenAIGatewayService_HandleFailoverSideEffects_DoesNotRereadResponseBody
// supplies captured rate-limit bytes beside a panic-on-read body. It checks
// nonpanic health handling, absent runtime block and eligible OAuth 429 retry.
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

// TestLogOpenAIInstructionsRequiredDebug_LogsRequestDetails logs recognized
// instructions rejection with client headers, query and a sensitive body
// fixture. It checks the listed metadata fields and absent body-preview field
// in the captured warning.
func TestLogOpenAIInstructionsRequiredDebug_LogsRequestDetails(t *testing.T) {
	// Capture the shared logger and restore it on return so this diagnostic
	// fixture does not leave its recording destination installed for later tests.
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

	require.True(t, logSink.ContainsMessageAtLevel("OpenAI upstream returned Instructions are required; request details logged for investigation", "warn"))
	require.True(t, logSink.ContainsFieldValue("request_user_agent", "curl/8.0"))
	require.True(t, logSink.ContainsFieldValue("request_model", "gpt-5.1-codex"))
	require.True(t, logSink.ContainsFieldValue("request_query", "trace=1"))
	require.True(t, logSink.ContainsFieldValue("provider_name", "codex max套餐"))
	require.True(t, logSink.ContainsFieldValue("request_headers", "openai-beta"))
	require.True(t, logSink.ContainsFieldValue("request_body_size", ""))
	require.False(t, logSink.ContainsFieldValue("request_body_preview", ""))
}

// TestLogOpenAIInstructionsRequiredDebug_NonTargetErrorSkipped supplies a
// forbidden response outside instructions-required classification. It checks
// the special warning message is absent from captured structured logs for that
// request.
func TestLogOpenAIInstructionsRequiredDebug_NonTargetErrorSkipped(t *testing.T) {
	// Capture the shared logger and restore it on return so this diagnostic
	// fixture does not leave its recording destination installed for later tests.
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

	require.False(t, logSink.ContainsMessage("OpenAI upstream returned Instructions are required; request details logged for investigation"))
}

// TestIsOpenAITransientProcessingError compares the listed processing,
// capacity and overload messages/codes with a missing-instructions rejection.
// It checks transient recognition in those fixtures and rejection of the
// input-error case.
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

// TestIsOpenAIContextWindowError compares nested, direct and plain-text
// context-window evidence with cancellation and echoed wording. It checks the
// listed positive forms and exclusion of the unrelated fixtures.
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

// TestOpenAITransientAndCapacityClassificationIgnoresEchoedJSON puts
// overload/capacity wording in an echo beside an ordinary error. It checks
// rejected echo classification and accepted direct plain-text overload
// evidence.
func TestOpenAITransientAndCapacityClassificationIgnoresEchoedJSON(t *testing.T) {
	body := []byte(`{"error":{"message":"upstream unavailable","code":"upstream_error"},"echo":"server is overloaded; selected model is at capacity"}`)

	require.False(t, openai.IsOpenAITransientProcessingError(http.StatusBadRequest, "upstream unavailable", body))
	require.False(t, openai.IsOpenAIRequestScopedCapacityShed("upstream unavailable", body))

	plainText := []byte(`server is overloaded; please retry later`)
	require.True(t, openai.IsOpenAITransientProcessingError(http.StatusServiceUnavailable, "", plainText))
	require.True(t, openai.IsOpenAIRequestScopedCapacityShed("", plainText))
}

// TestShouldFailoverOpenAIUpstreamResponseContextWindow502 compares a
// context-window HTTP 502 with temporary outage and echoed context text. It
// checks no recovery for the actual context rejection and recovery for the
// outage fixtures.
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

// TestOpenAIShouldFailoverUpstreamResponse_CyberWarningDoesNotFailover
// compares cyber-risk wording with inactive-account evidence at HTTP 401. It
// checks request-warning exclusion and provider recovery for the structured
// inactive fixture.
func TestOpenAIShouldFailoverUpstreamResponse_CyberWarningDoesNotFailover(t *testing.T) {
	body := []byte(`{"error":{"message":"This request has been flagged for potentially high-risk cyber activity."}}`)

	require.False(t,
		gatewayprovider.ShouldFailoverOpenAIResponse(http.StatusUnauthorized, "This request has been flagged for potentially high-risk cyber activity.", body),
	)
	require.True(t,
		gatewayprovider.ShouldFailoverOpenAIResponse(http.StatusUnauthorized, "User account is not active", []byte(`{"code":"USER_INACTIVE","message":"User account is not active"}`)),
	)
}

// TestOpenAIHandleErrorResponse_CyberWarningPassesThroughMessage supplies HTTP
// 403 with the risk-warning message. It checks retained client status/text and
// warning-chain status/message for this request-specific rejection.
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
