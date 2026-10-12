package postgres

import (
	"context"
	"fmt"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	keycore "github.com/TokenFlux/TokenRouter/internal/apikey"
)

// Administrative binding to an exclusive group can require a permission grant
// and a Key configuration update together. A configured Ent client opens one
// transaction for both writes, and optional UsersInTx binds the identity writer
// to that connection so a failed Key update can roll back the grant.
//
// Without a client, the configured users and Key writers run sequentially and
// an observer can report the skipped transaction. That path can retain the
// permission grant when the following Key write fails. The caller publishes
// cache invalidation after this operation succeeds; this helper owns commit and
// rollback only for a transaction it starts.

// GroupAccessWriter grants identity group permission during an administrative
// Key binding write.
type GroupAccessWriter interface {
	// AddGroupToAllowedGroups grants the supplied user access to an exclusive
	// group. Transactional callers pass the operation context and a writer bound
	// to the same transaction as the Key update.
	AddGroupToAllowedGroups(context.Context, int64, int64) error
}

// AdminGroupMutations coordinates an exclusive permission grant with selected
// Key configuration writes.
type AdminGroupMutations struct {
	// Client, Keys, Users and UsersInTx bind the grant and Key update. A
	// Client supplies the operation transaction; UsersInTx selects a writer using
	// that transaction, while nil Client leaves the default writers sequential.
	Client    *dbent.Client                     // optional owned-transaction creation dependency
	Keys      keycore.APIKeyRepository          // selected Key configuration writer
	Users     GroupAccessWriter                 // default identity permission writer
	UsersInTx func(*dbent.Tx) GroupAccessWriter // optional transaction-bound identity writer factory
	// Observer reports the nil-Client path before either writer runs. A nil
	// callback omits the notice while leaving the same sequential mutation behavior.
	Observer func(string, string, ...any) // optional skipped-transaction observation callback
}

// GrantGroupAndUpdateFields grants gid to the Key creator, then writes the
// selected Key fields. Callers supply usable users and Key writers that honor
// opCtx's transaction, or bind their writers directly to that transaction.
//
// A configured Client starts a transaction and exposes it through opCtx;
// UsersInTx can choose the identity writer using that same transaction. A
// failed grant or update rolls back this owned transaction, and commit failures
// are returned. With a nil Client, the observer can report skipped protection
// and the sequential grant remains saved if the subsequent update fails.
func (s *AdminGroupMutations) GrantGroupAndUpdateFields(ctx context.Context, key *keycore.APIKey, fields keycore.APIKeyUpdateFields, gid int64) error {
	opCtx := ctx
	users := s.Users
	var tx *dbent.Tx
	if s.Client == nil {
		if s.Observer != nil {
			s.Observer("service.admin", "Warning: entClient is nil, skipping transaction protection for exclusive group binding")
		}
	} else {
		var err error
		tx, err = s.Client.Tx(ctx)
		if err != nil {
			return fmt.Errorf("begin transaction: %w", err)
		}
		// Roll back this owned transaction after failed writes. The grant and Key
		// update participate together when both bound writers use its connection.
		defer func() { _ = tx.Rollback() }()
		opCtx = dbent.NewTxContext(ctx, tx)
		if s.UsersInTx != nil {
			users = s.UsersInTx(tx)
		}
	}
	if err := users.AddGroupToAllowedGroups(opCtx, key.UserID, gid); err != nil {
		return fmt.Errorf("add group to user allowed groups: %w", err)
	}
	if err := s.Keys.Update(opCtx, key, fields); err != nil {
		return fmt.Errorf("update api key: %w", err)
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit transaction: %w", err)
		}
	}
	return nil
}
