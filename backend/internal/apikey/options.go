package apikey

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/contact"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/team"
)

const (
	StatusActive = "active"

	TeamStatusActive = team.TeamStatusActive
	TeamRoleOwner    = team.TeamRoleOwner
)

var (
	ErrUserNotFound = identity.ErrUserNotFound

	ErrInsufficientPerms         = identity.ErrInsufficientPerms
	ErrTeamFeatureDisabled       = team.ErrTeamFeatureDisabled
	ErrTeamMembershipRequired    = team.ErrTeamMembershipRequired
	ErrTeamNotFound              = team.ErrTeamNotFound
	ErrTeamSuspended             = team.ErrTeamSuspended
	ErrTeamMemberDailyExceeded   = billing.ErrTeamMemberDailyExceeded
	ErrTeamMemberWeeklyExceeded  = billing.ErrTeamMemberWeeklyExceeded
	ErrTeamMemberMonthlyExceeded = billing.ErrTeamMemberMonthlyExceeded
)

// APIKeyAuthCacheConfig configures authentication caching and lookup protection.
type APIKeyAuthCacheConfig struct {
	L1Size             int                    `mapstructure:"l1_size"`
	L1TTLSeconds       int                    `mapstructure:"l1_ttl_seconds"`
	L2TTLSeconds       int                    `mapstructure:"l2_ttl_seconds"`
	NegativeTTLSeconds int                    `mapstructure:"negative_ttl_seconds"`
	JitterPercent      int                    `mapstructure:"jitter_percent"`
	Singleflight       bool                   `mapstructure:"singleflight"`
	LookupConcurrency  int                    `mapstructure:"lookup_concurrency"`
	InvalidAbuse       InvalidAuthAbuseConfig `mapstructure:"invalid_abuse"`
}

type InvalidAuthAbuseConfig struct {
	Enabled       bool `mapstructure:"enabled"`
	Threshold     int  `mapstructure:"threshold"`
	WindowSeconds int  `mapstructure:"window_seconds"`
	BlockSeconds  int  `mapstructure:"block_seconds"`
	Capacity      int  `mapstructure:"capacity"`
}

// Options contains startup settings for authentication, key generation, and team access.
type Options struct {
	Now             func() time.Time
	Calendar        timezone.Calendar
	APIKeyAuth      APIKeyAuthCacheConfig
	Default         struct{ APIKeyPrefix string }
	Team            struct{ Enabled bool }
	GroupFastPolicy func(string, bool) string
}

type (
	User                       = identity.User
	NotifyEmailEntry           = contact.Entry
	Team                       = team.Team
	TeamMembership             = team.TeamMembership
	UserSubscription           = billing.UserSubscription
	UserSubscriptionRepository interface {
		GetByID(context.Context, int64) (*UserSubscription, error)
		ListActiveByUserID(context.Context, int64) ([]UserSubscription, error)
	}
)

type (
	UserGroupRateRepository = billing.UserGroupRateRepository
	UserRepository          interface {
		GetByID(context.Context, int64) (*User, error)
	}
)

type GroupRepository interface {
	GetByID(context.Context, int64) (*routing.Group, error)
	GetByIDLite(context.Context, int64) (*routing.Group, error)
	ListActive(context.Context) ([]routing.Group, error)
}

type TeamRepository interface {
	GetContextByUserID(context.Context, int64) (*team.TeamContext, error)
}

type ConcurrencyReader interface {
	GetAPIKeyConcurrencyBatch(context.Context, []int64) (map[int64]int, error)
}

func cloneGroupClientProtocols(values []protocol.ProtocolID) []protocol.ProtocolID {
	if values == nil {
		return nil
	}
	out := make([]protocol.ProtocolID, len(values))
	copy(out, values)
	return out
}

func subscriptionPlanIncludesGroup(plan *billing.SubscriptionPlan, id int64) bool {
	return billing.SubscriptionAllowsGroup(&billing.UserSubscription{Plan: plan}, id)
}

// SetGroupFastPolicy installs the group Fast policy resolver.
func (s *APIKeyService) SetGroupFastPolicy(policy func(string, bool) string) {
	s.groupFastPolicy = policy
}

func (s *APIKeyService) groupPolicy(g *routing.Group) string {
	if s.groupFastPolicy != nil {
		return s.groupFastPolicy(g.OpenAIFastPolicy, g.ForceOpenAIFast)
	}
	if s.cfg != nil && s.cfg.GroupFastPolicy != nil {
		return s.cfg.GroupFastPolicy(g.OpenAIFastPolicy, g.ForceOpenAIFast)
	}
	// Standalone callers can provide an already-normalized policy.
	return g.OpenAIFastPolicy
}
