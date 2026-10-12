package ws

import (
	"math"
	"time"
)

const WSConnMaxAge = 60 * time.Minute

type WSPoolProvider struct {
	FingerprintMode string
	ID              int64
	Concurrency     int
	Type            string
}
type WSPoolOptions struct {
	MaxConnsPerProvider                                       int
	DynamicMaxConnsByProviderConcurrencyEnabled               bool
	OAuthMaxConnsFactor, APIKeyMaxConnsFactor                 float64
	MinIdlePerProvider, MaxIdlePerProvider, QueueLimitPerConn int
	PoolTargetUtilization                                     float64
	PrewarmCooldownMS, DialTimeoutSeconds                     int
}

func (p *WSPoolOptions) MaxConnsHardCap() int {
	if p != nil && p.MaxConnsPerProvider > 0 {
		return p.MaxConnsPerProvider
	}
	return 8
}

func (p *WSPoolOptions) DynamicMaxConnsEnabled() bool {
	if p != nil {
		return p.DynamicMaxConnsByProviderConcurrencyEnabled
	}
	return false
}

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

func (p *WSPoolOptions) EffectiveMaxConnsByProvider(provider *WSPoolProvider) int {
	hardCap := p.MaxConnsHardCap()
	if hardCap <= 0 {
		return 0
	}

	if provider == nil || !p.DynamicMaxConnsEnabled() {
		return hardCap
	}
	if provider.Concurrency <= 0 {
		// Nonpositive concurrency means unlimited; use the global connection cap.
		return hardCap
	}
	factor := p.MaxConnsFactorByProvider(provider)
	if factor <= 0 {
		factor = 1.0
	}
	effective := min(max(int(math.Ceil(float64(provider.Concurrency)*factor)), 1), hardCap)
	return effective
}

func (p *WSPoolOptions) MinIdle() int {
	if p != nil && p.MinIdlePerProvider >= 0 {
		return p.MinIdlePerProvider
	}
	return 0
}

func (p *WSPoolOptions) MaxIdle() int {
	if p != nil && p.MaxIdlePerProvider >= 0 {
		return p.MaxIdlePerProvider
	}
	return 4
}

func (p *WSPoolOptions) MaxConnAge() time.Duration {
	return WSConnMaxAge
}

func (p *WSPoolOptions) QueueLimit() int {
	if p != nil && p.QueueLimitPerConn > 0 {
		return p.QueueLimitPerConn
	}
	return 256
}

func (p *WSPoolOptions) TargetUtilization() float64 {
	if p != nil {
		ratio := p.PoolTargetUtilization
		if ratio > 0 && ratio <= 1 {
			return ratio
		}
	}
	return 0.7
}

func (p *WSPoolOptions) PrewarmCooldown() time.Duration {
	if p != nil && p.PrewarmCooldownMS > 0 {
		return time.Duration(p.PrewarmCooldownMS) * time.Millisecond
	}
	return 0
}

func (p *WSPoolOptions) DialTimeout() time.Duration {
	if p != nil && p.DialTimeoutSeconds > 0 {
		return time.Duration(p.DialTimeoutSeconds) * time.Second
	}
	return 10 * time.Second
}
