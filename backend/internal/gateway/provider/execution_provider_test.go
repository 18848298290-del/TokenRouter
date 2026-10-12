package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// TestExecutionProviderPreservesNativeGraphAndIsolation checks root references and credential-map isolation after copying.
func TestExecutionProviderPreservesNativeGraphAndIsolation(t *testing.T) {
	source := &provider.Record{ID: 1, Credentials: map[string]any{"access_token": "original"}}
	source.ProviderGroups = []provider.GroupMembership{{ProviderID: 1, GroupID: 3, Provider: source}}
	target := NewExecutionProvider(source)
	require.Same(t, &target.Record, target.Record.ProviderGroups[0].Provider)
	target.Record.Credentials["access_token"] = "target"
	require.Equal(t, "original", source.GetCredential("access_token"))
	snapshot := ExecutionRecord(target)
	require.Same(t, snapshot, snapshot.ProviderGroups[0].Provider)
	snapshot.Credentials["access_token"] = "snapshot"
	require.Equal(t, "target", target.View().GetCredential("access_token"))
	payload, err := json.Marshal(target)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(payload), "执行凭据与路线不能作为持久或公开载荷")
}

// TestExecutionProviderValueCopyAndBoundReader checks per-attempt field replacement and visibility through a previously bound reader.
func TestExecutionProviderValueCopyAndBoundReader(t *testing.T) {
	shared := NewExecutionProvider(&provider.Record{ID: 1, Credentials: map[string]any{"access_token": "shared"}})
	attempt := *shared
	attempt.Record.Credentials = map[string]any{"access_token": "attempt"}
	require.Equal(t, "shared", shared.View().GetCredential("access_token"))
	read := attempt.View().GetCredential
	ApplyExecutionRecord(&attempt, &provider.Record{ID: 1, Credentials: map[string]any{"access_token": "updated"}})
	require.Equal(t, "updated", read("access_token"))
	var absent *ExecutionProvider
	require.Nil(t, absent.View())
	require.Nil(t, ExecutionRecord(absent))
	require.Nil(t, ExecutionProviders(nil))
	require.Nil(t, ExecutionRecords(nil))
}
