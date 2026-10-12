package httpapi

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

// Fast policy checks read a frame's current or retained session model before
// deciding whether its service tier is accepted, removed or blocked. These
// scenarios compare default policy, user rules, explicit force and filtering,
// including requests that omit model or tier after a prior turn. Post-policy
// metadata describes the actual accepted frame rather than the client's
// pre-filter tier.
//
// Fixture frame wrappers call the production policy and lifecycle helpers
// while retaining observable scripted reads and writes. Separate usage atoms
// hold effort and request tier; session.update changes the fallback model
// while response.create publishes accepted turn metadata. Terminal-delivery
// scenarios hold the admission barrier through its write outcome and inspect
// the waiting flag after the next accepted request.

// fakePassthroughFrameConn returns queued frames, then ErrWSConnClosed, and
// records writes for assertions.
type fakePassthroughFrameConn struct {
	// reads and idx identify the next queued frame; writes retains copied payloads
	// for assertions. closeOnce records a close request but does not stop scripted
	// reads or writes, and tests serialize access to this unsynchronized fixture.
	reads     [][]byte // scripted client text frame bytes
	idx       int      // next scripted frame index
	writes    [][]byte // copied outbound frame payloads
	closeOnce bool     // fixture close call observed
}

// openAIWSPolicyEnforcingFrameConn adapts fixture policy callbacks to production frame filtering.
type openAIWSPolicyEnforcingFrameConn struct {
	// filter, writeFilter and onBlock are optional and are bound when once creates
	// core. Configure inner and callbacks before first use, then keep them stable;
	// once publishes construction, while frame synchronization belongs to inner.
	inner       openaiwsv2.FrameConn                                                        // wrapped fixture frame transport
	filter      func(coderws.MessageType, []byte) ([]byte, *tierpolicy.BlockedError, error) // optional inbound frame policy callback
	writeFilter func(coderws.MessageType, []byte) ([]byte, error)                           // optional outbound frame transformation
	onBlock     func(*tierpolicy.BlockedError)                                              // optional typed policy block observer
	once        sync.Once                                                                   // core frame wrapper initialization guard
	core        gatewayws.FrameConn                                                         // retained production frame wrapper
}

// openAIWSClientFrameConn exposes production turn-activity state for admission fixtures.
type openAIWSClientFrameConn struct {
	// waitingForNextTurn records the activity atom, and interTurnStarted carries
	// a nonblocking idle notification. Lifecycle callers order these publications
	// by invoking the helpers under its admission or terminal-delivery lock.
	interTurnStarted   chan struct{} // completed turn idle notifications
	waitingForNextTurn atomic.Bool   // client waiting between accepted turns
}

// openAIWSPassthroughUsageMeta exposes production session metadata and its shared
// effort atom to passthrough policy assertions.
type openAIWSPassthroughUsageMeta struct {
	*gatewayws.UsageMeta // shared production request metadata atoms
	// reasoningEffort points to the embedded UsageMeta.ReasoningEffort atomic.
	// Assertions read that same publication rather than a separately copied value.
	reasoningEffort *atomic.Pointer[string]
}

// openAIWSPassthroughTurnLifecycle exposes the production turn admission barrier
// for fixture terminal-delivery and next-request coordination.
type openAIWSPassthroughTurnLifecycle struct {
	*gatewayws.TurnLifecycle /* production admission and terminal barrier */
}

// TestWSResponseCreate_DefaultPassesPriorityAndNormalizesFast supplies
// priority, fast and padded mixed-case aliases under default policy. Priority
// survives, aliases become priority, and the first request retains its type,
// model and input text.
func TestWSResponseCreate_DefaultPassesPriorityAndNormalizesFast(t *testing.T) {
	svc := newWSFastPolicy(t, tierpolicy.Default())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	frame := []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"priority","input":[{"type":"input_text","text":"hi"}]}`)
	updated, blocked, err := gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String(), "default policy should preserve priority tier")
	// Tier normalization targets service_tier; assert type, model and input text
	// alongside it so the helper cannot silently alter those observed request fields.
	require.Equal(t, "response.create", gjson.GetBytes(updated, "type").String())
	require.Equal(t, "gpt-5.5", gjson.GetBytes(updated, "model").String())
	require.Equal(t, "hi", gjson.GetBytes(updated, "input.0.text").String())

	frame = []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"fast"}`)
	updated, blocked, err = gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String(), "fast alias should normalize before reaching upstream")

	// The same protocol alias can arrive with case and padding; exercise those
	// spellings so normalization reaches canonical priority under the pass action.
	frame = []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"  Fast  "}`)
	updated, blocked, err = gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String())
}

// TestWSResponseCreate_RejectsUltraBeforeUpstream supplies ultra reasoning in
// response.create and session.update frames. The frame helper returns a
// not-supported error with unchanged bytes and no service-tier block result
// for both inputs.
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

// TestWSResponseCreate_ExplicitFilterStripsServiceTier supplies priority and
// fast frames to an explicit priority filter. Both return no error or block
// and remove service_tier from the resulting frame.
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

// TestWSResponseCreate_UserScopedRuleOverridesGlobalRule combines a global
// priority filter with a pass rule for payer 42. That user retains priority
// while payer 43 receives a frame with no service_tier.
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

// TestWSResponseCreate_ForcePriorityRewritesKnownTier applies an any-tier
// force action to seven recognized tier spellings. Each successful
// response.create returns priority with no blocked result, including flex,
// default, fast and ultrafast inputs.
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

// TestWSResponseCreate_FlexPassThrough submits flex to the empty default
// policy. The helper returns no error or blocked result and retains flex in
// the outbound WS frame.
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

// TestWSResponseCreate_BlockReturnsTypedError supplies a whitelisted priority
// frame to a block action. The separate typed block carries the configured
// message, while the ordinary error stays nil and returned payload bytes
// remain equal to input.
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
	// Keep the input request available for rejection diagnostics when a block
	// supplies its separate typed result rather than accepted upstream payload.
	require.Equal(t, string(frame), string(updated))
}

// TestWSResponseCreate_NoServiceTierUntouched supplies a response.create with
// no requested tier under default policy. The helper returns byte-equal
// payload with no error or blocked result.
func TestWSResponseCreate_NoServiceTierUntouched(t *testing.T) {
	svc := newWSFastPolicy(t, tierpolicy.Default())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	frame := []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)
	updated, blocked, err := gatewayws.ApplyServiceTierFrame(frame, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, string(frame), string(updated), "no service_tier present must result in zero mutation")
}

// TestWSResponseCreate_NonResponseCreateFrameUntouched supplies
// response.cancel with a tier-shaped field under a broad filter rule. The
// frame is returned byte-equal with no policy error or block because this
// event is outside tier rewriting.
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

// TestWSResponseCreate_EmptyTypeFrameUntouched supplies absent and empty event
// types with priority fields under a filter. Both bodies remain byte-equal and
// produce no error or block in this direct frame-helper scenario.
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

	// Direct frame policy requires a recognized event label; the missing-type
	// input remains byte-equal even with a field shaped like a requested tier.
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

// ReadFrame returns the next scripted text payload by reference and advances
// its index. Exhausted frames return ErrWSConnClosed with text type; context
// and closeOnce are ignored, so tests control ending through the read script.
func (f *fakePassthroughFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if f.idx >= len(f.reads) {
		return coderws.MessageText, nil, openai.ErrWSConnClosed
	}
	payload := f.reads[f.idx]
	f.idx++
	return coderws.MessageText, payload, nil
}

// WriteFrame copies payload bytes into the fixture's writes slice and returns
// nil. It ignores context, message type and closeOnce; callers serialize this
// fixture and inspect copied payloads independently of their input buffers.
func (f *fakePassthroughFrameConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	cp := append([]byte(nil), payload...)
	f.writes = append(f.writes, cp)
	return nil
}

// Close records that fixture cleanup was called and returns nil. It changes no
// scripted read or write behavior, so later fixture operations remain
// available for assertions regardless of closeOnce.
func (f *fakePassthroughFrameConn) Close() error {
	f.closeOnce = true
	return nil
}

// gpt55WhitelistFastPolicy returns a priority filter whose whitelist names
// gpt-5.5 and its suffix forms. Other models select pass, making the retained
// model visible in output where an empty default rule set would yield
// identical frames.
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

// TestPolicyEnforcingFrameConn_FollowupFrameWithoutModelUsesCapturedModel
// retains gpt-5.5 from the first frame and filters a model-omitting followup.
// The wrapper removes service_tier while retaining response.create, making
// model reuse observable through the whitelist rule.
func TestPolicyEnforcingFrameConn_FollowupFrameWithoutModelUsesCapturedModel(t *testing.T) {
	// A whitelist produces different output for a missing retained model: its pass
	// fallback leaves priority present while an inherited gpt-5.5 selects filtering.
	svc := newWSFastPolicy(t, gpt55WhitelistFastPolicy())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	// Simulate the passthrough adapter capturing model from the first frame.
	firstFrame := []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"priority"}`)
	capturedSessionModel := openAIWSPassthroughPolicyModelForFrame(provider, firstFrame)
	require.Equal(t, "gpt-5.5", capturedSessionModel)

	// Later frames can omit model and use the captured session model.
	followupFrame := []byte(`{"type":"response.create","service_tier":"priority"}`)

	inner := &fakePassthroughFrameConn{ // wrapped fixture frame transport
		reads: [][]byte{followupFrame},
	}
	wrapper := &openAIWSPolicyEnforcingFrameConn{
		inner: inner,
		filter: func(msgType coderws.MessageType, payload []byte) ([]byte, *tierpolicy.BlockedError, error) {
			if msgType != coderws.MessageText {
				return payload, nil, nil
			}
			model := openAIWSPassthroughPolicyModelForFrame(provider, payload)
			// An omitted model uses the captured session choice so this frame's whitelist
			// decision follows the same conversation as the earlier model-bearing frame.
			if model == "" {
				model = capturedSessionModel
			}
			return gatewayws.ApplyServiceTierFrame(payload, model, svc.Input(context.Background(), provider, model))
		},
	}

	// Read through the production wrapper to observe the whitelist filter selected
	// from retained gpt-5.5, with the resulting response.create still identifiable.
	_, payload, err := wrapper.ReadFrame(context.Background())
	require.NoError(t, err)
	require.NotContains(t, string(payload), `"service_tier"`,
		"D5 regression: empty model on follow-up frame must fall back to capturedSessionModel; whitelist policy filters service_tier=priority for gpt-5.5")
	require.Equal(t, "response.create", gjson.GetBytes(payload, "type").String())
}

// TestOpenAIWSPassthroughPolicyModelDoesNotApplyProviderMapping enables
// passthrough on a provider mapping public-model to private-model. Fixture
// model readers return public-model for both response.create and
// session.update, checking their passthrough rule directly.
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

// TestPolicyEnforcingFrameConn_WithoutCapturedFallbackPolicyMisses filters a
// model-omitting frame while intentionally supplying no retained model. The
// whitelist misses and its pass fallback leaves service_tier present,
// contrasting the captured-model fixture's observable result.
func TestPolicyEnforcingFrameConn_WithoutCapturedFallbackPolicyMisses(t *testing.T) {
	// Use a whitelist to expose the missing captured model.
	svc := newWSFastPolicy(t, gpt55WhitelistFastPolicy())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	followupFrame := []byte(`{"type":"response.create","service_tier":"priority"}`)
	inner := &fakePassthroughFrameConn{reads: [][]byte{followupFrame}} // wrapped fixture frame transport
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

// TestApplyOpenAIFastPolicyToBody_BlockShortCircuitsUpstream applies a
// model-scoped priority block to an HTTP body. ApplyBody returns the
// configured typed BlockedError and byte-equal body, allowing its caller to
// stop before exchange.
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

// TestForwardAsAnthropicMessages_BetaFastModePassesOpenAIFastPolicyByDefault
// converts an Anthropic request and supplies priority corresponding to its
// tested Fast beta token. Applying default policy to the converted Responses
// body retains that priority tier without error.
func TestForwardAsAnthropicMessages_BetaFastModePassesOpenAIFastPolicyByDefault(t *testing.T) {
	svc := newWSFastPolicy(t, tierpolicy.Default())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	// Convert the Anthropic request before applying the beta-header tier.
	anthropicBody := []byte(`{"model":"gpt-5.5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	var anthropicReq protocolanthropic.AnthropicRequest
	require.NoError(t, json.Unmarshal(anthropicBody, &anthropicReq))
	responsesReq, err := protocolbridge.AnthropicToResponses(&anthropicReq, protocolforward.ConversionOptionsForModel(anthropicReq.Model))
	require.NoError(t, err)

	// Supply priority directly after recognizing the beta token; the fixture checks
	// subsequent policy handling after that manual tier assignment in the Responses body.
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

// TestPolicyEnforcingFrameConn_SessionUpdateRotatesCapturedModel starts with
// gpt-4o, forwards a session.update to gpt-5.5 and reads a model-omitting
// followup. The initial priority survives, session bytes remain equal, and the
// followup matches the filter and loses service_tier.
func TestPolicyEnforcingFrameConn_SessionUpdateRotatesCapturedModel(t *testing.T) {
	svc := newWSFastPolicy(t, gpt55WhitelistFastPolicy())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	// gpt-4o misses the whitelist, so fallback=pass retains service_tier.
	first := []byte(`{"type":"response.create","model":"gpt-4o","service_tier":"priority"}`)
	// Frame 2: session.update rotates the session model to gpt-5.5.
	rotate := []byte(`{"type":"session.update","session":{"model":"gpt-5.5"}}`)
	// The third frame omits model and inherits gpt-5.5.
	followup := []byte(`{"type":"response.create","service_tier":"priority"}`)

	inner := &fakePassthroughFrameConn{reads: [][]byte{first, rotate, followup}} // wrapped fixture frame transport

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
			// An omitted model uses the captured session choice so this frame's whitelist
			// decision follows the same conversation as the earlier model-bearing frame.
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

// TestPolicyModelFromSessionFrame_OnlySessionUpdate supplies a server
// session.created, response.create with session data and a model-less
// session.update. Every fixture model read returns empty, leaving the caller's
// retained session model available.
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

// TestApplyOpenAIFastPolicyToBody_PassNormalizesFastAlias uses a whitelist
// miss that selects pass. Fast and padded mixed-case aliases become priority,
// canonical priority stays byte-equal, and unknown turbo also stays
// byte-equal.
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

// TestPassthroughBilling_PostFilterServiceTier compares request metadata
// before and after an explicit priority filter. Outbound tier extraction
// becomes nil, and applying the filter again leaves the already filtered frame
// byte-equal.
func TestPassthroughBilling_PostFilterServiceTier(t *testing.T) {
	svc := newWSFastPolicy(t, openAIFastFilterPriorityPolicy())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	raw := []byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"priority"}`)

	// Read the pre-policy tier as a control; the later extraction uses the filtered
	// frame to observe the actual outbound billing metadata after policy removal.
	pre := requeststate.ExtractOpenAIServiceTierFromBody(raw)
	require.NotNil(t, pre)
	require.Equal(t, "priority", *pre,
		"sanity: raw first frame carries priority that pre-fix billing would have reported")

	// Filtering changes the accepted request before billing metadata is extracted,
	// so the next assertion reads the outbound frame rather than pre-policy intent.
	filtered, blocked, err := gatewayws.ApplyServiceTierFrame(raw, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.NotContains(t, string(filtered), `"service_tier"`)

	// Post-filter extraction supplies the outbound tier stored in forwarding
	// results. Observed response tier is recorded separately and does not replace
	// this extraction result before completion receives those values.
	post := requeststate.ExtractOpenAIServiceTierFromBody(filtered)
	require.Nil(t, post, "fix3: post-filter extraction must preserve a nil outbound tier")

	// Apply policy again after removal to check byte equality; repeated filtering
	// cannot create a tier value absent from the already accepted outbound request.
	again, blocked2, err := gatewayws.ApplyServiceTierFrame(filtered, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked2)
	require.Equal(t, string(filtered), string(again),
		"policy is idempotent: filtering an already-filtered frame leaves bytes unchanged")
}

// TestApplyOpenAIFastPolicyToBody_NonStringServiceTier supplies numeric, null,
// object, array and boolean tier values. HTTP ApplyBody returns byte-equal
// input without error. The WS inputs omit type, so their byte equality and
// absent blocked result exercise the helper's missing-event pass-through path.
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

// TestPassthroughBilling_MultiTurnServiceTierFollowsFilteredFrames simulates
// accepted-frame tier publication across priority, flex and omitted-tier
// requests. Filtering priority clears the atom, flex becomes current,
// response.cancel leaves flex intact, and a later omitted tier clears it
// again.
func TestPassthroughBilling_MultiTurnServiceTierFollowsFilteredFrames(t *testing.T) {
	svc := newWSFastPolicy(t, openAIFastFilterPriorityPolicy())
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	// Mirror per-frame atomic storage to check billing metadata across turns.
	var requestServiceTierPtr atomic.Pointer[string]
	capturedSessionModel := ""
	filter := func(msgType coderws.MessageType, payload []byte) ([]byte, *tierpolicy.BlockedError, error) { // optional inbound frame policy callback
		if msgType != coderws.MessageText {
			return payload, nil, nil
		}
		if updated := openAIWSPassthroughPolicyModelFromSessionFrame(provider, payload); updated != "" {
			capturedSessionModel = updated
		}
		model := openAIWSPassthroughPolicyModelForFrame(provider, payload)
		// An omitted model uses the captured session choice so this frame's whitelist
		// decision follows the same conversation as the earlier model-bearing frame.
		if model == "" {
			model = capturedSessionModel
		}
		out, blocked, policyErr := gatewayws.ApplyServiceTierFrame(payload, model, svc.Input(context.Background(), provider, model))
		if policyErr == nil && blocked == nil &&
			strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.create" {
			// Publish the accepted post-policy tier only after both ordinary error and
			// block checks pass, leaving previous metadata intact for rejected frames.
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

	// The simulated publisher updates tier only for accepted response.create;
	// exercise cancel with a tier-shaped field so prior flex remains the current atom.
	cancelFrame := []byte(`{"type":"response.cancel","service_tier":"priority"}`)
	_, blockedCancel, errCancel := filter(coderws.MessageText, cancelFrame)
	require.NoError(t, errCancel)
	require.Nil(t, blockedCancel)
	tierAfterCancel := requestServiceTierPtr.Load()
	require.NotNil(t, tierAfterCancel, "response.cancel must not clobber billing tier to nil")
	require.Equal(t, "flex", *tierAfterCancel,
		"non-response.create frames must not update billing tier even if they carry a service_tier-shaped field")

	// An accepted request with no tier publishes absence for its own turn;
	// retaining the earlier flex value here would misidentify this outbound request.
	turn3 := []byte(`{"type":"response.create","model":"gpt-5.5"}`)
	out3, blocked3, err3 := filter(coderws.MessageText, turn3)
	require.NoError(t, err3)
	require.Nil(t, blocked3)
	require.Equal(t, string(turn3), string(out3), "turn 3 has no service_tier — filter must not mutate")
	require.Nil(t, requestServiceTierPtr.Load(),
		"turn 3: response.create without service_tier overwrites billing to nil to match upstream default")
}

// TestPassthroughUsageMeta_TracksReasoningEffortAcrossTurns observes medium,
// flat x-high, explicit max and absent effort. Session update and cancel leave
// current medium intact, accepted requests publish xhigh and max, and a later
// request without usable effort clears the atom.
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
		// An omitted model uses the captured session choice so this frame's whitelist
		// decision follows the same conversation as the earlier model-bearing frame.
		if model == "" {
			model = capturedSessionModel
		}
		out, blocked, policyErr := gatewayws.ApplyServiceTierFrame(payload, model, svc.Input(context.Background(), provider, model))
		if policyErr == nil && blocked == nil &&
			strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.create" {
			// Capture effort from the accepted transformed frame; session updates and
			// canceled or blocked frames keep the current turn's effort observation intact.
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

// TestPassthroughBilling_BlockedFrameDoesNotMutateServiceTier initializes
// prior accepted tier flex, then submits a blocked priority request to its
// simulated filter. The blocked result is present and the tier atom still
// holds flex.
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
	requestServiceTierPtr.Store(&flexValue) // simulate prior accepted turn's flex tier

	filter := func(msgType coderws.MessageType, payload []byte) ([]byte, *tierpolicy.BlockedError, error) { // optional inbound frame policy callback
		if msgType != coderws.MessageText {
			return payload, nil, nil
		}
		out, blocked, policyErr := gatewayws.ApplyServiceTierFrame(payload, "gpt-5.5", svc.Input(context.Background(), provider, "gpt-5.5"))
		if policyErr == nil && blocked == nil &&
			strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.create" {
			// Publish the accepted post-policy tier only after both ordinary error and
			// block checks pass, leaving previous metadata intact for rejected frames.
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

// runtime returns the production policy frame wrapper constructed once from the
// stable fixture transport and callbacks. It converts message types and retains
// a recognized BlockedError cause by pointer when passing a policy block to the
// fixture observer. An unrecognized cause produces a new message-only
// BlockedError for that observer.
//
// Configure inner and callbacks before first invocation and keep them stable
// for the wrapper's lifetime. sync.Once publishes the completed core wrapper,
// while synchronization of later frame operations remains with the wrapped
// transport and production policy implementation.
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
				// Core blocks can carry another cause representation; recreate the fixture
				// type from its message so the optional observer still receives its expected value.
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

// ReadFrame delegates fixture policy reading to the retained production
// wrapper and converts its message type. Nil receiver or inner transport
// returns ErrWSConnClosed directly; the core handles accepted, transformed and
// blocked frame outcomes.
func (c *openAIWSPolicyEnforcingFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if c == nil || c.inner == nil {
		return coderws.MessageText, nil, openai.ErrWSConnClosed
	}
	typ, body, err := c.runtime().ReadFrame(ctx)
	return coderws.MessageType(typ), body, err
}

// WriteFrame delegates a client-bound write through the optional production
// write filter. Nil receiver or inner transport returns ErrWSConnClosed; the
// caller's context and bytes reach the retained wrapper with integer message
// type.
func (c *openAIWSPolicyEnforcingFrameConn) WriteFrame(ctx context.Context, typ coderws.MessageType, body []byte) error {
	if c == nil || c.inner == nil {
		return openai.ErrWSConnClosed
	}
	return c.runtime().WriteFrame(ctx, int(typ), body)
}

// Close releases the inner transport through the production policy wrapper.
// Nil receiver or missing transport returns nil; wrapper initialization occurs
// before a present transport's close, retaining the same optional callback
// wiring.
func (c *openAIWSPolicyEnforcingFrameConn) Close() error {
	if c == nil || c.inner == nil {
		return nil
	}
	return c.runtime().Close()
}

// markTurnStarted clears the fixture's waiting flag through production
// TurnActivity for an accepted request. Nil fixtures are ignored; callers use
// the lifecycle admission callback to order this atomic store against terminal
// completion.
func (c *openAIWSClientFrameConn) markTurnStarted() {
	if c != nil {
		gatewayws.TurnActivity{Waiting: &c.waitingForNextTurn, Started: c.interTurnStarted}.MarkStarted()
	}
}

// markTurnCompleted publishes completed-turn waiting state through production
// TurnActivity and its optional notification. Nil fixtures are ignored;
// channel signaling is nonblocking so idle publication does not wait for a
// reader.
func (c *openAIWSClientFrameConn) markTurnCompleted() {
	if c != nil {
		gatewayws.TurnActivity{Waiting: &c.waitingForNextTurn, Started: c.interTurnStarted}.MarkCompleted()
	}
}

// openAIWSPassthroughPolicyModelForFrame returns this fixture's policy model
// from the root field. Missing provider, empty payload or blank model returns
// empty; passthrough retains the client spelling, while other providers apply
// mapping then platform normalization.
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

// openAIWSPassthroughPolicyModelFromSessionFrame returns a fixture policy
// model only for session.update with nonblank session.model. Missing provider
// or bytes and other events return empty. Passthrough retains client spelling;
// otherwise mapping and normalization precede publication to the caller's
// retained model.
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

// newOpenAIWSPassthroughUsageMeta constructs production UsageMeta from the
// initial model and body. It exposes the embedded effort atom through a
// pointer alias so assertions read the same state updated by subsequent
// fixture callbacks.
func newOpenAIWSPassthroughUsageMeta(model string, body []byte) *openAIWSPassthroughUsageMeta {
	meta := gatewayws.NewUsageMeta(model, body, gatewayws.RequestUsageDecoder{})
	return &openAIWSPassthroughUsageMeta{UsageMeta: meta, reasoningEffort: &meta.ReasoningEffort}
}

// initFromFirstFrame publishes production metadata from the supplied accepted
// first-frame bytes. Nil wrapper is ignored and model is unused; the embedded
// UsageMeta retains its constructor's session model while reading tier and
// effort from this body.
func (m *openAIWSPassthroughUsageMeta) initFromFirstFrame(body []byte, model string) {
	if m != nil {
		m.InitFromFirstFrame(body)
	}
}

// updateSessionRequestModel delegates session.update model publication to
// production UsageMeta. Nil wrapper is ignored; the core changes its fallback
// model from recognized input while current accepted turn effort and tier
// remain separately published.
func (m *openAIWSPassthroughUsageMeta) updateSessionRequestModel(body []byte) {
	if m != nil {
		m.UpdateSessionRequestModel(body)
	}
}

// updateFromResponseCreate delegates accepted request metadata publication to
// production UsageMeta. Nil wrapper is ignored and mapped or requested
// arguments are unused; tier and effort are extracted directly from the
// supplied post-policy bytes.
func (m *openAIWSPassthroughUsageMeta) updateFromResponseCreate(body []byte, mapped, requested string) {
	if m != nil {
		m.UpdateFromResponseCreate(body)
	}
}

// newOpenAIWSPassthroughTurnLifecycle returns the production admission barrier
// with supplied initial in-flight state. The fixture exposes its terminal
// write operations so tests can pair delivery outcome with later
// response.create admission.
func newOpenAIWSPassthroughTurnLifecycle(inFlight bool) *openAIWSPassthroughTurnLifecycle {
	return &openAIWSPassthroughTurnLifecycle{gatewayws.NewTurnLifecycle(inFlight)}
}

// beginResponseCreate requests production turn admission and invokes the
// optional callback under its barrier lock after acceptance. Nil fixture
// returns false; callbacks do not reenter the lifecycle, and active-turn
// rejection leaves the next request unaccepted.
func (l *openAIWSPassthroughTurnLifecycle) beginResponseCreate(fn func()) bool {
	if l == nil {
		return false
	}
	return l.BeginResponseCreate(fn)
}

// beginTerminalWrite acquires production terminal write ownership and holds it
// until finishTerminalWrite. Nil fixture is ignored; callers pair every
// successful begin with one finish, including failed client sends, to release
// the barrier.
func (l *openAIWSPassthroughTurnLifecycle) beginTerminalWrite() {
	if l != nil {
		l.BeginTerminalWrite()
	}
}

// finishTerminalWrite releases a previously acquired terminal write barrier
// and publishes idle only after successful delivery. Nil fixture is ignored;
// the optional callback runs under the core lock and does not reenter
// lifecycle methods.
func (l *openAIWSPassthroughTurnLifecycle) finishTerminalWrite(ok bool, fn func()) {
	if l != nil {
		l.FinishTerminalWrite(ok, fn)
	}
}

// requestModelForFrame returns explicit or retained session model through
// production UsageMeta. Nil wrapper uses the protocol's direct frame model
// reader, allowing assertions to distinguish missing-session state from
// inherited-model selection.
func (m *openAIWSPassthroughUsageMeta) requestModelForFrame(body []byte) string {
	if m == nil {
		return gatewayws.RequestModelForFrame(body)
	}
	return m.RequestModelForFrame(body)
}

// TestOpenAIWSPassthroughTurnLifecycle_SerializesTerminalCommitAndNextTurn
// holds terminal write ownership while a later request attempts admission. No
// result arrives during the initial scheduling window; successful terminal
// delivery permits admission and clears waiting state, while failed delivery
// returns false and skips idle publication.
func TestOpenAIWSPassthroughTurnLifecycle_SerializesTerminalCommitAndNextTurn(t *testing.T) {
	clientFrameConn := &openAIWSClientFrameConn{interTurnStarted: make(chan struct{}, 1)}
	clientFrameConn.markTurnCompleted()
	lifecycle := newOpenAIWSPassthroughTurnLifecycle(true)
	lifecycle.beginTerminalWrite()

	// Buffer one admission outcome while terminal ownership is held, then inspect
	// a scheduling window before releasing success; the window does not prove worker entry.
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
	// A failed terminal send retains in-flight state even if the admission worker
	// runs after release, so either schedule must produce rejection and no idle callback.
	admitted = make(chan bool, 1)
	go func() {
		admitted <- lifecycle.beginResponseCreate(nil)
	}()
	lifecycle.finishTerminalWrite(false, func() {
		t.Error("failed terminal write must not commit idle state")
	})
	require.False(t, <-admitted, "failed terminal write must keep the current turn in flight")
}
