package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/enttest"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/creative"
)

// Workspace repository fixtures use SQLite with Ent schema creation to compare
// two browser partitions for the same user. Queries and idempotency lookups
// return the matching task, while legacy rows without a workspace remain
// hidden; these cases inspect stored results rather than PostgreSQL locking.

// These UUIDs name two browser partitions for the same fixture user, allowing
// stored task and idempotency queries to exercise workspace equality independently.
const (
	creativeRepoWorkspaceA = "11111111-1111-4111-8111-111111111111"
	creativeRepoWorkspaceB = "22222222-2222-4222-8222-222222222222"
)

// openCreativeRunRepositoryForTest opens a shared-memory SQLite database with
// the full Ent schema and foreign keys enabled. It returns the repository and
// a cleanup callback that callers register after fixture creation.
func openCreativeRunRepositoryForTest(t *testing.T) (creative.CreativeRunRepository, func()) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:creative_run_scope_%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	// The returned cleanup closes the Ent client and underlying SQLite pool after
	// repository assertions finish; pool close errors do not replace fixture results.
	return NewCreativeRunRepository(client), func() {
		require.NoError(t, client.Close())
		_ = db.Close()
	}
}

// creativeRepoWorkspaceRunParams returns metadata for the supplied
// run/workspace/key with zero output rows. User seven, group twelve, and a
// placeholder Key identify the fixture; it does not reserve funds.
func creativeRepoWorkspaceRunParams(runID, workspaceID string, key *string) creative.CreateCreativeRunParams {
	return creative.CreateCreativeRunParams{
		RunID:                runID,
		UserID:               7,
		WorkspaceID:          workspaceID,
		GroupID:              12,
		APIKeyID:             100,
		Model:                "gpt-image-1",
		RequestedModel:       "gpt-image-1",
		Operation:            creative.CreativeOperationGenerate,
		RequestedOutputCount: 0,
		ImageSize:            "1K",
		ResponseMIMEType:     "image/png",
		PromptHash:           "prompt-hash",
		RequestFingerprint:   "fingerprint-" + runID,
		IdempotencyKey:       key,
	}
}

// TestCreativeRunRepositoryWorkspaceScope creates two same-user tasks sharing
// a key across different workspaces. It checks separate lists and replay
// lookups, cross-workspace denial, and exclusion of a legacy row with no
// workspace.
func TestCreativeRunRepositoryWorkspaceScope(t *testing.T) {
	repo, cleanup := openCreativeRunRepositoryForTest(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	key := "same-key"

	first, err := repo.CreateCreativeRun(ctx, creativeRepoWorkspaceRunParams("crun_scope_a", creativeRepoWorkspaceA, &key))
	require.NoError(t, err)
	second, err := repo.CreateCreativeRun(ctx, creativeRepoWorkspaceRunParams("crun_scope_b", creativeRepoWorkspaceB, &key))
	require.NoError(t, err)

	firstList, err := repo.ListCreativeRunsForOwner(ctx, creative.CreativeRunScope{UserID: 7, WorkspaceID: creativeRepoWorkspaceA}, creative.CreativeRunFilter{Limit: 20})
	require.NoError(t, err)
	require.Len(t, firstList, 1)
	require.Equal(t, first.RunID, firstList[0].RunID)
	secondList, err := repo.ListCreativeRunsForOwner(ctx, creative.CreativeRunScope{UserID: 7, WorkspaceID: creativeRepoWorkspaceB}, creative.CreativeRunFilter{Limit: 20})
	require.NoError(t, err)
	require.Len(t, secondList, 1)
	require.Equal(t, second.RunID, secondList[0].RunID)

	got, err := repo.GetCreativeRunByIdempotencyKey(ctx, creative.CreativeRunScope{UserID: 7, WorkspaceID: creativeRepoWorkspaceA}, key)
	require.NoError(t, err)
	require.Equal(t, first.RunID, got.RunID)
	got, err = repo.GetCreativeRunByIdempotencyKey(ctx, creative.CreativeRunScope{UserID: 7, WorkspaceID: creativeRepoWorkspaceB}, key)
	require.NoError(t, err)
	require.Equal(t, second.RunID, got.RunID)

	_, err = repo.GetCreativeRunByRunIDForOwner(ctx, creative.CreativeRunScope{UserID: 7, WorkspaceID: creativeRepoWorkspaceB}, first.RunID)
	require.ErrorIs(t, err, creative.ErrCreativeRunNotFound)

	legacy, err := repo.CreateCreativeRun(ctx, creativeRepoWorkspaceRunParams("crun_scope_legacy", "", nil))
	require.NoError(t, err)
	require.Nil(t, legacy.WorkspaceID)
	firstList, err = repo.ListCreativeRunsForOwner(ctx, creative.CreativeRunScope{UserID: 7, WorkspaceID: creativeRepoWorkspaceA}, creative.CreativeRunFilter{Limit: 20})
	require.NoError(t, err)
	require.Len(t, firstList, 1)
}
