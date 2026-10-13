package identity

import (
	"time"
)

// Initial installation creates an administrator only after the user counts
// identify an empty database. Existing users block this setup path even when
// none has the admin role, so bootstrap cannot create a privileged account in
// an installation that already contains ordinary accounts.
//
// The database adapter obtains the total and administrator counts and passes
// those observations to DecideAdminBootstrap. Its reason distinguishes an
// existing administrator from users lacking one; callers can report why setup
// skipped creation. The decision itself neither locks the database nor creates
// a user, so persistence and concurrent installation handling stay with the
// adapter receiving the result.

// InitialAdminInput contains the setup administrator credentials, concurrency limit, and clock.
type InitialAdminInput struct {
	// Email and Password supply the initial credentials. CreateInitialAdmin uses
	// its password generator when Password is blank after trimming whitespace.
	// Email: administrator login email. Password: supplied plaintext password.
	Email, Password string
	Concurrency     int // initial concurrent request limit
	// Now supplies both stored timestamps through separate reads. The database
	// adapter calls it directly, so setup supplies a non-nil clock.
	Now func() time.Time
}

// DecideAdminBootstrap reports whether the observed user counts permit initial
// administrator creation and returns a reason for the setup response. Callers
// supply the total user count and the count whose role is admin.
//
// An administrator count above zero wins even if the total observation differs.
// Otherwise existing users produce users_exist_without_admin, and an empty
// observation permits creation with empty_database. The returned decision does
// not reserve the empty database; the caller owns the subsequent insert.
func DecideAdminBootstrap(total, admins int64) (bool, string) {
	if admins > 0 {
		return false, "admin_exists"
	}
	if total > 0 {
		return false, "users_exist_without_admin"
	}
	return true, "empty_database"
}
