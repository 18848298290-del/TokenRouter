package billing

import (
	"context"
)

// eligibility_options.go defines the funding snapshots, runtime options, and
// readers consumed by eligibility checks.
// Funding admission receives a paying user ID, a Key billing policy and an
// optional selected subscription. Runtime options separate the live
// minimum-balance reserve and date inputs from circuit-breaker settings
// captured when Eligibility is constructed. These small snapshots carry the
// fields used by the precheck; authentication and final group selection belong
// to their caller.

// Observe receives component labels and formatted billing events from injected
// application logging.
type Observe func(component, format string, args ...any)

// EligibilityOptions provides date helpers and funding-precheck configuration
// for one getter observation.
type EligibilityOptions struct {
	// Dates supplies cache-drop timestamps and the subscription-precheck
	// calendar. Several eligibility/window decisions still use independent
	// wall-clock helpers, so this value does not replace every clock.
	Dates   DateRuntime    // calendar and observation helper inputs
	Billing BillingOptions // funding-precheck configuration values
}

// BillingOptions contains the minimum reserve and construction-time
// balance-read breaker settings.
type BillingOptions struct {
	// MinimumBalanceReserve is read through the options getter during balance
	// admission. Nonpositive observed values disable the extra reserve; a balance
	// at a positive reserve remains eligible.
	MinimumBalanceReserve float64 // USD
	// CircuitBreaker is sampled by NewEligibility once to create its breaker.
	// Later option reads for reserve/calendar do not rebuild or live-update that
	// breaker configuration.
	CircuitBreaker CircuitBreakerOptions // balance-read breaker construction settings
}

// CircuitBreakerOptions configures balance-read failure handling that gates
// all funding admission paths.
type CircuitBreakerOptions struct {
	Enabled bool // construct balance-read circuit breaker
	// FailureThreshold, ResetTimeoutSeconds and HalfOpenRequests are captured
	// together when enabled. A nonpositive failure count or probe allowance
	// selects five or one respectively; a nonpositive computed reset duration
	// selects thirty seconds.
	FailureThreshold    int // failure-count opening threshold
	ResetTimeoutSeconds int // seconds
	HalfOpenRequests    int // half-open admission allowance count
}

// BalanceReader supplies recipient domain records for persisted balance
// loading by Eligibility.
type BalanceReader interface {
	// GetByID returns the recipient record used to load its stored balance, or a
	// read error. Eligibility requires a nonnil record on success because its
	// database-load helper reads Balance directly.
	GetByID(context.Context, int64) (*UserSummary, error)
}

// APIKeyRateLimitLoader supplies persisted Key window observations after
// absent or failing cache reads.
type APIKeyRateLimitLoader interface {
	// GetRateLimitData returns stored usage and optional window starts for one
	// Key. Success supplies nonnil data; eligibility can permit requests after
	// load errors and optionally invokes ResetRateLimitWindows when the same
	// implementation exposes it.
	GetRateLimitData(context.Context, int64) (*APIKeyRateLimitData, error)
}

// KeySnapshot carries the selected Key policy and spending ceilings needed for
// funding admission.
type KeySnapshot struct {
	ID int64 // selected API Key identifier
	// BillingMode is normalized by effectiveKeyBillingMode for admission.
	// Missing, empty or unsupported policies select auto there, allowing these
	// precheck snapshots to use the default policy.
	BillingMode string // configured Key funding policy
	// RateLimit5h, RateLimit1d and RateLimit7d cap billed USD over their elapsed
	// windows. HasRateLimits enables checking when any ceiling is positive;
	// nonpositive ceilings are disabled individually.
	RateLimit5h float64 // USD
	RateLimit1d float64 // USD
	RateLimit7d float64 // USD
}

// GroupSnapshot identifies the final request group for preferred-subscription
// coverage validation.
type GroupSnapshot struct {
	ID int64 // final selected group identifier
}

// CheckInput carries caller-resolved funding identities and resource views for
// an Eligibility precheck.
type CheckInput struct {
	// Payer supplies its ID when admission needs a balance read. The caller
	// provides a nonnil authenticated payer for those paths; Check does not
	// establish identity or owner authorization.
	Payer *UserSummary // caller-resolved paying user view
	// Key omission selects automatic funding and skips Key spending-limit checks.
	// When supplied, its policy and positive window ceilings are interpreted by
	// Eligibility.
	Key *KeySnapshot // optional selected Key snapshot
	// Group omission skips the preferred-subscription coverage comparison in
	// Eligibility. Consuming callers pass their resolved group when that plan
	// scope needs to be checked.
	Group *GroupSnapshot // optional final request group
	// Subscription is required by subscription mode, ignored by balance mode and
	// optional in auto mode. Effective eligibility copies the grant before
	// clearing expired counters for its local check.
	Subscription *UserSubscription // optional selected subscription view
	// Platform is forwarded by Check to CheckBillingEligibility but is unused
	// there. Funding policy and group coverage are determined from the supplied
	// payer, Key and subscription instead.
	Platform string // forwarded compatibility platform label
}

// Printf invokes the configured observer with its component, format and
// arguments. A nil observer is a no-op, allowing eligibility and redemption
// callers to report optional events without separate nil checks.
func (o Observe) Printf(component, format string, args ...any) {
	if o != nil {
		o(component, format, args...)
	}
}

// HasRateLimits reports any positive spending ceiling on a nonnil Key
// snapshot. Nil receivers are not accepted; zero, negative and NaN ceilings do
// not enable a window, while positive infinity qualifies.
func (k *KeySnapshot) HasRateLimits() bool {
	return k.RateLimit5h > 0 || k.RateLimit1d > 0 || k.RateLimit7d > 0
}

// effectiveKeyBillingMode returns the normalized Key funding policy,
// defaulting to auto for a nil snapshot or unsupported text. Empty text uses
// normalization's auto default; callers receive a supported policy for
// eligibility selection.
func effectiveKeyBillingMode(k *KeySnapshot) string {
	if k == nil {
		return APIKeyBillingModeAuto
	}
	mode, ok := NormalizeAPIKeyBillingMode(k.BillingMode)
	// Unsupported stored policy text selects the default precheck path, so
	// eligibility can still evaluate funding without returning the policy
	// parser's invalid result.
	if !ok {
		return APIKeyBillingModeAuto
	}
	return mode
}
