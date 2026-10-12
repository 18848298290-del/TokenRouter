package creative_test

// This file covers workspace isolation across Public.CreateRun and Queries task/output access.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// TestCreativeWorkspaceScopeIsolation checks task visibility and idempotency isolation between one user's
// workspaces.
func TestCreativeWorkspaceScopeIsolation(t *testing.T) {
	svc := newCreativeTestService()
	ctx := context.Background()
	firstScope := testCreativeScope(7)
	secondScope := creative.CreativeRunScope{UserID: 7, WorkspaceID: "22222222-2222-4222-8222-222222222222"}

	first, err := svc.CreateRun(ctx, firstScope, validCreateParams(), "same-idempotency-key")
	require.NoError(t, err)
	second, err := svc.CreateRun(ctx, secondScope, validCreateParams(), "same-idempotency-key")
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)

	firstList, err := svc.ListRuns(ctx, firstScope, creative.CreativeRunFilter{Limit: 20})
	require.NoError(t, err)
	require.Len(t, firstList.Data, 1)
	require.Equal(t, first.ID, firstList.Data[0].ID)

	secondList, err := svc.ListRuns(ctx, secondScope, creative.CreativeRunFilter{Limit: 20})
	require.NoError(t, err)
	require.Len(t, secondList.Data, 1)
	require.Equal(t, second.ID, secondList.Data[0].ID)

	_, err = svc.GetRun(ctx, firstScope, second.ID)
	require.ErrorIs(t, err, creative.ErrCreativeRunNotFound)

	legacyID := "crun_legacy_workspace_hidden"
	testassert.MustType[*creativeFakeRunRepo](svc.Repo).runs[legacyID] = &creative.CreativeRun{RunID: legacyID, UserID: 7}
	legacyList, err := svc.ListRuns(ctx, firstScope, creative.CreativeRunFilter{Limit: 20})
	require.NoError(t, err)
	for _, run := range legacyList.Data {
		require.NotEqual(t, legacyID, run.ID)
	}
	_, err = svc.GetRun(ctx, firstScope, legacyID)
	require.ErrorIs(t, err, creative.ErrCreativeRunNotFound)
	_, err = svc.GetOutputContent(ctx, firstScope, legacyID, 0)
	require.ErrorIs(t, err, creative.ErrCreativeRunNotFound)
	require.ErrorIs(t, svc.AckOutput(ctx, firstScope, legacyID, 0), creative.ErrCreativeRunNotFound)
}
