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

// sqlExecutor supplies the SQL operations used by persistence adapters.
type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// clientFromContext returns the transaction client from ent.NewTxContext,
// or defaultClient when the context has no transaction.
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

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// userSummaryFromEntity extracts billing data from a user entity, returning nil for nil input.
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

func paginationResultFromTotal(total int64, params pagination.PaginationParams) *pagination.PaginationResult {
	return pagination.ResultFromTotal(total, params)
}

// isUniqueConstraintViolation reports whether a database error is a PostgreSQL uniqueness violation.
func isUniqueConstraintViolation(err error) bool {
	return postgresinfra.IsUniqueConstraintViolation(err)
}
