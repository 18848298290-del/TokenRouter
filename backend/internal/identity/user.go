package identity

import (
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity/contact"
)

const (
	RoleAdmin    = "admin"
	RoleUser     = "user"
	StatusActive = "active"

	DefaultUserAPIKeyLimit              = 100
	MaxUserAPIKeyLimit                  = 2_147_483_647
	LinuxDoConnectSyntheticEmailDomain  = "@linuxdo-connect.invalid"
	OIDCConnectSyntheticEmailDomain     = "@oidc-connect.invalid"
	WeChatConnectSyntheticEmailDomain   = "@wechat-connect.invalid"
	DingTalkConnectSyntheticEmailDomain = "@dingtalk-connect.invalid"
)

// ErrUserNotFound is shared with billing callers.
var ErrUserNotFound = billing.ErrUserNotFound

type User struct {
	Subscriptions []billing.UserSubscription
	ID            int64
	Email         string
	// PreferredLocale is empty when browser or site defaults should select the language.
	PreferredLocale *string
	Username        string
	Notes           string
	AvatarURL       string
	AvatarSource    string
	AvatarMIME      string
	AvatarByteSize  int
	AvatarSHA256    string
	PasswordHash    string
	Role            string
	Balance         float64
	FrozenBalance   float64
	Concurrency     int
	Status          string
	AllowedGroups   []int64
	// DisabledPublicGroups contains public group IDs denied to this user by an administrator.
	DisabledPublicGroups []int64
	// GroupRestrictionsLoaded distinguishes loaded empty grants from missing repository data in auth caches.
	GroupRestrictionsLoaded bool
	TokenVersion            int64 // Credential version used by token validation; see ResolvedTokenVersion.
	// TokenVersionResolved indicates TokenVersion already contains the fingerprint-derived
	// value expected in JWT claims and refresh-token state.
	TokenVersionResolved bool
	SignupSource         string
	LastLoginAt          *time.Time
	LastActiveAt         *time.Time
	LastUsedAt           *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	DeletedAt            *time.Time // Non-nil for a soft-deleted user.

	// GroupRates contains user-specific group price multipliers.
	// map[groupID]rateMultiplier
	GroupRates map[int64]float64

	// TOTP authentication state.
	TotpSecretEncrypted *string    // AES-256-GCM encrypted TOTP secret.
	TotpEnabled         bool       // Whether TOTP is enabled.
	TotpEnabledAt       *time.Time // TOTP activation time.

	// Low-balance notification settings.
	BalanceNotifyEnabled       bool
	BalanceNotifyThresholdType string // "fixed" (default) | "percentage"
	BalanceNotifyThreshold     *float64
	BalanceNotifyExtraEmails   []NotifyEmailEntry
	TotalRecharged             float64

	// RPMLimit is the per-user requests-per-minute limit; zero means unlimited. It applies when
	// the group has no rpm_limit and the user/group pair has no rpm_override, using rpm:u:{userID}:{min}.
	RPMLimit int
	// APIKeyLimit bounds the number of API keys a user may create; zero means unlimited.
	APIKeyLimit int

	// UserGroupRPMOverride contains the auth snapshot's user/group RPM override.
	// Nil means no override for this key's user/group pair; checkRPM uses a non-nil value directly.
	// The field is transient and avoids a database lookup on each request.
	UserGroupRPMOverride *int
}

// NotifyEmailEntry is a verified notification address; user operations own verification.
type NotifyEmailEntry = contact.Entry

// Principal identifies the authenticated caller.
type Principal struct {
	UserID         int64
	Role           string
	SessionID      string
	CredentialKind string
}

func (u *User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

func (u *User) IsActive() bool {
	return u.Status == StatusActive
}

// CanBindGroup reports whether the user may bind the given group.
// Public groups are allowed unless disabled for the user; exclusive groups require an explicit grant.
func (u *User) CanBindGroup(groupID int64, isExclusive bool) bool {
	if u == nil || groupID <= 0 {
		return false
	}
	if !isExclusive {
		// Public groups use the user-specific deny list.
		return !slices.Contains(u.DisabledPublicGroups, groupID)
	}
	// Exclusive groups require membership in AllowedGroups.
	return slices.Contains(u.AllowedGroups, groupID)
}

func (u *User) SetPassword(password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(hash)
	return nil
}

func (u *User) CheckPassword(password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}

// ParseNotifyEmails accepts both legacy string lists and notification-entry lists.
func ParseNotifyEmails(raw string) []NotifyEmailEntry { return contact.ParseNotifyEmails(raw) }

func MarshalNotifyEmails(entries []NotifyEmailEntry) string {
	return contact.MarshalNotifyEmails(entries)
}

func IsReservedEmail(email string) bool {
	normalized := strings.ToLower(strings.TrimSpace(email))
	return strings.HasSuffix(normalized, LinuxDoConnectSyntheticEmailDomain) ||
		strings.HasSuffix(normalized, OIDCConnectSyntheticEmailDomain) ||
		strings.HasSuffix(normalized, WeChatConnectSyntheticEmailDomain) ||
		strings.HasSuffix(normalized, DingTalkConnectSyntheticEmailDomain)
}

// CopyUser returns a shallow copy of the user.
// Slices, maps, and pointers remain shared; cross-request caches perform their own deep copies.
func CopyUser(user *User) *User {
	if user == nil {
		return nil
	}
	copy := *user
	return &copy
}
