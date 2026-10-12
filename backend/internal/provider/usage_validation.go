package provider

import (
	"context"
	"errors"
	"reflect"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

func SameUpstreamUsageIdentity(expected, current *Record, expectedConfig UpstreamUsageQueryConfig) bool {
	if expected == nil || current == nil || expected.ID != current.ID || current.Type != ProviderTypeAPIKey ||
		expected.Platform != current.Platform || !reflect.DeepEqual(expected.Credentials, current.Credentials) ||
		!SameUpstreamUsageOptionalInt64(expected.ProxyID, current.ProxyID) || expected.Concurrency != current.Concurrency ||
		expected.Status != current.Status {
		return false
	}
	if expected.ProxyID != nil && !SameUpstreamUsageProxy(expected.Proxy, current.Proxy, *expected.ProxyID) {
		// Repositories may omit proxy details from both snapshots. In that case,
		// leave missing proxy configuration errors to client construction.
		if expected.Proxy != nil || current.Proxy != nil {
			return false
		}
	}
	for _, key := range []string{"enable_tls_fingerprint", "tls_fingerprint_profile_id", "tls_fingerprint_router_id"} {
		if !reflect.DeepEqual(ExtraValue(expected.Extra, key), ExtraValue(current.Extra, key)) {
			return false
		}
	}
	currentConfig, err := EffectiveUpstreamUsageConfig(current)
	if err != nil || currentConfig != expectedConfig {
		return false
	}
	return true
}

func SameUpstreamUsageOptionalInt64(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func SameUpstreamUsageProxy(expected, current *egress.Proxy, id int64) bool {
	if expected == nil || current == nil || expected.ID != id || current.ID != id {
		return false
	}
	return expected.Protocol == current.Protocol && expected.Host == current.Host && expected.Port == current.Port &&
		expected.Username == current.Username && expected.Password == current.Password && expected.Status == current.Status
}

func ExtraValue(extra map[string]any, key string) any {
	if extra == nil {
		return nil
	}
	return extra[key]
}

func ValidateNormalizedUsage(usage *UpstreamUsageInfo) error {
	return usageview.ValidateNormalizedUsage(usage)
}

func UpstreamUsageContextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrUpstreamUsageTimeout
	}
	return ErrUpstreamUsageRequestFailed
}
