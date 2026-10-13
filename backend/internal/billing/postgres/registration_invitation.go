package postgres

import (
	"context"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/redeemcode"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// Registration claims an unused, unexpired code together with its user
// creation flow. Load supplies the candidate snapshot for caller validation,
// while Consume conditionally changes stored status and last-recipient data so
// concurrent attempts cannot both consume an unused row. The bound client
// determines whether these operations join the outer transaction.
//
// Failed registration work can restore the supplied code snapshot through the
// common update builder. That restoration assigns core fields and clears
// absent optional values, so the caller needs a snapshot from the intended
// claim and controls when compensation is allowed. This adapter leaves
// invitation-type validation and transaction completion with the registration
// use case.

// RegistrationInvitations reads and conditionally consumes registration code
// snapshots through a bound Ent client.
type RegistrationInvitations struct {
	// client is the bound invitation connection used directly by every method.
	// Callers bind a transaction client when registration needs shared commit or
	// rollback.
	client *dbent.Client // bound invitation persistence client
}

// NewRegistrationInvitations binds a caller-owned Ent client for invitation
// operations. Methods use this bound client directly, so a transaction carried
// in ctx does not select a different client; callers supply the intended
// connection.
func NewRegistrationInvitations(client *dbent.Client) *RegistrationInvitations {
	return &RegistrationInvitations{client}
}

// RegistrationInvitationsInTx binds invitation reads and writes to a non-nil
// caller transaction client. The caller owns commit, rollback and compensation
// ordering for registration work; this constructor begins no additional
// transaction.
func RegistrationInvitationsInTx(tx *dbent.Tx) *RegistrationInvitations {
	return &RegistrationInvitations{tx.Client()}
}

// Load reads the code with the exact supplied spelling and returns its
// registration snapshot. Missing codes become ErrRedeemCodeNotFound; returned
// optional pointers come from the loaded entity, and the caller validates
// invitation type and eligibility before Consume.
func (r *RegistrationInvitations) Load(ctx context.Context, invitationCode string) (*billing.RedeemCode, error) {
	client := r.client
	entity, err := client.RedeemCode.Query().Where(redeemcode.CodeEQ(invitationCode)).Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, billing.ErrRedeemCodeNotFound
		}
		return nil, err
	}
	return &billing.RedeemCode{
		ID:        entity.ID,
		Code:      entity.Code,
		Type:      entity.Type,
		Value:     entity.Value,
		Status:    entity.Status,
		MaxUses:   entity.MaxUses,
		UsedCount: entity.UsedCount,
		ExpiresAt: entity.ExpiresAt,
		UsedBy:    entity.UsedBy,
		UsedAt:    entity.UsedAt,
		Notes:     registrationInvitationNotes(entity.Notes),
		CreatedAt: entity.CreatedAt,
		PlanID:    entity.PlanID,
	}, nil
}

// Consume conditionally marks an unused code with no expiry or a future expiry
// as used by userID. It sets count to one and records the latest
// recipient/time on the bound client; the caller owns invitation-type
// validation and transaction completion.
//
// Zero affected rows return ErrRedeemCodeUsed for missing, already claimed or
// expired targets. The update condition protects the unused claim against
// concurrency, and separate current UTC observations supply expiry checking
// and the recorded use time. Compensation, when needed, is ordered by the
// outer registration flow.
func (r *RegistrationInvitations) Consume(ctx context.Context, invitationID, userID int64) error {
	client := r.client
	// Claim only an unused, currently unexpired row while recording its
	// recipient; a competing claim that changed status leaves this update with
	// zero affected rows.
	affected, err := client.RedeemCode.Update().
		Where(
			redeemcode.IDEQ(invitationID),
			redeemcode.StatusEQ(billing.StatusUnused),
			redeemcode.Or(redeemcode.ExpiresAtIsNil(), redeemcode.ExpiresAtGT(time.Now().UTC())),
		).
		SetStatus(billing.StatusUsed).
		SetUsedCount(1).
		SetUsedBy(userID).
		SetUsedAt(time.Now().UTC()).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return billing.ErrRedeemCodeUsed
	}
	return nil
}

// RestoreSnapshot replaces the supplied code snapshot on the bound client,
// returning nil for nil input. The common builder clears nil optional fields;
// persistence errors propagate, and the caller authorizes and orders
// restoration relative to registration writes.
func (r *RegistrationInvitations) RestoreSnapshot(ctx context.Context, code *billing.RedeemCode) error {
	if code == nil {
		return nil
	}
	_, err := redeemUpdateBuilder(r.client, code).Save(ctx)
	return err
}

// registrationInvitationNotes returns stored invitation notes or an empty
// string when absent. Load uses this value in its registration snapshot, so
// absent notes and explicitly empty notes have the same string representation.
func registrationInvitationNotes(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
