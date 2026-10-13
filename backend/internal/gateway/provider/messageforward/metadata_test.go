package messageforward

import (
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// TestBuildOAuthMetadataUserID_FallbackWithoutAccountUUID supplies a
// fingerprint identity while provider metadata omits account identity. The
// generated string matches the legacy pattern with a nonempty client component
// and empty account field.
func TestBuildOAuthMetadataUserID_FallbackWithoutAccountUUID(t *testing.T) {
	parsed := &requeststate.ParsedRequest{
		Model:          "claude-sonnet-4-5",
		Stream:         true,
		MetadataUserID: "",
	}

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 123,
			Type:  capability.ProviderTypeOAuth,
			Extra: map[string]any{},
		}, // Provider and client identity fields are deliberately absent.
	}

	fp := &anthropic.Fingerprint{ClientID: "deadbeef"} // Client ID for the legacy metadata format.

	got := metadataUserID(parsed, provider, fp)
	require.NotEmpty(t, got)

	// The supplied fingerprint has no CLI version, so the legacy-format
	// matcher checks the empty account segment and derived session UUID.
	re := regexp.MustCompile(`^user_[a-zA-Z0-9]+_account__session_[a-f0-9-]{36}$`)
	require.True(t, re.MatchString(got), "unexpected user_id format: %s", got)
}

// TestBuildOAuthMetadataUserID_UsesAccountUUIDWhenPresent supplies provider
// client identity and an account UUID with no fingerprint. The legacy metadata
// string contains both configured identity components and a session UUID.
func TestBuildOAuthMetadataUserID_UsesAccountUUIDWhenPresent(t *testing.T) {
	parsed := &requeststate.ParsedRequest{
		Model:          "claude-sonnet-4-5",
		Stream:         true,
		MetadataUserID: "",
	}

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 123,
			Type: capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"account_uuid":      "acc-uuid",
				"claude_user_id":    "clientid123",
				"anthropic_user_id": "",
			},
		},
	}

	got := metadataUserID(parsed, provider, nil)
	require.NotEmpty(t, got)

	// The account UUID populates the account identity field.
	re := regexp.MustCompile(`^user_clientid123_account_acc-uuid_session_[a-f0-9-]{36}$`)
	require.True(t, re.MatchString(got), "unexpected user_id format: %s", got)
}

// TestBuildOAuthMetadataUserID_SessionIDStableAcrossTurns appends conversation
// turns while provider and fingerprint inputs stay fixed. The complete metadata
// identity stays equal across turns and differs for another opening message.
func TestBuildOAuthMetadataUserID_SessionIDStableAcrossTurns(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 777, Type: capability.ProviderTypeOAuth, Extra: map[string]any{"account_uuid": "acc-uuid"}}}
	fp := &anthropic.Fingerprint{ClientID: "clientid777", UserAgent: "claude-cli/2.1.161 (external, cli)"}

	mustParse := func(body string) *requeststate.ParsedRequest {
		parsed, err := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef([]byte(body)), capability.PlatformAnthropic)
		require.NoError(t, err)
		return parsed
	}

	round1 := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"first question"}]}`)
	round2 := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"first question"},` +
		`{"role":"assistant","content":"answer 1"},` +
		`{"role":"user","content":"second question"}]}`)
	round3 := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"first question"},` +
		`{"role":"assistant","content":"answer 1"},` +
		`{"role":"user","content":"second question"},` +
		`{"role":"assistant","content":"answer 2"},` +
		`{"role":"user","content":"third question"}]}`)

	id1 := metadataUserID(round1, provider, fp)
	id2 := metadataUserID(round2, provider, fp)
	id3 := metadataUserID(round3, provider, fp)

	require.NotEmpty(t, id1)
	require.Equal(t, id1, id2, "session_id 应随对话增长保持不变")
	require.Equal(t, id2, id3, "session_id 应跨所有轮次保持不变")

	// A different opening message represents a different session.
	other := mustParse(`{"model":"claude-sonnet-4-5","system":"sys","messages":[` +
		`{"role":"user","content":"a completely different opener"}]}`)
	idOther := metadataUserID(other, provider, fp)
	require.NotEqual(t, id1, idOther, "不同首条消息应派生不同 session_id")
}
