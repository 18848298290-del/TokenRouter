package postgres

import (
	"context"
	"fmt"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	keycore "github.com/TokenFlux/TokenRouter/internal/apikey"
)

// GroupAccessWriter lets identity grant group access within the key update transaction.
type GroupAccessWriter interface {
	AddGroupToAllowedGroups(context.Context, int64, int64) error
}
type AdminGroupMutations struct {
	Client    *dbent.Client
	Keys      keycore.APIKeyRepository
	Users     GroupAccessWriter
	UsersInTx func(*dbent.Tx) GroupAccessWriter
	Observer  func(string, string, ...any)
}

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
