package codec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
)

func TestProviderWirePreservesHistoricalFields(t *testing.T) {
	value := &provider.Record{ID: 1, Credentials: map[string]any{"access_token": "fixture-only"}, Extra: map[string]any{"counter": float64(3)}, Groups: []*accessview.GroupConfig{{ID: 7, AllowedProtocols: nil}}, ProviderGroups: []provider.GroupMembership{}}
	raw, err := MarshalProviderRecord(value)
	require.NoError(t, err)
	var stored map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &stored))
	require.JSONEq(t, `{"access_token":"fixture-only"}`, string(stored["Credentials"]))
	require.Equal(t, "[]", string(stored["ProviderGroups"]))
	var groups []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(stored["Groups"], &groups))
	require.Equal(t, "null", string(groups[0]["ProviderGroups"]))
	require.Equal(t, "null", string(groups[0]["AllowedProtocols"]))
	decoded, err := UnmarshalProviderRecord(raw)
	require.NoError(t, err)
	require.Equal(t, value.Credentials, decoded.Credentials)
	require.NotNil(t, decoded.ProviderGroups)
	require.Empty(t, decoded.ProviderGroups)
	require.Equal(t, value.Groups, decoded.Groups)
	// Storage encoding includes execution credentials; public provider JSON redacts them.
	public, err := json.Marshal(value)
	require.NoError(t, err)
	require.NotContains(t, string(public), "fixture-only")
}

func TestProviderWireReadsExistingNestedGroup(t *testing.T) {
	raw := []byte(`{"ID":1,"Credentials":{"api_key":"fixture-only"},"Groups":null,"ProviderGroups":[{"ProviderID":1,"GroupID":7,"Provider":null,"Group":{"ID":7,"Name":"historical","ProviderGroups":null,"AllowedProtocols":[]}}]}`)
	decoded, err := UnmarshalProviderRecord(raw)
	require.NoError(t, err)
	require.Nil(t, decoded.Groups)
	require.Len(t, decoded.ProviderGroups, 1)
	require.Equal(t, "historical", decoded.ProviderGroups[0].Group.Name)
	require.NotNil(t, decoded.ProviderGroups[0].Group.AllowedProtocols)
	again, err := MarshalProviderRecord(decoded)
	require.NoError(t, err)
	var stored map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(again, &stored))
	require.Contains(t, string(stored["ProviderGroups"]), `"ProviderGroups":null`)
}
