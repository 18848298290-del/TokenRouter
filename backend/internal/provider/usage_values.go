package provider

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// Administrator usage responses separate wallet balances, key quotas and
// subscription windows. Provider orchestration reuses the upstream normalized
// value types and adds provider identity, observation time and structured
// service errors, allowing one query result to serve display and internal
// consumers.

// Service and adapter errors distinguish unavailable execution, rejected provider
// configuration, failed queries and changed identities for administrator responses.
var (
	ErrUpstreamUsageUnavailable = apperror.ServiceUnavailable(
		"UPSTREAM_USAGE_UNAVAILABLE", "upstream usage query service is unavailable",
	)
	ErrUpstreamUsageProviderInvalid = apperror.BadRequest(
		"UPSTREAM_USAGE_PROVIDER_INVALID", "provider is not a supported API key provider",
	)
	ErrUpstreamUsageProviderDisabled = apperror.New(apperror.Category(422),
		"UPSTREAM_USAGE_PROVIDER_DISABLED", "provider is disabled",
	)
	ErrUpstreamUsageDisabled = apperror.New(apperror.Category(422),
		"UPSTREAM_USAGE_DISABLED", "upstream usage query is disabled for this provider",
	)
	ErrUpstreamUsageUnsupported = usageview.ErrUpstreamUsageUnsupported

	ErrUpstreamUsageWalletUnavailable = usageview.ErrUpstreamUsageWalletUnavailable

	ErrUpstreamUsageTimeout         = usageview.ErrUpstreamUsageTimeout
	ErrUpstreamUsageInvalidResponse = usageview.ErrUpstreamUsageInvalidResponse
	ErrUpstreamUsageRequestFailed   = usageview.ErrUpstreamUsageRequestFailed
	ErrUpstreamUsageIdentityChanged = apperror.Conflict(
		"UPSTREAM_USAGE_IDENTITY_CHANGED", "provider credentials or connection settings changed during the query",
	)
	ErrUpstreamUsageConfigInvalid = usageview.ErrUpstreamUsageConfigInvalid
	ErrUpstreamUsageBatchInvalid  = apperror.BadRequest(
		"UPSTREAM_USAGE_BATCH_INVALID", "upstream usage batch request is invalid",
	)
	ErrUpstreamUsageBatchTooLarge = apperror.BadRequest(
		"UPSTREAM_USAGE_BATCH_TOO_LARGE", "too many providers in one upstream usage query",
	)
)

// UpstreamUsageQueryConfig contains administrator choices for a fixed usage
// adapter. Credentials remain in the provider record; BaseURL changes the query
// root and is validated before execution.
type UpstreamUsageQueryConfig struct {
	Enabled bool   `json:"enabled"`            // administrator query enablement flag
	Adapter string `json:"adapter"`            // registered query format name
	BaseURL string `json:"base_url,omitempty"` // optional query root override
}

// UpstreamUsageAmount aliases optional normalized used, total and remaining
// amounts.
type UpstreamUsageAmount = usageview.UpstreamUsageAmount

// UpstreamUsageBalanceEntry aliases one wallet balance in its reported currency.
type UpstreamUsageBalanceEntry = usageview.UpstreamUsageBalanceEntry

// UpstreamUsageLimit aliases a named quota window with optional observations.
type UpstreamUsageLimit = usageview.UpstreamUsageLimit

// UpstreamUsageSubscription aliases normalized plan, quota windows and expiry
// observations.
type UpstreamUsageSubscription = usageview.UpstreamUsageSubscription

// UpstreamUsageInfo aliases the normalized result supplied by upstream usage
// adapters.
type UpstreamUsageInfo = usageview.UpstreamUsageInfo

// UpstreamUsageQueryResult carries one successful provider usage observation for
// administrator display. Internal consumers also retain the normalized Usage
// object used to fill these display fields.
type UpstreamUsageQueryResult struct {
	ProviderID int64     `json:"provider_id"`        // queried provider database ID
	Adapter    string    `json:"adapter"`            // executed query format name
	ObservedAt time.Time `json:"observed_at"`        // completed observation UTC time
	Provider   string    `json:"provider,omitempty"` // upstream normalization source label
	Mode       string    `json:"mode,omitempty"`     // normalized usage presentation mode
	Unit       string    `json:"unit,omitempty"`     // shared amount unit label
	// Balance, Balances, Available, Limits, Subscription and ExpiresAt initially
	// share normalized Usage values. CloneUpstreamUsageResult copies mutable data
	// for each waiter while retaining matching balance and subscription aliases.
	Balance      *UpstreamUsageAmount        `json:"balance,omitempty"`      // primary reported balance amounts
	Balances     []UpstreamUsageBalanceEntry `json:"balances,omitempty"`     // separate currency wallet balances
	Available    *bool                       `json:"available,omitempty"`    // optional account availability observation
	Limits       []UpstreamUsageLimit        `json:"limits,omitempty"`       // named reported quota windows
	Subscription *UpstreamUsageSubscription  `json:"subscription,omitempty"` // reported subscription plan details
	ExpiresAt    *time.Time                  `json:"expires_at,omitempty"`   // optional upstream expiry instant
	// Usage retains the normalized object for internal callers. Administrator JSON
	// excludes it and serializes the outer fields populated from that object.
	Usage *UpstreamUsageInfo `json:"-"` // internal normalized usage object
}

// UpstreamUsageMetrics snapshots process-local counts by adapter and outcome
// name.
type UpstreamUsageMetrics struct {
	// Counts is copied by SnapshotMetrics while the service metrics mutex is held;
	// callers can update this returned map independently of subsequent query
	// counts.
	Counts map[string]int64 `json:"counts"` // adapter and outcome counts
}

// UpstreamUsageAdapterOption names a usage format selectable by an
// administrator.
type UpstreamUsageAdapterOption struct {
	Name  string `json:"name"`  // registered adapter format name
	Label string `json:"label"` // administrator adapter display label
}
