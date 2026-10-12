package postgres

import (
	"context"
	"database/sql"

	"github.com/lib/pq"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// GroupLinks updates provider associations on the caller's connection; the caller owns commit and cache invalidation.
type GroupLinks struct{ exec postgresinfra.Executor }

func GroupLinksInTx(exec postgresinfra.Executor) GroupLinks { return GroupLinks{exec: exec} }
func (p GroupLinks) Clear(ctx context.Context, groupID int64) (sql.Result, error) {
	return p.exec.ExecContext(ctx, "DELETE FROM provider_groups WHERE group_id = $1", groupID)
}

func (p GroupLinks) Bind(ctx context.Context, groupID int64, providerIDs []int64) error {
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

func (p GroupLinks) Copy(ctx context.Context, targetID, sourceGroupID int64, oauthOnly bool) (sql.Result, error) {
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
