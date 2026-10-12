package httpapi

// Passthrough policy scenarios cover service-tier filtering, client frames, usage snapshots, and turn transitions.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	protocolforward "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	protocolbridge "github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	openaiwsv2 "github.com/TokenFlux/TokenRouter/internal/upstream/openai/ws/relay"
)

// fakePassthroughFrameConn returns queued frames, then ErrWSConnClosed, and records writes for assertions.
type fakePassthroughFrameConn struct {
	reads     [][]byte
	idx       int
	writes    [][]byte
	closeOnce bool
}

type openAIWSPolicyEnforcingFrameConn struct {
	inner       openaiwsv2.FrameConn
	filter      func(coderws.MessageType, []byte) ([]byte, *tierpolicy.BlockedError, error)
	writeFilter func(coderws.MessageType, []byte) ([]byte, error)
	onBlock     func(*tierpolicy.BlockedError)
	once        sync.Once
	core        gatewayws.FrameConn
}

type openAIWSClientFrameConn struct {
	interTurnStarted   chan struct{}
	waitingForNextTurn atomic.Bool
}

// openAIWSPassthroughUsageMeta holds session usage and reasoning effort.
type openAIWSPassthroughUsageMeta struct {
	*gatewayws.UsageMeta
	// Shared atomic reasoning-effort value.
	reasoningEffort *atomic.Pointer[string]
}

// openAIWSPassthroughTurnLifecycle wraps the shared turn barrier.
type openAIWSPassthroughTurnLifecycle struct{ *gatewayws.TurnLifecycle }

func TestWSResponseCreate_DefaultPassesPriorityAndNormalizesFast(t *testing.T) {
	svc := newWSFastPolicy(t, tierpolicy.Default())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	frame := []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"priority","input":[{"type":"input_text","text":"hi"}]}`)
	updated, blocked, err := gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String(), "default policy should preserve priority tier")
	// Tier normalization leaves the remaining request fields unchanged.
	require.Equal(t, "response.create", gjson.GetBytes(updated, "type").String())
	require.Equal(t, "gpt-5.5", gjson.GetBytes(updated, "model").String())
	require.Equal(t, "hi", gjson.GetBytes(updated, "input.0.text").String())

	frame = []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"fast"}`)
	updated, blocked, err = gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String(), "fast alias should normalize before reaching upstream")

	// Mixed case and surrounding whitespace also normalize to priority.
	frame = []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"  Fast  "}`)
	updated, blocked, err = gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String())
}

func TestWSResponseCreate_RejectsUltraBeforeUpstream(t *testing.T) {
	svc := newWSFastPolicy(t, tierpolicy.Default())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	frames := [][]byte{
		[]byte(`{"type":"response.create","model":"gpt-5.6-sol","reasoning":{"effort":"ultra"}}`),
		[]byte(`{"type":"session.update","session":{"model":"gpt-5.6-sol","reasoning":{"effort":"ultra"}}}`),
	}
	for _, frame := range frames {
		updated, blocked, err := gatewayws.ApplyServiceTierFrame(frame, "gpt-5.6-sol", svc.Input(context.Background(), provider, "gpt-5.6-sol"))
		require.ErrorContains(t, err, "not supported")
		require.Nil(t, blocked)
		require.Equal(t, frame, updated)
	}
}

func TestWSResponseCreate_ExplicitFilterStripsServiceTier(t *testing.T) {
	svc := newWSFastPolicy(t, openAIFastFilterPriorityPolicy())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	frame := []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"priority","input":[{"type":"input_text","text":"hi"}]}`)
	updated, blocked, err := gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.NotContains(t, string(updated), `"service_tier"`, "filter action should strip service_tier")

	frame = []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"fast"}`)
	updated, blocked, err = gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.NotContains(t, string(updated), `"service_tier"`)
}

func TestWSResponseCreate_UserScopedRuleOverridesGlobalRule(t *testing.T) {
	settings := &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{
			{
				ServiceTier: tierpolicy.OpenAIFastTierPriority,
				Action:      claude.BetaPolicyActionFilter,
				Scope:       claude.BetaPolicyScopeAll,
			},
			{
				ServiceTier: tierpolicy.OpenAIFastTierPriority,
				Action:      claude.BetaPolicyActionPass,
				Scope:       claude.BetaPolicyScopeAll,
				UserIDs:     []int64{42},
			},
		},
	}
	svc := newWSFastPolicy(t, settings)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	frame := []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"priority"}`)

	allowedUserCtx := apikey.WithAccessSnapshot(context.Background(), apikey.AccessSnapshot{PayerUserID: int64(42)})
	updated, blocked, err := gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(allowedUserCtx, provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String())

	otherUserCtx := apikey.WithAccessSnapshot(context.Background(), apikey.AccessSnapshot{PayerUserID: int64(43)})
	updated, blocked, err = gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(otherUserCtx, provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.NotContains(t, string(updated), `"service_tier"`)
}

func TestWSResponseCreate_ForcePriorityRewritesKnownTier(t *testing.T) {
	settings := &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{{
			ServiceTier: tierpolicy.OpenAIFastTierAny,
			Action:      tierpolicy.OpenAIFastPolicyActionForcePriority,
			Scope:       claude.BetaPolicyScopeAll,
		}},
	}
	svc := newWSFastPolicy(t, settings)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	for _, tier := range []string{"flex", "auto", "default", "scale", "fast", "priority", "ultrafast"} {
		frame := []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"` + tier + `"}`)
		updated, blocked, err := gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
		require.NoError(t, err)
		require.Nil(t, blocked)
		require.Equal(t, tierpolicy.OpenAIFastTierPriority, gjson.GetBytes(updated, "service_tier").String(),
			"tier %q should be forced to priority", tier)
	}
}

func TestWSResponseCreate_FlexPassThrough(t *testing.T) {
	svc := newWSFastPolicy(t, tierpolicy.Default())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	// The default policy has no rules, so flex passes through.
	frame := []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"flex"}`)
	updated, blocked, err := gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, "flex", gjson.GetBytes(updated, "service_tier").String(), "flex frames must reach upstream untouched under default policy")
}

func TestWSResponseCreate_BlockReturnsTypedError(t *testing.T) {
	settings := &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{{
			ServiceTier:    tierpolicy.OpenAIFastTierPriority,
			Action:         claude.BetaPolicyActionBlock,
			Scope:          claude.BetaPolicyScopeAll,
			ErrorMessage:   "ws fast blocked",
			ModelWhitelist: []string{"gpt-5.5"},
			FallbackAction: claude.BetaPolicyActionPass,
		}},
	}
	svc := newWSFastPolicy(t, settings)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	frame := []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"priority"}`)
	updated, blocked, err := gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.NotNil(t, blocked)
	require.Equal(t, "ws fast blocked", blocked.Message)
	// On block, payload returned unchanged so caller can inspect / log it.
	require.Equal(t, string(frame), string(updated))
}

func TestWSResponseCreate_NoServiceTierUntouched(t *testing.T) {
	svc := newWSFastPolicy(t, tierpolicy.Default())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	frame := []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)
	updated, blocked, err := gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, string(frame), string(updated), "no service_tier present must result in zero mutation")
}

func TestWSResponseCreate_NonResponseCreateFrameUntouched(t *testing.T) {
	settings := &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{{
			ServiceTier:    tierpolicy.OpenAIFastTierPriority,
			Action:         claude.BetaPolicyActionFilter,
			Scope:          claude.BetaPolicyScopeAll,
			ModelWhitelist: []string{"*"},
			FallbackAction: claude.BetaPolicyActionFilter,
		}},
	}
	svc := newWSFastPolicy(t, settings)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	// response.cancel leaves its service_tier field unchanged.
	frame := []byte(`{"type":"response.cancel","service_tier":"priority"}`)
	updated, blocked, err := gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, string(frame), string(updated))
}

// TestWSResponseCreate_EmptyTypeFrameUntouched checks that missing or empty types bypass response.create policy.
func TestWSResponseCreate_EmptyTypeFrameUntouched(t *testing.T) {
	settings := &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{{
			ServiceTier:    tierpolicy.OpenAIFastTierPriority,
			Action:         claude.BetaPolicyActionFilter,
			Scope:          claude.BetaPolicyScopeAll,
			ModelWhitelist: []string{"*"},
			FallbackAction: claude.BetaPolicyActionFilter,
		}},
	}
	svc := newWSFastPolicy(t, settings)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	// Frame with no "type" field: must pass through completely unchanged
	// even with a service_tier-shaped field present.
	frame := []byte(`{"service_tier":"priority","model":"gpt-5.5"}`)
	updated, blocked, err := gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, string(frame), string(updated), "empty type must NOT be policy-checked — Realtime spec requires type, malformed frames are passed through")

	// Explicit empty string also passes through.
	frame = []byte(`{"type":"","service_tier":"priority","model":"gpt-5.5"}`)
	updated, blocked, err = gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, string(frame), string(updated))
}

func (f *fakePassthroughFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if f.idx >= len(f.reads) {
		return coderws.MessageText, nil, openai.ErrWSConnClosed
	}
	payload := f.reads[f.idx]
	f.idx++
	return coderws.MessageText, payload, nil
}

func (f *fakePassthroughFrameConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	cp := append([]byte(nil), payload...)
	f.writes = append(f.writes, cp)
	return nil
}

func (f *fakePassthroughFrameConn) Close() error {
	f.closeOnce = true
	return nil
}

// gpt55WhitelistFastPolicy supplies a model whitelist to make captured-model selection observable.
// The empty default policy would produce the same result with or without a captured model.
func gpt55WhitelistFastPolicy() *tierpolicy.OpenAIFastPolicySettings {
	return &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{{
			ServiceTier:    tierpolicy.OpenAIFastTierPriority,
			Action:         claude.BetaPolicyActionFilter,
			Scope:          claude.BetaPolicyScopeAll,
			ModelWhitelist: []string{"gpt-5.5", "gpt-5.5*"},
			FallbackAction: claude.BetaPolicyActionPass,
		}},
	}
}

// TestPolicyEnforcingFrameConn_FollowupFrameWithoutModelUsesCapturedModel checks policy matching against the first frame's model.
func TestPolicyEnforcingFrameConn_FollowupFrameWithoutModelUsesCapturedModel(t *testing.T) {
	// A whitelist makes captured-model reuse observable:
	// the empty default policy would pass this frame in either case.
	svc := newWSFastPolicy(t, gpt55WhitelistFastPolicy())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	// Simulate the passthrough adapter capturing model from the first frame.
	firstFrame := []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"priority"}`)
	capturedSessionModel := openAIWSPassthroughPolicyModelForFrame(provider, firstFrame)
	require.Equal(t, "gpt-5.5", capturedSessionModel)

	// Later frames can omit model and use the captured session model.
	followupFrame := []byte(`{"type":"response.create","service_tier":"priority"}`)

	inner := &fakePassthroughFrameConn{
		reads: [][]byte{followupFrame},
	}
	wrapper := &openAIWSPolicyEnforcingFrameConn{
		inner: inner,
		filter: func(msgType coderws.MessageType, payload []byte) ([]byte, *tierpolicy.BlockedError, error) {
			if msgType != coderws.MessageText {
				return payload, nil, nil
			}
			model := openAIWSPassthroughPolicyModelForFrame(provider, payload)
			if model == "" {
				model = capturedSessionModel
			}
			return gatewayws.ApplyServiceTierFrame(payload, model, svc.Input(context.Background(), provider, model))
		},
	}

	// Read the follow-up frame through the wrapper. The policy MUST still
	// trigger filter (gpt-5.5 + priority → filter), so the service_tier
	// field is gone by the time the relay sees it.
	_, payload, err := wrapper.ReadFrame(context.Background())
	require.NoError(t, err)
	require.NotContains(t, string(payload), `"service_tier"`,
		"D5 regression: empty model on follow-up frame must fall back to capturedSessionModel; whitelist policy filters service_tier=priority for gpt-5.5")
	require.Equal(t, "response.create", gjson.GetBytes(payload, "type").String())
}

func TestOpenAIWSPassthroughPolicyModelDoesNotApplyProviderMapping(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"model_mapping": map[string]any{"public-model": "private-model"},
			},
			Extra: map[string]any{"openai_passthrough": true},
		},
	}

	responseCreate := []byte(`{"type":"response.create","model":"public-model"}`)
	require.Equal(t, "public-model", openAIWSPassthroughPolicyModelForFrame(provider, responseCreate))

	sessionUpdate := []byte(`{"type":"session.update","session":{"model":"public-model"}}`)
	require.Equal(t, "public-model", openAIWSPassthroughPolicyModelFromSessionFrame(provider, sessionUpdate))
}

// TestPolicyEnforcingFrameConn_WithoutCapturedFallbackPolicyMisses checks a missing frame and session model against the whitelist.
func TestPolicyEnforcingFrameConn_WithoutCapturedFallbackPolicyMisses(t *testing.T) {
	// Use a whitelist to expose the missing captured model.
	svc := newWSFastPolicy(t, gpt55WhitelistFastPolicy())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	followupFrame := []byte(`{"type":"response.create","service_tier":"priority"}`)
	inner := &fakePassthroughFrameConn{reads: [][]byte{followupFrame}}
	wrapper := &openAIWSPolicyEnforcingFrameConn{
		inner: inner,
		filter: func(msgType coderws.MessageType, payload []byte) ([]byte, *tierpolicy.BlockedError, error) {
			// Omit the captured model to exercise matching with an empty model.
			model := openAIWSPassthroughPolicyModelForFrame(provider, payload)
			return gatewayws.ApplyServiceTierFrame(payload, model, svc.Input(context.Background(), provider, model))
		},
	}

	_, payload, err := wrapper.ReadFrame(context.Background())
	require.NoError(t, err)
	// The empty model misses the whitelist, so fallback=pass retains service_tier.
	require.Contains(t, string(payload), `"service_tier"`,
		"sanity: without capturedSessionModel fallback the leak (D5) reproduces — confirms the fix is load-bearing")
}

// TestApplyOpenAIFastPolicyToBody_BlockShortCircuitsUpstream checks the typed block error and unchanged payload.
// Chat Completions and Messages callers use the error to end the request.
func TestApplyOpenAIFastPolicyToBody_BlockShortCircuitsUpstream(t *testing.T) {
	settings := &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{{
			ServiceTier:    tierpolicy.OpenAIFastTierPriority,
			Action:         claude.BetaPolicyActionBlock,
			Scope:          claude.BetaPolicyScopeAll,
			ErrorMessage:   "priority blocked",
			ModelWhitelist: []string{"gpt-5.5"},
			FallbackAction: claude.BetaPolicyActionPass,
		}},
	}
	svc := newWSFastPolicy(t, settings)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	body := []byte(`{"model":"gpt-5.5","service_tier":"priority","input":[]}`)
	updated, err := tierpolicy.ApplyBody(body, svc.Input(context.Background(), provider, "gpt-5.5"))
	require.Error(t, err)
	var blocked *tierpolicy.BlockedError
	require.True(t, errors.As(err, &blocked), "block must surface as typed error so caller can skip upstream HTTP request")
	require.Equal(t, "priority blocked", blocked.Message)
	require.Equal(t, string(body), string(updated), "block must not mutate body")
}

// TestForwardAsAnthropicMessages_BetaFastModePassesOpenAIFastPolicyByDefault checks beta fast-mode conversion
// to Responses priority and passage through the default policy using the conversion and policy helpers.
func TestForwardAsAnthropicMessages_BetaFastModePassesOpenAIFastPolicyByDefault(t *testing.T) {
	svc := newWSFastPolicy(t, tierpolicy.Default())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	// Convert the Anthropic request before applying the beta-header tier.
	anthropicBody := []byte(`{"model":"gpt-5.5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	var anthropicReq protocolanthropic.AnthropicRequest
	require.NoError(t, json.Unmarshal(anthropicBody, &anthropicReq))
	responsesReq, err := protocolbridge.AnthropicToResponses(&anthropicReq, protocolforward.ConversionOptionsForModel(anthropicReq.Model))
	require.NoError(t, err)

	// The fast-mode beta header selects priority.
	headers := http.Header{}
	headers.Set("anthropic-beta", claude.BetaFastMode)
	require.True(t, claude.ContainsBetaToken(headers.Get("anthropic-beta"), claude.BetaFastMode))
	responsesReq.ServiceTier = "priority"
	responsesReq.Model = "gpt-5.5"

	// Apply the Fast policy to the converted body.
	responsesBody, err := json.Marshal(responsesReq)
	require.NoError(t, err)
	require.Equal(t, "priority", gjson.GetBytes(responsesBody, "service_tier").String(), "前置：beta 翻译应当注入 priority")

	upstreamBody, policyErr := tierpolicy.ApplyBody(responsesBody, svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, policyErr)

	// The default policy passes the requested priority tier.
	require.Equal(t, "priority", gjson.GetBytes(upstreamBody, "service_tier").String(),
		"default policy should pass service_tier=priority through to upstream")
}

// TestPolicyEnforcingFrameConn_SessionUpdateRotatesCapturedModel checks the session model update.
// After gpt-4o changes to gpt-5.5, a response.create without model inherits gpt-5.5 and matches the tier filter.
func TestPolicyEnforcingFrameConn_SessionUpdateRotatesCapturedModel(t *testing.T) {
	svc := newWSFastPolicy(t, gpt55WhitelistFastPolicy())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	// gpt-4o misses the whitelist, so fallback=pass retains service_tier.
	first := []byte(`{"type":"response.create","model":"gpt-4o","service_tier":"priority"}`)
	// Frame 2: session.update rotates the session model to gpt-5.5.
	rotate := []byte(`{"type":"session.update","session":{"model":"gpt-5.5"}}`)
	// The third frame omits model and inherits gpt-5.5.
	followup := []byte(`{"type":"response.create","service_tier":"priority"}`)

	inner := &fakePassthroughFrameConn{reads: [][]byte{first, rotate, followup}}

	// Share capturedSessionModel across frames as the passthrough session does.
	capturedSessionModel := openAIWSPassthroughPolicyModelForFrame(provider, first)
	require.Equal(t, "gpt-4o", capturedSessionModel)
	wrapper := &openAIWSPolicyEnforcingFrameConn{
		inner: inner,
		filter: func(msgType coderws.MessageType, payload []byte) ([]byte, *tierpolicy.BlockedError, error) {
			if msgType != coderws.MessageText {
				return payload, nil, nil
			}
			if updated := openAIWSPassthroughPolicyModelFromSessionFrame(provider, payload); updated != "" {
				capturedSessionModel = updated
			}
			model := openAIWSPassthroughPolicyModelForFrame(provider, payload)
			if model == "" {
				model = capturedSessionModel
			}
			return gatewayws.ApplyServiceTierFrame(payload, model, svc.Input(context.Background(), provider, model))
		},
	}

	// Frame 1: gpt-4o miss whitelist → pass (service_tier preserved).
	_, payload1, err := wrapper.ReadFrame(context.Background())
	require.NoError(t, err)
	require.Contains(t, string(payload1), `"service_tier"`, "frame1: gpt-4o miss whitelist → pass keeps service_tier")

	// session.update passes through and changes the captured model to gpt-5.5.
	_, payload2, err := wrapper.ReadFrame(context.Background())
	require.NoError(t, err)
	require.Equal(t, string(rotate), string(payload2), "session.update frame is forwarded verbatim")
	require.Equal(t, "gpt-5.5", capturedSessionModel, "fix1: session.update must rotate capturedSessionModel")

	// Frame 3: empty model + new captured gpt-5.5 → matches whitelist → filter.
	_, payload3, err := wrapper.ReadFrame(context.Background())
	require.NoError(t, err)
	require.NotContains(t, string(payload3), `"service_tier"`,
		"fix1: post-rotate response.create without model must use refreshed capturedSessionModel and trigger filter")
}

// TestPolicyModelFromSessionFrame_OnlySessionUpdate checks that other events return no model update.
func TestPolicyModelFromSessionFrame_OnlySessionUpdate(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	// session.created is a server event and does not update the client uplink model.
	created := []byte(`{"type":"session.created","session":{"model":"gpt-5.5"}}`)
	require.Empty(t, openAIWSPassthroughPolicyModelFromSessionFrame(provider, created))

	// Non-session.* frames must NOT trigger rotation.
	notSession := []byte(`{"type":"response.create","session":{"model":"gpt-9"}}`)
	require.Empty(t, openAIWSPassthroughPolicyModelFromSessionFrame(provider, notSession))

	// Missing session.model returns an empty value, leaving the captured model intact.
	noModel := []byte(`{"type":"session.update","session":{"voice":"alloy"}}`)
	require.Empty(t, openAIWSPassthroughPolicyModelFromSessionFrame(provider, noModel))
}

// TestApplyOpenAIFastPolicyToBody_PassNormalizesFastAlias checks normalization to priority even when policy passes.
func TestApplyOpenAIFastPolicyToBody_PassNormalizesFastAlias(t *testing.T) {
	// Use a policy that deliberately misses gpt-4 so the action is pass.
	settings := &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{{
			ServiceTier:    tierpolicy.OpenAIFastTierPriority,
			Action:         claude.BetaPolicyActionFilter,
			Scope:          claude.BetaPolicyScopeAll,
			ModelWhitelist: []string{"gpt-5.5"},
			FallbackAction: claude.BetaPolicyActionPass,
		}},
	}
	svc := newWSFastPolicy(t, settings)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	// gpt-4 + "fast" → fallback pass. Body must be rewritten to "priority".
	body := []byte(`{"model":"gpt-4","service_tier":"fast"}`)
	updated, err := tierpolicy.ApplyBody(body, svc.Input(context.Background(), provider, "gpt-4"))
	require.NoError(t, err)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String(),
		"fix2: pass action must still normalize 'fast' → 'priority' so upstream OpenAI accepts the slug")

	// Already-canonical "priority" on pass: zero mutation (byte-equal).
	body = []byte(`{"model":"gpt-4","service_tier":"priority"}`)
	updated, err = tierpolicy.ApplyBody(body, svc.Input(context.Background(), provider, "gpt-4"))
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))

	// Mixed-case alias → normalized.
	body = []byte(`{"model":"gpt-4","service_tier":"  Fast  "}`)
	updated, err = tierpolicy.ApplyBody(body, svc.Input(context.Background(), provider, "gpt-4"))
	require.NoError(t, err)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String())

	// Unrecognized tier → still no-op (not normalized, since normTier == "").
	body = []byte(`{"model":"gpt-4","service_tier":"turbo"}`)
	updated, err = tierpolicy.ApplyBody(body, svc.Input(context.Background(), provider, "gpt-4"))
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))
}

// TestPassthroughBilling_PostFilterServiceTier checks that filtering priority leaves the outbound billing tier nil.
func TestPassthroughBilling_PostFilterServiceTier(t *testing.T) {
	svc := newWSFastPolicy(t, openAIFastFilterPriorityPolicy())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	raw := []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"priority"}`)

	// Extract billing metadata after filtering, since the input still contains priority.
	pre := requeststate.ExtractOpenAIServiceTierFromBody(raw)
	require.NotNil(t, pre)
	require.Equal(t, "priority", *pre,
		"sanity: raw first frame carries priority that pre-fix billing would have reported")

	// The gpt-5.5 priority rule filters the tier.
	filtered, blocked, err := gatewayws.ApplyServiceTierFrame(raw, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.NotContains(t, string(filtered), `"service_tier"`)

	// Post-filter: extracting from the rewritten frame returns nil. This
	// is the outbound tier stored in the forwarding result;
	// any observed response tier remains separate for usage-time resolution.
	post := requeststate.ExtractOpenAIServiceTierFromBody(filtered)
	require.Nil(t, post, "fix3: post-filter extraction must preserve a nil outbound tier")

	// And the byte-level invariant the adapter relies on: filtering an
	// already-filtered frame is a no-op (idempotent), so re-running the
	// policy doesn't accidentally re-introduce the field.
	again, blocked2, err := gatewayws.ApplyServiceTierFrame(filtered, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked2)
	require.Equal(t, string(filtered), string(again),
		"policy is idempotent: filtering an already-filtered frame leaves bytes unchanged")
}

// TestApplyOpenAIFastPolicyToBody_NonStringServiceTier checks that non-string tiers bypass policy unchanged.
func TestApplyOpenAIFastPolicyToBody_NonStringServiceTier(t *testing.T) {
	svc := newWSFastPolicy(t, tierpolicy.Default())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	// Numeric, null, composite, and boolean values do not identify a recognized tier.
	cases := [][]byte{
		[]byte(`{"model":"gpt-5.5","service_tier":1}`),
		[]byte(`{"model":"gpt-5.5","service_tier":null}`),
		[]byte(`{"model":"gpt-5.5","service_tier":{"nested":"priority"}}`),
		[]byte(`{"model":"gpt-5.5","service_tier":["priority"]}`),
		[]byte(`{"model":"gpt-5.5","service_tier":true}`),
	}
	for _, body := range cases {
		updated, err := tierpolicy.ApplyBody(body, svc.Input(context.Background(), provider, "gpt-5.5"))
		require.NoError(t, err, "non-string service_tier must not error: %s", string(body))
		require.Equal(t, string(body), string(updated),
			"non-string service_tier must pass through unchanged: %s", string(body))
	}

	// WebSocket frame handling also passes these payloads through unchanged.
	for _, body := range cases {
		frame := body
		updated, blocked, err := gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
		require.NoError(t, err, "non-string service_tier ws frame must not error: %s", string(frame))
		require.Nil(t, blocked, "non-string service_tier must not trigger block: %s", string(frame))
		require.Equal(t, string(frame), string(updated),
			"non-string service_tier ws frame must pass through unchanged: %s", string(frame))
	}
}

// TestPassthroughBilling_MultiTurnServiceTierFollowsFilteredFrames checks billing metadata after each turn's filter.
// Each response.create can specify its own service_tier.
// Successful response.create filtering updates the atomic tier pointer.
// Turn one filters priority to nil; turn two retains flex.
// Turn three omits the tier and resets it to nil; response.cancel leaves the pointer unchanged.
func TestPassthroughBilling_MultiTurnServiceTierFollowsFilteredFrames(t *testing.T) {
	svc := newWSFastPolicy(t, openAIFastFilterPriorityPolicy())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	// Mirror per-frame atomic storage to check billing metadata across turns.
	var requestServiceTierPtr atomic.Pointer[string]
	capturedSessionModel := ""
	filter := func(msgType coderws.MessageType, payload []byte) ([]byte, *tierpolicy.BlockedError, error) {
		if msgType != coderws.MessageText {
			return payload, nil, nil
		}
		if updated := openAIWSPassthroughPolicyModelFromSessionFrame(provider, payload); updated != "" {
			capturedSessionModel = updated
		}
		model := openAIWSPassthroughPolicyModelForFrame(provider, payload)
		if model == "" {
			model = capturedSessionModel
		}
		out, blocked, policyErr := gatewayws.ApplyServiceTierFrame(payload, model, svc.Input(context.Background(), provider, model))
		if policyErr == nil && blocked == nil &&
			strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.create" {
			requestServiceTierPtr.Store(requeststate.ExtractOpenAIServiceTierFromBody(out))
		}
		return out, blocked, policyErr
	}

	// First-frame initialization mirrors the adapter: extract from the
	// post-filter payload so a filter-on-first-frame clears the outbound tier.
	firstFrame := []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"priority"}`)
	firstOut, firstBlocked, firstErr := gatewayws.ApplyServiceTierFrame(firstFrame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, firstErr)
	require.Nil(t, firstBlocked)
	requestServiceTierPtr.Store(requeststate.ExtractOpenAIServiceTierFromBody(firstOut))
	capturedSessionModel = openAIWSPassthroughPolicyModelForFrame(provider, firstFrame)
	require.Nil(t, requestServiceTierPtr.Load(),
		"turn 1: filter strips service_tier=priority, leaving a nil outbound tier")

	// Turn 2: client switches to flex, should pass and update billing.
	turn2 := []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"flex"}`)
	out2, blocked2, err2 := filter(coderws.MessageText, turn2)
	require.NoError(t, err2)
	require.Nil(t, blocked2)
	require.Equal(t, "flex", gjson.GetBytes(out2, "service_tier").String(), "turn 2: flex must pass to upstream untouched")
	tier2 := requestServiceTierPtr.Load()
	require.NotNil(t, tier2, "turn 2: billing must update to reflect flex")
	require.Equal(t, "flex", *tier2)

	// A non-response.create frame with a stray service_tier-shaped field
	// must NOT overwrite the billing pointer (those frames don't carry
	// per-response service_tier in the Realtime spec).
	cancelFrame := []byte(`{"type":"response.cancel","service_tier":"priority"}`)
	_, blockedCancel, errCancel := filter(coderws.MessageText, cancelFrame)
	require.NoError(t, errCancel)
	require.Nil(t, blockedCancel)
	tierAfterCancel := requestServiceTierPtr.Load()
	require.NotNil(t, tierAfterCancel, "response.cancel must not clobber billing tier to nil")
	require.Equal(t, "flex", *tierAfterCancel,
		"non-response.create frames must not update billing tier even if they carry a service_tier-shaped field")

	// Turn 3: response.create without any service_tier. We deliberately
	// overwrite billing back to nil so it tracks what the upstream actually
	// sees on this turn (default tier).
	turn3 := []byte(`{"type":"response.create","model":"gpt-5.5"}`)
	out3, blocked3, err3 := filter(coderws.MessageText, turn3)
	require.NoError(t, err3)
	require.Nil(t, blocked3)
	require.Equal(t, string(turn3), string(out3), "turn 3 has no service_tier — filter must not mutate")
	require.Nil(t, requestServiceTierPtr.Load(),
		"turn 3: response.create without service_tier overwrites billing to nil to match upstream default")
}

func TestPassthroughUsageMeta_TracksReasoningEffortAcrossTurns(t *testing.T) {
	svc := newWSFastPolicy(t, tierpolicy.Default())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	firstFrame := []byte(`{"type":"response.create","model":"gpt-5.5","reasoning":{"effort":"medium"},"service_tier":"priority"}`)
	meta := newOpenAIWSPassthroughUsageMeta("", firstFrame)
	capturedSessionModel := openAIWSPassthroughPolicyModelForFrame(provider, firstFrame)
	firstOut, firstBlocked, firstErr := gatewayws.ApplyServiceTierFrame(firstFrame, capturedSessionModel, svc.Input(context.Background(), provider, capturedSessionModel))
	require.NoError(t, firstErr)
	require.Nil(t, firstBlocked)
	meta.initFromFirstFrame(firstOut, capturedSessionModel)
	require.NotNil(t, meta.reasoningEffort.Load())
	require.Equal(t, "medium", *meta.reasoningEffort.Load())

	process := func(payload []byte) ([]byte, *tierpolicy.BlockedError, error) {
		if updated := openAIWSPassthroughPolicyModelFromSessionFrame(provider, payload); updated != "" {
			capturedSessionModel = updated
		}
		meta.updateSessionRequestModel(payload)
		requestModelForThisFrame := meta.requestModelForFrame(payload)
		model := openAIWSPassthroughPolicyModelForFrame(provider, payload)
		if model == "" {
			model = capturedSessionModel
		}
		out, blocked, policyErr := gatewayws.ApplyServiceTierFrame(payload, model, svc.Input(context.Background(), provider, model))
		if policyErr == nil && blocked == nil &&
			strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.create" {
			meta.updateFromResponseCreate(out, model, requestModelForThisFrame)
		}
		return out, blocked, policyErr
	}

	_, blockedSession, errSession := process([]byte(`{"type":"session.update","session":{"model":"gpt-5-high"}}`))
	require.NoError(t, errSession)
	require.Nil(t, blockedSession)
	require.NotNil(t, meta.reasoningEffort.Load())
	require.Equal(t, "medium", *meta.reasoningEffort.Load(), "session.update 只刷新后续 fallback model，不覆盖当前 turn metadata")

	_, blockedCancel, errCancel := process([]byte(`{"type":"response.cancel","reasoning_effort":"x-high"}`))
	require.NoError(t, errCancel)
	require.Nil(t, blockedCancel)
	require.NotNil(t, meta.reasoningEffort.Load())
	require.Equal(t, "medium", *meta.reasoningEffort.Load(), "非 response.create 帧不能污染当前 turn metadata")

	_, blockedFlat, errFlat := process([]byte(`{"type":"response.create","reasoning_effort":"x-high"}`))
	require.NoError(t, errFlat)
	require.Nil(t, blockedFlat)
	require.NotNil(t, meta.reasoningEffort.Load())
	require.Equal(t, "xhigh", *meta.reasoningEffort.Load(), "flat reasoning_effort 必须进入 passthrough usage metadata")

	_, blockedMax, errMax := process([]byte(`{"type":"response.create","model":"deepseek-v4-flash","reasoning":{"effort":"max"}}`))
	require.NoError(t, errMax)
	require.Nil(t, blockedMax)
	require.NotNil(t, meta.reasoningEffort.Load())
	require.Equal(t, "max", *meta.reasoningEffort.Load(), "第三方模型显式 max 必须进入 passthrough usage metadata")

	_, blockedClear, errClear := process([]byte(`{"type":"response.create","model":"gpt-4o"}`))
	require.NoError(t, errClear)
	require.Nil(t, blockedClear)
	require.Nil(t, meta.reasoningEffort.Load(), "新的 response.create 无 effort 且无可推导后缀时必须清空旧值")
}

// TestPassthroughBilling_BlockedFrameDoesNotMutateServiceTier checks that a blocked frame leaves the previous billing tier intact.
func TestPassthroughBilling_BlockedFrameDoesNotMutateServiceTier(t *testing.T) {
	blockSettings := &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{{
			ServiceTier:    tierpolicy.OpenAIFastTierPriority,
			Action:         claude.BetaPolicyActionBlock,
			Scope:          claude.BetaPolicyScopeAll,
			ErrorMessage:   "blocked",
			ModelWhitelist: []string{"gpt-5.5"},
			FallbackAction: claude.BetaPolicyActionPass,
		}},
	}
	svc := newWSFastPolicy(t, blockSettings)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	var requestServiceTierPtr atomic.Pointer[string]
	flexValue := "flex"
	requestServiceTierPtr.Store(&flexValue) // simulate prior turn billed as flex

	filter := func(msgType coderws.MessageType, payload []byte) ([]byte, *tierpolicy.BlockedError, error) {
		if msgType != coderws.MessageText {
			return payload, nil, nil
		}
		out, blocked, policyErr := gatewayws.ApplyServiceTierFrame(payload, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
		if policyErr == nil && blocked == nil &&
			strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.create" {
			requestServiceTierPtr.Store(requeststate.ExtractOpenAIServiceTierFromBody(out))
		}
		return out, blocked, policyErr
	}

	frame := []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"priority"}`)
	_, blocked, err := filter(coderws.MessageText, frame)
	require.NoError(t, err)
	require.NotNil(t, blocked, "policy must block this frame")

	tier := requestServiceTierPtr.Load()
	require.NotNil(t, tier, "blocked frame must not clobber prior billing tier to nil")
	require.Equal(t, "flex", *tier,
		"blocked frame is never sent upstream; billing must retain the previous turn's tier")
}

func (c *openAIWSPolicyEnforcingFrameConn) runtime() gatewayws.FrameConn {
	c.once.Do(func() {
		var filter func(int, []byte) ([]byte, *gatewayws.PolicyBlocked, error)
		if c.filter != nil {
			filter = func(typ int, body []byte) ([]byte, *gatewayws.PolicyBlocked, error) {
				out, blocked, err := c.filter(coderws.MessageType(typ), body)
				if blocked == nil {
					return out, nil, err
				}
				return out, &gatewayws.PolicyBlocked{Message: blocked.Message, Cause: blocked}, err
			}
		}
		var writeFilter func(int, []byte) ([]byte, error)
		if c.writeFilter != nil {
			writeFilter = func(typ int, body []byte) ([]byte, error) { return c.writeFilter(coderws.MessageType(typ), body) }
		}
		var block func(*gatewayws.PolicyBlocked)
		if c.onBlock != nil {
			block = func(b *gatewayws.PolicyBlocked) {
				original, ok := b.Cause.(*tierpolicy.BlockedError)
				if !ok {
					original = &tierpolicy.BlockedError{Message: b.Message}
				}
				c.onBlock(original)
			}
		}
		c.core = gatewayws.NewPolicyFrames(openAIWSCoreFrames{c.inner}, filter, writeFilter, block, func(status int, reason string, err error) error {
			return gatewayhttp.NewOpenAIWSClientCloseError(coderws.StatusCode(status), reason, err)
		}, openai.ErrWSConnClosed)
	})
	return c.core
}

func (c *openAIWSPolicyEnforcingFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if c == nil || c.inner == nil {
		return coderws.MessageText, nil, openai.ErrWSConnClosed
	}
	typ, body, err := c.runtime().ReadFrame(ctx)
	return coderws.MessageType(typ), body, err
}

func (c *openAIWSPolicyEnforcingFrameConn) WriteFrame(ctx context.Context, typ coderws.MessageType, body []byte) error {
	if c == nil || c.inner == nil {
		return openai.ErrWSConnClosed
	}
	return c.runtime().WriteFrame(ctx, int(typ), body)
}

func (c *openAIWSPolicyEnforcingFrameConn) Close() error {
	if c == nil || c.inner == nil {
		return nil
	}
	return c.runtime().Close()
}

func (c *openAIWSClientFrameConn) markTurnStarted() {
	if c != nil {
		gatewayws.TurnActivity{Waiting: &c.waitingForNextTurn, Started: c.interTurnStarted}.MarkStarted()
	}
}

func (c *openAIWSClientFrameConn) markTurnCompleted() {
	if c != nil {
		gatewayws.TurnActivity{Waiting: &c.waitingForNextTurn, Started: c.interTurnStarted}.MarkCompleted()
	}
}

// openAIWSPassthroughPolicyModelForFrame resolves the policy model, skipping provider mapping when passthrough is enabled.
func openAIWSPassthroughPolicyModelForFrame(provider *gatewayprovider.ExecutionProvider, payload []byte) string {
	if provider == nil || len(payload) == 0 {
		return ""
	}
	original := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
	if original == "" {
		return ""
	}
	if provider.View().IsOpenAIPassthroughEnabled() {
		return original
	}
	return gatewayprovider.ExecutionModelPolicy(provider).NormalizeOpenAI(gatewayprovider.ExecutionModelPolicy(provider).Mapped(original))
}

// openAIWSPassthroughPolicyModelFromSessionFrame resolves session.model from session.update.
// It returns empty for other events or a missing model, leaving the captured model unchanged.
// Later response.create frames that omit model use the captured value for Fast policy matching.
func openAIWSPassthroughPolicyModelFromSessionFrame(provider *gatewayprovider.ExecutionProvider, payload []byte) string {
	if provider == nil || len(payload) == 0 {
		return ""
	}
	frameType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	if frameType != "session.update" {
		return ""
	}
	original := strings.TrimSpace(gjson.GetBytes(payload, "session.model").String())
	if original == "" {
		return ""
	}
	if provider.View().IsOpenAIPassthroughEnabled() {
		return original
	}
	return gatewayprovider.ExecutionModelPolicy(provider).NormalizeOpenAI(gatewayprovider.ExecutionModelPolicy(provider).Mapped(original))
}

func newOpenAIWSPassthroughUsageMeta(model string, body []byte) *openAIWSPassthroughUsageMeta {
	meta := gatewayws.NewUsageMeta(model, body, gatewayws.RequestUsageDecoder{})
	return &openAIWSPassthroughUsageMeta{UsageMeta: meta, reasoningEffort: &meta.ReasoningEffort}
}

func (m *openAIWSPassthroughUsageMeta) initFromFirstFrame(body []byte, model string) {
	if m != nil {
		m.InitFromFirstFrame(body)
	}
}

func (m *openAIWSPassthroughUsageMeta) updateSessionRequestModel(body []byte) {
	if m != nil {
		m.UpdateSessionRequestModel(body)
	}
}

func (m *openAIWSPassthroughUsageMeta) updateFromResponseCreate(body []byte, mapped, requested string) {
	if m != nil {
		m.UpdateFromResponseCreate(body)
	}
}

func newOpenAIWSPassthroughTurnLifecycle(inFlight bool) *openAIWSPassthroughTurnLifecycle {
	return &openAIWSPassthroughTurnLifecycle{gatewayws.NewTurnLifecycle(inFlight)}
}

func (l *openAIWSPassthroughTurnLifecycle) beginResponseCreate(fn func()) bool {
	if l == nil {
		return false
	}
	return l.BeginResponseCreate(fn)
}

func (l *openAIWSPassthroughTurnLifecycle) beginTerminalWrite() {
	if l != nil {
		l.BeginTerminalWrite()
	}
}

func (l *openAIWSPassthroughTurnLifecycle) finishTerminalWrite(ok bool, fn func()) {
	if l != nil {
		l.FinishTerminalWrite(ok, fn)
	}
}

// requestModelForFrame reads atomic session model metadata for concurrency tests.
func (m *openAIWSPassthroughUsageMeta) requestModelForFrame(body []byte) string {
	if m == nil {
		return gatewayws.RequestModelForFrame(body)
	}
	return m.RequestModelForFrame(body)
}

func TestOpenAIWSPassthroughTurnLifecycle_SerializesTerminalCommitAndNextTurn(t *testing.T) {
	clientFrameConn := &openAIWSClientFrameConn{interTurnStarted: make(chan struct{}, 1)}
	clientFrameConn.markTurnCompleted()
	lifecycle := newOpenAIWSPassthroughTurnLifecycle(true)
	lifecycle.beginTerminalWrite()

	admitted := make(chan bool, 1)
	go func() {
		admitted <- lifecycle.beginResponseCreate(clientFrameConn.markTurnStarted)
	}()
	select {
	case <-admitted:
		t.Fatal("next response.create was admitted before terminal commit completed")
	case <-time.After(50 * time.Millisecond):
	}

	lifecycle.finishTerminalWrite(true, clientFrameConn.markTurnCompleted)
	select {
	case ok := <-admitted:
		require.True(t, ok)
	case <-time.After(time.Second):
		t.Fatal("next response.create remained blocked after terminal commit")
	}
	require.False(t, clientFrameConn.waitingForNextTurn.Load(), "accepted next turn must win over terminal idle state")

	lifecycle = newOpenAIWSPassthroughTurnLifecycle(true)
	lifecycle.beginTerminalWrite()
	admitted = make(chan bool, 1)
	go func() {
		admitted <- lifecycle.beginResponseCreate(nil)
	}()
	lifecycle.finishTerminalWrite(false, func() {
		t.Error("failed terminal write must not commit idle state")
	})
	require.False(t, <-admitted, "failed terminal write must keep the current turn in flight")
}
