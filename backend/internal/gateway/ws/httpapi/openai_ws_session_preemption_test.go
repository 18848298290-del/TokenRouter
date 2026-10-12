package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// openAIWSSessionPreemptCacheStub models atomic owner token replacement for session tests.
type openAIWSSessionPreemptCacheStub struct {
	session.GatewayCache // unused ordinary gateway cache methods
	// mu protects owners across claims and token comparisons. Stored and returned
	// tokens are copied by Claim so fixture callers cannot change ownership bytes
	// through a retained slice. TTL and context cancellation are not simulated.
	mu     sync.Mutex        // owner token mutation lock
	owners map[string][]byte // group and cache-hash owners
}

// key formats the supplied group and cache hash for fixture owner lookup.
func (c *openAIWSSessionPreemptCacheStub) key(groupID int64, hash string) string {
	return fmt.Sprintf("%d:%s", groupID, hash)
}

// ClaimOpenAIResponsesSessionWindow replaces ownership for groupID and hash and
// returns a copied previous token. It ignores context and TTL, so tests use
// this owner map to model replacement separately from expiry and cancellation.
// The supplied new token is copied before storage so later mutation of the
// input bytes leaves ownership unchanged.
//
// The mutex covers prior-owner lookup and new-token publication as one
// operation. A first claim returns nil previous bytes; later claims observe the
// token installed by the preceding serialized claim.
func (c *openAIWSSessionPreemptCacheStub) ClaimOpenAIResponsesSessionWindow(_ context.Context, groupID int64, hash string, owner []byte, _ time.Duration) ([]byte, error) {
	// Publish a copied token and read the prior copied value in one critical
	// section so concurrent fixture claims cannot observe a partially replaced owner.
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owners == nil {
		c.owners = make(map[string][]byte)
	}
	key := c.key(groupID, hash)
	previous := append([]byte(nil), c.owners[key]...)
	c.owners[key] = append([]byte(nil), owner...)
	return previous, nil
}

// CompareAndRefreshOpenAIResponsesSessionWindow compares stored token bytes
// with expected under the fixture mutex. It ignores TTL and cancellation and
// performs no expiry update. An absent map entry compares as empty bytes, so an
// empty expected token also matches absence; tests use nonempty owner tokens
// for replacement detection.
func (c *openAIWSSessionPreemptCacheStub) CompareAndRefreshOpenAIResponsesSessionWindow(_ context.Context, groupID int64, hash string, expected []byte, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.owners[c.key(groupID, hash)]) == string(expected), nil
}

// CompareAndDeleteOpenAIResponsesSessionWindow deletes the keyed owner when its
// bytes match expected and reports that match. A mismatch leaves the map
// unchanged. Context is ignored and the fixture models no expiry.
//
// Comparison and deletion share one mutex, so a claim cannot publish a
// replacement between those steps. An absent entry compares as empty bytes:
// empty expected therefore returns true even when deletion changes no entry.
// The core supplies nonempty tokens, allowing stale release tests to
// distinguish live ownership from absence.
func (c *openAIWSSessionPreemptCacheStub) CompareAndDeleteOpenAIResponsesSessionWindow(_ context.Context, groupID int64, hash string, expected []byte) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := c.key(groupID, hash)
	if string(c.owners[key]) != string(expected) {
		return false, nil
	}
	delete(c.owners, key)
	return true, nil
}

// openAIWSSessionPreemptCacheHash returns the core's Key-scoped hash for
// remote owner fixtures.
func openAIWSSessionPreemptCacheHash(apiKeyID int64, sessionHash string) string {
	return ws.CacheHash(apiKeyID, sessionHash)
}

// TestOpenAIWSSessionPreemptRegistryCancelsSameScopedSessionOnly registers
// equal session hashes under two Keys and replaces one scope. Only that
// scope's prior context is canceled, and its stale cleanup leaves the
// replacement context live.
func TestOpenAIWSSessionPreemptRegistryCancelsSameScopedSessionOnly(t *testing.T) {
	var registry openAIWSSessionPreemptRegistry
	key := openAIWSSessionPreemptKey{groupID: 7, apiKeyID: 11, sessionHash: "sess"}
	other := openAIWSSessionPreemptKey{groupID: 7, apiKeyID: 12, sessionHash: "sess"}
	firstCtx, firstCancel := context.WithCancel(context.Background())
	firstCleanup, replaced := registry.Begin(key, firstCancel)
	require.False(t, replaced)
	otherCtx, otherCancel := context.WithCancel(context.Background())
	otherCleanup, replaced := registry.Begin(other, otherCancel)
	require.False(t, replaced)
	secondCtx, secondCancel := context.WithCancel(context.Background())
	secondCleanup, replaced := registry.Begin(key, secondCancel)
	require.True(t, replaced)

	require.ErrorIs(t, firstCtx.Err(), context.Canceled)
	require.NoError(t, otherCtx.Err())
	require.NoError(t, secondCtx.Err())
	firstCleanup()
	require.NoError(t, secondCtx.Err(), "stale cleanup must not remove the replacement")

	secondCleanup()
	otherCleanup()
}

// TestOpenAIWSSessionPreemptContextEligibilityAndLocalCancellation compares
// API Key, Grok and HTTP one-shot ineligibility with pooled OpenAI OAuth
// replacement. Eligible replacement cancels the first context with the
// preemption cause and clears its saved turn-state and connection hints.
func TestOpenAIWSSessionPreemptContextEligibilityAndLocalCancellation(t *testing.T) {
	stateStore := session.NewOpenAIWSStateStore(nil, gatewayprovider.LogOpenAIWSModeInfo)
	svc := newWSFixture(wsFixtureInputs{state: stateStore})
	oauth := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	apiKey := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	grok := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 3, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}

	_, cleanup, armed, _ := svc.beginOpenAIWSSessionPreemptContext(context.Background(), apiKey, 7, 11, "sess", false)
	cleanup()
	require.False(t, armed)
	_, cleanup, armed, _ = svc.beginOpenAIWSSessionPreemptContext(context.Background(), grok, 7, 11, "sess", false)
	cleanup()
	require.False(t, armed)
	_, cleanup, armed, _ = svc.beginOpenAIWSSessionPreemptContext(context.Background(), oauth, 7, 11, "sess", true)
	cleanup()
	require.False(t, armed, "HTTP-ingress one-shot must not participate")

	firstCtx, firstCleanup, armed, replaced := svc.beginOpenAIWSSessionPreemptContext(context.Background(), oauth, 7, 11, "sess", false)
	require.True(t, armed)
	require.False(t, replaced)
	stateStore.BindSessionTurnState(7, "sess", "turn-state", time.Hour)
	stateStore.BindSessionConn(7, "sess", "conn-1", time.Hour)
	_, secondCleanup, armed, replaced := svc.beginOpenAIWSSessionPreemptContext(context.Background(), oauth, 7, 11, "sess", false)
	require.True(t, armed)
	require.True(t, replaced)
	require.True(t, isOpenAIWSSessionPreempted(firstCtx))
	require.True(t, ws.IsSessionPreemptedError(context.Cause(firstCtx)))
	_, turnStateExists := stateStore.GetSessionTurnState(7, "sess")
	_, sessionConnExists := stateStore.GetSessionConn(7, "sess")
	require.False(t, turnStateExists)
	require.False(t, sessionConnExists)
	firstCleanup()
	secondCleanup()
}

// TestOpenAIWSIngressSessionPreemptionSurvivesNestedForwardCleanup enters
// preemption again under an already armed context and invokes nested cleanup.
// The first owner stays active until a separate connection replaces it,
// confirming registration survives attempt cleanup.
func TestOpenAIWSIngressSessionPreemptionSurvivesNestedForwardCleanup(t *testing.T) {
	groupID := int64(7)
	newContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
		c.Set("api_key", &apikey.APIKey{ID: 11, GroupID: &groupID})
		return c
	}

	svc := newWSFixture(wsFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	firstMessage := []byte(`{"type":"response.create","prompt_cache_key":"session-1","input":"hello"}`)

	firstCtx, firstCleanup, armed := svc.BeginOpenAIWSIngressSessionPreemption(
		context.Background(), newContext(), provider, firstMessage,
	)
	require.True(t, armed)
	defer firstCleanup()

	// ProxyResponsesWebSocketFromClient enters the same helper for each upstream
	// attempt. Its cleanup must not release the handler-owned registration.
	nestedCtx, nestedCleanup, armed := svc.BeginOpenAIWSIngressSessionPreemption(
		firstCtx, newContext(), provider, firstMessage,
	)
	require.True(t, armed)
	require.Equal(t, firstCtx, nestedCtx)
	nestedCleanup()
	require.NoError(t, firstCtx.Err())

	_, secondCleanup, armed := svc.BeginOpenAIWSIngressSessionPreemption(
		context.Background(), newContext(), provider, firstMessage,
	)
	require.True(t, armed)
	defer secondCleanup()
	require.True(t, ws.IsSessionPreemptedError(context.Cause(firstCtx)))
}

// TestOpenAIWSIngressSessionPreemptionRespectsResolvedMode starts matching
// sessions in dedicated and pooled connection modes. Dedicated sessions remain
// unarmed and uncanceled; pooled replacement arms ownership and cancels the
// first owner's context.
func TestOpenAIWSIngressSessionPreemptionRespectsResolvedMode(t *testing.T) {
	groupID := int64(7)
	newContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
		c.Set("api_key", &apikey.APIKey{ID: 11, GroupID: &groupID})
		return c
	}
	newProvider := func(mode string) *gatewayprovider.ExecutionProvider {
		return &gatewayprovider.ExecutionProvider{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: 1,
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeOAuth,
				Extra: map[string]any{
					"responses_ws_connection_mode": mode,
				},
			},
		}
	}
	firstMessage := []byte(`{"type":"response.create","prompt_cache_key":"session-1","input":"hello"}`)
	options := &wsFixtureOptions{}

	svc := newWSFixture(wsFixtureInputs{options: options})

	passthrough := newProvider("per_session")
	firstCtx, firstCleanup, armed := svc.BeginOpenAIWSIngressSessionPreemption(
		context.Background(), newContext(), passthrough, firstMessage,
	)
	require.False(t, armed)
	defer firstCleanup()
	secondCtx, secondCleanup, armed := svc.BeginOpenAIWSIngressSessionPreemption(
		context.Background(), newContext(), passthrough, firstMessage,
	)
	require.False(t, armed)
	defer secondCleanup()
	require.NoError(t, firstCtx.Err(), "concurrent passthrough request must remain isolated")
	require.NoError(t, secondCtx.Err())

	ctxPool := newProvider("pooled")
	sharedCtx, sharedCleanup, armed := svc.BeginOpenAIWSIngressSessionPreemption(
		context.Background(), newContext(), ctxPool, firstMessage,
	)
	require.True(t, armed)
	defer sharedCleanup()
	_, replacementCleanup, armed := svc.BeginOpenAIWSIngressSessionPreemption(
		context.Background(), newContext(), ctxPool, firstMessage,
	)
	require.True(t, armed)
	defer replacementCleanup()
	require.True(t, ws.IsSessionPreemptedError(context.Cause(sharedCtx)), "ctx_pool must retain shared-session preemption")
}

// TestOpenAIWSSessionPreemptRemoteClaimAndStaleReleaseAreAtomic claims two
// successive remote owner tokens then releases the first. The second claim
// reports the previous token, and stale compare-delete leaves the second owner
// stored.
func TestOpenAIWSSessionPreemptRemoteClaimAndStaleReleaseAreAtomic(t *testing.T) {
	cache := &openAIWSSessionPreemptCacheStub{}
	svc := newWSFixture(wsFixtureInputs{cache: cache})
	key := openAIWSSessionPreemptKey{groupID: 7, apiKeyID: 11, sessionHash: "sess"}

	previous, ok := svc.claimOpenAIWSSessionPreemptOwner(context.Background(), key, "owner-a")
	require.True(t, ok)
	require.Empty(t, previous)
	previous, ok = svc.claimOpenAIWSSessionPreemptOwner(context.Background(), key, "owner-b")
	require.True(t, ok)
	require.Equal(t, "owner-a", previous)
	svc.releaseOpenAIWSSessionPreemptOwner(context.Background(), key, "owner-a")

	cache.mu.Lock()
	current := string(cache.owners[cache.key(key.groupID, openAIWSSessionPreemptCacheHash(key.apiKeyID, key.sessionHash))])
	cache.mu.Unlock()
	require.Equal(t, "owner-b", current, "stale cleanup must preserve the replacement owner")
}

// TestNewOpenAIWSSessionPreemptKeyRequiresFullIsolationScope supplies missing
// group, Key and hash values before a valid padded hash. Invalid scope returns
// false, and the valid result retains the trimmed session hash.
func TestNewOpenAIWSSessionPreemptKeyRequiresFullIsolationScope(t *testing.T) {
	_, ok := newOpenAIWSSessionPreemptKey(0, 11, "sess")
	require.False(t, ok)
	_, ok = newOpenAIWSSessionPreemptKey(7, 0, "sess")
	require.False(t, ok)
	_, ok = newOpenAIWSSessionPreemptKey(7, 11, " ")
	require.False(t, ok)
	key, ok := newOpenAIWSSessionPreemptKey(7, 11, " sess ")
	require.True(t, ok)
	require.Equal(t, "sess", key.sessionHash)
}
