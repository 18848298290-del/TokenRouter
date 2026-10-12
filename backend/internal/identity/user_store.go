package identity

import (
	"context"
	_ "image/png"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// UserListFilters contains user search and relation-loading options.
type UserListFilters struct {
	Status    string // Account status
	Role      string // Account role
	Search    string // Search text for email, username, notes, or API Key
	GroupName string // Partial match on an authorized group name
	// APIKeyGroupID selects users with an undeleted API Key bound to this group.
	// Zero disables the filter. It matches api_keys.group_id independently of allowed_groups.
	APIKeyGroupID int64
	Attributes    map[int64]string // Custom attribute ID to filter value
	// IncludeSubscriptions controls whether ListWithFilters should load active subscriptions.
	// For large datasets this can be expensive; admin list pages should enable it on demand.
	// nil means not specified (default: load subscriptions for backward compatibility).
	IncludeSubscriptions *bool
	// IncludeDeleted includes users whose deleted_at is set.
	// Use it for the admin usage SearchUsers endpoint; other lists should exclude deleted users.
	IncludeDeleted bool
}

// UserUpdateFields selects the columns and relations UserRepository.Update may write.
//
// Callers must select each field they intend to change. Unselected fields retain their
// database values, protecting concurrent balance, activity, limit, role, and status updates
// from a stale read-modify-write snapshot.
//
// Balance and total_recharged are excluded. Use atomic balance operations such as
// AdjustBalance, SetBalance, UpdateBalance, or DeductBalance to change funds.
type UserUpdateFields struct {
	PreferredLocale bool
	Email           bool
	Username        bool
	Notes           bool
	PasswordHash    bool
	Role            bool
	Status          bool
	Concurrency     bool
	RPMLimit        bool
	// APIKeyLimit selects the per-user API Key count limit.
	APIKeyLimit  bool
	SignupSource bool
	LastLoginAt  bool
	LastActiveAt bool
	// BalanceNotifySettings selects the enabled flag, threshold type, and threshold.
	BalanceNotifySettings bool
	// BalanceNotifyExtraEmails is separate so threshold updates cannot overwrite email-list updates.
	BalanceNotifyExtraEmails bool
	// AllowedGroups selects synchronization of user_allowed_groups.
	AllowedGroups bool
	// DisabledPublicGroups selects synchronization of disabled public group relations.
	DisabledPublicGroups bool
}

// BalanceChange is the billing result type exposed to identity callers.
type BalanceChange = billing.BalanceChange

type UserRepository interface {
	Create(ctx context.Context, user *User) error
	CreateWithNormalizedEmailGuard(ctx context.Context, user *User, normalizedEmail string) error
	GetByID(ctx context.Context, id int64) (*User, error)
	// GetByIDIncludeDeleted includes soft-deleted users for administrative audit and usage views.
	GetByIDIncludeDeleted(ctx context.Context, id int64) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	GetFirstAdmin(ctx context.Context) (*User, error)
	// Update writes selected fields and retains database values for all other fields.
	Update(ctx context.Context, user *User, fields UserUpdateFields) error
	// UpdateWithNormalizedEmailGuard applies the field mask while locking the normalized registration email.
	UpdateWithNormalizedEmailGuard(ctx context.Context, user *User, normalizedEmail string, fields UserUpdateFields) error
	Delete(ctx context.Context, id int64) error
	GetUserAvatar(ctx context.Context, userID int64) (*UserAvatar, error)
	UpsertUserAvatar(ctx context.Context, userID int64, input UpsertUserAvatarInput) (*UserAvatar, error)
	DeleteUserAvatar(ctx context.Context, userID int64) error

	List(ctx context.Context, params pagination.PaginationParams) ([]User, *pagination.PaginationResult, error)
	ListWithFilters(ctx context.Context, params pagination.PaginationParams, filters UserListFilters) ([]User, *pagination.PaginationResult, error)
	GetLatestUsedAtByUserIDs(ctx context.Context, userIDs []int64) (map[int64]*time.Time, error)
	GetLatestUsedAtByUserID(ctx context.Context, userID int64) (*time.Time, error)
	UpdateUserLastActiveAt(ctx context.Context, userID int64, activeAt time.Time) error

	AddBalance(ctx context.Context, id int64, amount float64) error
	UpdateBalance(ctx context.Context, id int64, amount float64) error
	DeductBalance(ctx context.Context, id int64, amount float64) (float64, error)
	// AdjustBalance atomically adds delta and returns the before and after balances.
	// A negative result returns ErrBalanceNegative. Use this for administrative adjustments
	// so concurrent deductions cannot be overwritten by a stale balance snapshot.
	AdjustBalance(ctx context.Context, id int64, delta float64) (BalanceChange, error)
	// SetBalance atomically sets a nonnegative value and returns the before and after balances.
	SetBalance(ctx context.Context, id int64, value float64) (BalanceChange, error)
	UpdateConcurrency(ctx context.Context, id int64, amount int) error
	// BatchSetConcurrency sets each user limit, treating a negative value as zero.
	BatchSetConcurrency(ctx context.Context, userIDs []int64, value int) (int, error)
	// BatchAddConcurrency adds delta to each limit, clamping the result to zero.
	BatchAddConcurrency(ctx context.Context, userIDs []int64, delta int) (int, error)
	// BatchUpdateLimits writes the non-nil limit fields in one update.
	BatchUpdateLimits(ctx context.Context, userIDs []int64, concurrency, rpmLimit *int) (int, error)
	ExistsByEmail(ctx context.Context, email string) (bool, error)
	ExistsByNormalizedEmail(ctx context.Context, normalizedEmail string) (bool, error)
	LockRegistrationEmail(ctx context.Context, normalizedEmail string) error
	RemoveGroupFromAllowedGroups(ctx context.Context, groupID int64) (int64, error)
	// AddGroupToAllowedGroups grants a group idempotently, ignoring duplicate relations.
	AddGroupToAllowedGroups(ctx context.Context, userID int64, groupID int64) error
	// RemoveGroupFromUserAllowedGroups removes one user's access to the group.
	RemoveGroupFromUserAllowedGroups(ctx context.Context, userID int64, groupID int64) error
	ListUserAuthIdentities(ctx context.Context, userID int64) ([]UserAuthIdentityRecord, error)
	UnbindUserAuthProvider(ctx context.Context, userID int64, provider string) error

	// TOTP two-factor authentication.
	UpdateTotpSecret(ctx context.Context, userID int64, encryptedSecret *string) error
	EnableTotp(ctx context.Context, userID int64) error
	DisableTotp(ctx context.Context, userID int64) error
}

// RegistrationEmailDomainRepository provides atomic registration under the domain quota.
// Registration-specific callers use this interface separately from UserRepository.
type RegistrationEmailDomainRepository interface {
	CountUsersByEmailDomain(ctx context.Context, domain string) (int, error)
	CreateWithRegistrationEmailGuards(ctx context.Context, user *User, normalizedEmail, domain string) error
}

type UserAuthIdentityRecord struct {
	ProviderType    string
	ProviderKey     string
	ProviderSubject string
	VerifiedAt      *time.Time
	Issuer          *string
	Metadata        map[string]any
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type UserIdentitySummary struct {
	Provider      string     `json:"provider"`
	Bound         bool       `json:"bound"`
	BoundCount    int        `json:"bound_count"`
	DisplayName   string     `json:"display_name,omitempty"`
	AvatarURL     string     `json:"-"`
	SubjectHint   string     `json:"subject_hint,omitempty"`
	ProviderKey   string     `json:"provider_key,omitempty"`
	VerifiedAt    *time.Time `json:"verified_at,omitempty"`
	BindStartPath string     `json:"bind_start_path,omitempty"`
	CanBind       bool       `json:"can_bind"`
	CanUnbind     bool       `json:"can_unbind"`
	NoteKey       string     `json:"note_key,omitempty"`
	Note          string     `json:"note,omitempty"`
}

type UserIdentitySummarySet struct {
	Email    UserIdentitySummary `json:"email"`
	LinuxDo  UserIdentitySummary `json:"linuxdo"`
	OIDC     UserIdentitySummary `json:"oidc"`
	WeChat   UserIdentitySummary `json:"wechat"`
	DingTalk UserIdentitySummary `json:"dingtalk"`
}

type StartUserIdentityBindingRequest struct {
	Provider   string
	RedirectTo string
}

type StartUserIdentityBindingResult struct {
	Provider           string `json:"provider"`
	AuthorizeURL       string `json:"authorize_url"`
	Method             string `json:"method"`
	UseBrowserRedirect bool   `json:"use_browser_redirect"`
}

// UpdateProfileRequest contains optional profile changes.
type UpdateProfileRequest struct {
	ClearPreferredLocale   bool     `json:"-"`
	PreferredLocale        *string  `json:"preferred_locale"`
	Email                  *string  `json:"email"`
	Username               *string  `json:"username"`
	AvatarURL              *string  `json:"avatar_url"`
	Concurrency            *int     `json:"concurrency"`
	BalanceNotifyEnabled   *bool    `json:"balance_notify_enabled"`
	BalanceNotifyThreshold *float64 `json:"balance_notify_threshold"`
}

type UserAvatar struct {
	StorageProvider string
	StorageKey      string
	URL             string
	ContentType     string
	ByteSize        int
	SHA256          string
}

type UpsertUserAvatarInput struct {
	StorageProvider string
	StorageKey      string
	URL             string
	ContentType     string
	ByteSize        int
	SHA256          string
}

// ChangePasswordRequest contains the current and replacement passwords.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// IsEmpty reports whether Update can return without writing any fields.
func (f UserUpdateFields) IsEmpty() bool {
	return f == UserUpdateFields{}
}
