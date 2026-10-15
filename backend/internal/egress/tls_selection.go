package egress

import (
	"strconv"
)

// tls_selection.go resolves request TLS profiles and builds WebSocket
// identities from provider and router selections. A request can match a router
// rule while its referenced TLS profile is unavailable. Profile resolution then
// uses the provider selection, but WebSocket identity still reflects the
// matched router and its configured profile ID. Disabled fingerprint
// eligibility produces no profile, so its WebSocket identity is empty.
//
// Random router selections share the router-random identity, while random
// provider selections use a separate provider-random identity. Fixed router
// selections use the configured router and profile IDs; un-routed fixed
// selections use the supplied profile cache key. These values describe
// selection mode and may differ from the profile actually resolved after a
// router choice fails.

// TLSSelection contains provider TLS eligibility, its bound profile, and the request's router result.
//
// RouterMatched selects whether RouterProfileID is attempted before
// DirectProfileID. RouterID and RouterProfileID also contribute to WebSocket
// identity even when profile resolution later chooses the provider selection.
type TLSSelection struct {
	Enabled         bool  // provider fingerprint eligibility flag
	DirectProfileID int64 // provider-bound profile selection ID
	RouterMatched   bool  // request router match flag
	// RouterID identifies the matched router; RouterProfileID identifies its
	// configured profile selection.
	RouterID, RouterProfileID int64
}

// ResolveRequestPolicy resolves a matched router profile before the
// provider-bound profile and returns a copied request policy. A missing
// positive router profile or unavailable random router choice falls through to
// provider selection. Disabled fingerprint eligibility yields a nil profile in
// either path.
//
// The provider resolver can choose its cached fixed or random profile and
// otherwise supplies the built-in default. RequestPolicy clones the resolved
// lists again before returning the result for subsequent transport conversion.
func (s *TLSFingerprintProfileService) ResolveRequestPolicy(input TLSSelection) EgressPolicy {
	// A match can name a missing profile or an unavailable random selection.
	// Continue with the provider choice when routable resolution reports failure.
	if input.RouterMatched {
		if p, ok := s.ResolveRoutableTLSProfileByID(input.Enabled, input.RouterProfileID); ok {
			return RequestPolicy(RequestPolicyInput{TLSProfile: p})
		}
	}
	return RequestPolicy(RequestPolicyInput{TLSProfile: s.ResolveTLSProfileByID(input.Enabled, input.DirectProfileID)})
}

// WebSocketTLSIdentity builds a WebSocket pool identity from the configured
// selection mode and the presence of a resolved profile. No resolved profile
// produces an empty key. A router match uses its router and configured profile
// IDs even when resolution falls back to the provider profile.
//
// Random router choices share one router-random key, and random provider
// choices share a distinct provider-random key. Other provider selections use
// profileCacheKey supplied by the caller for the resolved fingerprint.
func WebSocketTLSIdentity(input TLSSelection, hasProfile bool, profileCacheKey string) string {
	if !hasProfile {
		return ""
	}
	if input.RouterMatched {
		if input.RouterProfileID == -1 {
			return "tls-router-random"
		}
		return "tls-router-" + strconv.FormatInt(input.RouterID, 10) + "-" + strconv.FormatInt(input.RouterProfileID, 10)
	}
	if input.DirectProfileID == -1 {
		return "tls-random"
	}
	return profileCacheKey
}
