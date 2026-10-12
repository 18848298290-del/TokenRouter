package apikey

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ipmatch"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

const (
	// API Key status constants.
	StatusAPIKeyActive         = "active"
	StatusAPIKeyDisabled       = "disabled"
	StatusAPIKeyQuotaExhausted = "quota_exhausted"
	StatusAPIKeyExpired        = "expired"

	// API Key Fast policies.
	APIKeyFastModePolicyFollowRequest = "follow_request"
	APIKeyFastModePolicyForceOn       = "force_on"
	APIKeyFastModePolicyForceOff      = "force_off"

	// Billing modes. Auto uses subscriptions first and covers the remainder from the balance.
	APIKeyBillingModeAuto         = "auto"
	APIKeyBillingModeSubscription = "subscription"
	APIKeyBillingModeBalance      = "balance"

	// Rate limit window durations.
	RateLimitWindow5h = 5 * time.Hour
	RateLimitWindow1d = 24 * time.Hour
	RateLimitWindow7d = 7 * 24 * time.Hour
)

type APIKey struct {
	ID     int64
	UserID int64
	TeamID *int64
	// TeamOwnerDisabled is the owner lock that ordinary team members cannot clear.
	TeamOwnerDisabled bool
	Key               string
	Name              string
	GroupID           *int64
	// IsComposite selects among multiple groups using a model prefix.
	IsComposite bool
	// CompositeGroups stores group mappings in user-configured order.
	CompositeGroups []APIKeyCompositeGroup
	Status          string
	// FastModePolicy controls request acceleration subject to higher-priority system policy.
	FastModePolicy string
	// BillingMode selects the funding source; subscription requires PreferredSubscriptionID.
	BillingMode             string
	PreferredSubscriptionID *int64
	// ModelMapping redirects client model names before group and provider mapping.
	ModelMapping map[string]string
	IPWhitelist  []string
	IPBlacklist  []string
	// Compiled IP rules avoid parsing addresses and CIDRs on each authentication.
	CompiledIPWhitelist *ipmatch.CompiledIPRules `json:"-"`
	CompiledIPBlacklist *ipmatch.CompiledIPRules `json:"-"`
	LastUsedAt          *time.Time
	LastUsedIP          *string // From the latest usage log for this key that includes an IP address.
	CreatedAt           time.Time
	UpdatedAt           time.Time
	// User supplies permissions and funding; team keys use the current team owner.
	User *User
	// ActorUser is the member who created the key; personal keys use User.
	ActorUser      *User
	Team           *Team
	TeamMembership *TeamMembership
	Group          *routing.Group
	// FallbackWhenGroupUnavailable permits fallback to the administrator-configured target.
	FallbackWhenGroupUnavailable bool
	// ConcurrencyLimit and RPMLimit are request limits; zero means unlimited.
	ConcurrencyLimit int
	RPMLimit         int
	// CurrentConcurrency is the number of active requests for this key.
	CurrentConcurrency int
	// ManagedBy identifies hidden server-managed keys, such as creative_studio execution keys.
	// User APIs must not expose or mutate these keys. Nil identifies an ordinary user key.
	ManagedBy *string

	// Quota fields
	Quota     float64    // Quota limit in USD (0 = unlimited)
	QuotaUsed float64    // Used quota amount
	ExpiresAt *time.Time // Expiration time (nil = never expires)

	// Rate limit fields
	RateLimit5h   float64    // Rate limit in USD per 5h (0 = unlimited)
	RateLimit1d   float64    // Rate limit in USD per 1d (0 = unlimited)
	RateLimit7d   float64    // Rate limit in USD per 7d (0 = unlimited)
	Usage5h       float64    // Used amount in current 5h window
	Usage1d       float64    // Used amount in current 1d window
	Usage7d       float64    // Used amount in current 7d window
	Window5hStart *time.Time // Start of current 5h window
	Window1dStart *time.Time // Start of current 1d window
	Window7dStart *time.Time // Start of current 7d window
}

// APIKeyCompositeGroup maps one model prefix to a group.
type APIKeyCompositeGroup struct {
	ID               int64
	APIKeyID         int64
	GroupID          int64
	Prefix           string
	NormalizedPrefix string
	SortOrder        int
	// UserGroupRPMOverride is request configuration in the auth snapshot, not a persisted mapping field.
	UserGroupRPMOverride *int
	Group                *routing.Group
}

// APIKeyListFilters holds optional filtering parameters for listing API keys.
type APIKeyListFilters struct {
	Search  string
	Status  string
	GroupID *int64 // nil: all groups; 0: unbound keys; positive: the specified group
	Scope   string // personal or team; empty returns both for compatibility
}

// NormalizeAPIKeyFastModePolicy validates and normalizes the key's Fast policy.
// An empty value follows the client request for compatibility with older clients.
func NormalizeAPIKeyFastModePolicy(value string) (string, bool) {
	switch value {
	case "", APIKeyFastModePolicyFollowRequest:
		return APIKeyFastModePolicyFollowRequest, true
	case APIKeyFastModePolicyForceOn, APIKeyFastModePolicyForceOff:
		return value, true
	default:
		return "", false
	}
}

// NormalizeAPIKeyBillingMode delegates validation to billing's funding rules.
func NormalizeAPIKeyBillingMode(value string) (string, bool) {
	return billing.NormalizeAPIKeyBillingMode(value)
}

// APIKeyEffectiveBillingMode returns the effective funding mode.
// Nil keys and missing or invalid modes default to auto.
func APIKeyEffectiveBillingMode(key *APIKey) string {
	if key == nil {
		return APIKeyBillingModeAuto
	}
	mode, ok := NormalizeAPIKeyBillingMode(key.BillingMode)
	if !ok {
		return APIKeyBillingModeAuto
	}
	return mode
}

// IsWindowExpired reports whether the window starting at start has expired.
// A nil start counts as expired because usage without an initialized window is stale.
func IsWindowExpired(start *time.Time, window time.Duration) bool {
	return billing.IsWindowExpired(start, window)
}

func (k *APIKey) IsActive() bool {
	return k.Status == StatusActive && !k.TeamOwnerDisabled
}

// HasRateLimits returns true if any rate limit window is configured.
func (k *APIKey) HasRateLimits() bool {
	return k.RateLimit5h > 0 || k.RateLimit1d > 0 || k.RateLimit7d > 0
}

// IsExpired checks if the API key has expired.
func (k *APIKey) IsExpired() bool {
	if k.ExpiresAt == nil {
		return false
	}
	return k.IsExpiredAt(time.Now())
}

// IsQuotaExhausted checks if the API key quota is exhausted.
func (k *APIKey) IsQuotaExhausted() bool {
	if k.Quota <= 0 {
		return false // unlimited
	}
	return k.QuotaUsed >= k.Quota
}

// GetQuotaRemaining returns remaining quota (-1 for unlimited).
func (k *APIKey) GetQuotaRemaining() float64 {
	if k.Quota <= 0 {
		return -1 // unlimited
	}
	remaining := k.Quota - k.QuotaUsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

// GetDaysUntilExpiry returns days until expiry (-1 for never expires).
func (k *APIKey) GetDaysUntilExpiry() int {
	if k.ExpiresAt == nil {
		return -1 // never expires
	}
	duration := time.Until(*k.ExpiresAt)
	if duration < 0 {
		return 0
	}
	return int(duration.Hours() / 24)
}

// EffectiveUsage5h returns the 5h window usage, or 0 if the window has expired.
func (k *APIKey) EffectiveUsage5h() float64 {
	if IsWindowExpired(k.Window5hStart, RateLimitWindow5h) {
		return 0
	}
	return k.Usage5h
}

// EffectiveUsage1d returns the 1d window usage, or 0 if the window has expired.
func (k *APIKey) EffectiveUsage1d() float64 {
	if IsWindowExpired(k.Window1dStart, RateLimitWindow1d) {
		return 0
	}
	return k.Usage1d
}

// EffectiveUsage7d returns the 7d window usage, or 0 if the window has expired.
func (k *APIKey) EffectiveUsage7d() float64 {
	if IsWindowExpired(k.Window7dStart, RateLimitWindow7d) {
		return 0
	}
	return k.Usage7d
}

// IsExpiredAt checks expiration against a caller-supplied time.
func (k *APIKey) IsExpiredAt(now time.Time) bool {
	return k.ExpiresAt != nil && now.After(*k.ExpiresAt)
}

// CopyAPIKey copies the key and its user and group views. Other reference fields remain shared.
func CopyAPIKey(value *APIKey) *APIKey {
	if value == nil {
		return nil
	}
	out := *value
	out.User = identity.CopyUser(value.User)
	out.ActorUser = identity.CopyUser(value.ActorUser)
	out.Group = routing.CloneGroup(value.Group)
	out.CompositeGroups = CopyCompositeGroups(value.CompositeGroups)
	return &out
}

// CopyCompositeGroup clones group policy while retaining binding order and request overrides.
func CopyCompositeGroup(value APIKeyCompositeGroup) APIKeyCompositeGroup {
	value.Group = routing.CloneGroup(value.Group)
	return value
}

func CopyCompositeGroups(values []APIKeyCompositeGroup) []APIKeyCompositeGroup {
	if values == nil {
		return nil
	}
	out := make([]APIKeyCompositeGroup, len(values))
	for i, value := range values {
		out[i] = CopyCompositeGroup(value)
	}
	return out
}

// GroupFromRouting clones a group for authentication or management callers.
func GroupFromRouting(group *routing.Group) *routing.Group {
	return routing.CloneGroup(group)
}

// RoutingGroup returns a group copy that callers can mutate independently.
func RoutingGroup(group *routing.Group) *routing.Group {
	return routing.CloneGroup(group)
}
