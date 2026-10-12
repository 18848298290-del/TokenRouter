package antigravity

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	AntigravityMaxRetries               = 3
	AntigravityRetryBaseDelay           = 1 * time.Second
	AntigravityRetryMaxDelay            = 16 * time.Second
	AntigravityRateLimitThreshold       = 7 * time.Second
	AntigravitySmartRetryMinWait        = 1 * time.Second  // minimum smart-retry wait
	AntigravitySmartRetryMaxAttempts    = 1                // one retry limits repeated throttling and wait time
	AntigravityDefaultRateLimitDuration = 30 * time.Second // used when retryDelay is absent or invalid

	// MODEL_CAPACITY_EXHAUSTED uses a separate retry budget.
	// Shared model capacity is retried on the same provider.
	// Retry at one-second intervals, up to 60 times.
	AntigravityModelCapacityRetryMaxAttempts = 60
	AntigravityModelCapacityRetryWait        = 1 * time.Second

	// Google RPC status and detail identifiers.
	GoogleRPCStatusResourceExhausted      = "RESOURCE_EXHAUSTED"
	GoogleRPCStatusUnavailable            = "UNAVAILABLE"
	GoogleRPCTypeRetryInfo                = "type.googleapis.com/google.rpc.RetryInfo"
	GoogleRPCTypeErrorInfo                = "type.googleapis.com/google.rpc.ErrorInfo"
	GoogleRPCReasonModelCapacityExhausted = "MODEL_CAPACITY_EXHAUSTED"
	GoogleRPCReasonRateLimitExceeded      = "RATE_LIMIT_EXCEEDED"

	// AntigravitySingleProviderSmartRetryMaxAttempts bounds retries after a long-delay 503.
	// HandleSmartRetry normally marks long model delays for provider switching.
	// Single-provider mode waits in place to avoid an additional local cooldown wait.
	AntigravitySingleProviderSmartRetryMaxAttempts = 3

	// AntigravitySingleProviderSmartRetryMaxWait caps each in-place retry delay.
	AntigravitySingleProviderSmartRetryMaxWait = 15 * time.Second

	// AntigravitySingleProviderSmartRetryTotalMaxWait caps accumulated retry waits.
	// Exhausting this budget returns the failure response.
	AntigravitySingleProviderSmartRetryTotalMaxWait = 30 * time.Second

	// AntigravityModelCapacityCooldown suppresses retries after capacity retries are exhausted.
	AntigravityModelCapacityCooldown = 10 * time.Second
)

// Track per-model cooldowns after capacity retries fail.
var (
	modelCapacityExhaustedMu    sync.RWMutex
	modelCapacityExhaustedUntil = make(map[string]time.Time) // modelName -> cooldown until
)

// AntigravityProviderSwitchError requests another provider after model rate limiting.
type AntigravityProviderSwitchError struct {
	OriginalProviderID int64
	RateLimitedModel   string
	IsStickySession    bool // allows callers to account for lost session cache affinity
}

func (e *AntigravityProviderSwitchError) Error() string {
	return fmt.Sprintf("provider %d model %s rate limited, need switch",
		e.OriginalProviderID, e.RateLimitedModel)
}

// IsAntigravityProviderSwitchError unwraps a provider-switch signal.
func IsAntigravityProviderSwitchError(err error) (*AntigravityProviderSwitchError, bool) {
	if switchErr, ok := errors.AsType[*AntigravityProviderSwitchError](err); ok {
		return switchErr, true
	}
	return nil, false
}
