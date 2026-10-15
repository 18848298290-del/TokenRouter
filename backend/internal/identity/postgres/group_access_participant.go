package postgres

import (
	"context"

	dbent "github.com/TokenFlux/TokenRouter/ent"
)

// group_access_participant.go supplies account group-grant writes bound to an
// existing Ent transaction.
// Administrator group replacement grants the destination before moving account
// keys. This participant uses the caller Ent transaction for the grant write,
// keeping permission changes in the same commit scope as key migration and
// removal of the preceding grant.

// GroupAccessParticipant writes account group grants within an
// administrator-owned Ent transaction.
type GroupAccessParticipant struct {
	// tx and store reference the same Ent transaction client.
	// AddGroupToAllowedGroups injects tx into context for the repository, while
	// the administrator operation owns commit or rollback.
	tx    *dbent.Tx  // caller grant-write transaction
	store *UserStore // transaction-backed account grant writer
}

// GroupAccessInTx constructs a grant participant using the supplied active Ent
// transaction. The caller retains transaction ownership, and the returned
// repository uses tx.Client for its account permission writes.
func GroupAccessInTx(tx *dbent.Tx) *GroupAccessParticipant {
	return &GroupAccessParticipant{tx: tx, store: &UserStore{client: tx.Client()}}
}

// AddGroupToAllowedGroups adds an account grant through the retained
// transaction-backed repository. It injects the transaction into context
// before delegation; the caller administrator operation remains responsible
// for commit or rollback.
func (p *GroupAccessParticipant) AddGroupToAllowedGroups(ctx context.Context, userID, groupID int64) error {
	return p.store.AddGroupToAllowedGroups(dbent.NewTxContext(ctx, p.tx), userID, groupID)
}
