package egress

import (
	"strconv"
)

// TLSSelection contains provider TLS eligibility, its bound profile, and the request's router result.
type TLSSelection struct {
	Enabled                   bool
	DirectProfileID           int64
	RouterMatched             bool
	RouterID, RouterProfileID int64
}

// ResolveRequestPolicy tries the router profile, then the provider profile, then built-in defaults.
func (s *TLSFingerprintProfileService) ResolveRequestPolicy(input TLSSelection) EgressPolicy {
	if input.RouterMatched {
		if p, ok := s.ResolveRoutableTLSProfileByID(input.Enabled, input.RouterProfileID); ok {
			return RequestPolicy(RequestPolicyInput{TLSProfile: p})
		}
	}
	return RequestPolicy(RequestPolicyInput{TLSProfile: s.ResolveTLSProfileByID(input.Enabled, input.DirectProfileID)})
}

// WebSocketTLSIdentity builds a pool key; random selections share a key within each selection mode.
// A router match uses router IDs even when the resolved profile came from provider fallback.
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
