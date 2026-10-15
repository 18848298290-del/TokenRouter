package provider

import (
	"context"
	"errors"
	"reflect"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// usage_validation.go compares usage-query identities and validates normalized
// upstream usage responses.
// An upstream usage response belongs to the provider identity and connection
// settings that produced it. Query execution rereads that record before and
// after the upstream call, rejecting changed credentials, status, proxy details,
// concurrency, TLS configuration or normalized query choices before returning an
// observation.
//
// Proxy metadata may be omitted by both repository reads even when ProxyID is
// present. That case leaves connection validation to client construction;
// one-sided missing metadata or changed proxy credentials fails comparison.
// Adapter value validation remains separate, checking the normalized display
// shape after the upstream response is available.

// SameUpstreamUsageIdentity compares the provider and query settings used by an
// observation with a reread record. Nil records, different IDs and a current
// non-API-Key type fail. Callers keep both snapshots stable during comparison.
//
// Credentials, status, concurrency, selected proxy, TLS settings and normalized
// query configuration must match. When both records omit the selected proxy
// details, client construction handles that missing configuration; a populated
// proxy on either side requires a matching proxy record.
func SameUpstreamUsageIdentity(expected, current *Record, expectedConfig UpstreamUsageQueryConfig) bool {
	if expected == nil || current == nil || expected.ID != current.ID || current.Type != ProviderTypeAPIKey ||
		expected.Platform != current.Platform || !reflect.DeepEqual(expected.Credentials, current.Credentials) ||
		!SameUpstreamUsageOptionalInt64(expected.ProxyID, current.ProxyID) || expected.Concurrency != current.Concurrency ||
		expected.Status != current.Status {
		return false
	}
	if expected.ProxyID != nil && !SameUpstreamUsageProxy(expected.Proxy, current.Proxy, *expected.ProxyID) {
		// Both reads can omit hydrated proxy metadata while retaining ProxyID. Allow
		// that pair through identity comparison so client construction reports missing
		// connection configuration; one-sided metadata still invalidates the observation.
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
	// A malformed reread supplies no comparable query configuration. Treat that
	// validation failure as changed identity so a completed observation cannot be
	// accepted for the rejected settings.
	if err != nil || currentConfig != expectedConfig {
		return false
	}
	return true
}

// SameUpstreamUsageOptionalInt64 compares optional IDs by presence and pointed
// value. Two absent pointers match; distinct pointers to the same ID also match,
// allowing repository copies of proxy IDs.
func SameUpstreamUsageOptionalInt64(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

// SameUpstreamUsageProxy compares two populated proxy records against the
// expected ID and connection fields. Either absent record fails; authentication
// and status changes reject results prepared for the earlier proxy.
func SameUpstreamUsageProxy(expected, current *egress.Proxy, id int64) bool {
	if expected == nil || current == nil || expected.ID != id || current.ID != id {
		return false
	}
	return expected.Protocol == current.Protocol && expected.Host == current.Host && expected.Port == current.Port &&
		expected.Username == current.Username && expected.Password == current.Password && expected.Status == current.Status
}

// ExtraValue returns the named Extra value, treating a nil map as absent.
// Returned mutable values remain shared with the supplied map and must stay
// stable while consumers compare them.
func ExtraValue(extra map[string]any, key string) any {
	if extra == nil {
		return nil
	}
	return extra[key]
}

// ValidateNormalizedUsage delegates normalized shape and numeric checks to
// upstream usageview. It returns validation errors directly and leaves the
// supplied usage values available for query response construction after success.
func ValidateNormalizedUsage(usage *UpstreamUsageInfo) error {
	return usageview.ValidateNormalizedUsage(usage)
}

// UpstreamUsageContextError maps a deadline-exceeded context to the usage
// timeout error. Cancellation and other context states select the general
// query-failure error used while waiting for query capacity.
func UpstreamUsageContextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrUpstreamUsageTimeout
	}
	return ErrUpstreamUsageRequestFailed
}
