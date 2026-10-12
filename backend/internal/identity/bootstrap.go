package identity

import (
	"time"
)

// InitialAdminInput contains the setup administrator credentials, concurrency limit, and clock.
type InitialAdminInput struct {
	Email, Password string
	Concurrency     int
	Now             func() time.Time
}

func DecideAdminBootstrap(total, admins int64) (bool, string) {
	if admins > 0 {
		return false, "admin_exists"
	}
	if total > 0 {
		return false, "users_exist_without_admin"
	}
	return true, "empty_database"
}
