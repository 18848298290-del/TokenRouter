package identity

import (
	"context"
	"fmt"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// PendingDatabase exposes complete storage operations; the PostgreSQL adapter owns transactions.
type PendingDatabase interface {
	FindLinuxDoCompatEmailUser(context.Context, string) (*User, error)
	FindOIDCCompatEmailUser(context.Context, string) (*User, error)
	FindDingTalkCompatEmailUser(context.Context, string) (*User, error)
	FindOAuthBindTarget(context.Context, int64) (*User, error)
	EnsureWeChatBindOwnership(context.Context, int64, string, WeChatIdentityChannel, string) error
	FindWeChatUserByLegacyOpenID(context.Context, PendingAuthIdentityKey, WeChatIdentityChannel, string) (*User, error)
	EnsureWeChatRuntimeIdentityBinding(context.Context, int64, PendingAuthIdentityKey, map[string]any) error

	HasDatabase() bool
	LoadAdoptionDecision(context.Context, int64) (*IdentityAdoptionDecision, error)
	FindOAuthIdentityUser(context.Context, PendingAuthIdentityKey) (*User, error)
	FindUserByNormalizedEmail(context.Context, string) (*User, error)
	UpdateProgress(context.Context, *PendingAuthSession, string, string, *int64, map[string]any) (*PendingAuthSession, error)
	EnsureRegistrationIdentityAvailable(context.Context, *PendingAuthSession) error
	IdentityExistsForUser(context.Context, *PendingAuthSession, int64) (bool, error)
	ApplyBinding(context.Context, PendingBinding) error
	ApplyAdoption(context.Context, *PendingAuthSession, *IdentityAdoptionDecision, *int64) error
	ApplyAdoptionAndConsume(context.Context, *PendingAuthSession, *IdentityAdoptionDecision, int64) error
	FinalizeCreatedAccount(context.Context, PendingAccountFinalization) error
}
type PendingBinding struct {
	Session                           *PendingAuthSession
	Decision                          *IdentityAdoptionDecision
	OverrideUserID                    *int64
	ForceBind, ApplyFirstBindDefaults bool
}
type PendingAccountFinalization struct {
	// PrepareDecision writes the email registration adoption choice after Begin and before binding.
	PrepareDecision func(context.Context) (*IdentityAdoptionDecision, error)

	Session                       *PendingAuthSession
	Decision                      *IdentityAdoptionDecision
	User                          *User
	InvitationCode, AffiliateCode string
	BeforeCommit                  func(context.Context, *PendingAuthSession) error
}

// PendingWriteError identifies the failed phase for response and cookie cleanup handling.
type PendingWriteError struct {
	Phase string
	Cause error
}

type OAuthAdoptionChoice struct{ AdoptDisplayName, AdoptAvatar *bool }

// PendingFlow manages adoption choices and compensates for account creation failures.
type PendingFlow struct {
	Store    PendingStore
	Database PendingDatabase
	Auth     *AuthService
	Profiles *UserService
}

// WeChatIdentityChannel identifies a channel by mode and application ID.
type WeChatIdentityChannel struct{ Mode, AppID string }

func (e *PendingWriteError) Error() string { return e.Cause.Error() }
func (e *PendingWriteError) Unwrap() error { return e.Cause }

func (c OAuthAdoptionChoice) HasDecision() bool {
	return c.AdoptDisplayName != nil || c.AdoptAvatar != nil
}

func (f *PendingFlow) Available() bool {
	return f != nil && f.Database != nil && f.Database.HasDatabase()
}

func (f *PendingFlow) AdoptionDecision(ctx context.Context, sessionID int64, choice OAuthAdoptionChoice, ensure bool) (*IdentityAdoptionDecision, error) {
	if !f.Available() {
		return nil, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}
	existing, err := f.Database.LoadAdoptionDecision(ctx, sessionID)
	if err != nil {
		return nil, infraerrors.InternalServer("PENDING_AUTH_ADOPTION_LOAD_FAILED", "failed to load oauth profile adoption decision").WithCause(err)
	}
	if !choice.HasDecision() {
		if existing != nil || !ensure {
			return existing, nil
		}
	}
	input := PendingIdentityAdoptionDecisionInput{PendingAuthSessionID: sessionID}
	if existing != nil {
		input.AdoptDisplayName = existing.AdoptDisplayName
		input.AdoptAvatar = existing.AdoptAvatar
		input.IdentityID = existing.IdentityID
	}
	if choice.AdoptDisplayName != nil {
		input.AdoptDisplayName = *choice.AdoptDisplayName
	}
	if choice.AdoptAvatar != nil {
		input.AdoptAvatar = *choice.AdoptAvatar
	}
	out, err := f.Store.UpsertAdoptionDecision(ctx, input)
	if err != nil {
		return nil, infraerrors.InternalServer("PENDING_AUTH_ADOPTION_SAVE_FAILED", "failed to save oauth profile adoption decision").WithCause(err)
	}
	return out, nil
}

// FinalizeCreatedAccount binds an identity after registration commits and compensates on failure.
func (f *PendingFlow) FinalizeCreatedAccount(ctx context.Context, p PendingAccountFinalization) error {
	if err := f.Database.FinalizeCreatedAccount(ctx, p); err != nil {
		return f.compensateCreatedAccount(ctx, p, err)
	}
	return nil
}

// CompleteSecondFactorBinding commits the identity binding before consuming the browser session.
func (f *PendingFlow) CompleteSecondFactorBinding(ctx context.Context, b *PendingOAuthBindLoginSession, userID int64) error {
	if !f.Available() {
		return infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}
	p, e := f.Store.GetBrowserSession(ctx, b.PendingSessionToken, b.BrowserSessionKey)
	if e != nil {
		return e
	}
	choice, e := f.AdoptionDecision(ctx, p.ID, OAuthAdoptionChoice{}, true)
	if e != nil {
		return e
	}
	if e = f.Database.ApplyBinding(ctx, PendingBinding{Session: p, Decision: choice, OverrideUserID: &userID, ForceBind: true, ApplyFirstBindDefaults: true}); e != nil {
		return infraerrors.InternalServer("PENDING_AUTH_BIND_APPLY_FAILED", "failed to bind pending oauth identity").WithCause(e)
	}
	_, e = f.Store.ConsumeBrowserSession(ctx, p.SessionToken, p.BrowserSessionKey)
	return e
}

// FinalizeCreatedAccountWithChoice saves the adoption choice before binding and compensates on failure.
func (f *PendingFlow) FinalizeCreatedAccountWithChoice(ctx context.Context, p PendingAccountFinalization, choice OAuthAdoptionChoice) error {
	decision, err := f.AdoptionDecision(ctx, p.Session.ID, choice, true)
	if err != nil {
		return f.compensateCreatedAccount(ctx, p, err)
	}
	p.Decision = decision
	return f.FinalizeCreatedAccount(ctx, p)
}

func (f *PendingFlow) compensateCreatedAccount(ctx context.Context, p PendingAccountFinalization, original error) error {
	if p.User != nil && p.User.ID > 0 {
		if rollbackErr := f.Auth.RollbackOAuthEmailAccountCreation(ctx, p.User.ID, strings.TrimSpace(p.InvitationCode)); rollbackErr != nil {
			return &PendingWriteError{Phase: "compensation", Cause: infraerrors.InternalServer("PENDING_AUTH_ACCOUNT_ROLLBACK_FAILED", "failed to rollback pending oauth account creation").WithCause(fmt.Errorf("original error: %w; rollback error: %v", original, rollbackErr))}
		}
	}
	return original
}

// FinalizeVerifiedAccount binds a created email account and compensates even when the binding transaction cannot begin.
func (f *PendingFlow) FinalizeVerifiedAccount(ctx context.Context, p PendingAccountFinalization) error {
	if !f.Available() {
		return &PendingWriteError{Phase: "not_ready", Cause: infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")}
	}
	session := *p.Session
	session.UpstreamIdentityClaims = ClonePendingMap(p.Session.UpstreamIdentityClaims)
	if code := strings.TrimSpace(p.InvitationCode); code != "" {
		session.UpstreamIdentityClaims["invitation_code"] = code
	}
	p.Session = &session
	p.PrepareDecision = func(ctx context.Context) (*IdentityAdoptionDecision, error) {
		return f.AdoptionDecision(ctx, session.ID, OAuthAdoptionChoice{}, true)
	}
	if err := f.Database.FinalizeCreatedAccount(ctx, p); err != nil {
		// Attempt account cleanup while returning the binding failure, even if cleanup also fails.
		_ = f.Auth.RollbackOAuthEmailAccountCreation(ctx, p.User.ID, strings.TrimSpace(p.InvitationCode))
		return err
	}
	return nil
}
