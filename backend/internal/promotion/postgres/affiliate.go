package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
)

// Affiliate grants pair profile totals with source receipts, and transfers
// retain fund observations that later balance edits would otherwise hide.
// Thaw clears deadlines on existing grant rows while moving their quota
// between frozen and available totals. Binding claims an unset inviter before
// incrementing its count; accrual records a source user and optional payment
// order alongside frozen or immediately available quota.
//
// Transactions may be owned here or supplied by an outer caller. Thaw clears
// due ledger deadlines before moving their sum between profile totals;
// transfer then claims all available quota, invokes same-transaction billing,
// and stores the resulting fund snapshots. Administrator reads distinguish
// these saved snapshots from current account state. Code collision loops
// attempt bounded regeneration, and parameterized record filters use trusted
// SQL columns supplied by the owning query.

// Affiliate generation uses a fixed code length and bounded collision
// attempts, while the overview query computes administrator quota
// observations.
const (
	affiliateCodeLength      = 12 // generated invitation code bytes
	affiliateCodeMaxAttempts = 12 // maximum generated collision attempts

	// affiliateUserOverviewSQL combines profile counts with distinct
	// contributing invitees and due frozen ledger amounts for a read-only
	// administrator overview.
	affiliateUserOverviewSQL = `
SELECT ua.user_id,
       COALESCE(u.email, ''),
       COALESCE(u.username, ''),
       ua.aff_code,
       COALESCE(ua.aff_rebate_rate_percent, 0)::double precision,
       (ua.aff_rebate_rate_percent IS NOT NULL) AS has_custom_rate,
       ua.aff_count,
       COALESCE(rebated.rebated_invitee_count, 0),
       (ua.aff_quota + COALESCE(matured.matured_frozen_quota, 0))::double precision,
       ua.aff_history_quota::double precision
FROM user_affiliates ua
JOIN users u ON u.id = ua.user_id
LEFT JOIN (
    SELECT user_id, COUNT(DISTINCT source_user_id)::integer AS rebated_invitee_count
    FROM user_affiliate_ledger
    WHERE action = 'accrue' AND source_user_id IS NOT NULL
    GROUP BY user_id
) rebated ON rebated.user_id = ua.user_id
LEFT JOIN (
    SELECT user_id, COALESCE(SUM(amount), 0)::double precision AS matured_frozen_quota
    FROM user_affiliate_ledger
    WHERE action = 'accrue' AND frozen_until IS NOT NULL AND frozen_until <= NOW()
    GROUP BY user_id
) matured ON matured.user_id = ua.user_id
WHERE ua.user_id = $1
LIMIT 1`
)

// affiliateCodeCharset supplies the generated invitation alphabet, omitting
// visually ambiguous letters and digits while administrator code validation
// accepts a wider set.
var affiliateCodeCharset = []byte("ABCDEFGHJKLMNPQRSTUVWXYZ23456789")

// affiliateQueryExecer supports affiliate SQL reads and writes on a selected
// connection.
type affiliateQueryExecer interface {
	// QueryContext executes affiliate SQL and returns caller-owned rows on the
	// selected connection.
	//
	// The caller scans and closes them; a transaction-backed implementation
	// keeps its transaction completion with the outer owner.
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	// ExecContext executes affiliate SQL writes and returns the statement result
	// through the selected connection.
	//
	// Callers supply bound values and keep related statements in one transaction
	// where consistency is required.
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// affiliateRepository persists affiliate profiles, quota changes, and their
// ledger receipts.
type affiliateRepository struct {
	// client is the fallback for affiliate SQL operations. Context-carried
	// transactions take precedence; the caller owns the connection's lifetime
	// and any joined transaction completion.
	client *dbent.Client // default affiliate persistence client
	// balances binds billing credit to the transfer's Ent transaction. A missing
	// factory makes transfer fail before credit, allowing the owning transaction
	// to roll back claimed quota. A supplied factory returns a usable writer on
	// that same transaction.
	// same-transaction transfer writer factory
	balances TransferBalanceFactory
}

// affiliateTransferSnapshot holds the fund values stored after one quota
// transfer.
type affiliateTransferSnapshot struct {
	// BalanceAfter, AvailableQuotaAfter, FrozenQuotaAfter, and HistoryQuotaAfter
	// are read after billing credit in the same transfer transaction and written
	// together into that transfer's ledger receipt.
	BalanceAfter        float64 // stored account balance amount
	AvailableQuotaAfter float64 // stored available affiliate quota
	FrozenQuotaAfter    float64 // stored frozen affiliate quota
	HistoryQuotaAfter   float64 // stored cumulative affiliate grants
}

// NewAffiliateRepository retains client and the same-transaction billing
// factory for affiliate persistence.
//
// Dependencies remain caller owned; context-carried Ent transactions take
// precedence for repository reads and mutations.
func NewAffiliateRepository(client *dbent.Client, balances TransferBalanceFactory) promotion.AffiliateRepository {
	return &affiliateRepository{client: client, balances: balances}
}

// EnsureUserAffiliate returns or attempts creation of the requested affiliate
// profile through the selected client.
//
// Nonpositive IDs return ErrUserNotFound; a context-carried transaction
// controls participation and completion.
func (r *affiliateRepository) EnsureUserAffiliate(ctx context.Context, userID int64) (*promotion.AffiliateSummary, error) {
	if userID <= 0 {
		return nil, identity.ErrUserNotFound
	}
	client := clientFromContext(ctx, r.client)
	return ensureUserAffiliateWithClient(ctx, client, userID)
}

// GetAffiliateByCode returns the profile owning a trimmed uppercase
// invitation code through the selected client.
//
// Absent codes return ErrAffiliateProfileNotFound, and a context-carried Ent
// transaction takes precedence.
func (r *affiliateRepository) GetAffiliateByCode(ctx context.Context, code string) (*promotion.AffiliateSummary, error) {
	client := clientFromContext(ctx, r.client)
	return queryAffiliateByCode(ctx, client, code)
}

// BindInviter conditionally assigns an inviter and records its invitation
// count in one transaction.
//
// It ensures both profiles, then changes only an invitee whose inviter is
// still NULL. Zero affected rows return false before changing the count, so
// competing or repeated bindings cannot increment it again. A successful
// claim increments the inviter before transaction completion. Callers
// validate self-invitation and acceptable identities before invoking this
// persistence operation.
func (r *affiliateRepository) BindInviter(ctx context.Context, userID, inviterID int64) (bool, error) {
	var bound bool
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, userID); err != nil {
			return err
		}
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, inviterID); err != nil {
			return err
		}

		// Claim only an unset inviter before incrementing its count; a lost or
		// repeated claim exits before any count write.
		res, err := txClient.ExecContext(txCtx,
			"UPDATE user_affiliates SET inviter_id = $1, updated_at = NOW() WHERE user_id = $2 AND inviter_id IS NULL",
			inviterID, userID,
		)
		if err != nil {
			return fmt.Errorf("bind inviter: %w", err)
		}
		// Production pq UPDATE results provide an affected count with a nil error.
		// Zero matches skip the inviter count because no unset relationship was
		// claimed by this statement.
		affected, _ := res.RowsAffected()
		if affected == 0 {
			bound = false
			return nil
		}

		if _, err = txClient.ExecContext(txCtx,
			"UPDATE user_affiliates SET aff_count = aff_count + 1, updated_at = NOW() WHERE user_id = $1",
			inviterID,
		); err != nil {
			return fmt.Errorf("increment inviter aff_count: %w", err)
		}
		bound = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return bound, nil
}

// AccrueQuota adds a positive grant to inviter totals and records its source
// in the ledger.
//
// Positive freezeHours selects frozen quota and a database deadline; other
// values select available quota. Either path increases lifetime history
// before ledger insertion in the same transaction. A missing target or
// nonpositive amount returns false. Callers supply finite amounts, serialize
// any cap decision, and coordinate duplicate orders because source_order_id
// has no uniqueness claim here.
func (r *affiliateRepository) AccrueQuota(ctx context.Context, inviterID, inviteeUserID int64, amount float64, freezeHours int, sourceOrderID *int64) (bool, error) {
	if amount <= 0 {
		return false, nil
	}

	var applied bool
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		// Choose frozen or available quota before ledger insertion, and increase
		// history in either path so receipts and profile totals share the
		// transaction.
		var updateSQL string
		if freezeHours > 0 {
			updateSQL = "UPDATE user_affiliates SET aff_frozen_quota = aff_frozen_quota + $1, aff_history_quota = aff_history_quota + $1, updated_at = NOW() WHERE user_id = $2"
		} else {
			updateSQL = "UPDATE user_affiliates SET aff_quota = aff_quota + $1, aff_history_quota = aff_history_quota + $1, updated_at = NOW() WHERE user_id = $2"
		}
		res, err := txClient.ExecContext(txCtx, updateSQL, amount, inviterID)
		if err != nil {
			return err
		}
		// For production pq UPDATE results, RowsAffected supplies a nil
		// error; zero matches skip ledger insertion because no affiliate
		// profile received the grant.
		affected, _ := res.RowsAffected()
		if affected == 0 {
			applied = false
			return nil
		}

		// Record the grant's source and freeze deadline after the profile update; a
		// ledger failure returns before the owning transaction commits.
		if freezeHours > 0 {
			if _, err = txClient.ExecContext(txCtx, `
INSERT INTO user_affiliate_ledger (user_id, action, amount, source_user_id, source_order_id, frozen_until, created_at, updated_at)
VALUES ($1, 'accrue', $2, $3, $4, NOW() + make_interval(hours => $5), NOW(), NOW())`,
				inviterID, amount, inviteeUserID, nullableInt64Arg(sourceOrderID), freezeHours); err != nil {
				return fmt.Errorf("insert affiliate accrue ledger: %w", err)
			}
		} else {
			if _, err = txClient.ExecContext(txCtx, `
INSERT INTO user_affiliate_ledger (user_id, action, amount, source_user_id, source_order_id, created_at, updated_at)
VALUES ($1, 'accrue', $2, $3, $4, NOW(), NOW())`, inviterID, amount, inviteeUserID, nullableInt64Arg(sourceOrderID)); err != nil {
				return fmt.Errorf("insert affiliate accrue ledger: %w", err)
			}
		}

		applied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

// GetAccruedRebateFromInvitee returns summed accrual ledger amounts for one
// inviter and invitee pair.
//
// It uses the selected client; cap callers keep this read inside
// WithLockedInviter's transaction. Query, scan, and explicit close failures
// propagate, but a failed first Next can leave a zero result because Rows.Err
// is not checked separately.
func (r *affiliateRepository) GetAccruedRebateFromInvitee(ctx context.Context, inviterID, inviteeUserID int64) (float64, error) {
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx,
		`SELECT COALESCE(SUM(amount), 0)::double precision FROM user_affiliate_ledger WHERE user_id = $1 AND source_user_id = $2 AND action = 'accrue'`,
		inviterID, inviteeUserID)
	if err != nil {
		return 0, fmt.Errorf("query accrued rebate from invitee: %w", err)
	}
	// The cap callback reuses this transaction connection for accrual, so finish
	// these sum rows before the next persistent quota statement.
	defer func() { _ = rows.Close() }()
	var total float64
	if rows.Next() {
		if err := rows.Scan(&total); err != nil {
			return 0, err
		}
	}
	return total, rows.Close()
}

// ThawFrozenQuota returns the due quota persistently moved by
// thawFrozenQuotaTx in a transaction.
//
// It joins a context-carried transaction when present; the owning caller then
// controls when that movement becomes committed.
func (r *affiliateRepository) ThawFrozenQuota(ctx context.Context, userID int64) (float64, error) {
	var thawed float64
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		var err error
		thawed, err = thawFrozenQuotaTx(txCtx, txClient, userID)
		return err
	})
	return thawed, err
}

// thawFrozenQuotaTx moves due ledger amounts into available quota on the
// supplied transaction client.
//
// The first statement clears frozen_until and sums the rows it changed, so
// another committed thaw skips those cleared deadlines.
// Positive sums then increase available quota and reduce frozen quota with a
// zero floor; lifetime history remains unchanged. Callers keep both
// statements in one transaction and own rollback when either fails. A failed
// first Next can produce zero because Rows.Err is not checked separately,
// while query, scan, and explicit close failures propagate.
func thawFrozenQuotaTx(txCtx context.Context, txClient *dbent.Client, userID int64) (float64, error) {
	// Clear due deadlines and obtain only the amounts changed by this statement,
	// giving repeated thaw calls distinct claims in the transaction.
	rows, err := txClient.QueryContext(txCtx, `
WITH matured AS (
    UPDATE user_affiliate_ledger
    SET frozen_until = NULL, updated_at = NOW()
    WHERE user_id = $1
      AND frozen_until IS NOT NULL
      AND frozen_until <= NOW()
    RETURNING amount
)
SELECT COALESCE(SUM(amount), 0) FROM matured`, userID)
	if err != nil {
		return 0, fmt.Errorf("thaw frozen quota: %w", err)
	}
	// Finish the deadline-update result even when scanning exits early;
	// deferred close errors are discarded while the explicit pre-write close
	// remains checked.
	defer func() { _ = rows.Close() }()

	var thawed float64
	if rows.Next() {
		if err := rows.Scan(&thawed); err != nil {
			return 0, err
		}
	}
	// Finish the deadline-update result before the profile write reuses the
	// transaction connection, returning close errors before moving quota totals.
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if thawed <= 0 {
		return 0, nil
	}

	// Move the claimed sum into available quota and reduce frozen funds in the
	// same transaction; keep lifetime history as accumulated grants.
	_, err = txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET aff_quota = aff_quota + $1,
    aff_frozen_quota = GREATEST(aff_frozen_quota - $1, 0),
    updated_at = NOW()
WHERE user_id = $2`, thawed, userID)
	if err != nil {
		return 0, fmt.Errorf("move thawed quota: %w", err)
	}
	return thawed, nil
}

// TransferQuotaToBalance moves the user's entire positive available quota
// into balance and records its post-transfer snapshots.
//
// It ensures the profile, persists due thaw, and row-locks a claim that
// clears available quota before billing credit. Empty claims return
// ErrAffiliateQuotaEmpty. The balance factory receives the same Ent
// transaction, so credit, balance readback, snapshot readback, and ledger
// insertion participate with the claimed quota. Callback errors skip an owned
// commit attempt; an outer transaction caller owns completion of joined
// writes.
func (r *affiliateRepository) TransferQuotaToBalance(ctx context.Context, userID int64) (float64, float64, error) {
	var transferred float64
	var newBalance float64

	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, userID); err != nil {
			return err
		}

		// Persist due thaw before claiming all available quota so a single transfer
		// includes matured grants and keeps its later claim consistent.
		if _, err := thawFrozenQuotaTx(txCtx, txClient, userID); err != nil {
			return fmt.Errorf("thaw before transfer: %w", err)
		}

		// Lock and clear the positive available amount before billing credit,
		// making this transaction the sole claimant of those transferable funds.
		rows, err := txClient.QueryContext(txCtx, `
WITH claimed AS (
	SELECT aff_quota::double precision AS amount
	FROM user_affiliates
	WHERE user_id = $1
	  AND aff_quota > 0
	FOR UPDATE
),
cleared AS (
	UPDATE user_affiliates ua
	SET aff_quota = 0,
	    updated_at = NOW()
	FROM claimed c
	WHERE ua.user_id = $1
	RETURNING c.amount
)
SELECT amount
FROM cleared`, userID)
		if err != nil {
			return fmt.Errorf("claim affiliate quota: %w", err)
		}

		if !rows.Next() {
			// Close an empty claim result before returning the quota-empty error; the
			// transaction owner handles cleanup of any earlier thaw writes.
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			return promotion.ErrAffiliateQuotaEmpty
		}
		if err := rows.Scan(&transferred); err != nil {
			// A failed scan cannot establish the claimed amount; close its result and
			// return before invoking the billing participation factory.
			_ = rows.Close()
			return err
		}
		// Complete claim-row cleanup before billing and snapshot queries reuse the
		// transaction connection, retaining the profile lock until transaction
		// completion.
		if err := rows.Close(); err != nil {
			return err
		}
		if transferred <= 0 {
			return promotion.ErrAffiliateQuotaEmpty
		}

		if r.balances == nil {
			return fmt.Errorf("affiliate transfer balance participant is unavailable")
		}
		tx := dbent.TxFromContext(txCtx)
		if tx == nil {
			return fmt.Errorf("affiliate transfer transaction is unavailable")
		}
		// Bind credit to this claimed-quota transaction so a later readback or
		// ledger failure can roll back both account and affiliate changes.
		if err := r.balances(tx).CreditAffiliateTransfer(txCtx, userID, transferred); err != nil {
			return fmt.Errorf("credit user balance by affiliate quota: %w", err)
		}

		newBalance, err = queryUserBalance(txCtx, txClient, userID)
		if err != nil {
			return err
		}

		snapshot, err := queryAffiliateTransferSnapshot(txCtx, txClient, userID)
		if err != nil {
			return err
		}

		// Store the post-credit account and quota snapshots in the same transfer
		// transaction, keeping later audit displays independent of current
		// balances.
		if _, err = txClient.ExecContext(txCtx, `
INSERT INTO user_affiliate_ledger (
    user_id,
    action,
    amount,
    source_user_id,
    balance_after,
    aff_quota_after,
    aff_frozen_quota_after,
    aff_history_quota_after,
    created_at,
    updated_at
)
VALUES ($1, 'transfer', $2, NULL, $3, $4, $5, $6, NOW(), NOW())`,
			userID,
			transferred,
			snapshot.BalanceAfter,
			snapshot.AvailableQuotaAfter,
			snapshot.FrozenQuotaAfter,
			snapshot.HistoryQuotaAfter,
		); err != nil {
			return fmt.Errorf("insert affiliate transfer ledger: %w", err)
		}

		return nil
	})
	if err != nil {
		return 0, 0, err
	}

	return transferred, newBalance, nil
}

// ListInvitees returns recent invitee profiles with unmasked account details
// and cumulative pair grants.
//
// Nonpositive limits select one hundred; ordering uses profile creation
// descending, and rows use the selected transaction or fallback client.
func (r *affiliateRepository) ListInvitees(ctx context.Context, inviterID int64, limit int) ([]promotion.AffiliateInvitee, error) {
	if limit <= 0 {
		limit = 100
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
SELECT ua.user_id,
       COALESCE(u.email, ''),
       COALESCE(u.username, ''),
       ua.created_at,
       COALESCE(SUM(ual.amount), 0)::double precision AS total_rebate
FROM user_affiliates ua
LEFT JOIN users u ON u.id = ua.user_id
LEFT JOIN user_affiliate_ledger ual
       ON ual.user_id = $1
      AND ual.source_user_id = ua.user_id
      AND ual.action = 'accrue'
WHERE ua.inviter_id = $1
GROUP BY ua.user_id, u.email, u.username, ua.created_at
ORDER BY ua.created_at DESC
LIMIT $2`, inviterID, limit)
	if err != nil {
		return nil, err
	}
	// Close the invitee result on normal completion or scan failure so its
	// connection can be reused; deferred close errors are discarded.
	defer func() { _ = rows.Close() }()

	invitees := make([]promotion.AffiliateInvitee, 0)
	for rows.Next() {
		var item promotion.AffiliateInvitee
		var createdAt time.Time
		if err := rows.Scan(&item.UserID, &item.Email, &item.Username, &createdAt, &item.TotalRebate); err != nil {
			return nil, err
		}
		item.CreatedAt = &createdAt
		invitees = append(invitees, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return invitees, nil
}

// ListAffiliateInviteRecords returns filtered invitation relationships, their
// grant totals, and the matching row count.
//
// Callers normalize pagination. Records use the inviter's current code and
// invitee profile creation time; count and page are separate queries.
func (r *affiliateRepository) ListAffiliateInviteRecords(ctx context.Context, filter promotion.AffiliateRecordFilter) ([]promotion.AffiliateInviteRecord, int64, error) {
	client := clientFromContext(ctx, r.client)
	where, args := buildAffiliateRecordWhere(filter, "ua.created_at", []string{
		"inviter.email", "inviter.username", "invitee.email", "invitee.username",
		"ua.inviter_id::text", "ua.user_id::text", "inviter_aff.aff_code",
	})

	total, err := queryAffiliateRecordCount(ctx, client, `
SELECT COUNT(*)
FROM user_affiliates ua
JOIN users invitee ON invitee.id = ua.user_id
JOIN users inviter ON inviter.id = ua.inviter_id
JOIN user_affiliates inviter_aff ON inviter_aff.user_id = ua.inviter_id
`+where, args...)
	if err != nil {
		return nil, 0, err
	}

	orderBy := buildAffiliateRecordOrderBy(filter, map[string]string{
		"inviter":      "inviter.email",
		"invitee":      "invitee.email",
		"aff_code":     "inviter_aff.aff_code",
		"total_rebate": "total_rebate",
		"created_at":   "ua.created_at",
	}, "ua.created_at")
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := client.QueryContext(ctx, `
SELECT ua.inviter_id,
       COALESCE(inviter.email, ''),
       COALESCE(inviter.username, ''),
       ua.user_id,
       COALESCE(invitee.email, ''),
       COALESCE(invitee.username, ''),
       COALESCE(inviter_aff.aff_code, ''),
       COALESCE(SUM(ual.amount), 0)::double precision AS total_rebate,
       ua.created_at
FROM user_affiliates ua
JOIN users invitee ON invitee.id = ua.user_id
JOIN users inviter ON inviter.id = ua.inviter_id
JOIN user_affiliates inviter_aff ON inviter_aff.user_id = ua.inviter_id
LEFT JOIN user_affiliate_ledger ual
       ON ual.user_id = ua.inviter_id
      AND ual.source_user_id = ua.user_id
      AND ual.action = 'accrue'
`+where+`
GROUP BY ua.inviter_id, inviter.email, inviter.username, ua.user_id, invitee.email, invitee.username, inviter_aff.aff_code, ua.created_at
`+orderBy+`
LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	// Finish the relationship page's rows after decoding or an early failure;
	// scan and iteration errors are returned while deferred close errors are
	// discarded.
	defer func() { _ = rows.Close() }()

	items := make([]promotion.AffiliateInviteRecord, 0)
	for rows.Next() {
		var item promotion.AffiliateInviteRecord
		if err := rows.Scan(
			&item.InviterID,
			&item.InviterEmail,
			&item.InviterUsername,
			&item.InviteeID,
			&item.InviteeEmail,
			&item.InviteeUsername,
			&item.AffCode,
			&item.TotalRebate,
			&item.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ListAffiliateRebateRecords returns filtered accrual ledger entries that
// still join to their source order and both accounts.
//
// Ledger amount supplies the recorded grant. Subscription list-price currency
// prefers the saved plan snapshot, then current plan, then USD; balance
// orders leave that currency empty. Charged currency comes independently from
// the provider snapshot with CNY as default. Callers normalize pagination,
// and count and page are separate reads over the same criteria.
func (r *affiliateRepository) ListAffiliateRebateRecords(ctx context.Context, filter promotion.AffiliateRecordFilter) ([]promotion.AffiliateRebateRecord, int64, error) {
	client := clientFromContext(ctx, r.client)
	where, args := buildAffiliateRecordWhere(filter, "ual.created_at", []string{
		"inviter.email", "inviter.username", "invitee.email", "invitee.username",
		"po.id::text", "po.out_trade_no", "po.payment_type", "po.status",
	})
	baseJoin := `
FROM user_affiliate_ledger ual
JOIN payment_orders po ON po.id = ual.source_order_id
LEFT JOIN subscription_plans sp ON sp.id = po.plan_id
JOIN users invitee ON invitee.id = ual.source_user_id
JOIN users inviter ON inviter.id = ual.user_id
WHERE ual.action = 'accrue'
  AND ual.source_order_id IS NOT NULL`
	// The base query already restricts accrual actions, so append optional
	// predicates with AND while retaining their numbered arguments and their
	// original values.
	if where != "" {
		where = strings.Replace(where, "WHERE ", " AND ", 1)
	}

	total, err := queryAffiliateRecordCount(ctx, client, "SELECT COUNT(*) "+baseJoin+where, args...)
	if err != nil {
		return nil, 0, err
	}

	orderBy := buildAffiliateRecordOrderBy(filter, map[string]string{
		"order":         "po.id",
		"inviter":       "inviter.email",
		"invitee":       "invitee.email",
		"order_amount":  "po.amount",
		"pay_amount":    "po.pay_amount",
		"rebate_amount": "ual.amount",
		"payment_type":  "po.payment_type",
		"order_status":  "po.status",
		"created_at":    "ual.created_at",
	}, "ual.created_at")
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	// Snapshot currencies are uppercased before empty strings are skipped;
	// whitespace-only text remains selected. Order fields supply monetary
	// observations independently of the ledger's granted quota.
	rows, err := client.QueryContext(ctx, `
SELECT po.id,
       po.out_trade_no,
       ual.user_id,
       COALESCE(inviter.email, ''),
       COALESCE(inviter.username, ''),
       ual.source_user_id,
       COALESCE(invitee.email, ''),
       COALESCE(invitee.username, ''),
       po.amount::double precision,
       po.pay_amount::double precision,
       COALESCE(NULLIF(po.order_type, ''), 'balance'),
       CASE
           WHEN COALESCE(NULLIF(po.order_type, ''), 'balance') = 'subscription'
           THEN COALESCE(
               NULLIF(UPPER(po.plan_snapshot->>'currency'), ''),
               NULLIF(UPPER(sp.currency), ''),
               'USD'
           )
           ELSE ''
       END,
       COALESCE(NULLIF(UPPER(po.provider_snapshot->>'currency'), ''), 'CNY'),
       ual.amount::double precision,
       po.payment_type,
       po.status,
       ual.created_at
`+baseJoin+where+`
`+orderBy+`
LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	// Finish the source-order page's rows on every return path; scan and
	// iteration failures stay visible while deferred close errors are
	// discarded.
	defer func() { _ = rows.Close() }()

	items := make([]promotion.AffiliateRebateRecord, 0)
	for rows.Next() {
		var item promotion.AffiliateRebateRecord
		if err := rows.Scan(
			&item.OrderID,
			&item.OutTradeNo,
			&item.InviterID,
			&item.InviterEmail,
			&item.InviterUsername,
			&item.InviteeID,
			&item.InviteeEmail,
			&item.InviteeUsername,
			&item.OrderAmount,
			&item.PayAmount,
			&item.OrderType,
			&item.OrderCurrency,
			&item.PayCurrency,
			&item.RebateAmount,
			&item.PaymentType,
			&item.OrderStatus,
			&item.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ListAffiliateTransferRecords returns filtered transfer receipts with their
// saved nullable fund observations and matching total.
//
// Each ledger snapshot converts SQL NULL into nil, retaining a stored zero as
// an available observation. SnapshotAvailable requires all four snapshots, so
// older or partial receipts stay distinguishable from complete ones. Current
// account values do not replace missing ledger data. Callers normalize
// pagination, and the count and page use separate queries.
func (r *affiliateRepository) ListAffiliateTransferRecords(ctx context.Context, filter promotion.AffiliateRecordFilter) ([]promotion.AffiliateTransferRecord, int64, error) {
	client := clientFromContext(ctx, r.client)
	where, args := buildAffiliateRecordWhere(filter, "ual.created_at", []string{
		"u.email", "u.username", "u.id::text",
	})
	baseJoin := `
FROM user_affiliate_ledger ual
JOIN users u ON u.id = ual.user_id
WHERE ual.action = 'transfer'`
	// The base query already restricts transfer actions, so append optional
	// predicates with AND while retaining their numbered arguments and their
	// original values.
	if where != "" {
		where = strings.Replace(where, "WHERE ", " AND ", 1)
	}

	total, err := queryAffiliateRecordCount(ctx, client, "SELECT COUNT(*) "+baseJoin+where, args...)
	if err != nil {
		return nil, 0, err
	}

	orderBy := buildAffiliateRecordOrderBy(filter, map[string]string{
		"user":                  "u.email",
		"amount":                "ual.amount",
		"balance_after":         "ual.balance_after",
		"available_quota_after": "ual.aff_quota_after",
		"frozen_quota_after":    "ual.aff_frozen_quota_after",
		"history_quota_after":   "ual.aff_history_quota_after",
		"created_at":            "ual.created_at",
	}, "ual.created_at")
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := client.QueryContext(ctx, `
SELECT ual.id,
       ual.user_id,
       COALESCE(u.email, ''),
       COALESCE(u.username, ''),
       ual.amount::double precision,
       ual.balance_after::double precision,
       ual.aff_quota_after::double precision,
       ual.aff_frozen_quota_after::double precision,
       ual.aff_history_quota_after::double precision,
       ual.created_at
`+baseJoin+where+`
`+orderBy+`
LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	// Finish the transfer page's result after snapshot decoding or an early
	// failure; deferred close errors are discarded while scan and iteration
	// errors remain returned.
	defer func() { _ = rows.Close() }()

	items := make([]promotion.AffiliateTransferRecord, 0)
	for rows.Next() {
		var item promotion.AffiliateTransferRecord
		var balanceAfter sql.NullFloat64
		var availableQuotaAfter sql.NullFloat64
		var frozenQuotaAfter sql.NullFloat64
		var historyQuotaAfter sql.NullFloat64
		if err := rows.Scan(
			&item.LedgerID,
			&item.UserID,
			&item.UserEmail,
			&item.Username,
			&item.Amount,
			&balanceAfter,
			&availableQuotaAfter,
			&frozenQuotaAfter,
			&historyQuotaAfter,
			&item.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		// Restore each nullable ledger amount as its own pointer; NULL leaves
		// its historical observation absent while a stored zero remains
		// available for audit.
		item.BalanceAfter = nullableFloat64Ptr(balanceAfter)
		item.AvailableQuotaAfter = nullableFloat64Ptr(availableQuotaAfter)
		item.FrozenQuotaAfter = nullableFloat64Ptr(frozenQuotaAfter)
		item.HistoryQuotaAfter = nullableFloat64Ptr(historyQuotaAfter)
		// Report a complete snapshot only when every ledger observation is present,
		// leaving old missing fields distinct from a stored zero.
		item.SnapshotAvailable = balanceAfter.Valid &&
			availableQuotaAfter.Valid &&
			frozenQuotaAfter.Valid &&
			historyQuotaAfter.Valid
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// GetAffiliateUserOverview returns an existing profile's counts, custom-rate
// marker, and computed available quota.
//
// The query retains a separate nonnull marker when coalescing an absent rate
// to zero, so administrator selection can distinguish global policy from an
// explicit disabled override. Available quota includes due frozen accruals
// using the database clock while ledger deadlines remain unchanged.
// Nonpositive IDs or absent joined profiles return ErrUserNotFound; the
// selected client supplies the read's transaction visibility.
func (r *affiliateRepository) GetAffiliateUserOverview(ctx context.Context, userID int64) (*promotion.AffiliateUserOverview, error) {
	if userID <= 0 {
		return nil, identity.ErrUserNotFound
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, affiliateUserOverviewSQL, userID)
	if err != nil {
		return nil, err
	}
	// The overview borrows its selected connection for one row; close that
	// result on every return and discard deferred close errors after choosing
	// the read result.
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, identity.ErrUserNotFound
	}

	var overview promotion.AffiliateUserOverview
	var customRate float64
	var hasCustomRate bool
	if err := rows.Scan(
		&overview.UserID,
		&overview.Email,
		&overview.Username,
		&overview.AffCode,
		&customRate,
		&hasCustomRate,
		&overview.InvitedCount,
		&overview.RebatedInviteeCount,
		&overview.AvailableQuota,
		&overview.HistoryQuota,
	); err != nil {
		return nil, err
	}
	// Keep the SQL presence flag alongside its coalesced number so
	// AdminGetUserOverview selects global policy for absence while retaining
	// a custom zero.
	if hasCustomRate {
		overview.RebateRatePercent = customRate
		overview.RebateRateCustom = true
	}
	return &overview, rows.Err()
}

// buildAffiliateRecordWhere returns inclusive time and optional search
// predicates paired with bound arguments.
//
// Placeholder numbers follow append order, so omitting StartAt keeps EndAt at
// the first parameter rather than shifting its value onto another condition.
// Search columns reuse one appended text argument and retain LIKE wildcard
// meaning. Callers supply trusted SQL column expressions; with no applicable
// condition, the returned SQL fragment and argument slice are empty.
func buildAffiliateRecordWhere(filter promotion.AffiliateRecordFilter, timeColumn string, searchColumns []string) (string, []any) {
	// Keep placeholder numbers aligned with argument append order across
	// optional dates and the shared search so each value stays attached to its
	// intended predicate.
	clauses := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if filter.StartAt != nil {
		args = append(args, *filter.StartAt)
		clauses = append(clauses, fmt.Sprintf("%s >= $%d", timeColumn, len(args)))
	}
	if filter.EndAt != nil {
		args = append(args, *filter.EndAt)
		clauses = append(clauses, fmt.Sprintf("%s <= $%d", timeColumn, len(args)))
	}
	search := strings.TrimSpace(filter.Search)
	if search != "" && len(searchColumns) > 0 {
		args = append(args, "%"+strings.ToLower(search)+"%")
		parts := make([]string, 0, len(searchColumns))
		for _, col := range searchColumns {
			parts = append(parts, fmt.Sprintf("LOWER(%s) LIKE $%d", col, len(args)))
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// buildAffiliateRecordOrderBy returns ordering from the query's supplied
// sort-column map and direction flag.
//
// Unsupported keys select fallbackColumn, nulls sort last, and callers
// provide trusted SQL expressions.
func buildAffiliateRecordOrderBy(filter promotion.AffiliateRecordFilter, sortColumns map[string]string, fallbackColumn string) string {
	column := sortColumns[filter.SortBy]
	if column == "" {
		// Unsupported administrator sort keys use the query's trusted fallback
		// column, preventing raw sort text from becoming a SQL expression.
		column = fallbackColumn
	}
	direction := "DESC"
	if !filter.SortDesc {
		direction = "ASC"
	}
	return "ORDER BY " + column + " " + direction + " NULLS LAST"
}

// queryAffiliateRecordCount reads the first int64 result of a query through
// the selected connection.
//
// No row returns zero with any iteration error; the helper owns and closes
// result rows.
func queryAffiliateRecordCount(ctx context.Context, client affiliateQueryExecer, query string, args ...any) (int64, error) {
	rows, err := client.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	// Release the count query's rows after its first value or an early error;
	// deferred close failures are discarded and leave the selected count
	// result unchanged.
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return 0, rows.Err()
	}
	var total int64
	if err := rows.Scan(&total); err != nil {
		return 0, err
	}
	return total, rows.Err()
}

// withTx runs fn on a context-carried transaction or creates and completes an
// owned transaction.
//
// A joined callback receives the existing context and client, leaving commit,
// rollback, and publication with its outer owner. Otherwise a new transaction
// context accompanies the callback, and commit follows successful completion.
// Deferred rollback cleanup covers early exits and discards its own result.
// Callbacks keep all dependent reads and writes on the supplied context and
// client.
func (r *affiliateRepository) withTx(ctx context.Context, fn func(txCtx context.Context, txClient *dbent.Client) error) error {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		// Join the outer transaction directly so nested affiliate operations share
		// its connection and retain commit, rollback, and publication with that
		// owner.
		return fn(ctx, tx.Client())
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin affiliate transaction: %w", err)
	}
	// Attempt rollback after callback or commit failure, returning the original
	// operation error while cleanup's own result is intentionally discarded.
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := fn(txCtx, tx.Client()); err != nil {
		return err
	}

	// Commit only after the callback finishes all related writes; joined
	// transactions take the earlier path and remain the outer owner's
	// responsibility.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit affiliate transaction: %w", err)
	}
	return nil
}

// ensureUserAffiliateWithClient returns the profile for userID, creating it
// when the initial lookup reports absence.
//
// Generated-code collisions trigger bounded attempts, while a concurrent
// creation for the same user is accepted through conflict-ignore on user_id.
// A final user lookup retrieves the actual winning profile, keeping the
// generated candidate from being mistaken for a saved code. The supplied
// client owns any enclosing transaction; uniqueness retry attempts do not
// create savepoints or restart it.
func ensureUserAffiliateWithClient(ctx context.Context, client affiliateQueryExecer, userID int64) (*promotion.AffiliateSummary, error) {
	summary, err := queryAffiliateByUserID(ctx, client, userID)
	if err == nil {
		return summary, nil
	}
	if !errors.Is(err, promotion.ErrAffiliateProfileNotFound) {
		return nil, err
	}

	// Attempt another generated code on a uniqueness collision; the supplied
	// transaction is reused, so this loop does not recover an aborted
	// transaction.
	for range affiliateCodeMaxAttempts {
		code, codeErr := generateAffiliateCode()
		if codeErr != nil {
			return nil, codeErr
		}
		_, insertErr := client.ExecContext(ctx, `
INSERT INTO user_affiliates (user_id, aff_code, created_at, updated_at)
VALUES ($1, $2, NOW(), NOW())
ON CONFLICT (user_id) DO NOTHING`, userID, code)
		if insertErr == nil {
			break
		}
		if isAffiliateUniqueViolation(insertErr) {
			continue
		}
		return nil, insertErr
	}

	// Reread by user identity after insertion or conflict-ignore so a concurrent
	// creator's saved code becomes the returned profile value.
	return queryAffiliateByUserID(ctx, client, userID)
}

// queryAffiliateByUserID returns the saved profile for userID or
// ErrAffiliateProfileNotFound.
//
// Nullable inviter and percentage values are scanned separately so NULL
// remains distinct from an explicit numeric zero. A saved zero percentage is
// therefore retained as a custom override, while an absent percentage can
// later select global policy. Numeric quota columns are read as float64, and
// the helper closes its rows before returning the profile container.
func queryAffiliateByUserID(ctx context.Context, client affiliateQueryExecer, userID int64) (*promotion.AffiliateSummary, error) {
	rows, err := client.QueryContext(ctx, `
SELECT user_id,
       aff_code,
       aff_code_custom,
       aff_rebate_rate_percent,
       inviter_id,
       aff_count,
       aff_quota::double precision,
       aff_frozen_quota::double precision,
       aff_history_quota::double precision,
       created_at,
       updated_at
FROM user_affiliates
WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	// Close the profile result before later repository statements reuse the
	// connection; deferred close errors are discarded after the lookup has
	// selected its result.
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, promotion.ErrAffiliateProfileNotFound
	}

	var out promotion.AffiliateSummary
	var inviterID sql.NullInt64
	var rebateRate sql.NullFloat64
	if err := rows.Scan(
		&out.UserID,
		&out.AffCode,
		&out.AffCodeCustom,
		&rebateRate,
		&inviterID,
		&out.AffCount,
		&out.AffQuota,
		&out.AffFrozenQuota,
		&out.AffHistoryQuota,
		&out.CreatedAt,
		&out.UpdatedAt,
	); err != nil {
		return nil, err
	}
	// Rebuild inviter and rate pointers from their SQL validity flags so an
	// absent relationship stays claimable and a stored zero remains a custom
	// override.
	if inviterID.Valid {
		out.InviterID = &inviterID.Int64
	}
	if rebateRate.Valid {
		v := rebateRate.Float64
		out.AffRebateRatePercent = &v
	}
	return &out, nil
}

// queryAffiliateByCode returns the saved owner of a trimmed uppercase
// invitation code or ErrAffiliateProfileNotFound.
//
// Nullable inviter and rate values retain the distinction between absence and
// an explicit custom zero. This lets binding recognize an unset relationship
// and lets later rate selection preserve a disabled custom percentage. Quota
// values are read as float64, and the helper owns result cleanup before
// returning its new profile container.
func queryAffiliateByCode(ctx context.Context, client affiliateQueryExecer, code string) (*promotion.AffiliateSummary, error) {
	rows, err := client.QueryContext(ctx, `
SELECT user_id,
       aff_code,
       aff_code_custom,
       aff_rebate_rate_percent,
       inviter_id,
       aff_count,
       aff_quota::double precision,
       aff_frozen_quota::double precision,
       aff_history_quota::double precision,
       created_at,
       updated_at
FROM user_affiliates
WHERE aff_code = $1
LIMIT 1`, strings.ToUpper(strings.TrimSpace(code)))
	if err != nil {
		return nil, err
	}
	// Finish the code lookup's result before inviter binding reuses the
	// connection; a deferred close error is discarded after selecting the
	// profile or lookup failure.
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, promotion.ErrAffiliateProfileNotFound
	}

	var out promotion.AffiliateSummary
	var inviterID sql.NullInt64
	var rebateRate sql.NullFloat64
	if err := rows.Scan(
		&out.UserID,
		&out.AffCode,
		&out.AffCodeCustom,
		&rebateRate,
		&inviterID,
		&out.AffCount,
		&out.AffQuota,
		&out.AffFrozenQuota,
		&out.AffHistoryQuota,
		&out.CreatedAt,
		&out.UpdatedAt,
	); err != nil {
		return nil, err
	}
	// Use SQL validity flags to restore inviter and rate pointers; nil
	// permits binding or global policy while a present zero retains its saved
	// meaning.
	if inviterID.Valid {
		out.InviterID = &inviterID.Int64
	}
	if rebateRate.Valid {
		v := rebateRate.Float64
		out.AffRebateRatePercent = &v
	}
	return &out, nil
}

// queryUserBalance returns the user's stored balance through the supplied SQL
// client.
//
// An absent row returns ErrUserNotFound; transfer callers use the credit
// transaction to observe its resulting amount.
func queryUserBalance(ctx context.Context, client affiliateQueryExecer, userID int64) (float64, error) {
	rows, err := client.QueryContext(ctx,
		"SELECT balance::double precision FROM users WHERE id = $1 LIMIT 1",
		userID,
	)
	if err != nil {
		return 0, err
	}
	// Close the credited-balance result before transfer snapshot and ledger
	// work reuse this connection; deferred close errors leave the selected
	// balance result unchanged.
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return 0, identity.ErrUserNotFound
	}
	var balance float64
	if err := rows.Scan(&balance); err != nil {
		return 0, err
	}
	return balance, nil
}

// queryAffiliateTransferSnapshot returns balance and all affiliate totals
// from the joined user/profile row.
//
// Transfer callers read it after credit on the same transaction; an absent
// joined row returns ErrUserNotFound.
func queryAffiliateTransferSnapshot(ctx context.Context, client affiliateQueryExecer, userID int64) (*affiliateTransferSnapshot, error) {
	rows, err := client.QueryContext(ctx, `
SELECT u.balance::double precision,
       ua.aff_quota::double precision,
       ua.aff_frozen_quota::double precision,
       ua.aff_history_quota::double precision
FROM users u
JOIN user_affiliates ua ON ua.user_id = u.id
WHERE u.id = $1
LIMIT 1`, userID)
	if err != nil {
		return nil, fmt.Errorf("query affiliate transfer snapshot: %w", err)
	}
	// Close the snapshot result before transfer ledger insertion reuses the
	// connection; deferred close errors are discarded after selecting the
	// observations or read failure.
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, identity.ErrUserNotFound
	}

	var snapshot affiliateTransferSnapshot
	if err := rows.Scan(
		&snapshot.BalanceAfter,
		&snapshot.AvailableQuotaAfter,
		&snapshot.FrozenQuotaAfter,
		&snapshot.HistoryQuotaAfter,
	); err != nil {
		return nil, err
	}
	return &snapshot, rows.Err()
}

// nullableFloat64Ptr returns a pointer to the value or nil when the SQL
// observation is NULL.
//
// The pointer belongs to this converted value, distinguishing missing
// historical snapshots from stored zero.
func nullableFloat64Ptr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}

// generateAffiliateCode returns a twelve-byte invitation code from the
// generated-code alphabet.
//
// The alphabet excludes I, O, zero, and one. With the repository's Go
// toolchain, rand.Read fills its buffer or terminates the process when
// entropy reading fails.
func generateAffiliateCode() (string, error) {
	buf := make([]byte, affiliateCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate affiliate code: %w", err)
	}
	for i := range buf {
		// The 32-symbol alphabet divides all 256 byte values evenly.
		buf[i] = affiliateCodeCharset[int(buf[i])%len(affiliateCodeCharset)]
	}
	return string(buf), nil
}

// isAffiliateUniqueViolation reports whether err wraps a PostgreSQL
// unique-violation error.
func isAffiliateUniqueViolation(err error) bool {
	if pqErr, ok := errors.AsType[*pq.Error](err); ok {
		return string(pqErr.Code) == "23505"
	}
	return false
}

// UpdateUserAffCode stores a normalized custom invitation code and its
// administrator marker in a transaction.
//
// It requires a positive user ID and nonempty trimmed text, then ensures the
// profile before the replacement write. An existing profile is reused; a
// newly created profile and its custom-code assignment participate together.
// Code uniqueness failures become ErrAffiliateCodeTaken. Joined transactions
// retain completion with the outer owner, and callers validate the wider
// code-format rules before persistence.
func (r *affiliateRepository) UpdateUserAffCode(ctx context.Context, userID int64, newCode string) error {
	if userID <= 0 {
		return identity.ErrUserNotFound
	}
	code := strings.ToUpper(strings.TrimSpace(newCode))
	if code == "" {
		return promotion.ErrAffiliateCodeInvalid
	}

	return r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, userID); err != nil {
			return err
		}
		res, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET aff_code = $1,
    aff_code_custom = true,
    updated_at = NOW()
WHERE user_id = $2`, code, userID)
		if err != nil {
			if isAffiliateUniqueViolation(err) {
				// Expose code uniqueness as the shared business conflict so administrator
				// callers can distinguish a taken code from unrelated SQL failures.
				return promotion.ErrAffiliateCodeTaken
			}
			return fmt.Errorf("update aff_code: %w", err)
		}
		// Production pq UPDATE results provide an affected count with a nil
		// error; a zero count identifies the missing profile before
		// custom-code success is reported.
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return identity.ErrUserNotFound
		}
		return nil
	})
}

// ResetUserAffCode generates and stores a replacement code while clearing its
// custom marker in a transaction.
//
// It ensures the profile first, then retries generated candidates on
// recognized uniqueness errors up to the configured bound. Attempts reuse the
// same transaction without savepoint recovery or restart. Successful updates
// return the saved candidate; missing users, SQL failures, and exhausted
// attempts return errors, while entropy failures follow
// generateAffiliateCode's toolchain behavior.
func (r *affiliateRepository) ResetUserAffCode(ctx context.Context, userID int64) (string, error) {
	if userID <= 0 {
		return "", identity.ErrUserNotFound
	}
	var newCode string
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, userID); err != nil {
			return err
		}
		// Try replacement candidates within the same transaction on recognized
		// collisions; no savepoint clears a PostgreSQL transaction aborted by a
		// uniqueness error.
		for range affiliateCodeMaxAttempts {
			candidate, codeErr := generateAffiliateCode()
			if codeErr != nil {
				return codeErr
			}
			res, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET aff_code = $1,
    aff_code_custom = false,
    updated_at = NOW()
WHERE user_id = $2`, candidate, userID)
			if err != nil {
				if isAffiliateUniqueViolation(err) {
					continue
				}
				return fmt.Errorf("reset aff_code: %w", err)
			}
			// Production pq UPDATE results provide an affected count with a
			// nil error; require a matched profile before returning this
			// candidate as its saved code.
			affected, _ := res.RowsAffected()
			if affected == 0 {
				return identity.ErrUserNotFound
			}
			newCode = candidate
			return nil
		}
		return fmt.Errorf("reset aff_code: exhausted attempts")
	})
	if err != nil {
		return "", err
	}
	return newCode, nil
}

// SetUserRebateRate stores a supplied inviter override after ensuring that
// user's profile exists.
//
// Nil becomes SQL NULL and restores global rate selection, while a supplied
// zero remains a disabled custom percentage. The profile creation and
// override update use one transaction so an owned failure can roll both back
// together. Callers validate finite bounds before persistence and retain
// commit or rollback ownership when the context supplies an outer
// transaction.
func (r *affiliateRepository) SetUserRebateRate(ctx context.Context, userID int64, ratePercent *float64) error {
	if userID <= 0 {
		return identity.ErrUserNotFound
	}
	return r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, userID); err != nil {
			return err
		}
		// Convert nil to a SQL NULL argument so clearing the override remains
		// distinct from a saved zero percentage that disables grants.
		res, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET aff_rebate_rate_percent = $1,
    updated_at = NOW()
WHERE user_id = $2`, nullableArg(ratePercent), userID)
		if err != nil {
			return fmt.Errorf("set aff_rebate_rate_percent: %w", err)
		}
		// Production pq UPDATE results provide an affected count with a nil
		// error; zero reports a missing profile after the override write
		// attempt.
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return identity.ErrUserNotFound
		}
		return nil
	})
}

// BatchSetUserRebateRate stores one override for the submitted identifiers
// after ensuring positive target profiles.
//
// Empty input succeeds immediately, and nil removes overrides. Profile
// creation precedes the common SQL update in one transaction, keeping a
// failed owned batch from committing only its newly ensured profiles. The SQL
// predicate uses the original identifier slice; callers validate percentages
// and normalize IDs, as AdminBatchSetUserRebateRate does before delegation.
func (r *affiliateRepository) BatchSetUserRebateRate(ctx context.Context, userIDs []int64, ratePercent *float64) error {
	if len(userIDs) == 0 {
		return nil
	}
	return r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		// Ensure each positive target profile before the common rate write so a
		// later failure leaves profile creation and updates within the same
		// transaction.
		for _, uid := range userIDs {
			if uid <= 0 {
				continue
			}
			if _, err := ensureUserAffiliateWithClient(txCtx, txClient, uid); err != nil {
				return err
			}
		}
		_, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET aff_rebate_rate_percent = $1,
    updated_at = NOW()
WHERE user_id = ANY($2)`, nullableArg(ratePercent), pq.Array(userIDs))
		if err != nil {
			return fmt.Errorf("batch set aff_rebate_rate_percent: %w", err)
		}
		return nil
	})
}

// nullableArg returns SQL NULL input for nil or the pointed-to percentage
// otherwise.
//
// SetUserRebateRate and batch updates use this distinction to retain a custom
// zero while clearing absent overrides.
func nullableArg(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// nullableInt64Arg returns SQL NULL input for nil or the pointed-to source
// identifier otherwise.
//
// Accrual uses it to distinguish a grant with no reliable source order from
// an associated order.
func nullableInt64Arg(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// ListUsersWithCustomSettings returns accounts selected by a custom code or
// saved rate and their matching count.
//
// A NULL rate stays nil on each result, while a stored zero becomes a nonnil
// pointer so administrators can distinguish cleared policy from a disabled
// override. Code-only accounts remain eligible for the list with no rate.
// Pages start at one; sizes outside one through two hundred select twenty.
// Search uses email or username ILIKE, and count and page are separate queries
// on the selected client.
func (r *affiliateRepository) ListUsersWithCustomSettings(ctx context.Context, filter promotion.AffiliateAdminFilter) ([]promotion.AffiliateAdminEntry, int64, error) {
	page := max(filter.Page, 1)
	pageSize := filter.PageSize
	if pageSize <= 0 || pageSize > 200 {
		// Oversized or nonpositive custom-user pages use the HTTP twenty-row
		// default, keeping this direct repository query bounded for administrator
		// displays.
		pageSize = 20
	}
	offset := (page - 1) * pageSize
	likePattern := "%" + strings.TrimSpace(filter.Search) + "%"

	const baseFrom = `
FROM user_affiliates ua
JOIN users u ON u.id = ua.user_id
WHERE (ua.aff_code_custom = true OR ua.aff_rebate_rate_percent IS NOT NULL)
  AND (u.email ILIKE $1 OR u.username ILIKE $1)`

	client := clientFromContext(ctx, r.client)

	total, err := scanInt64(ctx, client, "SELECT COUNT(*)"+baseFrom, likePattern)
	if err != nil {
		return nil, 0, fmt.Errorf("count affiliate admin entries: %w", err)
	}

	listQuery := `
SELECT ua.user_id,
       COALESCE(u.email, ''),
       COALESCE(u.username, ''),
       ua.aff_code,
       ua.aff_code_custom,
       ua.aff_rebate_rate_percent,
       ua.aff_count` + baseFrom + `
ORDER BY ua.updated_at DESC
LIMIT $2 OFFSET $3`

	rows, err := client.QueryContext(ctx, listQuery, likePattern, pageSize, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list affiliate admin entries: %w", err)
	}
	// Finish the custom-settings page after decoding or early failure so its
	// connection can be reused; scan and iteration failures are returned, and
	// deferred close errors are discarded.
	defer func() { _ = rows.Close() }()

	entries := make([]promotion.AffiliateAdminEntry, 0)
	for rows.Next() {
		var e promotion.AffiliateAdminEntry
		var rebate sql.NullFloat64
		if err := rows.Scan(&e.UserID, &e.Email, &e.Username, &e.AffCode,
			&e.AffCodeCustom, &rebate, &e.AffCount); err != nil {
			return nil, 0, err
		}
		// Retain SQL presence when rebuilding each percentage pointer; a
		// saved zero is still a custom override and a code-only account can
		// keep a nil rate.
		if rebate.Valid {
			v := rebate.Float64
			e.AffRebateRatePercent = &v
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// scanInt64 returns the first int64 query value or zero when no row is
// available.
//
// Query and scan failures are returned; a missing row also reports any
// iteration failure.
func scanInt64(ctx context.Context, client affiliateQueryExecer, query string, args ...any) (int64, error) {
	rows, err := client.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	// Finish this scalar query on every return path before its connection is
	// reused; deferred close errors leave the chosen count or read failure
	// unchanged.
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		// An empty single-value query becomes zero for count consumers after
		// iteration errors are checked, keeping their empty-list result usable.
		return 0, nil
	}
	var v int64
	if err := rows.Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}
