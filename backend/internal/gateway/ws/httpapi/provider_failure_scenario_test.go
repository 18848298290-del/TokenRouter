package httpapi

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

// WS provider failures use close status, disabled-store mode and HTTP retry
// policy to select different recovery actions. These scenarios retain
// diagnostic body and headers while checking that OAuth rate-limit retries
// receive a deadline and bounded delay, unlike ordinary API Key 429 behavior.

// TestClassifyOpenAIWSReadFallbackReason compares policy-violation,
// message-too-big and generic read errors. The shared classifier returns their
// distinct fallback reason labels for pool reconnect diagnostics.
func TestClassifyOpenAIWSReadFallbackReason(t *testing.T) {
	require.Equal(t, "policy_violation", gatewayprovider.ClassifyOpenAIWSReadFallbackReason(coderws.CloseError{Code: coderws.StatusPolicyViolation}))
	require.Equal(t, "message_too_big", gatewayprovider.ClassifyOpenAIWSReadFallbackReason(coderws.CloseError{Code: coderws.StatusMessageTooBig}))
	require.Equal(t, "read_event", gatewayprovider.ClassifyOpenAIWSReadFallbackReason(errors.New("io")))
}

// TestShouldForceNewConnOnStoreDisabled compares strict, off and adaptive
// policies after several read outcomes. Strict always selects a new
// connection, off never does, and adaptive selects known policy or size
// failures including a prewarm prefix.
func TestShouldForceNewConnOnStoreDisabled(t *testing.T) {
	require.True(t, openai.ShouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeStrict, ""))
	require.False(t, openai.ShouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeOff, "policy_violation"))

	require.True(t, openai.ShouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeAdaptive, "policy_violation"))
	require.True(t, openai.ShouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeAdaptive, "prewarm_message_too_big"))
	require.False(t, openai.ShouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeAdaptive, "read_event"))
}

// TestOpenAIWSRateLimitFailoverError_OAuthKeepsSameProviderDeadline compares
// OAuth and API Key 429 failures with a Retry-After header. OAuth retains
// same-provider retry with a deadline and delay at most eight seconds; API Key
// has neither, and OAuth diagnostics retain the original body and header.
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
