package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// bootstrap_admin.go checks stored user counts and creates the initial
// administrator when setup permits it.
// Initial setup creates an administrator only when the observed user database
// is empty. Separate SQL counts identify existing administrators or ordinary
// accounts, and DecideAdminBootstrap supplies the skip reason before any
// password generation or insert is attempted.
//
// The counts include every stored user row and are read before the insert
// outside an owned transaction. That observation does not reserve an empty
// database for concurrent setup callers. A blank supplied password invokes the
// generator; setup supplies a clock for both timestamps, and the insert saves
// the hashed credential with the requested concurrency allowance.

// CreateInitialAdmin reads user counts and inserts an initial active
// administrator when the bootstrap decision permits it. The boolean reports
// insertion, and the reason identifies creation or a skipped database state.
//
// Supply a nonnil clock and a password generator when the input password is
// blank after trimming. Count reads and the insert use the supplied pool
// separately, so this helper does not serialize concurrent initial setup.
// Password-generation, hashing, and SQL failures stop the result and remain
// errors for the setup caller.
func CreateInitialAdmin(ctx context.Context, db *sql.DB, input identity.InitialAdminInput, password func() (string, error)) (bool, string, error) {
	var total, admins int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(1) FROM users").Scan(&total); err != nil {
		return false, "", err
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(1) FROM users WHERE role = $1", identity.RoleAdmin).Scan(&admins); err != nil {
		return false, "", err
	}
	create, reason := identity.DecideAdminBootstrap(total, admins)
	if !create {
		return false, reason, nil
	}
	if strings.TrimSpace(input.Password) == "" {
		value, err := password()
		if err != nil {
			return false, "", fmt.Errorf("failed to generate admin password: %w", err)
		}
		input.Password = value
	}
	user := &identity.User{Email: input.Email, Role: identity.RoleAdmin, Status: identity.StatusActive, Balance: 0, Concurrency: input.Concurrency, CreatedAt: input.Now(), UpdatedAt: input.Now()}
	if err := user.SetPassword(input.Password); err != nil {
		return false, "", err
	}
	_, err := db.ExecContext(ctx, `INSERT INTO users (email,password_hash,role,balance,concurrency,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, user.Email, user.PasswordHash, user.Role, user.Balance, user.Concurrency, user.Status, user.CreatedAt, user.UpdatedAt)
	if err != nil {
		return false, "", err
	}
	return true, reason, nil
}
