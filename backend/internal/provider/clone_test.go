package provider

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
)

// TestCloneValuesPreservesShapesAndIsolation checks payloads, value types, and independent copies of slices sharing a prefix.
func TestCloneValuesPreservesShapesAndIsolation(t *testing.T) {
	nested := []any{map[string]any{"number": json.Number("1.123456789")}, "second"}
	values := map[string]any{"short": nested[:1], "long": nested, "nil": []any(nil), "empty": []any{}, "raw": json.RawMessage(`{"ok":true}`)}
	before, err := json.Marshal(values)
	require.NoError(t, err)
	copied := CloneValues(values)
	after, err := json.Marshal(copied)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
	copiedLong, ok := copied["long"].([]any)
	require.True(t, ok)
	copiedEntry, ok := copiedLong[0].(map[string]any)
	require.True(t, ok)
	originalEntry, ok := nested[0].(map[string]any)
	require.True(t, ok)
	require.IsType(t, json.Number(""), copiedEntry["number"])
	copiedEntry["number"] = json.Number("9")
	require.Equal(t, json.Number("1.123456789"), originalEntry["number"])
	copiedRaw, ok := copied["raw"].(json.RawMessage)
	require.True(t, ok)
	originalRaw, ok := values["raw"].(json.RawMessage)
	require.True(t, ok)
	copiedRaw[0] = '['
	require.Equal(t, byte('{'), originalRaw[0])
}

func TestCloneValuesRetainsInvalidCycleForEncodingError(t *testing.T) {
	values := map[string]any{}
	values["self"] = values
	_, err := json.Marshal(CloneValues(values))
	require.Error(t, err)
}

func TestCloneRecordIsolatesRelationsAndPrivateValues(t *testing.T) {
	expires := time.Now()
	source := &Record{ID: 1, GroupIDs: []int64{3}, Credentials: map[string]any{"token": "private-token"}, Extra: map[string]any{"cookie": "private-cookie"}, Proxy: &egress.Proxy{Password: "private-password", ExpiresAt: &expires}, Groups: []*accessview.GroupConfig{{ID: 3, ModelRouting: map[string][]int64{"model": {1}}}}}
	source.ProviderGroups = []GroupMembership{{ProviderID: 1, GroupID: 3, Provider: source, Group: source.Groups[0]}}
	copied := CloneRecord(source)
	require.Same(t, copied, copied.ProviderGroups[0].Provider)
	copied.Credentials["token"] = "changed"
	copied.Groups[0].ModelRouting["model"][0] = 9
	copied.Proxy.Password = "changed"
	copied.Proxy.ExpiresAt = nil
	require.Equal(t, "private-token", source.Credentials["token"])
	require.Equal(t, int64(1), source.Groups[0].ModelRouting["model"][0])
	require.Equal(t, "private-password", source.Proxy.Password)
	require.NotNil(t, source.Proxy.ExpiresAt)
	require.NotContains(t, fmt.Sprintf("%+v %#v", source, source), "private-")
	source.ProviderGroups = nil
	payload, err := json.Marshal(source)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "private-")
}

// TestCloneValuesCopiesNormalizedProtocolIDs checks named protocol slices as well as JSON-decoded []any values.
func TestCloneValuesCopiesNormalizedProtocolIDs(t *testing.T) {
	protocols := []protocol.ProtocolID{protocol.ProtocolOpenAIResponses}
	cloned := CloneValues(map[string]any{"upstream_protocols": protocols})
	copied, ok := cloned["upstream_protocols"].([]protocol.ProtocolID)
	if !ok {
		t.Fatal("协议切片类型发生改变")
	}
	copied[0] = protocol.ProtocolAnthropicMessages
	if protocols[0] != protocol.ProtocolOpenAIResponses {
		t.Fatal("协议副本污染原值")
	}
}

// TestCopyRecordIntoKeepsRootAndSupportsSameRecord checks root references and methods bound before an in-place copy.
func TestCopyRecordIntoKeepsRootAndSupportsSameRecord(t *testing.T) {
	source := &Record{ID: 1, Credentials: map[string]any{"token": "source"}}
	source.ProviderGroups = []GroupMembership{{Provider: source}}
	var target Record
	CopyRecordInto(&target, source)
	require.Same(t, &target, target.ProviderGroups[0].Provider)
	target.Credentials["token"] = "target"
	require.Equal(t, "source", source.GetCredential("token"))
	CopyRecordInto(&target, &target)
	require.Same(t, &target, target.ProviderGroups[0].Provider)
	require.Equal(t, "target", target.GetCredential("token"))
	CopyRecordInto(&target, nil)
	require.Zero(t, target.ID)
	require.Nil(t, target.Credentials)
	require.Nil(t, target.ProviderGroups)
}
