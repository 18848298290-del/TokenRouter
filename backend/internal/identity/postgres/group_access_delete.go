package postgres

import (
	"context"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// GroupAccessDeletion removes user group grants in the group deletion transaction.
type GroupAccessDeletion struct{ exec postgresinfra.Executor }

func GroupAccessDeletionInTx(exec postgresinfra.Executor) GroupAccessDeletion {
	return GroupAccessDeletion{exec: exec}
}

func (p GroupAccessDeletion) Delete(ctx context.Context, id int64) error {
	if _, err := p.exec.ExecContext(ctx, "DELETE FROM user_allowed_groups WHERE group_id = $1", id); err != nil {
		return err
	}
	if _, err := p.exec.ExecContext(ctx, "DELETE FROM user_disabled_public_groups WHERE group_id = $1", id); err != nil {
		return err
	}
	return nil
}
