package postgres

import (
	"context"
	"database/sql"
	"errors"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// entity_helpers.go selects billing persistence clients, translates database
// errors and maps user records.
// Billing persistence adapters can join an Ent transaction passed in context
// or use their configured client. These shared helpers select that connection,
// translate known missing/unique errors to application errors, and map billing
// user data while keeping undecodable notification contacts empty.
//
// User mapping carries the scalar billing fields and retained optional
// pointers from the Ent entity; group permission slices are populated by other
// readers. Database error mapping keeps the original cause when a supplied
// application error matches, and unrelated failures stay available to the
// caller.

// sqlExecutor provides SQL execution and row queries for billing persistence
// adapters.
type sqlExecutor interface {
	// ExecContext executes caller SQL with supplied arguments and returns its
	// affected-row result. The owning adapter decides whether absence is a domain
	// error and owns transaction lifecycle.
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	// QueryContext executes caller SQL and returns rows for the owning adapter to
	// consume. That caller closes the rows, checks scan/iteration errors and
	// interprets empty results.
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// clientFromContext returns the Ent transaction client from ctx when present,
// otherwise defaultClient. Both connections remain caller-owned; this helper
// performs no transaction creation, commit or rollback and requires a usable
// context.
func clientFromContext(ctx context.Context, defaultClient *dbent.Client) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return defaultClient
}

// translatePersistenceError maps missing-row and uniqueness errors to supplied
// application errors, retaining the database error as the cause. Nil mappings
// and unmatched errors leave the original error unchanged.
func translatePersistenceError(err error, notFound, conflict *apperror.ApplicationError) error {
	if err == nil {
		return nil
	}

	if notFound != nil && (errors.Is(err, sql.ErrNoRows) || dbent.IsNotFound(err)) {
		return notFound.WithCause(err)
	}

	if conflict != nil && postgresinfra.IsUniqueConstraintViolation(err) {
		return conflict.WithCause(err)
	}

	return err
}

// uniquePositiveInt64s returns distinct positive IDs in their first-supplied
// order. Empty input yields nil; a nonempty input containing no usable IDs
// yields an allocated empty slice for the owning query helper.
func uniquePositiveInt64s(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// derefString returns the pointed-to text or an empty string for nil.
// Redemption and subscription conversion use it for optional stored notes,
// making absent notes and an explicitly empty value share the domain string.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// userSummaryFromEntity maps billing user data or returns nil for nil input.
// Optional timestamps and threshold pointers remain shared with the entity;
// notification contacts are parsed from saved text, while group permission
// slices are left for other reader paths.
func userSummaryFromEntity(u *dbent.User) *billing.UserSummary {
	if u == nil {
		return nil
	}
	out := &billing.UserSummary{
		ID:                         u.ID,
		Email:                      u.Email,
		Username:                   u.Username,
		Role:                       u.Role,
		Balance:                    u.Balance,
		FrozenBalance:              u.FrozenBalance,
		Concurrency:                u.Concurrency,
		Status:                     u.Status,
		LastActiveAt:               u.LastActiveAt,
		CreatedAt:                  u.CreatedAt,
		UpdatedAt:                  u.UpdatedAt,
		BalanceNotifyEnabled:       u.BalanceNotifyEnabled,
		BalanceNotifyThresholdType: u.BalanceNotifyThresholdType,
		BalanceNotifyThreshold:     u.BalanceNotifyThreshold,
		TotalRecharged:             u.TotalRecharged,
		APIKeyLimit:                u.APIKeyLimit,
		DeletedAt:                  u.DeletedAt, RPMLimit: u.RpmLimit,
	}
	if u.BalanceNotifyExtraEmails != "" && u.BalanceNotifyExtraEmails != "[]" {
		out.BalanceNotifyExtraEmails = billing.ParseNotifyEmails(u.BalanceNotifyExtraEmails)
	}
	return out
}

// paginationResultFromTotal delegates page metadata construction using the
// supplied count and parameters.
func paginationResultFromTotal(total int64, params pagination.PaginationParams) *pagination.PaginationResult {
	return pagination.ResultFromTotal(total, params)
}

// isUniqueConstraintViolation delegates recognition of PostgreSQL uniqueness
// failures for billing error mapping.
func isUniqueConstraintViolation(err error) bool {
	return postgresinfra.IsUniqueConstraintViolation(err)
}
