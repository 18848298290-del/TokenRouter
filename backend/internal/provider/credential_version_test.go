package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestObserveCredentialVersionEmptyInputs distinguishes an absent record from
// one whose credentials are absent. Present records receive writable empty
// objects, and separate observations own separate credential maps.
func TestObserveCredentialVersionEmptyInputs(t *testing.T) {
	require.Equal(t, CredentialVersion{}, ObserveCredentialVersion(nil))
	for _, credentials := range []map[string]any{nil, {}} {
		value := &Record{ID: 41, Credentials: credentials}
		first := ObserveCredentialVersion(value)
		second := ObserveCredentialVersion(value)
		require.NotNil(t, first.Credentials)
		require.Empty(t, first.Credentials)
		require.Nil(t, first.ProxyID)
		first.Credentials["new"] = "first"
		require.Empty(t, second.Credentials)
		require.Empty(t, value.Credentials)
	}
}

// TestObserveCredentialVersionValueOwnership captures a provider before its
// mutable authentication data changes. Supported nested containers and proxy
// values remain independent, while unsupported pointers keep their source owner.
func TestObserveCredentialVersionValueOwnership(t *testing.T) {
	proxyID := int64(7)
	unsupported := 11
	nested := map[string]any{"token": "observed"}
	scopes := []string{"read"}
	payload := []byte{1, 2}
	labels := map[string]string{"region": "old"}
	value := &Record{
		ID:       41,
		Platform: PlatformOpenAI,
		Type:     ProviderTypeOAuth,
		Status:   StatusActive,
		ProxyID:  &proxyID,
		Credentials: map[string]any{
			"nested":      nested,
			"list":        []any{nested, scopes, payload, labels},
			"number":      int64(9007199254740993),
			"nil_map":     map[string]any(nil),
			"nil_slice":   []any(nil),
			"unsupported": &unsupported,
		},
	}
	observed := ObserveCredentialVersion(value)
	require.Equal(t, int64(41), observed.ID)
	require.Equal(t, PlatformOpenAI, observed.Platform)
	require.Equal(t, ProviderTypeOAuth, observed.Type)
	require.Equal(t, StatusActive, observed.Status)
	require.NotSame(t, value.ProxyID, observed.ProxyID)

	proxyID = 8
	nested["token"] = "changed"
	scopes[0] = "write"
	payload[0] = 9
	labels["region"] = "new"
	value.Credentials["new"] = "source"
	require.Equal(t, int64(7), *observed.ProxyID)
	observedNested, ok := observed.Credentials["nested"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "observed", observedNested["token"])
	require.Equal(t, []any{
		map[string]any{"token": "observed"},
		[]string{"read"},
		[]byte{1, 2},
		map[string]string{"region": "old"},
	}, observed.Credentials["list"])
	require.NotContains(t, observed.Credentials, "new")
	require.Equal(t, int64(9007199254740993), observed.Credentials["number"])
	require.Equal(t, map[string]any(nil), observed.Credentials["nil_map"])
	require.Equal(t, []any(nil), observed.Credentials["nil_slice"])
	require.Same(t, &unsupported, observed.Credentials["unsupported"])

	*observed.ProxyID = 9
	observedNested["token"] = "snapshot"
	observedList, ok := observed.Credentials["list"].([]any)
	require.True(t, ok)
	observedScopes, ok := observedList[1].([]string)
	require.True(t, ok)
	observedScopes[0] = "snapshot"
	require.Equal(t, int64(8), proxyID)
	require.Equal(t, "changed", nested["token"])
	require.Equal(t, "write", scopes[0])
	observedListNested, ok := observedList[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "snapshot", observedListNested["token"])
}
