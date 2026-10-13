package provider

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// testCodexFingerprintSeed supplies a stable valid seed shared by opted-in
// fingerprint fixtures and deterministic identity assertions.
const testCodexFingerprintSeed = "11111111-1111-4111-8111-111111111111"

// newTestOAuthProvider creates an OAuth record and adds the fixed seed when its
// fingerprint mode needs one. The supplied Extra map is retained and can be
// mutated during setup; existing seed values are preserved.
func newTestOAuthProvider(id int64, extra map[string]any) *providercore.Record {
	if providercore.CodexFingerprintModeRequiresSeed(providercore.CodexFingerprintModeFromExtra(extra)) {
		if extra == nil {
			extra = make(map[string]any)
		}
		if _, exists := extra[providercore.CodexFingerprintSeedExtraKey]; !exists {
			extra[providercore.CodexFingerprintSeedExtraKey] = testCodexFingerprintSeed
		}
	}
	return &providercore.Record{
		LoadLocation: time.LoadLocation, ID: id,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Extra:    extra,
	}
}

// TestResolveConvergedInstallationID_UsesDeviceID supplies a configured device
// ID together with a valid seed. Installation selection must return that device
// ID before attempting seed-derived identity.
func TestResolveConvergedInstallationID_UsesDeviceID(t *testing.T) {
	provider := newTestOAuthProvider(1, map[string]any{"openai_device_id": "real-device-id"})
	assert.Equal(t, "real-device-id", ConvergedInstallationID(provider, testCodexFingerprintSeed))
}

// TestResolveConvergedInstallationID_DerivesFromSeed supplies a record without
// a device ID and a fixed seed. Repeated derivation must produce the same
// parseable UUID installation identity.
func TestResolveConvergedInstallationID_DerivesFromSeed(t *testing.T) {
	provider := newTestOAuthProvider(42, nil)
	result := ConvergedInstallationID(provider, testCodexFingerprintSeed)
	_, err := uuid.Parse(result)
	require.NoError(t, err, "派生值应为合法 UUID")
	assert.Equal(t, result, ConvergedInstallationID(provider, testCodexFingerprintSeed), "确定性")
}

// TestResolveConvergedInstallationID_DifferentSeeds compares two seeds for one
// record without device configuration. Their derived installation UUIDs must
// differ so separate stored seeds do not converge into one identity.
func TestResolveConvergedInstallationID_DifferentSeeds(t *testing.T) {
	provider := newTestOAuthProvider(1, nil)
	a := ConvergedInstallationID(provider, testCodexFingerprintSeed)
	b := ConvergedInstallationID(provider, "22222222-2222-4222-8222-222222222222")
	assert.NotEqual(t, a, b)
}

// TestResolveCodexFingerprintIDsFromRequest_ExplicitOff configures fingerprint
// mode off on an OAuth record. Request-derived fingerprint IDs must remain nil
// so outbound rewriting has no generated identity to apply.
func TestResolveCodexFingerprintIDsFromRequest_ExplicitOff(t *testing.T) {
	provider := newTestOAuthProvider(1, map[string]any{providercore.CodexFingerprintModeExtraKey: "off"})
	ids := CodexFingerprintIDsFromRequest(provider, nil)
	assert.Nil(t, ids, "显式 off 模式应返回 nil")
}

// TestResolveCodexFingerprintIDsFromRequest_DefaultIsOff leaves the record
// without fingerprint configuration. Request-derived IDs must be nil, retaining
// the default opt-in requirement for generated client identity.
func TestResolveCodexFingerprintIDsFromRequest_DefaultIsOff(t *testing.T) {
	provider := newTestOAuthProvider(1, nil)
	assert.Nil(t, CodexFingerprintIDsFromRequest(provider, nil), "无 extra 应视为 off")
}

// TestResolveCodexFingerprintIDsFromRequest_ExplicitOptInHonored enables
// device, session, and full modes through seeded fixtures. Each mode must
// produce nonnil IDs with an installation value and the selected mode label.
func TestResolveCodexFingerprintIDsFromRequest_ExplicitOptInHonored(t *testing.T) {
	for _, mode := range []string{"device", "session", "full"} {
		t.Run(mode, func(t *testing.T) {
			provider := newTestOAuthProvider(1, map[string]any{providercore.CodexFingerprintModeExtraKey: mode})
			ids := CodexFingerprintIDsFromRequest(provider, nil)
			require.NotNil(t, ids, "显式配置必须生效")
			assert.Equal(t, mode, ids.Mode)
			assert.NotEmpty(t, ids.InstallationID)
		})
	}
}

// TestResolveCodexFingerprintIDsFromRequest_EnabledModesRequireValidSeed
// supplies absent, blank, noncanonical, nil-UUID, and wrong-type seeds. Enabled
// modes must return nil even when a configured device ID is also available.
func TestResolveCodexFingerprintIDsFromRequest_EnabledModesRequireValidSeed(t *testing.T) {
	for _, tt := range []struct {
		name  string         // invalid seed scenario label
		extra map[string]any // supplied fingerprint configuration fields
	}{
		{name: "missing", extra: map[string]any{providercore.CodexFingerprintModeExtraKey: "device"}},
		{name: "missing with device override", extra: map[string]any{providercore.CodexFingerprintModeExtraKey: "device", "openai_device_id": "real-device"}},
		{name: "blank", extra: map[string]any{providercore.CodexFingerprintModeExtraKey: "session", providercore.CodexFingerprintSeedExtraKey: ""}},
		{name: "uppercase", extra: map[string]any{providercore.CodexFingerprintModeExtraKey: "full", providercore.CodexFingerprintSeedExtraKey: "11111111-1111-4111-8111-AAAAAAAAAAAA"}},
		{name: "nil uuid", extra: map[string]any{providercore.CodexFingerprintModeExtraKey: "device", providercore.CodexFingerprintSeedExtraKey: "00000000-0000-0000-0000-000000000000"}},
		{name: "non string", extra: map[string]any{providercore.CodexFingerprintModeExtraKey: "session", providercore.CodexFingerprintSeedExtraKey: 123}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := &providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Extra: tt.extra}
			require.Nil(t, CodexFingerprintIDsFromRequest(provider, nil))
		})
	}
}

// TestApplyCodexFingerprintHeaders_OffMode supplies existing installation and
// window headers with nil generated IDs. Header application must retain those
// values when convergence is disabled.
func TestApplyCodexFingerprintHeaders_OffMode(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-installation-id", "original-install-id")
	h.Set("x-codex-window-id", "original-window-id")

	openai.ApplyCodexFingerprintHeaders(h, nil)

	assert.Equal(t, "original-install-id", h.Get("x-codex-installation-id"), "nil ids 不改写")
	assert.Equal(t, "original-window-id", h.Get("x-codex-window-id"), "nil ids 不改写")
}

// TestApplyCodexFingerprintHeaders_DeviceMode supplies client session metadata
// alongside a configured device identity. Rewriting must converge installation
// fields while retaining session, window, and unrelated embedded metadata.
func TestApplyCodexFingerprintHeaders_DeviceMode(t *testing.T) {
	provider := newTestOAuthProvider(1, map[string]any{
		providercore.CodexFingerprintModeExtraKey: "device",
		"openai_device_id":                        "converged-device",
	})
	turnMetadata := `{"installation_id":"user-install","session_id":"user-session","sandbox":"seccomp"}`
	h := http.Header{}
	h.Set("x-codex-installation-id", "user-install")
	h.Set("x-codex-window-id", "user-window:0")
	h.Set("x-codex-turn-metadata", turnMetadata)

	ids := CodexFingerprintIDsFromRequest(provider, nil)
	openai.ApplyCodexFingerprintHeaders(h, ids)

	assert.Equal(t, "converged-device", h.Get("x-codex-installation-id"), "installation_id 应收敛")
	assert.Equal(t, "user-window:0", h.Get("x-codex-window-id"), "device 模式不改写 window_id")

	var meta map[string]any
	require.NoError(t, json.Unmarshal([]byte(h.Get("x-codex-turn-metadata")), &meta))
	assert.Equal(t, "converged-device", meta["installation_id"])
	assert.Equal(t, "user-session", meta["session_id"], "device 模式不改写 session_id")
	assert.Equal(t, "seccomp", meta["sandbox"], "非指纹字段保留原样")
}

// TestApplyCodexFingerprintHeaders_SessionMode supplies one client session to a
// seeded OAuth record. Outbound headers and embedded metadata must use
// associated installation, session, thread, window, and replacement turn fields while
// retaining unrelated metadata.
func TestApplyCodexFingerprintHeaders_SessionMode(t *testing.T) {
	provider := newTestOAuthProvider(1, map[string]any{
		providercore.CodexFingerprintModeExtraKey: "session",
	})
	clientHeaders := http.Header{}
	clientHeaders.Set("session-id", "client-session-aaa")

	turnMetadata := `{"installation_id":"user-install","session_id":"user-session","thread_id":"user-thread","turn_id":"user-turn","window_id":"user-thread:0","sandbox":"seccomp","thread_source":"user"}`
	h := http.Header{}
	h.Set("x-codex-installation-id", "user-install")
	h.Set("x-codex-window-id", "user-thread:0")
	h.Set("x-codex-turn-metadata", turnMetadata)
	h.Set("x-client-request-id", "user-thread")

	ids := CodexFingerprintIDsFromRequest(provider, clientHeaders)
	openai.ApplyCodexFingerprintHeaders(h, ids)

	seed, ok := providercore.CodexFingerprintSeed(provider.Extra)
	require.True(t, ok)
	convergedInstall := ConvergedInstallationID(provider, seed)
	convergedSession := openai.ResolveConvergedSessionID(seed)
	convergedThread := openai.ResolveConvergedThreadID(seed, "client-session-aaa")

	assert.Equal(t, convergedInstall, h.Get("x-codex-installation-id"))
	assert.Equal(t, convergedSession, h.Get("session-id"))
	assert.Equal(t, convergedSession, h.Get("session_id"), "下划线形式也应被改写")
	assert.Equal(t, convergedThread, h.Get("thread-id"))
	assert.Equal(t, convergedThread, h.Get("x-client-request-id"))
	assert.Equal(t, convergedThread+":0", h.Get("x-codex-window-id"))

	var meta map[string]any
	require.NoError(t, json.Unmarshal([]byte(h.Get("x-codex-turn-metadata")), &meta))
	assert.Equal(t, convergedInstall, meta["installation_id"])
	assert.Equal(t, convergedSession, meta["session_id"])
	assert.Equal(t, convergedThread, meta["thread_id"])
	assert.NotEqual(t, "user-turn", meta["turn_id"], "turn_id 应被新生成的值替换")
	assert.Equal(t, "seccomp", meta["sandbox"], "sandbox 保留原样")
	assert.Equal(t, "user", meta["thread_source"], "thread_source 保留原样")
}

// TestApplyCodexFingerprintHeaders_SessionMode_DifferentClients compares two
// client sessions on the same seeded record. Installation and provider session
// must agree while client-derived thread and window identities remain distinct.
func TestApplyCodexFingerprintHeaders_SessionMode_DifferentClients(t *testing.T) {
	provider := newTestOAuthProvider(1, map[string]any{
		providercore.CodexFingerprintModeExtraKey: "session",
	})

	makeTurnMeta := func() string {
		return `{"installation_id":"x","session_id":"x","thread_id":"x","turn_id":"x","window_id":"x:0"}`
	}

	clientA := http.Header{}
	clientA.Set("session-id", "client-A")
	idsA := CodexFingerprintIDsFromRequest(provider, clientA)
	hA := http.Header{}
	hA.Set("x-codex-turn-metadata", makeTurnMeta())
	openai.ApplyCodexFingerprintHeaders(hA, idsA)

	clientB := http.Header{}
	clientB.Set("session-id", "client-B")
	idsB := CodexFingerprintIDsFromRequest(provider, clientB)
	hB := http.Header{}
	hB.Set("x-codex-turn-metadata", makeTurnMeta())
	openai.ApplyCodexFingerprintHeaders(hB, idsB)

	assert.Equal(t, hA.Get("session-id"), hB.Get("session-id"), "session_id 应相同")
	assert.NotEqual(t, hA.Get("thread-id"), hB.Get("thread-id"), "不同客户端 thread_id 应不同")
	assert.NotEqual(t, hA.Get("x-codex-window-id"), hB.Get("x-codex-window-id"), "不同客户端 window_id 应不同")
	assert.Equal(t, hA.Get("x-codex-installation-id"), hB.Get("x-codex-installation-id"))
}

// TestApplyCodexFingerprintHeaders_FullMode compares two clients under full
// convergence. Their thread and window identities must agree, and the thread
// must equal the provider-derived session.
func TestApplyCodexFingerprintHeaders_FullMode(t *testing.T) {
	provider := newTestOAuthProvider(1, map[string]any{
		providercore.CodexFingerprintModeExtraKey: "full",
	})
	seed, ok := providercore.CodexFingerprintSeed(provider.Extra)
	require.True(t, ok)
	convergedSession := openai.ResolveConvergedSessionID(seed)

	clientA := http.Header{}
	clientA.Set("session-id", "client-A")
	idsA := CodexFingerprintIDsFromRequest(provider, clientA)
	hA := http.Header{}
	hA.Set("x-codex-turn-metadata", `{"installation_id":"x","session_id":"x","thread_id":"x","turn_id":"x","window_id":"x:0"}`)
	openai.ApplyCodexFingerprintHeaders(hA, idsA)

	clientB := http.Header{}
	clientB.Set("session-id", "client-B")
	idsB := CodexFingerprintIDsFromRequest(provider, clientB)
	hB := http.Header{}
	hB.Set("x-codex-turn-metadata", `{"installation_id":"x","session_id":"x","thread_id":"x","turn_id":"x","window_id":"x:0"}`)
	openai.ApplyCodexFingerprintHeaders(hB, idsB)

	assert.Equal(t, hA.Get("thread-id"), hB.Get("thread-id"), "full 模式 thread_id 应相同")
	assert.Equal(t, convergedSession, hA.Get("thread-id"), "full 模式 thread_id 应等于 session_id")
	assert.Equal(t, hA.Get("x-codex-window-id"), hB.Get("x-codex-window-id"), "full 模式 window_id 应相同")
}

// TestFingerprintIDs_HeaderAndBody_TurnID_Consistent reuses one attempt ID
// object for headers and decoded body metadata. Header, top-level body, and
// embedded body turn IDs must agree, and the two embedded timestamps must match.
func TestFingerprintIDs_HeaderAndBody_TurnID_Consistent(t *testing.T) {
	provider := newTestOAuthProvider(1, map[string]any{
		providercore.CodexFingerprintModeExtraKey: "session",
	})
	clientHeaders := http.Header{}
	clientHeaders.Set("session-id", "client-session-xyz")

	ids := CodexFingerprintIDsFromRequest(provider, clientHeaders)
	require.NotNil(t, ids)

	h := http.Header{}
	h.Set("x-codex-turn-metadata", `{"installation_id":"x","session_id":"x","thread_id":"x","turn_id":"x","window_id":"x:0"}`)
	openai.ApplyCodexFingerprintHeaders(h, ids)

	// Reuse this attempt's generated IDs across header and body rewriting.
	// Creating another object would derive another turn identity and read the
	// clock again before the equivalence assertions.
	reqBody := map[string]any{
		"client_metadata": map[string]any{
			"x-codex-installation-id": "x",
			"session_id":              "x",
			"turn_id":                 "x",
			"x-codex-turn-metadata":   `{"installation_id":"x","session_id":"x","thread_id":"x","turn_id":"x","window_id":"x:0"}`,
		},
	}
	openai.ApplyCodexFingerprintClientMetadata(reqBody, ids)

	var headerMeta map[string]any
	require.NoError(t, json.Unmarshal([]byte(h.Get("x-codex-turn-metadata")), &headerMeta))
	headerTurnID, ok := headerMeta["turn_id"].(string)
	require.True(t, ok, "头 turn-metadata 应包含 string 类型的 turn_id")

	cm, ok := reqBody["client_metadata"].(map[string]any)
	require.True(t, ok, "请求体应包含 client_metadata")
	bodyTurnID, ok := cm["turn_id"].(string)
	require.True(t, ok, "体 client_metadata 应包含 string 类型的 turn_id")

	embeddedRaw, ok := cm["x-codex-turn-metadata"].(string)
	require.True(t, ok, "体 client_metadata 应包含 x-codex-turn-metadata 字符串")
	var bodyMeta map[string]any
	require.NoError(t, json.Unmarshal([]byte(embeddedRaw), &bodyMeta))
	bodyEmbeddedTurnID, ok := bodyMeta["turn_id"].(string)
	require.True(t, ok, "体内嵌 turn-metadata 应包含 string 类型的 turn_id")

	assert.Equal(t, headerTurnID, bodyTurnID, "头和体的 turn_id 必须一致")
	assert.Equal(t, headerTurnID, bodyEmbeddedTurnID, "头和体内嵌 turn-metadata 的 turn_id 必须一致")
	assert.Equal(t, ids.TurnID, headerTurnID, "所有 turn_id 都应来自同一份 ids")
	assert.Equal(t, headerMeta["turn_started_at_unix_ms"], bodyMeta["turn_started_at_unix_ms"], "头和体的 timestamp 必须一致")
	assert.Equal(t, float64(ids.TurnStartedAtUnixMs), headerMeta["turn_started_at_unix_ms"])
}

// TestFingerprintIDs_MalformedEmbeddedMetadataRebuiltConsistently starts with
// invalid header and body turn-metadata JSON. Rebuilt embedded objects must
// agree on their six embedded fingerprint fields for that attempt.
func TestFingerprintIDs_MalformedEmbeddedMetadataRebuiltConsistently(t *testing.T) {
	provider := newTestOAuthProvider(2, map[string]any{providercore.CodexFingerprintModeExtraKey: "session"})
	clientHeaders := make(http.Header)
	clientHeaders.Set("session-id", "client-session-malformed")
	ids := CodexFingerprintIDsFromRequest(provider, clientHeaders)
	require.NotNil(t, ids)

	h := make(http.Header)
	h.Set("x-codex-turn-metadata", "{malformed")
	openai.ApplyCodexFingerprintHeaders(h, ids)

	reqBody := map[string]any{
		"client_metadata": map[string]any{
			"session_id":            "client-session-malformed",
			"x-codex-turn-metadata": "[malformed",
		},
	}
	require.True(t, openai.ApplyCodexFingerprintClientMetadata(reqBody, ids))

	var headerMeta map[string]any
	require.NoError(t, json.Unmarshal([]byte(h.Get("x-codex-turn-metadata")), &headerMeta))
	clientMetadata, ok := reqBody["client_metadata"].(map[string]any)
	require.True(t, ok)
	bodyRaw, ok := clientMetadata["x-codex-turn-metadata"].(string)
	require.True(t, ok)
	var bodyMeta map[string]any
	require.NoError(t, json.Unmarshal([]byte(bodyRaw), &bodyMeta))

	for _, key := range []string{"installation_id", "session_id", "thread_id", "turn_id", "window_id", "turn_started_at_unix_ms"} {
		assert.Equal(t, headerMeta[key], bodyMeta[key], "rebuilt metadata field %s must match", key)
	}
}

// TestApplyCodexFingerprintClientMetadata_OffMode supplies decoded client
// metadata with nil fingerprint IDs. The map helper must report no modification
// when no convergence identity is configured.
func TestApplyCodexFingerprintClientMetadata_OffMode(t *testing.T) {
	reqBody := map[string]any{
		"client_metadata": map[string]any{
			"x-codex-installation-id": "original",
		},
	}
	modified := openai.ApplyCodexFingerprintClientMetadata(reqBody, nil)
	assert.False(t, modified, "nil ids 不改写")
}

// TestApplyCodexFingerprintClientMetadata_DeviceMode rewrites decoded metadata
// with a device override. Installation identity must change while the client
// session and unrelated embedded sandbox value remain intact.
func TestApplyCodexFingerprintClientMetadata_DeviceMode(t *testing.T) {
	provider := newTestOAuthProvider(1, map[string]any{
		providercore.CodexFingerprintModeExtraKey: "device",
		"openai_device_id":                        "converged-device",
	})
	ids := CodexFingerprintIDsFromRequest(provider, nil)
	require.NotNil(t, ids)

	embeddedMeta := `{"installation_id":"x","session_id":"user-session","sandbox":"seccomp"}`
	reqBody := map[string]any{
		"client_metadata": map[string]any{
			"x-codex-installation-id": "original-install",
			"session_id":              "user-session",
			"x-codex-turn-metadata":   embeddedMeta,
		},
	}

	modified := openai.ApplyCodexFingerprintClientMetadata(reqBody, ids)
	require.True(t, modified)

	cm, ok := reqBody["client_metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "converged-device", cm["x-codex-installation-id"])
	assert.Equal(t, "user-session", cm["session_id"], "device 模式不改 session_id")

	turnMetaStr, ok := cm["x-codex-turn-metadata"].(string)
	require.True(t, ok)
	var meta map[string]any
	require.NoError(t, json.Unmarshal([]byte(turnMetaStr), &meta))
	assert.Equal(t, "converged-device", meta["installation_id"])
	assert.Equal(t, "seccomp", meta["sandbox"], "非指纹字段保留原样")
}

// TestApplyCodexFingerprintClientMetadata_SessionMode applies a seeded identity
// to decoded and embedded metadata. Installation and session values must match
// in both, while decoded thread and window values follow the client session and
// the embedded sandbox value remains unchanged.
func TestApplyCodexFingerprintClientMetadata_SessionMode(t *testing.T) {
	provider := newTestOAuthProvider(1, map[string]any{
		providercore.CodexFingerprintModeExtraKey: "session",
	})
	clientHeaders := http.Header{}
	clientHeaders.Set("session-id", "client-session-aaa")

	ids := CodexFingerprintIDsFromRequest(provider, clientHeaders)
	require.NotNil(t, ids)

	embeddedMeta := `{"installation_id":"x","session_id":"x","thread_id":"x","turn_id":"x","window_id":"x:0","sandbox":"seccomp"}`
	reqBody := map[string]any{
		"client_metadata": map[string]any{
			"x-codex-installation-id": "original-install",
			"session_id":              "original-session",
			"x-codex-turn-metadata":   embeddedMeta,
		},
	}

	modified := openai.ApplyCodexFingerprintClientMetadata(reqBody, ids)
	require.True(t, modified)

	cm, ok := reqBody["client_metadata"].(map[string]any)
	require.True(t, ok)
	seed, ok := providercore.CodexFingerprintSeed(provider.Extra)
	require.True(t, ok)
	convergedInstall := ConvergedInstallationID(provider, seed)
	convergedSession := openai.ResolveConvergedSessionID(seed)
	convergedThread := openai.ResolveConvergedThreadID(seed, "client-session-aaa")

	assert.Equal(t, convergedInstall, cm["x-codex-installation-id"])
	assert.Equal(t, convergedSession, cm["session_id"])
	assert.Equal(t, convergedThread, cm["thread_id"])
	assert.Equal(t, convergedThread+":0", cm["x-codex-window-id"])

	turnMetaStr, ok := cm["x-codex-turn-metadata"].(string)
	require.True(t, ok)
	var meta map[string]any
	require.NoError(t, json.Unmarshal([]byte(turnMetaStr), &meta))
	assert.Equal(t, convergedInstall, meta["installation_id"])
	assert.Equal(t, convergedSession, meta["session_id"])
	assert.Equal(t, "seccomp", meta["sandbox"], "非指纹字段保留原样")
}

// TestApplyCodexFingerprintClientMetadata_FullMode rewrites decoded metadata
// for a supplied client session. Full convergence must set both session and
// thread to the provider-derived session value.
func TestApplyCodexFingerprintClientMetadata_FullMode(t *testing.T) {
	provider := newTestOAuthProvider(1, map[string]any{
		providercore.CodexFingerprintModeExtraKey: "full",
	})
	clientHeaders := http.Header{}
	clientHeaders.Set("session-id", "any-client")

	ids := CodexFingerprintIDsFromRequest(provider, clientHeaders)
	require.NotNil(t, ids)

	reqBody := map[string]any{
		"client_metadata": map[string]any{
			"session_id":            "x",
			"thread_id":             "x",
			"x-codex-turn-metadata": `{"installation_id":"x","session_id":"x","thread_id":"x","turn_id":"x","window_id":"x:0"}`,
		},
	}

	modified := openai.ApplyCodexFingerprintClientMetadata(reqBody, ids)
	require.True(t, modified)

	cm, ok := reqBody["client_metadata"].(map[string]any)
	require.True(t, ok)
	seed, ok := providercore.CodexFingerprintSeed(provider.Extra)
	require.True(t, ok)
	convergedSession := openai.ResolveConvergedSessionID(seed)

	assert.Equal(t, convergedSession, cm["session_id"])
	assert.Equal(t, convergedSession, cm["thread_id"], "full 模式 thread_id 应等于 session_id")
}

// rawVsMapClientMetadata applies one ID object to decoded and raw JSON paths
// and returns their metadata maps. It requires valid JSON and an actual raw
// rewrite, failing the test if either preparation cannot complete.
func rawVsMapClientMetadata(t *testing.T, body []byte, ids *openai.FingerprintIDs) (map[string]any, map[string]any) {
	t.Helper()

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	openai.ApplyCodexFingerprintClientMetadata(decoded, ids)
	mapCM, _ := decoded["client_metadata"].(map[string]any)

	rawBody, changed, err := openai.ApplyCodexFingerprintClientMetadataRaw(body, ids)
	require.NoError(t, err)
	require.True(t, changed)
	var rawDecoded map[string]any
	require.NoError(t, json.Unmarshal(rawBody, &rawDecoded))
	rawCM, _ := rawDecoded["client_metadata"].(map[string]any)
	return mapCM, rawCM
}

// cloneCodexFingerprintIDsForTest copies attempt IDs and resets original-body
// session capture state. Nil input returns nil; fresh capture lets map and raw
// variants inspect their own unmodified source metadata.
func cloneCodexFingerprintIDsForTest(ids *openai.FingerprintIDs) *openai.FingerprintIDs {
	if ids == nil {
		return nil
	}
	cloned := *ids
	cloned.OriginalBodySessionID = ""
	cloned.OriginalBodySessionIDCaptured = false
	return &cloned
}

// applyMapAndRawFingerprintBodiesForTest rewrites map and raw bodies using
// separate ID copies with fresh source capture. It fails on JSON or rewrite
// errors and returns both decoded bodies for equivalence assertions.
func applyMapAndRawFingerprintBodiesForTest(t *testing.T, body []byte, ids *openai.FingerprintIDs) (map[string]any, map[string]any) {
	t.Helper()

	// Each variant captures its own original body session before rewriting.
	// Separate ID copies prevent the first variant's capture state from hiding the
	// second variant's behavior.
	mapIDs := cloneCodexFingerprintIDsForTest(ids)
	rawIDs := cloneCodexFingerprintIDsForTest(ids)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	openai.ApplyCodexFingerprintClientMetadata(decoded, mapIDs)

	rawBody, _, err := openai.ApplyCodexFingerprintClientMetadataRaw(body, rawIDs)
	require.NoError(t, err)
	var rawDecoded map[string]any
	require.NoError(t, json.Unmarshal(rawBody, &rawDecoded))
	return decoded, rawDecoded
}

// TestApplyCodexFingerprintPromptCacheKey_MapRawEquivalence compares session
// and full modes for session-derived and explicit cache keys. Map and raw paths
// must converge defaults consistently while retaining an explicit cache
// override.
func TestApplyCodexFingerprintPromptCacheKey_MapRawEquivalence(t *testing.T) {
	for _, mode := range []providercore.CodexFingerprintMode{providercore.CodexFingerprintSession, providercore.CodexFingerprintFull} {
		t.Run(string(mode)+"/default", func(t *testing.T) {
			provider := newTestOAuthProvider(4300, map[string]any{providercore.CodexFingerprintModeExtraKey: string(mode)})
			ids := CodexFingerprintIDs(provider, "header-session", mode)
			require.NotNil(t, ids)

			body := []byte(`{"model":"gpt-5.6-sol","prompt_cache_key":"body-session","client_metadata":{"session_id":" body-session ","trace":"keep"},"input":[]}`)
			mapBody, rawBody := applyMapAndRawFingerprintBodiesForTest(t, body, ids)

			require.Equal(t, mapBody["prompt_cache_key"], rawBody["prompt_cache_key"])
			require.Equal(t, ids.SessionID, mapBody["prompt_cache_key"])
			mapCM, _ := mapBody["client_metadata"].(map[string]any)
			rawCM, _ := rawBody["client_metadata"].(map[string]any)
			require.Equal(t, ids.SessionID, mapCM["session_id"])
			require.Equal(t, mapCM["session_id"], rawCM["session_id"])
			require.Equal(t, "keep", rawCM["trace"])
		})
	}

	t.Run("explicit override", func(t *testing.T) {
		provider := newTestOAuthProvider(4301, map[string]any{providercore.CodexFingerprintModeExtraKey: "session"})
		ids := CodexFingerprintIDs(provider, "header-session", providercore.CodexFingerprintSession)
		require.NotNil(t, ids)

		body := []byte(`{"model":"gpt-5.6-sol","prompt_cache_key":"explicit-cache","client_metadata":{"session_id":"body-session"},"input":[]}`)
		mapBody, rawBody := applyMapAndRawFingerprintBodiesForTest(t, body, ids)

		require.Equal(t, "explicit-cache", mapBody["prompt_cache_key"])
		require.Equal(t, "explicit-cache", rawBody["prompt_cache_key"])
		mapCM, _ := mapBody["client_metadata"].(map[string]any)
		rawCM, _ := rawBody["client_metadata"].(map[string]any)
		require.Equal(t, ids.SessionID, mapCM["session_id"])
		require.Equal(t, ids.SessionID, rawCM["session_id"])
	})
}

// TestApplyCodexFingerprintPromptCacheKey_Negatives supplies absent, nonstring,
// mismatched, and unsupported source identities across enabled and off modes.
// Cache-key presence and values must remain unchanged when session-derived
// replacement cannot apply.
func TestApplyCodexFingerprintPromptCacheKey_Negatives(t *testing.T) {
	sessionProvider := newTestOAuthProvider(4310, map[string]any{providercore.CodexFingerprintModeExtraKey: "session"})
	sessionIDs := CodexFingerprintIDs(sessionProvider, "header-session", providercore.CodexFingerprintSession)
	require.NotNil(t, sessionIDs)
	deviceProvider := newTestOAuthProvider(4311, map[string]any{providercore.CodexFingerprintModeExtraKey: "device"})
	deviceIDs := CodexFingerprintIDs(deviceProvider, "header-session", providercore.CodexFingerprintDevice)
	require.NotNil(t, deviceIDs)

	tests := []struct {
		name string // cache-key preservation scenario label
		body []byte // supplied request JSON bytes
		// ids shares the table's source identity with other cases. Each variant
		// copies it and resets body-session capture before rewriting its own input.
		ids          *openai.FingerprintIDs // supplied attempt fingerprint identity
		wantExists   bool                   // expected cache-key field presence
		wantCacheKey any                    // expected retained cache-key value
		// wantRawString supplies the unchanged JSON expected when ids is nil. Enabled
		// cases inspect decoded cache fields instead.
		wantRawString string
	}{
		{
			name:       "missing key is not injected",
			body:       []byte(`{"client_metadata":{"session_id":"body-session"}}`),
			ids:        sessionIDs,
			wantExists: false,
		},
		{
			name:         "empty key preserved",
			body:         []byte(`{"prompt_cache_key":"","client_metadata":{"session_id":"body-session"}}`),
			ids:          sessionIDs,
			wantExists:   true,
			wantCacheKey: "",
		},
		{
			name:         "whitespace-different key is an explicit override",
			body:         []byte(`{"prompt_cache_key":" body-session ","client_metadata":{"session_id":"body-session"}}`),
			ids:          sessionIDs,
			wantExists:   true,
			wantCacheKey: " body-session ",
		},
		{
			name:         "non-string key preserved",
			body:         []byte(`{"prompt_cache_key":123,"client_metadata":{"session_id":"body-session"}}`),
			ids:          sessionIDs,
			wantExists:   true,
			wantCacheKey: float64(123),
		},
		{
			name:         "missing source metadata preserves key",
			body:         []byte(`{"prompt_cache_key":"body-session"}`),
			ids:          sessionIDs,
			wantExists:   true,
			wantCacheKey: "body-session",
		},
		{
			name:         "non-string source session preserves key",
			body:         []byte(`{"prompt_cache_key":"123","client_metadata":{"session_id":123}}`),
			ids:          sessionIDs,
			wantExists:   true,
			wantCacheKey: "123",
		},
		{
			name:         "non-object source metadata preserves key",
			body:         []byte(`{"prompt_cache_key":"body-session","client_metadata":"bad"}`),
			ids:          sessionIDs,
			wantExists:   true,
			wantCacheKey: "body-session",
		},
		{
			name:         "device mode preserves key",
			body:         []byte(`{"prompt_cache_key":"body-session","client_metadata":{"session_id":"body-session"}}`),
			ids:          deviceIDs,
			wantExists:   true,
			wantCacheKey: "body-session",
		},
		{
			name:          "off mode preserves body",
			body:          []byte(`{"prompt_cache_key":"body-session","client_metadata":{"session_id":"body-session"}}`),
			ids:           nil,
			wantExists:    true,
			wantCacheKey:  "body-session",
			wantRawString: `{"prompt_cache_key":"body-session","client_metadata":{"session_id":"body-session"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mapBody map[string]any
			require.NoError(t, json.Unmarshal(tt.body, &mapBody))
			changedMap := openai.ApplyCodexFingerprintClientMetadata(mapBody, cloneCodexFingerprintIDsForTest(tt.ids))

			rawBody, changedRaw, err := openai.ApplyCodexFingerprintClientMetadataRaw(tt.body, cloneCodexFingerprintIDsForTest(tt.ids))
			require.NoError(t, err)
			if tt.ids == nil {
				require.False(t, changedMap)
				require.False(t, changedRaw)
				require.JSONEq(t, tt.wantRawString, string(rawBody))
				return
			}
			require.True(t, changedMap)
			require.True(t, changedRaw)

			rawDecoded := map[string]any{}
			require.NoError(t, json.Unmarshal(rawBody, &rawDecoded))
			_, mapExists := mapBody["prompt_cache_key"]
			_, rawExists := rawDecoded["prompt_cache_key"]
			require.Equal(t, tt.wantExists, mapExists)
			require.Equal(t, tt.wantExists, rawExists)
			if tt.wantExists {
				require.Equal(t, tt.wantCacheKey, mapBody["prompt_cache_key"])
				require.Equal(t, tt.wantCacheKey, rawDecoded["prompt_cache_key"])
			}
		})
	}
}

// TestApplyCodexFingerprintClientMetadataRaw_MatchesMapVariant compares
// missing, object, and nonobject metadata under every enabled mode. Raw
// rewriting must produce the same decoded metadata values as the map path.
func TestApplyCodexFingerprintClientMetadataRaw_MatchesMapVariant(t *testing.T) {
	embedded := `{\"installation_id\":\"real-install\",\"session_id\":\"real-session\",\"sandbox\":\"seatbelt\"}`
	bodies := map[string]string{
		"no_client_metadata": `{"model":"gpt-5.6-sol","input":[],"stream":true}`,
		"object_with_extras": `{"model":"gpt-5.6-sol","client_metadata":{"session_id":"client-session","traceparent":"00-abc-def-01","x-codex-turn-metadata":"` + embedded + `"},"stream":true}`,
		"non_object_value":   `{"model":"gpt-5.6-sol","client_metadata":"bogus","stream":true}`,
	}
	for _, mode := range []providercore.CodexFingerprintMode{providercore.CodexFingerprintDevice, providercore.CodexFingerprintSession, providercore.CodexFingerprintFull} {
		provider := newTestOAuthProvider(4242, map[string]any{providercore.CodexFingerprintModeExtraKey: string(mode)})
		ids := CodexFingerprintIDs(provider, "client-sess-raw", mode)
		require.NotNil(t, ids)
		for name, body := range bodies {
			t.Run(string(mode)+"/"+name, func(t *testing.T) {
				mapCM, rawCM := rawVsMapClientMetadata(t, []byte(body), ids)
				assert.Equal(t, mapCM, rawCM, "raw 字节版与 map 版的 client_metadata 结果必须逐点一致")
			})
		}
	}
}

// TestApplyCodexFingerprintClientMetadataRaw_PreservesUnrelatedFields adds
// identity to a body carrying model, input, stream, and an explicit cache key.
// Model, stream, and the explicit cache key must remain while generated session
// and turn values appear.
func TestApplyCodexFingerprintClientMetadataRaw_PreservesUnrelatedFields(t *testing.T) {
	provider := newTestOAuthProvider(4243, map[string]any{providercore.CodexFingerprintModeExtraKey: "session"})
	ids := CodexFingerprintIDs(provider, "client-sess-preserve", providercore.CodexFingerprintSession)
	require.NotNil(t, ids)

	body := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"hi"}],"stream":true,"prompt_cache_key":"pck-1"}`)
	out, changed, err := openai.ApplyCodexFingerprintClientMetadataRaw(body, ids)
	require.NoError(t, err)
	require.True(t, changed)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(out, &decoded))
	assert.Equal(t, "gpt-5.6-sol", decoded["model"])
	assert.Equal(t, "pck-1", decoded["prompt_cache_key"])
	assert.Equal(t, true, decoded["stream"])
	cm, _ := decoded["client_metadata"].(map[string]any)
	require.NotNil(t, cm)
	assert.Equal(t, ids.SessionID, cm["session_id"])
	assert.Equal(t, ids.TurnID, cm["turn_id"])
}

// TestApplyCodexFingerprintClientMetadataRaw_Noop supplies nil IDs and then an
// empty body. Both operations must report no change and return the
// corresponding original body value.
func TestApplyCodexFingerprintClientMetadataRaw_Noop(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol"}`)
	out, changed, err := openai.ApplyCodexFingerprintClientMetadataRaw(body, nil)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, body, out)

	out, changed, err = openai.ApplyCodexFingerprintClientMetadataRaw(nil, &openai.FingerprintIDs{Mode: string(providercore.CodexFingerprintSession), InstallationID: "x"})
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Nil(t, out)
}

// TestApplyCodexFingerprintClientMetadataRaw_NonObjectBodyUntouched supplies
// arrays, scalar text, and malformed JSON with enabled IDs. Raw rewriting must
// leave those inputs byte-for-byte unchanged and report no modification.
func TestApplyCodexFingerprintClientMetadataRaw_NonObjectBodyUntouched(t *testing.T) {
	provider := newTestOAuthProvider(4244, map[string]any{providercore.CodexFingerprintModeExtraKey: "session"})
	ids := CodexFingerprintIDs(provider, "client-sess-nonobj", providercore.CodexFingerprintSession)
	require.NotNil(t, ids)

	for _, body := range []string{`[1,2,3]`, `"plain string"`, `not json at all`} {
		out, changed, err := openai.ApplyCodexFingerprintClientMetadataRaw([]byte(body), ids)
		require.NoError(t, err)
		assert.False(t, changed, "非 JSON 对象 body 不应被改写: %s", body)
		assert.Equal(t, []byte(body), out)
	}
}
