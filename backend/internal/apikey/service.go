package apikey

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/ristretto"
	"golang.org/x/sync/singleflight"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ipmatch"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

const (
	MaxAPIKeyCredentialBytes        = 128
	KeyDefaultAuthLookupConcurrency = 64
	KeyDefaultNegativeAuthCacheSize = 16384
	KeyApiKeyMaxErrorsPerHour       = 20
	KeyApiKeyLastUsedMinTouch       = 30 * time.Second
	// DECIMAL(20,8) permits 12 integer digits; values must be below this exclusive bound.
	KeyApiKeyLimitUpperBound = 1_000_000_000_000
	// KeyApiKeyLastUsedFailBackoff delays retries after database errors to bound synchronous writes.
	KeyApiKeyLastUsedFailBackoff = 5 * time.Second
)

var (
	ErrGroupNotAllowed                   = infraerrors.Forbidden("GROUP_NOT_ALLOWED", "user is not allowed to bind this group")
	ErrGroupDisabledForUser              = infraerrors.Forbidden("GROUP_DISABLED_FOR_USER", "user is not allowed to use this public group")
	ErrAPIKeyExists                      = infraerrors.Conflict("API_KEY_EXISTS", "api key already exists")
	ErrAPIKeyRotationConflict            = infraerrors.Conflict("API_KEY_ROTATION_CONFLICT", "API Key 已变更，请刷新后重试")
	ErrAPIKeyLimitReached                = infraerrors.Conflict("API_KEY_LIMIT_REACHED", "api key limit reached")
	ErrAPIKeyTooShort                    = infraerrors.BadRequest("API_KEY_TOO_SHORT", "api key must be at least 16 characters")
	ErrAPIKeyInvalidChars                = infraerrors.BadRequest("API_KEY_INVALID_CHARS", "api key can only contain letters, numbers, underscores, and hyphens")
	ErrAPIKeyLimitInvalid                = infraerrors.BadRequest("API_KEY_LIMIT_INVALID", "api key limits must be finite, non-negative, and less than 1000000000000")
	ErrAPIKeyExpiryInvalid               = infraerrors.BadRequest("API_KEY_EXPIRY_INVALID", "expires_in_days must be greater than zero")
	ErrAPIKeyRateLimited                 = infraerrors.TooManyRequests("API_KEY_RATE_LIMITED", "too many failed attempts, please try again later")
	ErrAPIKeyAuthOverloaded              = infraerrors.ServiceUnavailable("API_KEY_AUTH_OVERLOADED", "api key authentication is temporarily overloaded")
	ErrInvalidIPPattern                  = infraerrors.BadRequest("INVALID_IP_PATTERN", "invalid IP or CIDR pattern")
	ErrInvalidAPIKeyFastModePolicy       = infraerrors.BadRequest("INVALID_API_KEY_FAST_MODE_POLICY", "invalid API key fast mode policy")
	ErrInvalidAPIKeyBillingMode          = infraerrors.BadRequest("INVALID_API_KEY_BILLING_MODE", "invalid API key billing mode")
	ErrPreferredSubscriptionRequired     = infraerrors.BadRequest("PREFERRED_SUBSCRIPTION_REQUIRED", "subscription billing mode requires a subscription")
	ErrPreferredSubscriptionInvalid      = billing.ErrPreferredSubscriptionInvalid
	ErrPreferredSubscriptionGroup        = billing.ErrPreferredSubscriptionGroup
	ErrPreferredSubscriptionInsufficient = billing.ErrPreferredSubscriptionInsufficient
	ErrCompositeKeyGroupsRequired        = infraerrors.BadRequest("COMPOSITE_KEY_GROUPS_REQUIRED", "composite api key requires at least one group")
	ErrCompositeKeyTooManyGroups         = infraerrors.BadRequest("COMPOSITE_KEY_TOO_MANY_GROUPS", "composite api key supports at most 20 groups")
	ErrCompositeKeyPrefixInvalid         = infraerrors.BadRequest("COMPOSITE_KEY_PREFIX_INVALID", "composite api key prefix is invalid")
	ErrCompositeKeyPrefixDuplicate       = infraerrors.BadRequest("COMPOSITE_KEY_PREFIX_DUPLICATE", "composite api key prefixes must be unique")
	ErrCompositeKeyGroupDuplicate        = infraerrors.BadRequest("COMPOSITE_KEY_GROUP_DUPLICATE", "composite api key groups must be unique")
	ErrCompositeKeyGroupConflict         = infraerrors.BadRequest("COMPOSITE_KEY_GROUP_CONFLICT", "composite api key cannot use group_id")
	ErrCompositeKeyTargetRequired        = infraerrors.BadRequest("COMPOSITE_KEY_TARGET_GROUP_REQUIRED", "converting a composite api key requires a target group")
	ErrCompositeKeyPrefixRequired        = infraerrors.BadRequest("COMPOSITE_KEY_MODEL_PREFIX_REQUIRED", "composite api key model must use prefix/model_id")
	ErrCompositeKeyPrefixNotFound        = infraerrors.BadRequest("COMPOSITE_KEY_PREFIX_NOT_FOUND", "composite api key model prefix was not found")
	ErrCompositeKeyUnsupported           = infraerrors.BadRequest("COMPOSITE_KEY_ENDPOINT_UNSUPPORTED", "composite api key is not supported for this endpoint")
	// ErrAPIKeyExpired reports expiration during spending admission.
	ErrAPIKeyExpired = infraerrors.Forbidden("API_KEY_EXPIRED", "The API key has expired.")
	// ErrAPIKeyQuotaExhausted reports that the key has consumed its quota.
	ErrAPIKeyQuotaExhausted = infraerrors.TooManyRequests("API_KEY_QUOTA_EXHAUSTED", "The API key quota has been exhausted.")

	// Rate limit errors.
	ErrAPIKeyRateLimit5hExceeded = billing.ErrAPIKeyRateLimit5hExceeded
	ErrAPIKeyRateLimit1dExceeded = billing.ErrAPIKeyRateLimit1dExceeded
	ErrAPIKeyRateLimit7dExceeded = billing.ErrAPIKeyRateLimit7dExceeded
	ErrTeamActorInactive         = infraerrors.Forbidden("TEAM_ACTOR_INACTIVE", "The member assigned to this team API key is inactive")
	ErrTeamBillingOwnerInactive  = infraerrors.Forbidden("TEAM_BILLING_OWNER_INACTIVE", "The team billing owner is inactive")

	// ErrAPIKeyNotFound reports a missing API key.
	ErrAPIKeyNotFound = billing.ErrAPIKeyNotFound
)

// APIKeyUpdateFields selects columns written by APIKeyRepository.Update.
//
// Billing atomically increments quota_used and the usage_5h/1d/7d counters.
// Writing a complete key snapshot during edits would overwrite concurrent charges.
// Callers select configuration columns individually and select usage columns only for resets.
// Unselected columns retain their current database values.
type APIKeyUpdateFields struct {
	Name      bool
	Status    bool
	Quota     bool
	GroupID   bool
	ExpiresAt bool
	// CompositeConfiguration writes is_composite and group mappings in the same transaction.
	CompositeConfiguration bool
	// FastModePolicy writes the key acceleration policy.
	FastModePolicy bool
	// BillingConfiguration writes the funding mode and preferred subscription together.
	BillingConfiguration bool
	// ModelMapping replaces all model redirect rules for the key.
	ModelMapping bool
	// FallbackWhenGroupUnavailable writes the unavailable-group fallback policy.
	FallbackWhenGroupUnavailable bool
	// QuotaUsed selects an explicit usage reset; billing uses IncrementQuotaUsed.
	QuotaUsed bool
	// RateLimits writes the 5h, 1d, and 7d spending thresholds.
	RateLimits bool
	// Select request limits independently so edits do not overwrite each other.
	ConcurrencyLimit bool
	RPMLimit         bool
	// RateLimitUsage writes all three usage counters and their window start times.
	// Use it for explicit resets; billing uses IncrementRateLimitUsage.
	RateLimitUsage bool
	// IPRules writes both IP allowlist and denylist fields.
	IPRules bool
}

type APIKeyRepository interface {
	Create(ctx context.Context, key *APIKey) error
	GetByID(ctx context.Context, id int64) (*APIKey, error)
	// GetKeyAndOwnerID reads the credential and owner ID for operations such as deletion.
	GetKeyAndOwnerID(ctx context.Context, id int64) (string, int64, error)
	GetByKey(ctx context.Context, key string) (*APIKey, error)
	// GetByKeyForAuth loads fields and relationships needed for authentication.
	GetByKeyForAuth(ctx context.Context, key string) (*APIKey, error)
	// Update writes selected fields and retains current database values for other columns.
	Update(ctx context.Context, key *APIKey, fields APIKeyUpdateFields) error
	// RotateCredential replaces the credential after checking the owner and old credential for concurrent changes.
	RotateCredential(ctx context.Context, key *APIKey, oldKey string) error
	Delete(ctx context.Context, id int64) error
	// DeleteWithAudit retains its historical interface name.
	// Implementations atomically soft-delete the key and replace the credential with a tombstone.
	DeleteWithAudit(ctx context.Context, id int64) error

	ListByUserID(ctx context.Context, userID int64, params pagination.PaginationParams, filters APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error)
	VerifyOwnership(ctx context.Context, userID int64, apiKeyIDs []int64) ([]int64, error)
	CountByUserID(ctx context.Context, userID int64) (int64, error)
	ExistsByKey(ctx context.Context, key string) (bool, error)
	ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]APIKey, *pagination.PaginationResult, error)
	SearchAPIKeys(ctx context.Context, userID int64, keyword string, limit int) ([]APIKey, error)
	ClearGroupIDByGroupID(ctx context.Context, groupID int64) (int64, error)
	// UpdateGroupIDByUserAndGroup moves the user's bindings from oldGroupID to newGroupID.
	UpdateGroupIDByUserAndGroup(ctx context.Context, userID, oldGroupID, newGroupID int64) (int64, error)
	CountByGroupID(ctx context.Context, groupID int64) (int64, error)
	ListKeysByUserID(ctx context.Context, userID int64) ([]string, error)
	ListKeysByGroupID(ctx context.Context, groupID int64) ([]string, error)

	// Quota methods
	IncrementQuotaUsed(ctx context.Context, id int64, amount float64) (float64, error)
	UpdateLastUsed(ctx context.Context, id int64, usedAt time.Time) error

	// Rate limit methods
	IncrementRateLimitUsage(ctx context.Context, id int64, cost float64) error
	ResetRateLimitWindows(ctx context.Context, id int64) error
	GetRateLimitData(ctx context.Context, id int64) (*APIKeyRateLimitData, error)
}

type APIKeyRateLimitData = billing.APIKeyRateLimitData

// APIKeyQuotaUsageState captures the latest quota fields after an atomic quota update.
// It is intentionally small so repositories can return it from a single SQL statement.
type APIKeyQuotaUsageState = billing.APIKeyQuotaUsageState

// APIKeyCache defines cache operations for API key service.
type APIKeyCache interface {
	GetCreateAttemptCount(ctx context.Context, userID int64) (int, error)
	IncrementCreateAttemptCount(ctx context.Context, userID int64) error
	DeleteCreateAttemptCount(ctx context.Context, userID int64) error

	IncrementDailyUsage(ctx context.Context, apiKey string) error
	SetDailyUsageExpiry(ctx context.Context, apiKey string, ttl time.Duration) error

	GetAuthCache(ctx context.Context, key string) (*APIKeyAuthCacheEntry, error)
	SetAuthCache(ctx context.Context, key string, entry *APIKeyAuthCacheEntry, ttl time.Duration) error
	DeleteAuthCache(ctx context.Context, key string) error

	// Pub/Sub for L1 cache invalidation across instances
	PublishAuthCacheInvalidation(ctx context.Context, cacheKey string) error
	SubscribeAuthCacheInvalidation(ctx context.Context, handler func(cacheKey string)) error
}

type KeyAuthCacheSubscriptionReadyKey struct{}

// APIKeyAuthCacheInvalidator invalidates authentication entries by credential, user, or group.
type APIKeyAuthCacheInvalidator interface {
	InvalidateAuthCacheByKey(ctx context.Context, key string)
	InvalidateAuthCacheByUserID(ctx context.Context, userID int64)
	InvalidateAuthCacheByGroupID(ctx context.Context, groupID int64)
}

// CreateAPIKeyRequest configures a new key.
type CreateAPIKeyRequest struct {
	Name        string `json:"name"`
	Scope       string `json:"scope"`
	GroupID     *int64 `json:"group_id"`
	IsComposite bool   `json:"is_composite"`
	// CompositeGroups contains all composite group mappings.
	CompositeGroups []APIKeyCompositeGroupInput `json:"composite_groups"`
	CustomKey       *string                     `json:"custom_key"`   // Optional custom credential
	IPWhitelist     []string                    `json:"ip_whitelist"` // Allowed IP addresses and CIDRs
	IPBlacklist     []string                    `json:"ip_blacklist"` // Denied IP addresses and CIDRs
	// FastModePolicy defaults to following the client request when empty.
	FastModePolicy string `json:"fast_mode_policy"`
	// BillingMode defaults to automatic funding selection when empty.
	BillingMode string `json:"billing_mode"`
	// PreferredSubscriptionID applies when BillingMode is subscription.
	PreferredSubscriptionID *int64 `json:"preferred_subscription_id"`
	// ModelMapping contains all model redirects for this key.
	ModelMapping map[string]string `json:"model_mapping"`

	// Quota fields
	Quota         float64 `json:"quota"`           // Quota limit in USD (0 = unlimited)
	ExpiresInDays *int    `json:"expires_in_days"` // Days until expiry (nil = never expires)

	// Key request limits; zero means unlimited.
	ConcurrencyLimit int `json:"concurrency_limit"`
	RPMLimit         int `json:"rpm_limit"`

	// Rate limit fields (0 = unlimited)
	RateLimit5h float64 `json:"rate_limit_5h"`
	RateLimit1d float64 `json:"rate_limit_1d"`
	RateLimit7d float64 `json:"rate_limit_7d"`

	// FallbackWhenGroupUnavailable permits a configured fallback from an unavailable group; nil defaults to true.
	FallbackWhenGroupUnavailable *bool `json:"fallback_when_group_unavailable"`
}

// APIKeyBillingSubscriptionOption is an active subscription available when configuring a key.
// Group details narrow UI choices; the server validates access again during writes and request processing.
type APIKeyBillingSubscriptionOption struct {
	ID               int64
	PlanID           int64
	PlanName         string
	ExpiresAt        time.Time
	GroupsRestricted bool
	ApplicableGroups []int64
}

// UpdateAPIKeyRequest selects changes to an existing key.
type UpdateAPIKeyRequest struct {
	Name        *string `json:"name"`
	GroupID     *int64  `json:"group_id"`
	IsComposite *bool   `json:"is_composite"`
	// CompositeGroups replaces all current mappings when non-nil.
	CompositeGroups *[]APIKeyCompositeGroupInput `json:"composite_groups"`
	Status          *string                      `json:"status"`
	IPWhitelist     *[]string                    `json:"ip_whitelist"` // nil: unchanged; empty: clear the allowlist
	IPBlacklist     *[]string                    `json:"ip_blacklist"` // nil: unchanged; empty: clear the denylist
	// FastModePolicy leaves the policy unchanged when nil.
	FastModePolicy *string `json:"fast_mode_policy"`
	// BillingMode leaves the mode unchanged when nil; subscription uses the supplied or existing subscription ID.
	BillingMode             *string `json:"billing_mode"`
	PreferredSubscriptionID *int64  `json:"preferred_subscription_id"`
	// ModelMapping leaves rules unchanged when nil; an empty map clears them.
	ModelMapping *map[string]string `json:"model_mapping"`

	// Quota fields
	Quota           *float64   `json:"quota"`       // Quota limit in USD (nil = no change, 0 = unlimited)
	ExpiresAt       *time.Time `json:"expires_at"`  // Expiration time (nil = no change)
	ClearExpiration bool       `json:"-"`           // Clear expiration (internal use)
	ResetQuota      *bool      `json:"reset_quota"` // Reset quota_used to 0

	// Nil leaves each request limit unchanged; zero clears it.
	ConcurrencyLimit *int `json:"concurrency_limit"`
	RPMLimit         *int `json:"rpm_limit"`

	// Rate limit fields (nil = no change, 0 = unlimited)
	RateLimit5h         *float64 `json:"rate_limit_5h"`
	RateLimit1d         *float64 `json:"rate_limit_1d"`
	RateLimit7d         *float64 `json:"rate_limit_7d"`
	ResetRateLimitUsage *bool    `json:"reset_rate_limit_usage"` // Reset all usage counters to 0

	// FallbackWhenGroupUnavailable leaves the policy unchanged when nil.
	FallbackWhenGroupUnavailable *bool `json:"fallback_when_group_unavailable"`
}

// Manual usage resets need to invalidate the separately cached spending counters.
// RateLimitCacheInvalidator invalidates rate limit cache entries on manual reset.
type RateLimitCacheInvalidator interface {
	InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error
}

type APIKeyService struct {
	calendar                  timezone.Calendar
	groupFastPolicy           func(string, bool) string
	runtimeStart              sync.Once
	runtimeStop               sync.Once
	runtimeMu                 sync.Mutex
	operations                authOperationGate
	stopped                   chan struct{}
	subscriberMu              sync.Mutex
	subscriberStopped         bool
	apiKeyRepo                APIKeyRepository
	userRepo                  UserRepository
	groupRepo                 GroupRepository
	userSubRepo               UserSubscriptionRepository
	userGroupRateRepo         UserGroupRateRepository
	cache                     APIKeyCache
	rateLimitCacheInvalid     RateLimitCacheInvalidator // optional: invalidate Redis rate limit cache
	concurrencyService        ConcurrencyReader
	teamRepo                  TeamRepository
	cfg                       *Options
	authCacheL1               atomic.Pointer[ristretto.Cache]
	authNegativeCacheL1       atomic.Pointer[ristretto.Cache]
	authCfg                   KeyApiKeyAuthCacheConfig
	authGroup                 singleflight.Group
	authLookupSlots           chan struct{}
	authLookupTotal           atomic.Uint64
	authLookupRejected        atomic.Uint64
	authLookupInFlight        atomic.Int64
	invalidAuthAbuse          *KeyInvalidAuthAbuseLimiter
	authInvalidationStart     sync.Once
	authInvalidationStop      sync.Once
	authInvalidationCancel    context.CancelFunc
	authInvalidationWG        sync.WaitGroup
	authInvalidationConnected atomic.Bool
	authInvalidationFailures  atomic.Uint64
	lastUsedTouchL1           sync.Map // keyID -> nextAllowedAt(time.Time)
	lastUsedTouchSF           singleflight.Group
}

type APIKeyAuthLookupMetrics struct {
	Total    uint64 `json:"total"`
	Rejected uint64 `json:"rejected"`
	InFlight int64  `json:"in_flight"`
	Capacity int    `json:"capacity"`
}

// NewAPIKeyLimitReachedError reports the current key count and configured maximum.
func NewAPIKeyLimitReachedError(current int64, limit int) error {
	return ErrAPIKeyLimitReached.WithMetadata(map[string]string{
		"current": strconv.FormatInt(current, 10),
		"limit":   strconv.Itoa(limit),
	})
}

// IsEmpty reports whether no columns are selected for update.
func (f APIKeyUpdateFields) IsEmpty() bool {
	return f == APIKeyUpdateFields{}
}

func KeyWithAuthCacheSubscriptionReady(ctx context.Context, ready func()) context.Context {
	return context.WithValue(ctx, KeyAuthCacheSubscriptionReadyKey{}, ready)
}

// NotifyAuthCacheSubscriptionReady invokes the callback after the cache server confirms subscription.
func NotifyAuthCacheSubscriptionReady(ctx context.Context) {
	if ready, ok := ctx.Value(KeyAuthCacheSubscriptionReadyKey{}).(func()); ok && ready != nil {
		ready()
	}
}

// ValidateAPIKeyLimit checks that a quota or spending limit is finite, nonnegative, and below the DECIMAL(20,8) bound.
func ValidateAPIKeyLimit(field string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value >= KeyApiKeyLimitUpperBound {
		return ErrAPIKeyLimitInvalid.WithMetadata(map[string]string{
			"field":         field,
			"max_exclusive": strconv.FormatFloat(KeyApiKeyLimitUpperBound, 'f', 0, 64),
		})
	}
	return nil
}

// ValidateAPIKeyExpiresInDays requires a positive validity period.
func ValidateAPIKeyExpiresInDays(days int) error {
	if days <= 0 {
		return ErrAPIKeyExpiryInvalid
	}
	return nil
}

func KeyValidateCreateAPIKeyRequest(req CreateAPIKeyRequest) error {
	if err := ValidateRequestLimits(&req.ConcurrencyLimit, &req.RPMLimit); err != nil {
		return err
	}
	limits := []struct {
		field string
		value float64
	}{
		{field: "quota", value: req.Quota},
		{field: "rate_limit_5h", value: req.RateLimit5h},
		{field: "rate_limit_1d", value: req.RateLimit1d},
		{field: "rate_limit_7d", value: req.RateLimit7d},
	}
	for _, limit := range limits {
		if err := ValidateAPIKeyLimit(limit.field, limit.value); err != nil {
			return err
		}
	}
	if req.ExpiresInDays != nil {
		return ValidateAPIKeyExpiresInDays(*req.ExpiresInDays)
	}
	return nil
}

func KeyValidateUpdateAPIKeyRequest(req UpdateAPIKeyRequest) error {
	if err := ValidateRequestLimits(req.ConcurrencyLimit, req.RPMLimit); err != nil {
		return err
	}
	limits := []struct {
		field string
		value *float64
	}{
		{field: "quota", value: req.Quota},
		{field: "rate_limit_5h", value: req.RateLimit5h},
		{field: "rate_limit_1d", value: req.RateLimit1d},
		{field: "rate_limit_7d", value: req.RateLimit7d},
	}
	for _, limit := range limits {
		if limit.value == nil {
			continue
		}
		if err := ValidateAPIKeyLimit(limit.field, *limit.value); err != nil {
			return err
		}
	}
	return nil
}

// NewAPIKeyService constructs the key service; call Start after binding dependencies.
func NewAPIKeyService(
	apiKeyRepo APIKeyRepository,
	userRepo UserRepository,
	groupRepo GroupRepository,
	userSubRepo UserSubscriptionRepository,
	userGroupRateRepo UserGroupRateRepository,
	cache APIKeyCache,
	cfg *Options,
) *APIKeyService {
	calendar := timezone.NewCalendar(time.Local)
	if cfg != nil {
		calendar = timezone.NewCalendar(cfg.Calendar.Location())
	}
	svc := &APIKeyService{
		calendar:          calendar,
		apiKeyRepo:        apiKeyRepo,
		userRepo:          userRepo,
		groupRepo:         groupRepo,
		userSubRepo:       userSubRepo,
		userGroupRateRepo: userGroupRateRepo,
		cache:             cache,
		cfg:               cfg,
	}
	svc.authCfg = KeyNewAPIKeyAuthCacheConfig(cfg)
	lookupConcurrency := KeyDefaultAuthLookupConcurrency
	if cfg != nil && cfg.APIKeyAuth.LookupConcurrency > 0 {
		lookupConcurrency = cfg.APIKeyAuth.LookupConcurrency
	}
	svc.authLookupSlots = make(chan struct{}, lookupConcurrency)
	svc.invalidAuthAbuse = KeyNewInvalidAuthAbuseLimiter(cfg)
	return svc
}

func (s *APIKeyService) AuthLookupMetrics() APIKeyAuthLookupMetrics {
	if s == nil {
		return APIKeyAuthLookupMetrics{}
	}
	return APIKeyAuthLookupMetrics{
		Total:    s.authLookupTotal.Load(),
		Rejected: s.authLookupRejected.Load(),
		InFlight: s.authLookupInFlight.Load(),
		Capacity: cap(s.authLookupSlots),
	}
}

// SetRateLimitCacheInvalidator sets the optional rate limit cache invalidator.
// Called after construction (e.g. in wire) to avoid circular dependencies.
func (s *APIKeyService) SetRateLimitCacheInvalidator(inv RateLimitCacheInvalidator) {
	s.rateLimitCacheInvalid = inv
}

// SetConcurrencyService supplies live request counts for keys.
func (s *APIKeyService) SetConcurrencyService(concurrencyService ConcurrencyReader) {
	s.concurrencyService = concurrencyService
}

// SetTeamRepository supplies team context lookups.
func (s *APIKeyService) SetTeamRepository(repo TeamRepository) {
	s.teamRepo = repo
}

func (s *APIKeyService) KeyCompileAPIKeyIPRules(apiKey *APIKey) {
	if apiKey == nil {
		return
	}
	apiKey.CompiledIPWhitelist = ipmatch.CompileIPRules(apiKey.IPWhitelist)
	apiKey.CompiledIPBlacklist = ipmatch.CompileIPRules(apiKey.IPBlacklist)
}

// GenerateKey creates a random credential using the configured prefix.
func (s *APIKeyService) GenerateKey() (string, error) {
	prefix := s.cfg.Default.APIKeyPrefix
	if prefix == "" {
		prefix = "sk-"
	}
	return GenerateAPIKeyString(prefix)
}

// GenerateAPIKeyString creates a prefixed random credential for ordinary and managed keys.
func GenerateAPIKeyString(prefix string) (string, error) {
	// Use 32 cryptographically random bytes before hex encoding.
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	if strings.TrimSpace(prefix) == "" {
		prefix = "sk-"
	}

	key := prefix + hex.EncodeToString(bytes)
	return key, nil
}

// ValidateCustomKey requires at least 16 bytes drawn from the accepted ASCII characters.
func (s *APIKeyService) ValidateCustomKey(key string) error {
	// Check minimum length before character validation.
	if len(key) < 16 {
		return ErrAPIKeyTooShort
	}

	// Accept ASCII letters, digits, underscores, and hyphens.
	for _, c := range key {
		if (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '_' || c == '-' {
			continue
		}
		return ErrAPIKeyInvalidChars
	}

	return nil
}

// KeyCheckAPIKeyRateLimit rejects excessive custom-credential creation failures.
func (s *APIKeyService) KeyCheckAPIKeyRateLimit(ctx context.Context, userID int64) error {
	if s.cache == nil {
		return nil
	}

	count, err := s.cache.GetCreateAttemptCount(ctx, userID)
	if err != nil {
		// Cache errors do not block key creation.
		return nil
	}

	if count >= KeyApiKeyMaxErrorsPerHour {
		return ErrAPIKeyRateLimited
	}

	return nil
}

// KeyIncrementAPIKeyErrorCount records a failed custom-credential creation attempt.
func (s *APIKeyService) KeyIncrementAPIKeyErrorCount(ctx context.Context, userID int64) {
	if s.cache == nil {
		return
	}

	_ = s.cache.IncrementCreateAttemptCount(ctx, userID)
}

// KeyCanUserBindGroup checks the user's group permissions.
// Exclusive grants and disabled public groups are resolved by User.CanBindGroup.
func (s *APIKeyService) KeyCanUserBindGroup(ctx context.Context, user *User, group *routing.Group) bool {
	return user.CanBindGroup(group.ID, group.IsExclusive)
}

// KeyResolveAPIKeyBillingConfiguration validates the funding mode and selected subscription.
// The subscription must belong to billingUserID; callers pass the team owner for team keys.
func (s *APIKeyService) KeyResolveAPIKeyBillingConfiguration(ctx context.Context, billingUserID int64, rawMode string, preferredSubscriptionID *int64) (string, *int64, *UserSubscription, error) {
	mode, ok := NormalizeAPIKeyBillingMode(rawMode)
	if !ok {
		return "", nil, nil, ErrInvalidAPIKeyBillingMode
	}
	if mode != APIKeyBillingModeSubscription {
		return mode, nil, nil, nil
	}
	if preferredSubscriptionID == nil || *preferredSubscriptionID <= 0 {
		return "", nil, nil, ErrPreferredSubscriptionRequired
	}
	if s == nil || s.userSubRepo == nil || billingUserID <= 0 {
		return "", nil, nil, ErrPreferredSubscriptionInvalid
	}

	subscription, err := s.userSubRepo.GetByID(ctx, *preferredSubscriptionID)
	if err != nil || subscription == nil || subscription.UserID != billingUserID || !subscription.IsEffective() || subscription.Plan == nil {
		return "", nil, nil, ErrPreferredSubscriptionInvalid
	}
	id := subscription.ID
	return mode, &id, subscription, nil
}

// KeyValidatePreferredSubscriptionGroups checks ordinary and composite bindings against the subscription plan.
// Plans without a group restriction defer to the user's group permissions.
func KeyValidatePreferredSubscriptionGroups(subscription *UserSubscription, group *routing.Group, compositeGroups []APIKeyCompositeGroup) error {
	if subscription == nil || subscription.Plan == nil {
		return ErrPreferredSubscriptionInvalid
	}
	// A restricted plan requires at least one concrete group to validate.
	if len(subscription.Plan.GroupIDs) > 0 && (group == nil || group.ID <= 0) && len(compositeGroups) == 0 {
		return ErrPreferredSubscriptionGroup
	}
	if group != nil && group.ID > 0 && !subscriptionPlanIncludesGroup(subscription.Plan, group.ID) {
		return ErrPreferredSubscriptionGroup
	}
	for _, binding := range compositeGroups {
		if binding.GroupID > 0 && !subscriptionPlanIncludesGroup(subscription.Plan, binding.GroupID) {
			return ErrPreferredSubscriptionGroup
		}
	}
	return nil
}

// KeyBillingUserIDForScope resolves the key's payer for the requested scope.
func (s *APIKeyService) KeyBillingUserIDForScope(ctx context.Context, userID int64, scope string) (int64, error) {
	if !strings.EqualFold(strings.TrimSpace(scope), "team") {
		return userID, nil
	}
	if s.cfg != nil && !s.cfg.Team.Enabled {
		return 0, ErrTeamFeatureDisabled
	}
	if s.teamRepo == nil {
		return 0, ErrTeamFeatureDisabled
	}
	teamCtx, err := s.teamRepo.GetContextByUserID(ctx, userID)
	if err != nil {
		return 0, err
	}
	return teamCtx.Owner.UserID, nil
}

// KeyBillingUserForAPIKey resolves the current payer independently of authentication hydration.
// Disabled or owner-locked team keys may lack an owner view in apiKey.User.
func (s *APIKeyService) KeyBillingUserForAPIKey(ctx context.Context, apiKey *APIKey) (*User, error) {
	if apiKey == nil || apiKey.UserID <= 0 || s == nil {
		return nil, ErrUserNotFound
	}
	if apiKey.TeamID == nil {
		if apiKey.User != nil && apiKey.User.ID == apiKey.UserID {
			return apiKey.User, nil
		}
		if s.userRepo == nil {
			return nil, ErrUserNotFound
		}
		return s.userRepo.GetByID(ctx, apiKey.UserID)
	}
	if s.userRepo == nil {
		return nil, ErrUserNotFound
	}
	if s.cfg != nil && !s.cfg.Team.Enabled {
		return nil, ErrTeamFeatureDisabled
	}
	if s.teamRepo == nil {
		return nil, ErrTeamFeatureDisabled
	}
	teamCtx, err := s.teamRepo.GetContextByUserID(ctx, apiKey.UserID)
	if err != nil {
		return nil, err
	}
	if teamCtx == nil || teamCtx.Team == nil || teamCtx.Membership == nil || teamCtx.Owner == nil || teamCtx.Team.ID != *apiKey.TeamID {
		return nil, ErrTeamMembershipRequired
	}
	if teamCtx.Membership.JoinedAt.After(apiKey.CreatedAt) {
		return nil, ErrTeamMembershipRequired
	}
	return s.userRepo.GetByID(ctx, teamCtx.Owner.UserID)
}

// KeyCanUserUseBoundGroup checks composite scope and the payer's current permission for the bound group.
func (s *APIKeyService) KeyCanUserUseBoundGroup(ctx context.Context, apiKey *APIKey) bool {
	if apiKey == nil || apiKey.GroupID == nil || apiKey.Group == nil {
		return true
	}
	if !apiKey.AllowsRuntimeGroup(*apiKey.GroupID) {
		return false
	}
	if apiKey.Group.IsExclusive {
		return true
	}
	user := apiKey.User
	if user == nil {
		return true
	}
	if user.GroupRestrictionsLoaded {
		return user.CanBindGroup(apiKey.Group.ID, apiKey.Group.IsExclusive)
	}
	if s.userRepo == nil {
		return true
	}
	loadedUser, err := s.userRepo.GetByID(ctx, apiKey.UserID)
	if err != nil {
		return true
	}
	return loadedUser.CanBindGroup(apiKey.Group.ID, apiKey.Group.IsExclusive)
}

// Create validates and creates a personal or team key.
func (s *APIKeyService) Create(ctx context.Context, userID int64, req CreateAPIKeyRequest) (*APIKey, error) {
	if !req.IsComposite && (req.GroupID == nil || *req.GroupID <= 0) {
		return nil, infraerrors.BadRequest("GROUP_REQUIRED", "API Key must be assigned to a group")
	}
	if err := KeyValidateCreateAPIKeyRequest(req); err != nil {
		return nil, err
	}
	fastModePolicy, ok := NormalizeAPIKeyFastModePolicy(req.FastModePolicy)
	if !ok {
		return nil, ErrInvalidAPIKeyFastModePolicy
	}
	modelMapping, err := NormalizeAPIKeyModelMapping(req.ModelMapping)
	if err != nil {
		return nil, err
	}

	// Resolve the actor first, then select the payer for the requested key scope.
	actor, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	user := actor
	var teamID *int64
	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	if scope == "" {
		scope = "personal"
	}
	if scope != "personal" && scope != "team" {
		return nil, infraerrors.BadRequest("API_KEY_SCOPE_INVALID", "api key 作用域必须为 personal 或 team")
	}
	if scope == "team" {
		if s.cfg != nil && !s.cfg.Team.Enabled {
			return nil, ErrTeamFeatureDisabled
		}
		if s.teamRepo == nil {
			return nil, ErrTeamFeatureDisabled
		}
		teamCtx, teamErr := s.teamRepo.GetContextByUserID(ctx, userID)
		if teamErr != nil {
			return nil, teamErr
		}
		if teamCtx.Team.Status != TeamStatusActive {
			return nil, ErrTeamSuspended
		}
		user, err = s.userRepo.GetByID(ctx, teamCtx.Owner.UserID)
		if err != nil {
			return nil, fmt.Errorf("get team owner: %w", err)
		}
		id := teamCtx.Team.ID
		teamID = &id
	}

	// Validate funding against the payer; team keys use the team owner's subscriptions.
	billingMode, preferredSubscriptionID, preferredSubscription, err := s.KeyResolveAPIKeyBillingConfiguration(
		ctx,
		user.ID,
		req.BillingMode,
		req.PreferredSubscriptionID,
	)
	if err != nil {
		return nil, err
	}

	// Validate allowlist patterns before persistence.
	if len(req.IPWhitelist) > 0 {
		if invalid := ipmatch.ValidateIPPatterns(req.IPWhitelist); len(invalid) > 0 {
			return nil, fmt.Errorf("%w: %v", ErrInvalidIPPattern, invalid)
		}
	}

	// Validate denylist patterns before persistence.
	if len(req.IPBlacklist) > 0 {
		if invalid := ipmatch.ValidateIPPatterns(req.IPBlacklist); len(invalid) > 0 {
			return nil, fmt.Errorf("%w: %v", ErrInvalidIPPattern, invalid)
		}
	}

	// Check each group binding against the payer's permissions.
	var compositeGroups []APIKeyCompositeGroup
	if req.IsComposite {
		if req.GroupID != nil {
			return nil, ErrCompositeKeyGroupConflict
		}
		compositeGroups, err = s.KeyPrepareCompositeGroups(ctx, user, req.CompositeGroups)
		if err != nil {
			return nil, err
		}
	} else if req.GroupID != nil {
		group, err := s.groupRepo.GetByID(ctx, *req.GroupID)
		if err != nil {
			return nil, fmt.Errorf("get group: %w", err)
		}

		// The payer must be allowed to bind the requested group.
		if !s.KeyCanUserBindGroup(ctx, user, group) {
			return nil, ErrGroupNotAllowed
		}
	}
	if billingMode == APIKeyBillingModeSubscription {
		var group *routing.Group
		if req.GroupID != nil {
			group, err = s.groupRepo.GetByID(ctx, *req.GroupID)
			if err != nil {
				return nil, fmt.Errorf("get group: %w", err)
			}
		}
		if err := KeyValidatePreferredSubscriptionGroups(preferredSubscription, group, compositeGroups); err != nil {
			return nil, err
		}
	}

	var key string

	// Custom credentials require duplicate and failed-attempt checks.
	if req.CustomKey != nil && *req.CustomKey != "" {
		// Apply creation-attempt limits to custom credentials.
		if err := s.KeyCheckAPIKeyRateLimit(ctx, userID); err != nil {
			return nil, err
		}

		// Reject invalid custom credential syntax before querying for duplicates.
		if err := s.ValidateCustomKey(*req.CustomKey); err != nil {
			return nil, err
		}

		// Check active credential uniqueness.
		exists, err := s.apiKeyRepo.ExistsByKey(ctx, *req.CustomKey)
		if err != nil {
			return nil, fmt.Errorf("check key exists: %w", err)
		}
		if exists {
			// A duplicate credential counts toward the creation-attempt limit.
			s.KeyIncrementAPIKeyErrorCount(ctx, userID)
			return nil, ErrAPIKeyExists
		}

		key = *req.CustomKey
	} else {
		// Generate a credential when the caller did not supply one.
		var err error
		key, err = s.GenerateKey()
		if err != nil {
			return nil, fmt.Errorf("generate key: %w", err)
		}
	}

	// Unavailable-group fallback defaults to enabled; callers can disable it.
	fallbackWhenGroupUnavailable := true
	if req.FallbackWhenGroupUnavailable != nil {
		fallbackWhenGroupUnavailable = *req.FallbackWhenGroupUnavailable
	}

	// Persist the actor as owner and the resolved team scope.
	apiKey := &APIKey{
		UserID:                       userID,
		TeamID:                       teamID,
		Key:                          key,
		Name:                         html.EscapeString(req.Name),
		GroupID:                      req.GroupID,
		IsComposite:                  req.IsComposite,
		CompositeGroups:              compositeGroups,
		Status:                       StatusActive,
		FastModePolicy:               fastModePolicy,
		BillingMode:                  billingMode,
		PreferredSubscriptionID:      preferredSubscriptionID,
		ModelMapping:                 modelMapping,
		IPWhitelist:                  req.IPWhitelist,
		IPBlacklist:                  req.IPBlacklist,
		Quota:                        req.Quota,
		QuotaUsed:                    0,
		ConcurrencyLimit:             req.ConcurrencyLimit,
		RPMLimit:                     req.RPMLimit,
		RateLimit5h:                  req.RateLimit5h,
		RateLimit1d:                  req.RateLimit1d,
		RateLimit7d:                  req.RateLimit7d,
		FallbackWhenGroupUnavailable: fallbackWhenGroupUnavailable,
	}
	apiKey.ActorUser = actor
	apiKey.User = user

	// Set expiration time if specified
	if req.ExpiresInDays != nil && *req.ExpiresInDays > 0 {
		expiresAt := s.now().AddDate(0, 0, *req.ExpiresInDays)
		apiKey.ExpiresAt = &expiresAt
	}

	if err := s.apiKeyRepo.Create(ctx, apiKey); err != nil {
		return nil, fmt.Errorf("create api key: %w", err)
	}

	s.InvalidateAuthCacheByKey(ctx, apiKey.Key)
	s.KeyCompileAPIKeyIPRules(apiKey)

	return apiKey, nil
}

// List returns the user's filtered keys and current concurrency counts.
func (s *APIKeyService) List(ctx context.Context, userID int64, params pagination.PaginationParams, filters APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error) {
	keys, pagination, err := s.apiKeyRepo.ListByUserID(ctx, userID, params, filters)
	if err != nil {
		return nil, nil, fmt.Errorf("list api keys: %w", err)
	}
	s.KeyFillCurrentConcurrency(ctx, keys)
	return keys, pagination, nil
}

func (s *APIKeyService) KeyFillCurrentConcurrency(ctx context.Context, keys []APIKey) {
	if s == nil || s.concurrencyService == nil || len(keys) == 0 {
		return
	}
	ids := make([]int64, 0, len(keys))
	for i := range keys {
		if keys[i].ID > 0 {
			ids = append(ids, keys[i].ID)
		}
	}
	counts, err := s.concurrencyService.GetAPIKeyConcurrencyBatch(ctx, ids)
	if err != nil {
		return
	}
	for i := range keys {
		keys[i].CurrentConcurrency = counts[keys[i].ID]
	}
}

func (s *APIKeyService) KeyCurrentConcurrencyForAPIKey(ctx context.Context, apiKeyID int64) int {
	if s == nil || s.concurrencyService == nil || apiKeyID <= 0 {
		return 0
	}
	counts, err := s.concurrencyService.GetAPIKeyConcurrencyBatch(ctx, []int64{apiKeyID})
	if err != nil {
		return 0
	}
	return counts[apiKeyID]
}

func (s *APIKeyService) VerifyOwnership(ctx context.Context, userID int64, apiKeyIDs []int64) ([]int64, error) {
	if len(apiKeyIDs) == 0 {
		return []int64{}, nil
	}

	validIDs, err := s.apiKeyRepo.VerifyOwnership(ctx, userID, apiKeyIDs)
	if err != nil {
		return nil, fmt.Errorf("verify api key ownership: %w", err)
	}
	return validIDs, nil
}

// GetByID loads a visible key, compiles IP rules, and fills live concurrency.
func (s *APIKeyService) GetByID(ctx context.Context, id int64) (*APIKey, error) {
	apiKey, err := s.apiKeyRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}
	if apiKey != nil && apiKey.ManagedBy != nil {
		// Treat hidden managed keys as missing in user-facing reads.
		return nil, fmt.Errorf("get api key: %w", ErrAPIKeyNotFound)
	}
	s.KeyCompileAPIKeyIPRules(apiKey)
	if apiKey != nil {
		apiKey.CurrentConcurrency = s.KeyCurrentConcurrencyForAPIKey(ctx, apiKey.ID)
	}
	return apiKey, nil
}

// GetByKey loads a credential for authentication using the configured caches.
func (s *APIKeyService) GetByKey(ctx context.Context, key string) (*APIKey, error) {
	if key == "" || len(key) > MaxAPIKeyCredentialBytes {
		return nil, ErrAPIKeyNotFound
	}
	// A nil service cannot resolve credentials.
	if s == nil {
		return nil, fmt.Errorf("get api key: %w", ErrAPIKeyNotFound)
	}
	if !s.operations.enter() {
		return nil, ErrAuthenticationStopped
	}
	defer s.operations.leave()
	cacheKey := s.KeyAuthCacheKey(key)

	if entry, ok := s.KeyGetAuthCacheEntry(ctx, cacheKey); ok {
		if apiKey, used, err := s.KeyApplyAuthCacheEntry(key, entry); used {
			if err != nil {
				return nil, fmt.Errorf("get api key: %w", err)
			}
			if !apiKey.IsComposite {
				apiKey = s.KeyApplyExplicitGroupFallback(ctx, apiKey)
				if !s.KeyCanUserUseBoundGroup(ctx, apiKey) {
					return nil, fmt.Errorf("get api key: %w", ErrGroupDisabledForUser)
				}
			}
			s.KeyCompileAPIKeyIPRules(apiKey)
			return apiKey, nil
		}
	}

	if s.authCfg.singleflight {
		value, err, _ := s.authGroup.Do(cacheKey, func() (any, error) {
			return s.KeyLoadAuthCacheEntry(ctx, key, cacheKey)
		})
		if err != nil {
			return nil, err
		}
		entry, _ := value.(*APIKeyAuthCacheEntry)
		if apiKey, used, err := s.KeyApplyAuthCacheEntry(key, entry); used {
			if err != nil {
				return nil, fmt.Errorf("get api key: %w", err)
			}
			if !apiKey.IsComposite {
				apiKey = s.KeyApplyExplicitGroupFallback(ctx, apiKey)
				if !s.KeyCanUserUseBoundGroup(ctx, apiKey) {
					return nil, fmt.Errorf("get api key: %w", ErrGroupDisabledForUser)
				}
			}
			s.KeyCompileAPIKeyIPRules(apiKey)
			return apiKey, nil
		}
	} else {
		entry, err := s.KeyLoadAuthCacheEntry(ctx, key, cacheKey)
		if err != nil {
			return nil, err
		}
		if apiKey, used, err := s.KeyApplyAuthCacheEntry(key, entry); used {
			if err != nil {
				return nil, fmt.Errorf("get api key: %w", err)
			}
			if !apiKey.IsComposite {
				apiKey = s.KeyApplyExplicitGroupFallback(ctx, apiKey)
				if !s.KeyCanUserUseBoundGroup(ctx, apiKey) {
					return nil, fmt.Errorf("get api key: %w", ErrGroupDisabledForUser)
				}
			}
			s.KeyCompileAPIKeyIPRules(apiKey)
			return apiKey, nil
		}
	}

	apiKey, err := s.KeyLookupAPIKeyForAuth(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}
	apiKey.Key = key
	if !apiKey.IsComposite {
		apiKey = s.KeyApplyExplicitGroupFallback(ctx, apiKey)
		if !s.KeyCanUserUseBoundGroup(ctx, apiKey) {
			return nil, fmt.Errorf("get api key: %w", ErrGroupDisabledForUser)
		}
	}
	s.KeyCompileAPIKeyIPRules(apiKey)
	return apiKey, nil
}

// SelectCompositeGroupForRequest creates a group-specific request view without mutating the cached key.
func (s *APIKeyService) SelectCompositeGroupForRequest(ctx context.Context, apiKey *APIKey, binding *APIKeyCompositeGroup) (*APIKey, error) {
	if apiKey == nil || !apiKey.IsComposite || binding == nil || binding.Group == nil {
		return nil, ErrCompositeKeyPrefixNotFound
	}
	selected := *apiKey
	selected.CompositeGroups = KeyCloneCompositeBindings(apiKey.CompositeGroups)
	groupID := binding.GroupID
	selected.GroupID = &groupID
	selected.Group = binding.Group
	if apiKey.User != nil {
		userCopy := *apiKey.User
		userCopy.UserGroupRPMOverride = binding.UserGroupRPMOverride
		selected.User = &userCopy
	}
	prepared := s.KeyApplyExplicitGroupFallback(ctx, &selected)
	if !s.KeyCanUserUseBoundGroup(ctx, prepared) {
		return nil, ErrGroupDisabledForUser
	}
	return prepared, nil
}

// KeyApplyExplicitGroupFallback resolves the configured unavailable-group target; unbound keys stay unbound.
func (s *APIKeyService) KeyApplyExplicitGroupFallback(ctx context.Context, apiKey *APIKey) *APIKey {
	if apiKey == nil || s.groupRepo == nil || apiKey.GroupID == nil || apiKey.Group == nil || !apiKey.FallbackWhenGroupUnavailable {
		return apiKey
	}
	current := apiKey.Group
	if current.IsActive() || strings.EqualFold(current.Status, "deleted") {
		return apiKey
	}
	targetID := current.UnavailableFallbackGroupID
	if targetID == nil || *targetID <= 0 || *targetID == current.ID {
		return apiKey
	}
	resolved, err := s.ResolveRuntimeGroup(ctx, apiKey, *targetID)
	if err != nil {
		return apiKey
	}
	// Later subscription and protocol admission uses the resolved group.
	return resolved
}

// KeyRefreshFallbackUserGroupRPMOverride reloads the user RPM limit after a group change.
// The cached override belongs to the previous group and cannot be reused.
func (s *APIKeyService) KeyRefreshFallbackUserGroupRPMOverride(ctx context.Context, apiKey *APIKey, groupID int64) {
	if apiKey == nil || apiKey.User == nil {
		return
	}
	apiKey.User.UserGroupRPMOverride = nil
	if s.userGroupRateRepo == nil || apiKey.User.ID <= 0 || groupID <= 0 {
		return
	}
	override, err := s.userGroupRateRepo.GetRPMOverrideByUserAndGroup(ctx, apiKey.User.ID, groupID)
	if err == nil {
		apiKey.User.UserGroupRPMOverride = override
	}
}

// Update validates and applies the selected changes to an owned key.
func (s *APIKeyService) Update(ctx context.Context, id int64, userID int64, req UpdateAPIKeyRequest) (*APIKey, error) {
	if err := KeyValidateUpdateAPIKeyRequest(req); err != nil {
		return nil, err
	}
	apiKey, err := s.apiKeyRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}
	if apiKey != nil && apiKey.ManagedBy != nil {
		// Hide managed keys from user mutation endpoints.
		return nil, fmt.Errorf("get api key: %w", ErrAPIKeyNotFound)
	}

	// Only the credential owner may update the key.
	if apiKey.UserID != userID {
		return nil, ErrInsufficientPerms
	}
	if apiKey.TeamID != nil {
		apiKey, err = s.KeyHydrateTeamAPIKey(ctx, apiKey, nil)
		if err != nil {
			return nil, err
		}
	}

	// Validate supplied allowlist patterns.
	if req.IPWhitelist != nil && len(*req.IPWhitelist) > 0 {
		if invalid := ipmatch.ValidateIPPatterns(*req.IPWhitelist); len(invalid) > 0 {
			return nil, fmt.Errorf("%w: %v", ErrInvalidIPPattern, invalid)
		}
	}

	// Validate supplied denylist patterns.
	if req.IPBlacklist != nil && len(*req.IPBlacklist) > 0 {
		if invalid := ipmatch.ValidateIPPatterns(*req.IPBlacklist); len(invalid) > 0 {
			return nil, fmt.Errorf("%w: %v", ErrInvalidIPPattern, invalid)
		}
	}
	if req.ModelMapping != nil {
		modelMapping, normalizeErr := NormalizeAPIKeyModelMapping(*req.ModelMapping)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		apiKey.ModelMapping = modelMapping
	}

	// Select columns for this update while billing increments usage atomically.
	// Include usage columns only when the request asks to reset them.
	var fields APIKeyUpdateFields
	// Quota and expiration changes can reactivate the key.
	// Compare the final status with this value to select the status column when needed.
	originalStatus := apiKey.Status
	originalIsComposite := apiKey.IsComposite

	// Resolve the final funding configuration before checking ordinary or composite group bindings.
	targetBillingMode := APIKeyEffectiveBillingMode(apiKey)
	targetPreferredSubscriptionID := apiKey.PreferredSubscriptionID
	billingConfigurationRequested := req.BillingMode != nil || req.PreferredSubscriptionID != nil
	var billingUser *User
	var preferredSubscription *UserSubscription
	if billingConfigurationRequested {
		billingUser, err = s.KeyBillingUserForAPIKey(ctx, apiKey)
		if err != nil {
			return nil, fmt.Errorf("get billing user: %w", err)
		}
		apiKey.User = billingUser
		if req.BillingMode != nil {
			normalizedMode, ok := NormalizeAPIKeyBillingMode(*req.BillingMode)
			if !ok {
				return nil, ErrInvalidAPIKeyBillingMode
			}
			targetBillingMode = normalizedMode
		}
		if req.PreferredSubscriptionID != nil {
			targetPreferredSubscriptionID = req.PreferredSubscriptionID
		}
		if targetBillingMode != APIKeyBillingModeSubscription {
			targetPreferredSubscriptionID = nil
		} else if req.BillingMode != nil && req.PreferredSubscriptionID == nil && apiKey.PreferredSubscriptionID == nil {
			return nil, ErrPreferredSubscriptionRequired
		}

		resolvedMode, resolvedSubscriptionID, resolvedSubscription, resolveErr := s.KeyResolveAPIKeyBillingConfiguration(
			ctx,
			billingUser.ID,
			targetBillingMode,
			targetPreferredSubscriptionID,
		)
		if resolveErr != nil {
			return nil, resolveErr
		}
		targetBillingMode = resolvedMode
		targetPreferredSubscriptionID = resolvedSubscriptionID
		preferredSubscription = resolvedSubscription
		apiKey.BillingMode = targetBillingMode
		apiKey.PreferredSubscriptionID = targetPreferredSubscriptionID
		fields.BillingConfiguration = true
	}

	// Apply supplied configuration fields and record their write masks.
	if req.Name != nil {
		apiKey.Name = html.EscapeString(*req.Name)
		fields.Name = true
	}
	if req.FastModePolicy != nil {
		fastModePolicy, ok := NormalizeAPIKeyFastModePolicy(*req.FastModePolicy)
		if !ok {
			return nil, ErrInvalidAPIKeyFastModePolicy
		}
		apiKey.FastModePolicy = fastModePolicy
		fields.FastModePolicy = true
	}
	if req.ModelMapping != nil {
		fields.ModelMapping = true
	}

	targetComposite := apiKey.IsComposite
	if req.IsComposite != nil {
		targetComposite = *req.IsComposite
	}

	// Validate type changes and complete mapping replacements before writing.
	if targetComposite {
		if req.GroupID != nil {
			return nil, ErrCompositeKeyGroupConflict
		}
		if !apiKey.IsComposite && req.CompositeGroups == nil {
			return nil, ErrCompositeKeyGroupsRequired
		}
		if req.CompositeGroups != nil {
			billingUserID := userID
			if apiKey.TeamID != nil {
				if s.teamRepo == nil {
					return nil, ErrTeamFeatureDisabled
				}
				teamCtx, teamErr := s.teamRepo.GetContextByUserID(ctx, userID)
				if teamErr != nil || teamCtx.Team.ID != *apiKey.TeamID {
					return nil, ErrTeamMembershipRequired
				}
				billingUserID = teamCtx.Owner.UserID
			}
			user, err := s.userRepo.GetByID(ctx, billingUserID)
			if err != nil {
				return nil, fmt.Errorf("get user: %w", err)
			}
			bindings, err := s.KeyPrepareCompositeGroups(ctx, user, *req.CompositeGroups)
			if err != nil {
				return nil, err
			}
			apiKey.CompositeGroups = bindings
			apiKey.User = user
			fields.CompositeConfiguration = true
		}
		apiKey.IsComposite = true
		apiKey.GroupID = nil
		apiKey.Group = nil
	} else if apiKey.IsComposite {
		if req.GroupID == nil || *req.GroupID <= 0 {
			return nil, ErrCompositeKeyTargetRequired
		}
		apiKey.IsComposite = false
		apiKey.CompositeGroups = nil
	}
	if apiKey.IsComposite != originalIsComposite {
		fields.CompositeConfiguration = true
		fields.GroupID = true
	}

	if !targetComposite && req.GroupID != nil {
		// Resolve the payer before checking access to the new ordinary group.
		billingUserID := userID
		if apiKey.TeamID != nil {
			if s.teamRepo == nil {
				return nil, ErrTeamFeatureDisabled
			}
			teamCtx, teamErr := s.teamRepo.GetContextByUserID(ctx, userID)
			if teamErr != nil || teamCtx.Team.ID != *apiKey.TeamID {
				return nil, ErrTeamMembershipRequired
			}
			billingUserID = teamCtx.Owner.UserID
		}
		user, err := s.userRepo.GetByID(ctx, billingUserID)
		if err != nil {
			return nil, fmt.Errorf("get user: %w", err)
		}

		group, err := s.groupRepo.GetByID(ctx, *req.GroupID)
		if err != nil {
			return nil, fmt.Errorf("get group: %w", err)
		}

		if !s.KeyCanUserBindGroup(ctx, user, group) {
			return nil, ErrGroupNotAllowed
		}

		apiKey.GroupID = req.GroupID
		apiKey.Group = group
		apiKey.User = user
		fields.GroupID = true
	}
	if !apiKey.IsComposite && !s.KeyCanUserUseBoundGroup(ctx, apiKey) {
		return nil, ErrGroupDisabledForUser
	}
	// Funding or binding changes require all final groups to fit the selected subscription plan.
	if targetBillingMode == APIKeyBillingModeSubscription && (billingConfigurationRequested || req.GroupID != nil || req.CompositeGroups != nil || req.IsComposite != nil) {
		if billingUser == nil {
			billingUser, err = s.KeyBillingUserForAPIKey(ctx, apiKey)
			if err != nil {
				return nil, fmt.Errorf("get billing user: %w", err)
			}
			apiKey.User = billingUser
		}
		if preferredSubscription == nil {
			_, _, preferredSubscription, err = s.KeyResolveAPIKeyBillingConfiguration(
				ctx,
				billingUser.ID,
				targetBillingMode,
				targetPreferredSubscriptionID,
			)
			if err != nil {
				return nil, err
			}
		}
		if err := KeyValidatePreferredSubscriptionGroups(preferredSubscription, apiKey.Group, apiKey.CompositeGroups); err != nil {
			return nil, err
		}
	}

	if req.Status != nil {
		if apiKey.TeamOwnerDisabled && *req.Status == StatusAPIKeyActive {
			return nil, ErrInsufficientPerms
		}
		apiKey.Status = *req.Status
		fields.Status = true
		// A supplied status clears the custom-credential creation-attempt counter.
		if s.cache != nil {
			_ = s.cache.DeleteCreateAttemptCount(ctx, apiKey.UserID)
		}
	}

	// Update quota fields
	if req.Quota != nil {
		apiKey.Quota = *req.Quota
		fields.Quota = true
		// Reactivate a quota-exhausted key when the new quota has room or becomes unlimited.
		if apiKey.Status == StatusAPIKeyQuotaExhausted && (*req.Quota <= 0 || *req.Quota > apiKey.QuotaUsed) {
			apiKey.Status = StatusActive
		}
	}
	if req.ResetQuota != nil && *req.ResetQuota {
		apiKey.QuotaUsed = 0
		fields.QuotaUsed = true
		// If resetting quota and status was quota_exhausted, reactivate
		if apiKey.Status == StatusAPIKeyQuotaExhausted {
			apiKey.Status = StatusActive
		}
	}
	if req.ClearExpiration {
		apiKey.ExpiresAt = nil
		fields.ExpiresAt = true
		// If clearing expiry and status was expired, reactivate
		if apiKey.Status == StatusAPIKeyExpired {
			apiKey.Status = StatusActive
		}
	} else if req.ExpiresAt != nil {
		apiKey.ExpiresAt = req.ExpiresAt
		fields.ExpiresAt = true
		// If extending expiry and status was expired, reactivate
		if apiKey.Status == StatusAPIKeyExpired && s.now().Before(*req.ExpiresAt) {
			apiKey.Status = StatusActive
		}
	}

	// Nil leaves an IP rule set unchanged; an empty slice clears it.
	if req.IPWhitelist != nil {
		apiKey.IPWhitelist = *req.IPWhitelist
		fields.IPRules = true
	}
	if req.IPBlacklist != nil {
		apiKey.IPBlacklist = *req.IPBlacklist
		fields.IPRules = true
	}

	// Update rate limit configuration
	if req.ConcurrencyLimit != nil {
		apiKey.ConcurrencyLimit = *req.ConcurrencyLimit
		fields.ConcurrencyLimit = true
	}
	if req.RPMLimit != nil {
		apiKey.RPMLimit = *req.RPMLimit
		fields.RPMLimit = true
	}
	if req.RateLimit5h != nil {
		apiKey.RateLimit5h = *req.RateLimit5h
		fields.RateLimits = true
	}
	if req.RateLimit1d != nil {
		apiKey.RateLimit1d = *req.RateLimit1d
		fields.RateLimits = true
	}
	if req.RateLimit7d != nil {
		apiKey.RateLimit7d = *req.RateLimit7d
		fields.RateLimits = true
	}
	if req.FallbackWhenGroupUnavailable != nil {
		apiKey.FallbackWhenGroupUnavailable = *req.FallbackWhenGroupUnavailable
		fields.FallbackWhenGroupUnavailable = true
	}
	resetRateLimit := req.ResetRateLimitUsage != nil && *req.ResetRateLimitUsage
	if resetRateLimit {
		apiKey.Usage5h = 0
		apiKey.Usage1d = 0
		apiKey.Usage7d = 0
		apiKey.Window5hStart = nil
		apiKey.Window1dStart = nil
		apiKey.Window7dStart = nil
		fields.RateLimitUsage = true
	}

	// Persist status changes caused by quota or expiration updates.
	if apiKey.Status != originalStatus {
		fields.Status = true
	}

	if err := s.apiKeyRepo.Update(ctx, apiKey, fields); err != nil {
		return nil, fmt.Errorf("update api key: %w", err)
	}

	s.InvalidateAuthCacheByKey(ctx, apiKey.Key)
	s.KeyCompileAPIKeyIPRules(apiKey)

	// Invalidate Redis rate limit cache so reset takes effect immediately
	if resetRateLimit && s.rateLimitCacheInvalid != nil {
		_ = s.rateLimitCacheInvalid.InvalidateAPIKeyRateLimit(ctx, apiKey.ID)
	}

	return apiKey, nil
}

// Delete soft-deletes an owned key and invalidates its cached state.
func (s *APIKeyService) Delete(ctx context.Context, id int64, userID int64) error {
	existing, err := s.apiKeyRepo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get api key: %w", err)
	}
	if existing != nil && existing.ManagedBy != nil {
		// Treat hidden managed keys as missing on user-facing deletion.
		return fmt.Errorf("get api key: %w", ErrAPIKeyNotFound)
	}

	// Require ownership before deletion.
	if existing == nil || existing.UserID != userID {
		return ErrInsufficientPerms
	}

	// Atomically replace the credential with a tombstone and soft-delete the key.
	if err := s.apiKeyRepo.DeleteWithAudit(ctx, id); err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}

	// Invalidate cached state after successful database deletion.
	if s.cache != nil {
		_ = s.cache.DeleteCreateAttemptCount(ctx, userID)
	}
	s.InvalidateAuthCacheByKey(ctx, existing.Key)
	s.lastUsedTouchL1.Delete(id)

	return nil
}

// ValidateTeamKeyLifecycle checks that the key still belongs to an active team relationship.
func (s *APIKeyService) ValidateTeamKeyLifecycle(apiKey *APIKey) error {
	if apiKey == nil || apiKey.TeamID == nil {
		return nil
	}
	if s != nil && s.cfg != nil && !s.cfg.Team.Enabled {
		return ErrTeamFeatureDisabled
	}
	if apiKey.Team == nil || apiKey.TeamMembership == nil || apiKey.Team.ID != *apiKey.TeamID || apiKey.TeamMembership.TeamID != *apiKey.TeamID {
		return ErrTeamMembershipRequired
	}
	if apiKey.TeamMembership.UserID != apiKey.UserID || apiKey.TeamMembership.JoinedAt.After(apiKey.CreatedAt) {
		return ErrTeamMembershipRequired
	}
	if apiKey.Team.Status != TeamStatusActive {
		return ErrTeamSuspended
	}
	if apiKey.ActorUser == nil || !apiKey.ActorUser.IsActive() {
		return ErrTeamActorInactive
	}
	if apiKey.User == nil || !apiKey.User.IsActive() {
		return ErrTeamBillingOwnerInactive
	}
	return nil
}

// CheckTeamMemberLimits checks calendar-period limits from the authentication snapshot.
func (s *APIKeyService) CheckTeamMemberLimits(apiKey *APIKey) error {
	if err := s.ValidateTeamKeyLifecycle(apiKey); err != nil {
		return err
	}
	return KeyCheckTeamMemberLimitSnapshot(apiKey.TeamMembership)
}

// KeyCheckTeamMemberLimitSnapshot exempts owners and delegates other member limits to billing.
func KeyCheckTeamMemberLimitSnapshot(member *TeamMembership) error {
	if member == nil || member.Role == TeamRoleOwner {
		return nil
	}
	return billing.CheckMemberQuotaSnapshot(billing.MemberQuotaSnapshot{DailyLimitUSD: member.DailyLimitUSD, WeeklyLimitUSD: member.WeeklyLimitUSD, MonthlyLimitUSD: member.MonthlyLimitUSD, DailyUsageUSD: member.DailyUsageUSD, WeeklyUsageUSD: member.WeeklyUsageUSD, MonthlyUsageUSD: member.MonthlyUsageUSD})
}

// TouchLastUsed throttles writes to api_keys.last_used_at and coalesces concurrent touches.
// The write is synchronous; callers may treat failures as best effort.
func (s *APIKeyService) TouchLastUsed(ctx context.Context, keyID int64) error {
	if keyID <= 0 {
		return nil
	}

	if !s.operations.enter() {
		return ErrAuthenticationStopped
	}
	defer s.operations.leave()

	now := s.now()
	if v, ok := s.lastUsedTouchL1.Load(keyID); ok {
		if nextAllowedAt, ok := v.(time.Time); ok && now.Before(nextAllowedAt) {
			return nil
		}
	}

	_, err, _ := s.lastUsedTouchSF.Do(strconv.FormatInt(keyID, 10), func() (any, error) {
		latest := s.now()
		if v, ok := s.lastUsedTouchL1.Load(keyID); ok {
			if nextAllowedAt, ok := v.(time.Time); ok && latest.Before(nextAllowedAt) {
				return nil, nil
			}
		}

		if err := s.apiKeyRepo.UpdateLastUsed(ctx, keyID, latest); err != nil {
			s.lastUsedTouchL1.Store(keyID, latest.Add(KeyApiKeyLastUsedFailBackoff))
			return nil, fmt.Errorf("touch api key last used: %w", err)
		}
		s.lastUsedTouchL1.Store(keyID, latest.Add(KeyApiKeyLastUsedMinTouch))
		return nil, nil
	})
	return err
}

// IncrementUsage increments an optional daily key usage counter.
func (s *APIKeyService) IncrementUsage(ctx context.Context, keyID int64) error {
	// Bucket the Redis counter by the configured calendar date.
	if s.cache != nil {
		cacheKey := fmt.Sprintf("apikey:usage:%d:%s", keyID, s.calendar.Now().Format("2006-01-02"))
		if err := s.cache.IncrementDailyUsage(ctx, cacheKey); err != nil {
			return fmt.Errorf("increment usage: %w", err)
		}
		// Refresh the counter expiry to 24 hours after the increment.
		_ = s.cache.SetDailyUsageExpiry(ctx, cacheKey, 24*time.Hour)
	}
	return nil
}

// GetAvailableGroups lists active groups the user may bind.
// Permission checks use the user's exclusive grants and public-group restrictions.
func (s *APIKeyService) GetAvailableGroups(ctx context.Context, userID int64) ([]routing.Group, error) {
	// Load current user permissions.
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}

	// Inactive groups are not offered for binding.
	allGroups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active groups: %w", err)
	}

	// Apply user permissions to the active group list.
	availableGroups := make([]routing.Group, 0)
	for _, group := range allGroups {
		if s.KeyCanUserBindGroupInternal(user, &group) {
			availableGroups = append(availableGroups, group)
		}
	}

	return availableGroups, nil
}

// GetAvailableGroupsForScopeWithSubscription intersects payer access with the selected plan's groups.
// A nil subscriptionID returns all groups available to the payer.
func (s *APIKeyService) GetAvailableGroupsForScopeWithSubscription(ctx context.Context, userID int64, scope string, subscriptionID *int64) ([]routing.Group, error) {
	billingUserID, err := s.KeyBillingUserIDForScope(ctx, userID, scope)
	if err != nil {
		return nil, err
	}
	groups, err := s.GetAvailableGroups(ctx, billingUserID)
	if err != nil || subscriptionID == nil {
		return groups, err
	}
	_, _, subscription, err := s.KeyResolveAPIKeyBillingConfiguration(ctx, billingUserID, APIKeyBillingModeSubscription, subscriptionID)
	if err != nil {
		return nil, err
	}
	if subscription.Plan == nil || len(subscription.Plan.GroupIDs) == 0 {
		return groups, nil
	}
	filtered := make([]routing.Group, 0, len(groups))
	for i := range groups {
		if subscriptionPlanIncludesGroup(subscription.Plan, groups[i].ID) {
			filtered = append(filtered, groups[i])
		}
	}
	return filtered, nil
}

// ListBillingSubscriptionsForScope lists active subscriptions available to the current payer.
func (s *APIKeyService) ListBillingSubscriptionsForScope(ctx context.Context, userID int64, scope string) ([]APIKeyBillingSubscriptionOption, error) {
	if s == nil || s.userSubRepo == nil {
		return nil, ErrPreferredSubscriptionInvalid
	}
	billingUserID, err := s.KeyBillingUserIDForScope(ctx, userID, scope)
	if err != nil {
		return nil, err
	}
	subscriptions, err := s.userSubRepo.ListActiveByUserID(ctx, billingUserID)
	if err != nil {
		return nil, err
	}
	options := make([]APIKeyBillingSubscriptionOption, 0, len(subscriptions))
	for i := range subscriptions {
		subscription := &subscriptions[i]
		if !subscription.IsEffective() || subscription.Plan == nil {
			continue
		}
		options = append(options, APIKeyBillingSubscriptionOption{
			ID:               subscription.ID,
			PlanID:           subscription.PlanID,
			PlanName:         billing.LocalizePlan(subscription.Plan, locale.FromContext(ctx)).Name,
			ExpiresAt:        subscription.ExpiresAt,
			GroupsRestricted: len(subscription.Plan.GroupIDs) > 0,
			ApplicableGroups: append([]int64(nil), subscription.Plan.GroupIDs...),
		})
	}
	return options, nil
}

// KeyCanUserBindGroupInternal checks the user's binding permission for a group.
func (s *APIKeyService) KeyCanUserBindGroupInternal(user *User, group *routing.Group) bool {
	return user.CanBindGroup(group.ID, group.IsExclusive)
}

func (s *APIKeyService) SearchAPIKeys(ctx context.Context, userID int64, keyword string, limit int) ([]APIKey, error) {
	keys, err := s.apiKeyRepo.SearchAPIKeys(ctx, userID, keyword, limit)
	if err != nil {
		return nil, fmt.Errorf("search api keys: %w", err)
	}
	return keys, nil
}

// GetUserGroupRates returns the user's group-specific rate multipliers.
// The map is keyed by group ID.
func (s *APIKeyService) GetUserGroupRates(ctx context.Context, userID int64) (map[int64]float64, error) {
	if s.userGroupRateRepo == nil {
		return nil, nil
	}
	rates, err := s.userGroupRateRepo.GetByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user group rates: %w", err)
	}
	return rates, nil
}

// GetUserGroupRatesForScope resolves the team owner's multipliers for team key configuration.
func (s *APIKeyService) GetUserGroupRatesForScope(ctx context.Context, userID int64, scope string) (map[int64]float64, error) {
	if !strings.EqualFold(strings.TrimSpace(scope), "team") {
		return s.GetUserGroupRates(ctx, userID)
	}
	if s.cfg != nil && !s.cfg.Team.Enabled {
		return nil, ErrTeamFeatureDisabled
	}
	if s.teamRepo == nil {
		return nil, ErrTeamFeatureDisabled
	}
	teamCtx, err := s.teamRepo.GetContextByUserID(ctx, userID)
	if err != nil || teamCtx == nil || teamCtx.Owner == nil {
		return nil, ErrTeamMembershipRequired
	}
	return s.GetUserGroupRates(ctx, teamCtx.Owner.UserID)
}

// CheckAPIKeyQuotaAndExpiry checks if the API key is valid for use (not expired, quota not exhausted)
// Returns nil if valid, error if invalid.
func (s *APIKeyService) CheckAPIKeyQuotaAndExpiry(apiKey *APIKey) error {
	// Check expiration
	if apiKey.ExpiresAt != nil && apiKey.IsExpiredAt(s.now()) {
		return ErrAPIKeyExpired
	}

	// Check quota
	if apiKey.IsQuotaExhausted() {
		return ErrAPIKeyQuotaExhausted
	}

	return nil
}

// UpdateQuotaUsed updates the quota_used field after a request
// Also checks if quota is exhausted and updates status accordingly.
func (s *APIKeyService) UpdateQuotaUsed(ctx context.Context, apiKeyID int64, cost float64) error {
	if cost <= 0 {
		return nil
	}

	type quotaStateReader interface {
		IncrementQuotaUsedAndGetState(ctx context.Context, id int64, amount float64) (*APIKeyQuotaUsageState, error)
	}

	if repo, ok := s.apiKeyRepo.(quotaStateReader); ok {
		state, err := repo.IncrementQuotaUsedAndGetState(ctx, apiKeyID, cost)
		if err != nil {
			return fmt.Errorf("increment quota used: %w", err)
		}
		if state != nil && state.Status == StatusAPIKeyQuotaExhausted && strings.TrimSpace(state.Key) != "" {
			s.InvalidateAuthCacheByKey(ctx, state.Key)
		}
		return nil
	}

	// Use repository to atomically increment quota_used
	newQuotaUsed, err := s.apiKeyRepo.IncrementQuotaUsed(ctx, apiKeyID, cost)
	if err != nil {
		return fmt.Errorf("increment quota used: %w", err)
	}

	// Check if quota is now exhausted and update status if needed
	apiKey, err := s.apiKeyRepo.GetByID(ctx, apiKeyID)
	if err != nil {
		return nil // Keep the completed usage increment when the follow-up read fails.
	}

	// If quota is set and now exhausted, update status
	if apiKey.Quota > 0 && newQuotaUsed >= apiKey.Quota {
		apiKey.Status = StatusAPIKeyQuotaExhausted
		// Write status alone so the snapshot cannot overwrite the just-incremented quota
		// or concurrent window usage.
		if err := s.apiKeyRepo.Update(ctx, apiKey, APIKeyUpdateFields{Status: true}); err != nil {
			return nil // Don't fail the request
		}
		// Invalidate cache so next request sees the new status
		s.InvalidateAuthCacheByKey(ctx, apiKey.Key)
	}

	return nil
}

// GetRateLimitData returns rate limit usage and window state for an API key.
func (s *APIKeyService) GetRateLimitData(ctx context.Context, id int64) (*APIKeyRateLimitData, error) {
	return s.apiKeyRepo.GetRateLimitData(ctx, id)
}

// UpdateRateLimitUsage atomically increments rate limit usage counters in the DB.
func (s *APIKeyService) UpdateRateLimitUsage(ctx context.Context, apiKeyID int64, cost float64) error {
	if cost <= 0 {
		return nil
	}
	return s.apiKeyRepo.IncrementRateLimitUsage(ctx, apiKeyID, cost)
}
