package ws

import (
	"math"
	"time"
)

// pool_options.go calculates WebSocket pool capacity and interprets idle,
// queue, utilization, and timing settings.
// WebSocket connection capacity can be fixed or derived from provider
// concurrency, but both choices remain bounded by the configured hard cap.
// Dynamic sizing uses a provider-type factor and rounds upward before clamping
// to at least one connection and at most that cap.
//
// Nil and nonpositive settings have field-specific defaults. Idle limits accept
// zero, so no retained idle sockets is a distinct choice from the nil default;
// queue capacity instead requires a positive value and otherwise uses its
// default. Cooldown and dial settings convert stored units to durations for
// their consumers. Connection lifetime remains sixty minutes independent of the
// option pointer, and callers publish valid immutable option snapshots before
// pool acquisition or reconciliation reads them.

// WSConnMaxAge bounds the lifetime of a pooled upstream connection, forcing
// retirement after active ownership ends so later acquisitions establish a fresh socket.
const WSConnMaxAge = 60 * time.Minute

// WSPoolProvider supplies provider identity, concurrency, and fingerprint
// settings used by pooling.
type WSPoolProvider struct {
	FingerprintMode string // resolved handshake fingerprint mode
	ID              int64  // pooled upstream provider identifier
	Concurrency     int    // provider concurrent request limit
	Type            string // provider credential type label
}

// WSPoolOptions contains limits and timing values interpreted by WebSocket pool
// readers.
type WSPoolOptions struct {
	MaxConnsPerProvider                         int  // provider connection hard cap
	DynamicMaxConnsByProviderConcurrencyEnabled bool // concurrency-based capacity selection flag
	// OAuthMaxConnsFactor scales OAuth concurrency, and APIKeyMaxConnsFactor scales
	// API-key concurrency. Positive values apply during dynamic sizing; missing
	// usable factors select one before the product is clamped to the hard cap.
	OAuthMaxConnsFactor, APIKeyMaxConnsFactor float64
	// MinIdlePerProvider names the warm idle floor, MaxIdlePerProvider names the
	// retained idle ceiling, and QueueLimitPerConn names per-connection queue
	// capacity. Idle readers accept zero; the queue reader replaces nonpositive
	// values with its default for provider-wide and preferred-connection admission.
	MinIdlePerProvider, MaxIdlePerProvider, QueueLimitPerConn int
	PoolTargetUtilization                                     float64 // pool spare-capacity utilization ratio
	// PrewarmCooldownMS is warmup cooldown in milliseconds, and DialTimeoutSeconds
	// is dial budget in seconds. Readers convert positive settings to durations;
	// nonpositive values select zero cooldown and the default dial timeout.
	PrewarmCooldownMS, DialTimeoutSeconds int
}

// MaxConnsHardCap returns a positive configured provider connection cap or
// eight. Nil options and nonpositive configured values use that default, which
// also bounds dynamically derived capacity.
func (p *WSPoolOptions) MaxConnsHardCap() int {
	if p != nil && p.MaxConnsPerProvider > 0 {
		return p.MaxConnsPerProvider
	}
	return 8
}

// DynamicMaxConnsEnabled reports whether concurrency-based pool sizing is
// selected in these options. Nil options return false, so
// EffectiveMaxConnsByProvider uses its fixed hard-cap path until a configured
// snapshot enables dynamic sizing.
func (p *WSPoolOptions) DynamicMaxConnsEnabled() bool {
	if p != nil {
		return p.DynamicMaxConnsByProviderConcurrencyEnabled
	}
	return false
}

// MaxConnsFactorByProvider returns the positive configured factor for OAuth or
// API-key providers. Nil inputs, other provider types, and nonpositive factors
// select one, allowing dynamic sizing to apply a type-specific multiplier.
func (p *WSPoolOptions) MaxConnsFactorByProvider(provider *WSPoolProvider) float64 {
	if p == nil || provider == nil {
		return 1.0
	}
	switch provider.Type {
	case "oauth":
		if p.OAuthMaxConnsFactor > 0 {
			return p.OAuthMaxConnsFactor
		}
	case "apikey":
		if p.APIKeyMaxConnsFactor > 0 {
			return p.APIKeyMaxConnsFactor
		}
	}
	return 1.0
}

// EffectiveMaxConnsByProvider returns the provider capacity permitted by these
// options. Nil providers, disabled dynamic sizing, or nonpositive provider
// concurrency select the hard cap. Nonpositive concurrency is treated as
// unlimited provider admission rather than a zero-capacity pool.
//
// For a bounded provider, its concurrency is multiplied by the selected type
// factor and rounded upward. The result is clamped between one and the hard
// cap, so small fractional factors can retain a usable connection while larger
// products remain within configured capacity. Callers supply validated option
// values to keep numeric conversion within their supported ranges.
func (p *WSPoolOptions) EffectiveMaxConnsByProvider(provider *WSPoolProvider) int {
	hardCap := p.MaxConnsHardCap()
	if hardCap <= 0 {
		return 0
	}

	if provider == nil || !p.DynamicMaxConnsEnabled() {
		return hardCap
	}
	if provider.Concurrency <= 0 {
		// Provider admission treats nonpositive concurrency as unlimited. Retain the
		// hard cap here so that admission setting cannot create unbounded upstream sockets.
		return hardCap
	}
	factor := p.MaxConnsFactorByProvider(provider)
	if factor <= 0 {
		factor = 1.0
	}
	// Round fractional capacity up, then retain a usable connection within the
	// global cap; dynamic admission never derives zero from a positive bounded provider.
	effective := min(max(int(math.Ceil(float64(provider.Concurrency)*factor)), 1), hardCap)
	return effective
}

// MinIdle returns the configured nonnegative idle floor or zero. A configured
// zero is accepted, allowing callers to disable retained warm capacity while
// acquisition can still create needed connections.
func (p *WSPoolOptions) MinIdle() int {
	if p != nil && p.MinIdlePerProvider >= 0 {
		return p.MinIdlePerProvider
	}
	return 0
}

// MaxIdle returns the configured nonnegative idle ceiling or four. A configured
// zero is accepted, letting pool release hand a compatible connection to
// waiters and otherwise avoid retaining it idle.
func (p *WSPoolOptions) MaxIdle() int {
	if p != nil && p.MaxIdlePerProvider >= 0 {
		return p.MaxIdlePerProvider
	}
	return 4
}

// MaxConnAge returns the fixed sixty-minute lifetime for pooled WebSocket
// connections.
func (p *WSPoolOptions) MaxConnAge() time.Duration {
	return WSConnMaxAge
}

// QueueLimit returns the positive per-connection queue setting or its default
// of 256. Nil and nonpositive values use that default; acquisition multiplies
// effective capacity by this value through its overflow-safe admission check.
func (p *WSPoolOptions) QueueLimit() int {
	if p != nil && p.QueueLimitPerConn > 0 {
		return p.QueueLimitPerConn
	}
	return 256
}

// TargetUtilization returns a configured ratio in the open-zero, closed-one
// range or 0.7. Nil, nonpositive, and above-one values use the default for
// spare-capacity decisions during pool acquisition.
func (p *WSPoolOptions) TargetUtilization() float64 {
	if p != nil {
		ratio := p.PoolTargetUtilization
		if ratio > 0 && ratio <= 1 {
			return ratio
		}
	}
	return 0.7
}

// PrewarmCooldown returns the positive stored millisecond cooldown as a
// duration. Nil or nonpositive values return zero, letting the warmup owner
// interpret that setting without an additional delay.
func (p *WSPoolOptions) PrewarmCooldown() time.Duration {
	if p != nil && p.PrewarmCooldownMS > 0 {
		return time.Duration(p.PrewarmCooldownMS) * time.Millisecond
	}
	return 0
}

// DialTimeout returns the positive stored seconds timeout or ten seconds. Nil
// or nonpositive values select that default, and acquisition uses the duration
// to bound an individual upstream connection attempt.
func (p *WSPoolOptions) DialTimeout() time.Duration {
	if p != nil && p.DialTimeoutSeconds > 0 {
		return time.Duration(p.DialTimeoutSeconds) * time.Second
	}
	return 10 * time.Second
}
