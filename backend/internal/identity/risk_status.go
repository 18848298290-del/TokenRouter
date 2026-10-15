package identity

import "context"

// risk_status.go defines the account view and status-write operations used by
// moderation decisions.
// Moderation reads a user's identity, role and current status before deciding
// whether a risk action applies. Status writes select that single repository
// field, so the reduced read view can support moderation decisions without
// supplying a full user update snapshot.

// RiskUser is the account view read by moderation status commands.
type RiskUser struct {
	ID                  int64  // local account identifier
	Role, Status, Email string // global account role, current account status, and stored account email
}

// RiskStatusCommands exposes account reads and selected status writes to moderation.
type RiskStatusCommands struct {
	users UserRepository /* account read and update repository */
}

// NewRiskStatusCommands binds moderation account commands to the supplied repository.
func NewRiskStatusCommands(users UserRepository) *RiskStatusCommands {
	return &RiskStatusCommands{users: users}
}

// Read loads the reduced account view used by moderation decisions.
// A nil repository result stays nil with its accompanying error; when a user
// value is returned, its ID, role, status and email are copied into RiskUser.
func (s *RiskStatusCommands) Read(ctx context.Context, id int64) (*RiskUser, error) {
	u, e := s.users.GetByID(ctx, id)
	if u == nil {
		return nil, e
	}
	return &RiskUser{ID: u.ID, Role: u.Role, Status: u.Status, Email: u.Email}, e
}

// SetStatus writes the supplied status through the account repository.
// It selects only UserUpdateFields.Status, leaving validation and persistence
// errors with the repository and preventing unrelated zero-valued user fields
// from becoming requested updates.
func (s *RiskStatusCommands) SetStatus(ctx context.Context, id int64, status string) error {
	return s.users.Update(ctx, &User{ID: id, Status: status}, UserUpdateFields{Status: true})
}
