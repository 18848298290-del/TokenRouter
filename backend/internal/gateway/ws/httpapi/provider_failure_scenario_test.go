package httpapi

// Provider failure scenarios cover error classification, reconnect conditions, and provider retry deadlines.

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func TestClassifyOpenAIWSReadFallbackReason(t *testing.T) {
	require.Equal(t, "policy_violation", gatewayprovider.ClassifyOpenAIWSReadFallbackReason(coderws.CloseError{Code: coderws.StatusPolicyViolation}))
	require.Equal(t, "message_too_big", gatewayprovider.ClassifyOpenAIWSReadFallbackReason(coderws.CloseError{Code: coderws.StatusMessageTooBig}))
	require.Equal(t, "read_event", gatewayprovider.ClassifyOpenAIWSReadFallbackReason(errors.New("io")))
}

func TestShouldForceNewConnOnStoreDisabled(t *testing.T) {
	require.True(t, openai.ShouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeStrict, ""))
	require.False(t, openai.ShouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeOff, "policy_violation"))

	require.True(t, openai.ShouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeAdaptive, "policy_violation"))
	require.True(t, openai.ShouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeAdaptive, "prewarm_message_too_big"))
	require.False(t, openai.ShouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeAdaptive, "read_event"))
}

func TestOpenAIWSRateLimitFailoverError_OAuthKeepsSameProviderDeadline(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	headers := http.Header{"Retry-After": []string{"30"}}
	body := []byte(`{"error":{"type":"rate_limit_error","message":"limited"}}`)

	oauthErr := (gatewayprovider.OpenAIFailoverPolicy{Health: svc.Output.Health}).NewProviderFailure(&gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 904,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
		},
	}, http.StatusTooManyRequests, headers, body, strings.TrimSpace("limited"), false, false)
	require.True(t, oauthErr.RetryableOnSameProvider)
	require.False(t, oauthErr.SameProviderRetryDeadline.IsZero())
	require.Positive(t, oauthErr.SameProviderRetryDelay)
	require.LessOrEqual(t, oauthErr.SameProviderRetryDelay, 8*time.Second)
	require.Equal(t, body, oauthErr.ResponseBody)
	require.Equal(t, "30", http.Header(oauthErr.ResponseHeaders).Get("Retry-After"))

	apiKeyErr := (gatewayprovider.OpenAIFailoverPolicy{Health: svc.Output.Health}).NewProviderFailure(&gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 905,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
		},
	}, http.StatusTooManyRequests, headers, body, strings.TrimSpace("limited"), false, false)
	require.False(t, apiKeyErr.RetryableOnSameProvider)
	require.True(t, apiKeyErr.SameProviderRetryDeadline.IsZero())
	require.Zero(t, apiKeyErr.SameProviderRetryDelay)
}
