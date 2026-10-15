package postgres

import (
	"context"
	"database/sql"

	"github.com/lib/pq"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// group_links.go changes provider-group memberships through a caller-supplied
// transaction executor.
// Group edits and duplication change provider membership through the
// transaction connection supplied by routing storage. Existing pairs are
// retained on insertion conflicts, while copying filters deleted providers
// and can exclude API Key records. The routing caller commits the group
// changes and publishes scheduling updates.

// GroupLinks updates provider associations on the caller's connection.
// The caller owns commit and cache invalidation.
type GroupLinks struct {
	// exec is borrowed from the routing operation; its owner keeps the connection
	// open and decides when association changes are committed and published.
	exec postgresinfra.Executor
}

// GroupLinksInTx binds provider membership operations to the caller's
// executor and returns their adapter. The executor is borrowed, so the caller
// keeps its connection available and owns transaction completion and
// scheduling publication.
func GroupLinksInTx(exec postgresinfra.Executor) GroupLinks { return GroupLinks{exec: exec} }

// Clear removes all membership pairs for groupID through the borrowed
// executor. It returns the database result for the caller's affected-row
// decisions; commit and scheduling updates remain with the group operation
// using this adapter.
func (p GroupLinks) Clear(ctx context.Context, groupID int64) (sql.Result, error) {
	return p.exec.ExecContext(ctx, "DELETE FROM provider_groups WHERE group_id = $1", groupID)
}

// Bind inserts each supplied provider ID into groupID, retaining an existing
// pair on conflict. It performs no provider-type filtering or ID
// normalization; the routing caller validates its selection and owns commit
// and event publication.
func (p GroupLinks) Bind(ctx context.Context, groupID int64, providerIDs []int64) error {
	// Keep an existing pair when duplication or replacement includes it again, so
	// the group operation can retain its association without failing the transaction.
	_, err := p.exec.ExecContext(
		ctx,
		`INSERT INTO provider_groups (provider_id, group_id, created_at)
		 SELECT unnest($1::bigint[]), $2, NOW()
		 ON CONFLICT (provider_id, group_id) DO NOTHING`,
		pq.Array(providerIDs),
		groupID,
	)
	return err
}

// Copy inserts source-group memberships into targetID while omitting deleted
// providers and existing destination pairs. When oauthOnly is true, its SQL
// excludes API Key providers; other stored types remain eligible, and the
// caller owns the transaction and publication.
func (p GroupLinks) Copy(ctx context.Context, targetID, sourceGroupID int64, oauthOnly bool) (sql.Result, error) {
	// Copy source links through the caller connection, filtering deleted providers
	// before destination conflicts are ignored; the group owner publishes after commit.
	return p.exec.ExecContext(
		ctx,
		`INSERT INTO provider_groups (provider_id, group_id, created_at)
		 SELECT ag.provider_id, $2, NOW()
		 FROM provider_groups ag
		 JOIN providers a ON a.id = ag.provider_id
		 WHERE ag.group_id = $1
		   AND a.deleted_at IS NULL
		   AND (NOT $3 OR a.type <> $4)
		 ON CONFLICT (provider_id, group_id) DO NOTHING`,
		sourceGroupID,
		targetID,
		oauthOnly,
		"apikey",
	)
}
