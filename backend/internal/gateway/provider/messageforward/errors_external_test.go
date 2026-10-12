package messageforward_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/messageforward"
	gatewaytelemetry "github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

type errorRulesFixtureRepo struct {
	errorpolicy.ErrorPassthroughRepository
	rules []*errorpolicy.ErrorPassthroughRule
}

// TestAnthropicErrorEntryRuleAndMonitoring checks the HTTP adapter's error response and monitoring flag.
func TestAnthropicErrorEntryRuleAndMonitoring(t *testing.T) {
	for _, tc := range []struct {
		name        string
		retry, skip bool
	}{
		{name: "normal_skip", skip: true},
		{name: "normal_keep"},
		{name: "retry_skip", retry: true, skip: true},
		{name: "retry_keep", retry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			rule := newNonFailoverPassthroughRule(http.StatusUnprocessableEntity, "invalid schema", http.StatusTeapot, "规则安全消息")
			rule.SkipMonitoring = tc.skip
			gatewayhttp.BindErrorPassthroughService(c, newErrorRulesTestService([]*errorpolicy.ErrorPassthroughRule{rule}))
			resp := &http.Response{
				StatusCode: http.StatusUnprocessableEntity,
				Header:     http.Header{},
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"message":"Invalid schema in upstream request"}}`))),
			}
			provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey}}
			svc := messageforward.NewRuntime(messageforward.Dependencies{}, messageforward.Options{})
			var result *forwardcore.Result
			var err error
			if tc.retry {
				result, err = messageforward.ErrorForTest(svc, context.Background(), gatewayhttp.NewMessageForwardBoundary(c, nil), provider, resp, true)
			} else {
				result, err = messageforward.ErrorForTest(svc, context.Background(), gatewayhttp.NewMessageForwardBoundary(c, nil), provider, resp, false)
			}
			require.Nil(t, result)
			require.ErrorContains(t, err, "passthrough rule matched")
			require.Equal(t, http.StatusTeapot, recorder.Code)
			require.True(t, gatewayhttp.IsResponseCommitted(c))
			var payload struct {
				Type  string                         `json:"type"`
				Error struct{ Type, Message string } `json:"error"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
			require.Equal(t, "error", payload.Type)
			require.Equal(t, "upstream_error", payload.Error.Type)
			require.Equal(t, "规则安全消息", payload.Error.Message)
			flag, present := c.Get(gatewayhttp.OpsSkipPassthroughKey)
			require.Equal(t, tc.skip, present)
			if tc.skip {
				require.Equal(t, true, flag)
			}
		})
	}
}

func (r errorRulesFixtureRepo) List(context.Context) ([]*errorpolicy.ErrorPassthroughRule, error) {
	return r.rules, nil
}

func newErrorRulesTestService(rules []*errorpolicy.ErrorPassthroughRule) *errorpolicy.ErrorPassthroughService {
	s := errorpolicy.NewErrorPassthroughService(errorRulesFixtureRepo{rules: rules}, nil, gatewaytelemetry.ErrorRules)
	if err := s.StartContext(context.Background()); err != nil {
		panic(err)
	}
	return s
}

func newNonFailoverPassthroughRule(statusCode int, keyword string, respCode int, customMessage string) *errorpolicy.ErrorPassthroughRule {
	return &errorpolicy.ErrorPassthroughRule{
		ID:              1,
		Name:            "non-failover-rule",
		Enabled:         true,
		Priority:        1,
		ErrorCodes:      []int{statusCode},
		Keywords:        []string{keyword},
		MatchMode:       errorpolicy.MatchModeAll,
		PassthroughCode: false,
		ResponseCode:    &respCode,
		PassthroughBody: false,
		CustomMessage:   &customMessage,
	}
}

func TestGatewayHandleErrorResponse_NoRuleKeepsDefault(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := messageforward.NewRuntime(messageforward.Dependencies{}, messageforward.Options{})
	respBody := []byte(`{"error":{"message":"Invalid schema for field messages"}}`)
	resp := &http.Response{
		StatusCode: http.StatusUnprocessableEntity,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header:     http.Header{},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 11, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey}}

	_, err := messageforward.ErrorForTest(svc, context.Background(), gatewayhttp.NewMessageForwardBoundary(c, nil), provider, resp, false)
	require.Error(t, err)
	assert.Equal(t, http.StatusBadGateway, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errField["type"])
	assert.Equal(t, "Upstream request failed", errField["message"])
}

func TestGatewayHandleErrorResponse_AppliesRuleFor422(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	ruleSvc := newErrorRulesTestService([]*errorpolicy.ErrorPassthroughRule{newNonFailoverPassthroughRule(http.StatusUnprocessableEntity, "invalid schema", http.StatusTeapot, "上游请求失败")})
	gatewayhttp.BindErrorPassthroughService(c, ruleSvc)

	svc := messageforward.NewRuntime(messageforward.Dependencies{}, messageforward.Options{})
	respBody := []byte(`{"error":{"message":"Invalid schema for field messages"}}`)
	resp := &http.Response{
		StatusCode: http.StatusUnprocessableEntity,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header:     http.Header{},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey}}

	_, err := messageforward.ErrorForTest(svc, context.Background(), gatewayhttp.NewMessageForwardBoundary(c, nil), provider, resp, false)
	require.Error(t, err)
	assert.Equal(t, http.StatusTeapot, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errField["type"])
	assert.Equal(t, "上游请求失败", errField["message"])
}

func TestHandleErrorResponse_SetsResponseCommitted(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := messageforward.NewRuntime(messageforward.Dependencies{}, messageforward.Options{})
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"message":"temperature: range: 0..1"}}`))),
		Header:     http.Header{},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 100, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey}}

	_, err := messageforward.ErrorForTest(svc, context.Background(), gatewayhttp.NewMessageForwardBoundary(c, nil), provider, resp, false)
	require.Error(t, err)
	assert.True(t, gatewayhttp.IsResponseCommitted(c), "non-failover error path must mark response committed")
	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
}

func TestHandleErrorResponse_PassthroughRuleSetsCommitted(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	ruleSvc := newErrorRulesTestService([]*errorpolicy.ErrorPassthroughRule{
		newNonFailoverPassthroughRule(http.StatusBadRequest, "temperature", http.StatusBadRequest, "参数错误"),
	})
	gatewayhttp.BindErrorPassthroughService(c, ruleSvc)

	svc := messageforward.NewRuntime(messageforward.Dependencies{}, messageforward.Options{})
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"message":"temperature: range: 0..1"}}`))),
		Header:     http.Header{},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 200, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey}}

	_, err := messageforward.ErrorForTest(svc, context.Background(), gatewayhttp.NewMessageForwardBoundary(c, nil), provider, resp, false)
	require.Error(t, err)
	assert.True(t, gatewayhttp.IsResponseCommitted(c), "passthrough rule path must mark response committed")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok, "payload[\"error\"] should be map[string]any")
	assert.Equal(t, "参数错误", errField["message"])
}
