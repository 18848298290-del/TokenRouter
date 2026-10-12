package identity

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	AdjustmentTypeAdminConcurrency = billing.AdjustmentTypeAdminConcurrency
	AdjustmentTypeAdminBalance     = billing.AdjustmentTypeAdminBalance
)

var ErrRPMStatusUnavailable = infraerrors.New(501, "RPM_STATUS_UNAVAILABLE", "RPM cache not available")

// CreateUserInput represents input for creating a new user via admin operations.
type CreateUserInput struct {
	Email         string
	Password      string
	Username      string
	Notes         string
	Role          string // Empty defaults to user; valid values are admin and user.
	Balance       *float64
	Concurrency   int
	RPMLimit      int
	APIKeyLimit   *int // nil inherits the system default; zero means unlimited.
	AllowedGroups []int64
	// DisabledPublicGroups lists public group IDs denied to this user.
	DisabledPublicGroups []int64
	// ActorAdminID identifies the acting administrator for privilege-change audit logs.
	ActorAdminID int64
}

type UpdateUserInput struct {
	Email         string
	Password      string
	Username      *string
	Notes         *string
	Role          string   // Empty leaves the role unchanged; valid values are admin and user.
	Balance       *float64 // nil leaves the value unchanged; zero sets it to zero.
	Concurrency   *int     // nil leaves the value unchanged; zero sets it to zero.
	RPMLimit      *int     // nil leaves the value unchanged; zero sets it to zero.
	APIKeyLimit   *int     // nil leaves the value unchanged; zero means unlimited.
	Status        string
	AllowedGroups *[]int64 // nil leaves the list unchanged; an empty slice clears it.
	// DisabledPublicGroups is nil to leave the list unchanged or empty to clear it.
	DisabledPublicGroups *[]int64
	// GroupRates maps group IDs to user-specific rate multipliers.
	// A nil rate removes the override for that group.
	GroupRates map[int64]*float64
	// ActorAdminID identifies the acting administrator for privilege-change audit logs.
	ActorAdminID int64
}

type AdminBindAuthIdentityInput struct {
	ProviderType    string
	ProviderKey     string
	ProviderSubject string
	Issuer          *string
	Metadata        map[string]any
	Channel         *AdminBindAuthIdentityChannelInput
}

type AdminBindAuthIdentityChannelInput struct {
	Channel        string
	ChannelAppID   string
	ChannelSubject string
	Metadata       map[string]any
}

type AdminBoundAuthIdentity struct {
	UserID          int64                          `json:"user_id"`
	ProviderType    string                         `json:"provider_type"`
	ProviderKey     string                         `json:"provider_key"`
	ProviderSubject string                         `json:"provider_subject"`
	VerifiedAt      *time.Time                     `json:"verified_at,omitempty"`
	Issuer          *string                        `json:"issuer,omitempty"`
	Metadata        map[string]any                 `json:"metadata"`
	CreatedAt       time.Time                      `json:"created_at"`
	UpdatedAt       time.Time                      `json:"updated_at"`
	Channel         *AdminBoundAuthIdentityChannel `json:"channel,omitempty"`
}

type AdminBoundAuthIdentityChannel struct {
	Channel        string         `json:"channel"`
	ChannelAppID   string         `json:"channel_app_id"`
	ChannelSubject string         `json:"channel_subject"`
	Metadata       map[string]any `json:"metadata"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// ReplaceUserGroupResult reports the number of keys moved between groups.
type ReplaceUserGroupResult struct {
	MigratedKeys int64 // Number of migrated keys.
}

// UserRPMStatus reports the user's request count and limit for the current minute.
type UserRPMStatus struct {
	UserRPMUsed  int                  `json:"user_rpm_used"`
	UserRPMLimit int                  `json:"user_rpm_limit"`
	PerGroup     []UserGroupRPMStatus `json:"per_group"`
}

// UserGroupRPMStatus reports the current-minute request count and limit for a user/group pair.
type UserGroupRPMStatus struct {
	GroupID   int64  `json:"group_id"`
	GroupName string `json:"group_name"`
	Used      int    `json:"used"`
	Limit     int    `json:"limit"`
	Source    string `json:"source"` // "group" | "override"
}

// UserAdmin manages users through the stores and services in AdminDependencies.
type (
	UserAdmin         struct{ AdminDependencies }
	AdminDependencies struct {
		Now           func() time.Time
		Users         UserRepository
		Groups        AdminGroupReader
		Keys          AdminKeyReader
		Rates         billing.UserGroupRateRepository
		RPM           AdminRPMReader
		Settings      AdminUserSettings
		Subscriptions DefaultSubscriptionAssigner
		Balances      billing.BalanceAdjuster
		Records       AdminAdjustmentRecords
		Invalidator   AdminInvalidator
		BalanceCache  UserBalanceCache
		Affiliates    AdminAffiliateAccruer
		Transactions  AdminTransactions
		Observer      Observer
		Background    func(string, func()) bool
	}
)

type AdminUserSettings interface {
	GetDefaultBalance(context.Context) float64
	GetDefaultUserAPIKeyLimit(context.Context) int
	IsRegistrationEmailNormalizationEnabled(context.Context) bool
	GetDefaultSubscriptions(context.Context) []DefaultSubscriptionSetting
	IsAffiliateAdminRechargeEnabled(context.Context) bool
}
type AdminAdjustmentRecords interface {
	RecordAdjustment(context.Context, int64, string, float64, string) error
	GetUserBalanceHistory(context.Context, int64, int, int, string) ([]billing.RedeemCode, int64, float64, error)
}
type AdminGroup struct {
	ID           int64
	Name, Status string
	IsExclusive  bool
	RPMLimit     int
}
type AdminGroupReader interface {
	GetByID(context.Context, int64) (*AdminGroup, error)
	GetByIDLite(context.Context, int64) (*AdminGroup, error)
}
type AdminKeySummary struct {
	ID      int64
	Key     string
	GroupID *int64
}
type AdminKeyReader interface {
	List(context.Context, int64, int, int, string, string) ([]AdminKeySummary, int64, error)
	ListKeysByUserID(context.Context, int64) ([]string, error)
}
type AdminKeyParticipant interface {
	DeleteWithAudit(context.Context, int64) error
	UpdateGroupIDByUserAndGroup(context.Context, int64, int64, int64) (int64, error)
}
type AdminRPMReader interface {
	GetUserRPM(context.Context, int64) (int, error)
	GetUserGroupRPM(context.Context, int64, int64) (int, error)
}
type AdminInvalidator interface {
	InvalidateAuthCacheByUserID(context.Context, int64)
	InvalidateAuthCacheByKey(context.Context, string)
}
type AdminAffiliateAccruer interface {
	AccrueInviteRebate(context.Context, int64, float64) (float64, error)
}
type AdminGroupRateBatchReader interface {
	GetByUserIDs(context.Context, []int64) (map[int64]map[int64]float64, error)
}
type AdminTransactions interface {
	HasDatabase() bool
	DeleteUserAndKeys(context.Context, int64, []AdminKeySummary) error
	ReplaceUserGroup(context.Context, int64, int64, int64) (int64, error)
	BindAuthIdentity(context.Context, PreparedAdminIdentityBinding) (*AdminBoundAuthIdentity, error)
}
type PreparedAdminIdentityBinding struct {
	UserID                                     int64
	ProviderType, ProviderKey, ProviderSubject string
	CompatibleKeys                             []string
	Issuer                                     *string
	Metadata                                   map[string]any
	Channel                                    *AdminBindAuthIdentityChannelInput
	VerifiedAt                                 time.Time
}

func NewUserAdmin(d AdminDependencies) *UserAdmin { return &UserAdmin{d} }
func (s *UserAdmin) RunBackground(name string, fn func()) bool {
	if s.Background != nil {
		return s.Background(name, fn)
	}
	fn()
	return true
}
