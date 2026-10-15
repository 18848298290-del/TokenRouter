package postgres

import (
	"context"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// group_access_delete.go deletes account grants and denials referencing a
// group during its removal.
// Group deletion removes both exclusive-access grants and administrator
// public-group denials before its owner completes the database deletion. Both
// tables can reference the removed group, so the participant processes them
// through the executor supplied by the outer deletion transaction.
//
// A failure in either permission-table delete stops this participant and
// returns the database error. The first delete may already have executed when
// the second fails; transaction rollback belongs to the group-deletion owner.
// The participant keeps no separate transaction or recovery state, and its
// retained executor must stay available until those writes finish.

// GroupAccessDeletion removes account group permissions through the deletion
// transaction executor.
type GroupAccessDeletion struct {
	// exec is supplied by the group-deletion owner. Keep it in that transaction
	// through both permission-table deletes and complete the transaction at the
	// caller.
	exec postgresinfra.Executor
}

// GroupAccessDeletionInTx retains the executor for a group-deletion
// transaction participant. The caller supplies the active transaction executor
// and owns completion after both user permission tables have been processed.
func GroupAccessDeletionInTx(exec postgresinfra.Executor) GroupAccessDeletion {
	return GroupAccessDeletion{exec: exec}
}

// Delete removes one group ID from exclusive grants and public-group denials
// through the caller executor. Keep both writes in the group-deletion
// transaction and handle its returned error at that owner.
//
// The first database failure stops the sequence. If public-denial deletion
// fails after exclusive grants were removed, the outer caller rolls back that
// preceding work with the rest of group deletion. Successful return leaves
// commit to the same owner.
func (p GroupAccessDeletion) Delete(ctx context.Context, id int64) error {
	// Delete exclusive grants before public denials within the same caller
	// transaction. A later denial-table failure relies on that owner to roll back
	// the earlier grant deletion.
	if _, err := p.exec.ExecContext(ctx, "DELETE FROM user_allowed_groups WHERE group_id = $1", id); err != nil {
		return err
	}
	if _, err := p.exec.ExecContext(ctx, "DELETE FROM user_disabled_public_groups WHERE group_id = $1", id); err != nil {
		return err
	}
	return nil
}
