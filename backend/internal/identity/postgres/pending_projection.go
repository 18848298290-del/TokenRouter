package postgres

import (
	"context"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// Pending authentication stores return Ent entities while identity flows
// consume domain values. The repository facade delegates lifecycle and
// consumption decisions to AuthPendingIdentityService, then maps fields into
// domain structs with their stored optional pointers and metadata maps still
// shared.

// PendingRepository maps stored browser-pending state to the identity flow
// domain types.
type PendingRepository struct {
	store *AuthPendingIdentityService // pending lifecycle storage service
}

// NewPendingRepository constructs the pending-state facade over the supplied
// Ent client. A nonnil first clock selects expiry observations; an absent or
// nil first clock uses time.Now for lifecycle checks.
func NewPendingRepository(client *dbent.Client, clocks ...func() time.Time) *PendingRepository {
	return &PendingRepository{store: NewAuthPendingIdentityService(client, clocks...)}
}

// CreatePendingSession stores prepared pending input and maps the resulting
// entity to identity state. Empty tokens and zero expiry use the underlying
// service defaults; returned pointer and map fields remain shared.
func (s *PendingRepository) CreatePendingSession(ctx context.Context, input CreatePendingAuthSessionInput) (*identity.PendingAuthSession, error) {
	v, err := s.store.CreatePendingSession(ctx, input)
	return PendingAuthSessionFromEntity(v), err
}

// IssueCompletionCode delegates creation of a one-time code for a stored
// pending session. Nonpositive TTL selects the service default; the returned
// plaintext code belongs to the caller for browser delivery.
func (s *PendingRepository) IssueCompletionCode(ctx context.Context, input IssuePendingAuthCompletionCodeInput) (*IssuePendingAuthCompletionCodeResult, error) {
	return s.store.IssueCompletionCode(ctx, input)
}

// ConsumeCompletionCode delegates browser-bound code consumption and maps the
// consumed session result. Invalid, expired, consumed, or mismatched browser
// state returns the underlying error for the identity completion caller.
func (s *PendingRepository) ConsumeCompletionCode(ctx context.Context, rawCode, browserSessionKey string) (*identity.PendingAuthSession, error) {
	v, err := s.store.ConsumeCompletionCode(ctx, rawCode, browserSessionKey)
	return PendingAuthSessionFromEntity(v), err
}

// ConsumeBrowserSession delegates browser token consumption and maps the
// resulting session. Storage rechecks lifecycle state during the write; the
// caller handles missing, expired, consumed, and browser-mismatch errors.
func (s *PendingRepository) ConsumeBrowserSession(ctx context.Context, sessionToken, browserSessionKey string) (*identity.PendingAuthSession, error) {
	v, err := s.store.ConsumeBrowserSession(ctx, sessionToken, browserSessionKey)
	return PendingAuthSessionFromEntity(v), err
}

// GetBrowserSession delegates a validated browser-session read and maps its
// entity result. It leaves the session available for later consumption, with
// optional pointers and metadata fields retained by the mapped value.
func (s *PendingRepository) GetBrowserSession(ctx context.Context, sessionToken, browserSessionKey string) (*identity.PendingAuthSession, error) {
	v, err := s.store.GetBrowserSession(ctx, sessionToken, browserSessionKey)
	return PendingAuthSessionFromEntity(v), err
}

// UpsertAdoptionDecision delegates saved profile-choice persistence and maps
// the decision entity. The storage service owns its transaction and lock
// participation; optional IdentityID remains shared in the returned mapped
// value.
func (s *PendingRepository) UpsertAdoptionDecision(ctx context.Context, input PendingIdentityAdoptionDecisionInput) (*identity.IdentityAdoptionDecision, error) {
	v, err := s.store.UpsertAdoptionDecision(ctx, input)
	return IdentityAdoptionDecisionFromEntity(v), err
}

// PendingAuthSessionFromEntity maps a stored pending record to identity state,
// returning nil for nil input. Optional pointers and both metadata maps remain
// shared; callers copy nested values before independent mutation.
func PendingAuthSessionFromEntity(v *dbent.PendingAuthSession) *identity.PendingAuthSession {
	if v == nil {
		return nil
	}
	return &identity.PendingAuthSession{ID: v.ID, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, SessionToken: v.SessionToken, Intent: v.Intent, ProviderType: v.ProviderType, ProviderKey: v.ProviderKey, ProviderSubject: v.ProviderSubject, TargetUserID: v.TargetUserID, RedirectTo: v.RedirectTo, ResolvedEmail: v.ResolvedEmail, RegistrationPasswordHash: v.RegistrationPasswordHash, UpstreamIdentityClaims: v.UpstreamIdentityClaims, LocalFlowState: v.LocalFlowState, BrowserSessionKey: v.BrowserSessionKey, CompletionCodeHash: v.CompletionCodeHash, CompletionCodeExpiresAt: v.CompletionCodeExpiresAt, EmailVerifiedAt: v.EmailVerifiedAt, PasswordVerifiedAt: v.PasswordVerifiedAt, TotpVerifiedAt: v.TotpVerifiedAt, ExpiresAt: v.ExpiresAt, ConsumedAt: v.ConsumedAt}
}

// PendingAuthSessionToEntity maps identity pending state into an Ent value,
// returning nil for nil input. Optional pointers and both metadata maps remain
// shared with the supplied flow state.
func PendingAuthSessionToEntity(v *identity.PendingAuthSession) *dbent.PendingAuthSession {
	if v == nil {
		return nil
	}
	return &dbent.PendingAuthSession{ID: v.ID, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, SessionToken: v.SessionToken, Intent: v.Intent, ProviderType: v.ProviderType, ProviderKey: v.ProviderKey, ProviderSubject: v.ProviderSubject, TargetUserID: v.TargetUserID, RedirectTo: v.RedirectTo, ResolvedEmail: v.ResolvedEmail, RegistrationPasswordHash: v.RegistrationPasswordHash, UpstreamIdentityClaims: v.UpstreamIdentityClaims, LocalFlowState: v.LocalFlowState, BrowserSessionKey: v.BrowserSessionKey, CompletionCodeHash: v.CompletionCodeHash, CompletionCodeExpiresAt: v.CompletionCodeExpiresAt, EmailVerifiedAt: v.EmailVerifiedAt, PasswordVerifiedAt: v.PasswordVerifiedAt, TotpVerifiedAt: v.TotpVerifiedAt, ExpiresAt: v.ExpiresAt, ConsumedAt: v.ConsumedAt}
}

// IdentityAdoptionDecisionFromEntity maps a stored adoption choice into
// identity state, returning nil for nil input. Its optional identity pointer
// remains shared, while scalar choices and timestamps are copied.
func IdentityAdoptionDecisionFromEntity(v *dbent.IdentityAdoptionDecision) *identity.IdentityAdoptionDecision {
	if v == nil {
		return nil
	}
	return &identity.IdentityAdoptionDecision{ID: v.ID, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, PendingAuthSessionID: v.PendingAuthSessionID, IdentityID: v.IdentityID, AdoptDisplayName: v.AdoptDisplayName, AdoptAvatar: v.AdoptAvatar, DecidedAt: v.DecidedAt}
}

// IdentityAdoptionDecisionToEntity maps an identity adoption choice into an
// Ent value, returning nil for nil input. Its optional identity pointer
// remains shared, while scalar choices and timestamps are copied.
func IdentityAdoptionDecisionToEntity(v *identity.IdentityAdoptionDecision) *dbent.IdentityAdoptionDecision {
	if v == nil {
		return nil
	}
	return &dbent.IdentityAdoptionDecision{ID: v.ID, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, PendingAuthSessionID: v.PendingAuthSessionID, IdentityID: v.IdentityID, AdoptDisplayName: v.AdoptDisplayName, AdoptAvatar: v.AdoptAvatar, DecidedAt: v.DecidedAt}
}
