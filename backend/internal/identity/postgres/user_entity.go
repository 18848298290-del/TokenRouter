package postgres

import (
	"context"
	"database/sql"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

type SQLExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}
type sqlQueryer = postgresinfra.Queryer

func UserFromEntity(u *dbent.User) *identitycore.User {
	if u == nil {
		return nil
	}
	out := &identitycore.User{
		PreferredLocale:            u.PreferredLocale,
		ID:                         u.ID,
		Email:                      u.Email,
		Username:                   u.Username,
		Notes:                      u.Notes,
		PasswordHash:               u.PasswordHash,
		Role:                       u.Role,
		Balance:                    u.Balance,
		FrozenBalance:              u.FrozenBalance,
		Concurrency:                u.Concurrency,
		Status:                     u.Status,
		SignupSource:               u.SignupSource,
		LastLoginAt:                u.LastLoginAt,
		LastActiveAt:               u.LastActiveAt,
		TotpSecretEncrypted:        u.TotpSecretEncrypted,
		TotpEnabled:                u.TotpEnabled,
		TotpEnabledAt:              u.TotpEnabledAt,
		BalanceNotifyEnabled:       u.BalanceNotifyEnabled,
		BalanceNotifyThresholdType: u.BalanceNotifyThresholdType,
		BalanceNotifyThreshold:     u.BalanceNotifyThreshold,
		TotalRecharged:             u.TotalRecharged,
		RPMLimit:                   u.RpmLimit,
		APIKeyLimit:                u.APIKeyLimit,
		CreatedAt:                  u.CreatedAt,
		UpdatedAt:                  u.UpdatedAt,
		DeletedAt:                  u.DeletedAt,
	}
	// Accept both legacy string lists and NotifyEmailEntry lists.
	if u.BalanceNotifyExtraEmails != "" && u.BalanceNotifyExtraEmails != "[]" {
		out.BalanceNotifyExtraEmails = identitycore.ParseNotifyEmails(u.BalanceNotifyExtraEmails)
	}
	return out
}
