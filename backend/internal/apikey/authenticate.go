package apikey

import (
	"context"
	"fmt"
)

const (
	AuthenticationLookup       AuthenticationFailureKind = "lookup"
	AuthenticationDisabled     AuthenticationFailureKind = "disabled"
	AuthenticationTeam         AuthenticationFailureKind = "team"
	AuthenticationMemberLimit  AuthenticationFailureKind = "member_limit"
	AuthenticationIP           AuthenticationFailureKind = "ip"
	AuthenticationUserMissing  AuthenticationFailureKind = "user_missing"
	AuthenticationUserInactive AuthenticationFailureKind = "user_inactive"
)

// @project-doc docs/architecture/gateway_request_lifecycle.md#apikey_authentication
// AccessSnapshot separates credential ownership, payer, and actor. Billing resolves the funding source.
// key belongs to this authentication attempt; L1 and L2 keep separate snapshots.
type AccessSnapshot struct {
	KeyID          int64
	OwnerUserID    int64
	PayerUserID    int64
	ActorUserID    int64
	TeamID         *int64
	key            *APIKey
	fastModePolicy string
}

// AuthenticationInput carries the parsed client address and whether to enforce member spending limits.
type AuthenticationInput struct {
	ClientIP          string
	CheckMemberLimits bool
}

type AuthenticationFailureKind string

// AuthenticationFailure records the failure stage; HTTP adapters choose status codes and response bodies.
type AuthenticationFailure struct {
	Kind     AuthenticationFailureKind
	Cause    error
	ClientIP string
}

// ipAccessDenied carries the public denial message for HTTP adapters to format.
type ipAccessDenied string

// KeyView returns the key view owned by this authentication attempt.
func (a *AccessSnapshot) KeyView() *APIKey {
	if a == nil {
		return nil
	}
	return a.key
}

func (e *AuthenticationFailure) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Kind)
}

func (e *AuthenticationFailure) Unwrap() error { return e.Cause }

// Authenticate checks the key, team, member limits, IP rules, and payer in that order.
// A non-nil snapshot on failure is diagnostic data; callers must check the error.
// Funding admission checks expiration and quota later so non-consuming endpoints can read existing data.
func (s *APIKeyService) Authenticate(ctx context.Context, credential string, input AuthenticationInput) (*AccessSnapshot, error) {
	key, err := s.GetByKey(ctx, credential)
	if err != nil {
		return nil, &AuthenticationFailure{Kind: AuthenticationLookup, Cause: err}
	}
	return s.authenticateKey(key, input)
}

// authenticateKey validates loaded identity data for initial authentication and later connection turns.
func (s *APIKeyService) authenticateKey(key *APIKey, input AuthenticationInput) (*AccessSnapshot, error) {
	if key == nil {
		return nil, &AuthenticationFailure{Kind: AuthenticationLookup, Cause: ErrAPIKeyNotFound}
	}
	access := &AccessSnapshot{KeyID: key.ID, OwnerUserID: key.UserID, ActorUserID: key.UserID, TeamID: clonePointer(key.TeamID), key: key, fastModePolicy: key.FastModePolicy}
	if key.User != nil {
		access.PayerUserID = key.User.ID
	}
	if key.ActorUser != nil {
		access.ActorUserID = key.ActorUser.ID
	}
	fail := func(kind AuthenticationFailureKind, err error) (*AccessSnapshot, error) {
		return access, &AuthenticationFailure{Kind: kind, Cause: err}
	}
	if !key.IsActive() && key.Status != StatusAPIKeyExpired && key.Status != StatusAPIKeyQuotaExhausted {
		return fail(AuthenticationDisabled, nil)
	}
	if err := s.ValidateTeamKeyLifecycle(key); err != nil {
		return fail(AuthenticationTeam, err)
	}
	if input.CheckMemberLimits {
		if err := s.CheckTeamMemberLimits(key); err != nil {
			return fail(AuthenticationMemberLimit, err)
		}
	}
	if len(key.IPWhitelist) > 0 || len(key.IPBlacklist) > 0 {
		if allowed, _ := CheckIPRestrictionWithCompiledRules(input.ClientIP, key.CompiledIPWhitelist, key.CompiledIPBlacklist); !allowed {
			ip := input.ClientIP
			if ip == "" {
				ip = "unknown"
			}
			return access, &AuthenticationFailure{Kind: AuthenticationIP, ClientIP: ip, Cause: ipAccessDenied(ip)}
		}
	}
	if key.User == nil {
		return fail(AuthenticationUserMissing, nil)
	}
	if !key.User.IsActive() {
		return fail(AuthenticationUserInactive, nil)
	}
	return access, nil
}

func (e ipAccessDenied) Error() string { return fmt.Sprintf("Access denied. Your IP is %s", string(e)) }
