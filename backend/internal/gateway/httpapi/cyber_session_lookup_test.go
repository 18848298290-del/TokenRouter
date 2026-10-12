package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

func newCyberBlockTestCtx(headers map[string]string, body string) (*gin.Context, []byte) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c.Request = req
	return c, []byte(body)
}

func TestCyberSessionExplicitBlockKey(t *testing.T) {
	c1, b1 := newCyberBlockTestCtx(map[string]string{"session_id": "sess-abc"}, `{}`)
	k1 := CyberSessionExplicitBlockKey(101, c1, b1)
	require.NotEmpty(t, k1)

	// The same session under different API keys has separate block keys.
	c2, b2 := newCyberBlockTestCtx(map[string]string{"session_id": "sess-abc"}, `{}`)
	require.NotEqual(t, k1, CyberSessionExplicitBlockKey(202, c2, b2))

	// The same session and API key produce a stable block key.
	c3, b3 := newCyberBlockTestCtx(map[string]string{"session_id": "sess-abc"}, `{}`)
	require.Equal(t, k1, CyberSessionExplicitBlockKey(101, c3, b3))

	// prompt_cache_key in body counts as explicit.
	c4, b4 := newCyberBlockTestCtx(nil, `{"prompt_cache_key":"pck-1"}`)
	require.NotEmpty(t, CyberSessionExplicitBlockKey(101, c4, b4))

	// Without an explicit session identity this lookup returns no block key.
	c5, b5 := newCyberBlockTestCtx(nil, `{"input":"hello world"}`)
	require.Empty(t, CyberSessionExplicitBlockKey(101, c5, b5))

	// conversation_id header counts as explicit; key is stable and non-empty.
	c6, b6 := newCyberBlockTestCtx(map[string]string{"conversation_id": "conv-xyz"}, `{}`)
	k6 := CyberSessionExplicitBlockKey(101, c6, b6)
	require.NotEmpty(t, k6)
	c6b, b6b := newCyberBlockTestCtx(map[string]string{"conversation_id": "conv-xyz"}, `{}`)
	require.Equal(t, k6, CyberSessionExplicitBlockKey(101, c6b, b6b), "conversation_id key must be stable")
}

func TestCyberTranscriptBlockKeysRequireModelGeneratedHistory(t *testing.T) {
	first := []byte(`{"instructions":"shared","input":[{"role":"user","content":"fixed environment"},{"role":"user","content":"question one"}]}`)
	second := []byte(`{"instructions":"shared","input":[{"role":"user","content":"fixed environment"},{"role":"user","content":"question two"}]}`)
	firstKeys := session.CyberSessionTranscriptBlockKeys(77, first)
	secondKeys := session.CyberSessionTranscriptBlockKeys(77, second)
	require.Len(t, firstKeys, 1)
	require.Len(t, secondKeys, 1)
	require.NotEqual(t, firstKeys[0], secondKeys[0])

	hit := []byte(`{"messages":[{"role":"user","content":"setup"},{"role":"assistant","content":"ready"},{"role":"user","content":"trigger"}]}`)
	continuation := []byte(`{"messages":[{"role":"user","content":"setup"},{"role":"assistant","content":"ready"},{"role":"user","content":"different trigger"},{"role":"assistant","content":"blocked"},{"role":"user","content":"continue"}]}`)
	hitKeys := session.CyberSessionTranscriptBlockKeys(77, hit)
	require.Len(t, hitKeys, 2)
	require.Contains(t, session.CyberSessionTranscriptLookupKeys(77, continuation), hitKeys[1])
}

func TestCyberTranscriptBlockKeysWebSocketResponseCreate(t *testing.T) {
	body := []byte(`{"type":"response.create","response":{"prompt_cache_key":"ws-session","input":[{"role":"user","content":"setup"},{"role":"assistant","content":"ready"},{"role":"user","content":"trigger"}]}}`)
	c, _ := newCyberBlockTestCtx(nil, string(body))
	require.NotEmpty(t, CyberSessionExplicitBlockKey(88, c, body))
	require.Len(t, session.CyberSessionTranscriptBlockKeys(88, body), 2)
}

func TestCyberTranscriptLookupKeysAreBoundedAndKeepNewestOrder(t *testing.T) {
	messages := make([]map[string]string, 256+44)
	for i := range messages {
		messages[i] = map[string]string{"role": "user", "content": "message-" + strconv.Itoa(i)}
	}
	body, err := json.Marshal(map[string]any{"messages": messages})
	require.NoError(t, err)

	keys := session.CyberSessionTranscriptLookupKeys(77, body)
	require.Len(t, keys, 256)

	firstRetainedBody, err := json.Marshal(map[string]any{"messages": messages[:45]})
	require.NoError(t, err)
	firstRetainedPrefix := session.CyberSessionTranscriptLookupKeys(77, firstRetainedBody)
	require.Equal(t, firstRetainedPrefix[len(firstRetainedPrefix)-1], keys[0])

	fullKey := session.CyberSessionTranscriptBlockKeys(77, body)[0]
	require.Equal(t, fullKey, keys[len(keys)-1])
}

// TestFindCyberSessionBlocked_EmptyAndNilService checks that missing lookup dependencies fail open.
// Nil services and missing stores return no block key and tolerate an empty mark request.
func TestFindCyberSessionBlocked_EmptyAndNilService(t *testing.T) {
	var nilSvc *session.CyberBlocks
	require.Empty(t, FindCyberSessionForRequest(context.Background(), nilSvc, 1, nil, nil, "", ""))
	require.NotPanics(t, func() { nilSvc.MarkCyberSessionBlocked(context.Background(), "", []string{"k"}) })

	svc := session.NewCyberBlocks(nil, nil, nil)
	require.Empty(t, FindCyberSessionForRequest(context.Background(), svc, 1, nil, nil, "", ""))
}

// TestCyberSessionBlock_RoundTrip records a block through the cache/store adapter
// and checks lookup of the blocked session and an unrelated session.

func TestCyberSessionBlock_RoundTrip(t *testing.T) {
	// The fixture supplies settings read by GetCyberSessionBlockRuntime.
	settingSvc := moderation.NewRuntimeSettings(&fakeSettingRepo{
		vals: map[string]string{
			moderation.SettingKeyCyberSessionBlockEnabled:    "true",
			moderation.SettingKeyCyberSessionBlockTTLSeconds: "60",
		},
	}, settings.ErrSettingNotFound)

	combo := &comboCacheAndStore{}
	svc := session.NewCyberBlocks(session.AdaptCyberSessionBlockStore(combo), settingSvc.GetCyberSessionBlockRuntime, nil)

	ctx := context.Background()
	const testKey = "deadbeef1234"

	c, body := newCyberBlockTestCtx(map[string]string{"session_id": "sess-roundtrip"}, `{}`)
	explicitKey := CyberSessionExplicitBlockKey(1, c, body)
	require.Empty(t, FindCyberSessionForRequest(ctx, svc, 1, c, body, "203.0.113.1", "client/1.0"))

	svc.MarkCyberSessionBlocked(ctx, "", []string{explicitKey, testKey})

	require.Equal(t, explicitKey, FindCyberSessionForRequest(ctx, svc, 1, c, body, "203.0.113.1", "client/1.0"))
}

func TestFindCyberSessionBlockedForRequestUsesScopeForTranscript(t *testing.T) {
	settingSvc := moderation.NewRuntimeSettings(&fakeSettingRepo{vals: map[string]string{
		moderation.SettingKeyCyberSessionBlockEnabled:    "true",
		moderation.SettingKeyCyberSessionBlockTTLSeconds: "60",
	}}, settings.ErrSettingNotFound)
	combo := &comboCacheAndStore{}
	svc := session.NewCyberBlocks(session.AdaptCyberSessionBlockStore(combo), settingSvc.GetCyberSessionBlockRuntime, nil)
	ctx := context.Background()

	hitBody := []byte(`{"messages":[{"role":"user","content":"setup"},{"role":"assistant","content":"ready"},{"role":"user","content":"trigger"}]}`)
	nextBody := []byte(`{"messages":[{"role":"user","content":"setup"},{"role":"assistant","content":"ready"},{"role":"user","content":"different trigger"},{"role":"assistant","content":"blocked"},{"role":"user","content":"continue"}]}`)
	nextCtx, _ := newCyberBlockTestCtx(nil, string(nextBody))
	const clientIP = "203.0.113.20"
	const userAgent = "Codex CLI 1.2.3"
	blockKey := session.CyberSessionTranscriptBlockKeys(9, hitBody)[1]

	// Without an active source scope, transcript candidates are never blocks.
	require.Empty(t, FindCyberSessionForRequest(ctx, svc, 9, nextCtx, nextBody, clientIP, userAgent))
	scopeKey := session.CyberSessionScopeKey(9, clientIP, userAgent)
	svc.MarkCyberSessionBlocked(ctx, scopeKey, []string{blockKey})
	require.Equal(t, blockKey, FindCyberSessionForRequest(ctx, svc, 9, nextCtx, nextBody, clientIP, "Codex CLI 1.2.4"))
}

func TestFindCyberSessionBlockedForRequestFailsClosedOnScopedTranscriptOverflow(t *testing.T) {
	settingSvc := moderation.NewRuntimeSettings(&fakeSettingRepo{vals: map[string]string{
		moderation.SettingKeyCyberSessionBlockEnabled:    "true",
		moderation.SettingKeyCyberSessionBlockTTLSeconds: "60",
	}}, settings.ErrSettingNotFound)
	combo := &comboCacheAndStore{}
	svc := session.NewCyberBlocks(session.AdaptCyberSessionBlockStore(combo), settingSvc.GetCyberSessionBlockRuntime, nil)
	ctx := context.Background()
	const apiKeyID = int64(9)
	const clientIP = "203.0.113.20"
	const userAgent = "Codex CLI 1.2.3"

	messages := make([]map[string]string, 256+1)
	for i := range messages {
		messages[i] = map[string]string{"role": "user", "content": "message-" + strconv.Itoa(i)}
	}
	body, err := json.Marshal(map[string]any{"messages": messages})
	require.NoError(t, err)
	c, _ := newCyberBlockTestCtx(nil, string(body))
	require.Empty(t, FindCyberSessionForRequest(ctx, svc, apiKeyID, c, body, clientIP, userAgent), "overflow alone must not bypass the scope gate")
	combo.store.scopes = map[string]bool{session.CyberSessionScopeKey(apiKeyID, clientIP, userAgent): true}

	require.Equal(t, "transcript_lookup_limit_exceeded", FindCyberSessionForRequest(ctx, svc, apiKeyID, c, body, clientIP, userAgent))
	require.Zero(t, combo.store.findCalls, "overflow must not issue an unbounded Redis lookup")
}

func TestCyberSessionScopeKeyNormalizesUserAgentVersion(t *testing.T) {
	base := session.CyberSessionScopeKey(7, "203.0.113.10", "Codex CLI 1.2.3")
	require.NotEmpty(t, base)
	require.Equal(t, base, session.CyberSessionScopeKey(7, "203.0.113.10", "Codex CLI 1.2.4"))
	require.NotEqual(t, base, session.CyberSessionScopeKey(8, "203.0.113.10", "Codex CLI 1.2.3"))
	require.NotEqual(t, base, session.CyberSessionScopeKey(7, "203.0.113.11", "Codex CLI 1.2.3"))
}
