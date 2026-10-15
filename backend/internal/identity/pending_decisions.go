package identity

import (
	"context"
	"errors"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// pending_decisions.go selects pending registration and account-choice
// transitions from provider ownership, email matches, and local requirements.
// Pending registration first distinguishes a provider identity owner from an
// account found by normalized email. Those observations select account
// creation or existing-account choice, while a mismatched identity email is
// rejected instead of silently selecting another target.
//
// Legacy sessions can already carry a frontend step and need no storage
// rewrite. Newly imposed email requirements upgrade otherwise completed state
// and clear its target before asking for choice. Profile-prompt skipping
// additionally needs usable upstream hints and a current identity association
// for an issuable login target. These decisions return progression
// eligibility or saved state; account proof, session consumption and eventual
// token issuance remain separate phases.

// EmailShouldCreatePendingRegistration decides whether an email OAuth
// observation lacks both an identity owner and a matching account. An
// existing identity owner must match the supplied lowercased/trimmed email; a
// mismatch returns the identity-email conflict instead of account-creation
// eligibility.
//
// Without that identity, only ErrUserNotFound from normalized account lookup
// returns true. A successful lookup, even nil data without an error, returns
// false. Other lookup failures remain errors, and this decision does not
// verify email claims, registration policy or ownership at a later write;
// callers provide verified input and still complete those separate checks.
func (f *PendingFlow) EmailShouldCreatePendingRegistration(ctx context.Context, input EmailOAuthIdentityInput) (bool, error) {
	if !f.Available() {
		return false, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}
	identityUser, err := f.Database.FindOAuthIdentityUser(ctx, PendingAuthIdentityKey{
		ProviderType:    strings.TrimSpace(input.ProviderType),
		ProviderKey:     strings.TrimSpace(input.ProviderKey),
		ProviderSubject: strings.TrimSpace(input.ProviderSubject),
	})
	if err != nil {
		return false, err
	}
	email := strings.TrimSpace(strings.ToLower(input.Email))
	if identityUser != nil {
		if !strings.EqualFold(strings.TrimSpace(identityUser.Email), email) {
			return false, infraerrors.Conflict("AUTH_IDENTITY_EMAIL_MISMATCH", "oauth identity belongs to a different email")
		}
		return false, nil
	}
	if _, err := f.Database.FindUserByNormalizedEmail(ctx, email); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// LegacyRegistrationStatus reports whether a legacy pending session still
// requires frontend progression. A nonblank normalized step returns the
// original session with required=true without writing, while absence of both
// email requirements returns it with false.
//
// Only an otherwise completed session newly requiring email progression needs
// database availability and UpdateProgress. That update supplies a nil
// target, which the PostgreSQL adapter clears, and saves the upgraded choice
// payload. Readiness is therefore checked only for this transition, and
// persistence errors remain errors rather than proof that a frontend step was
// already saved.
func (f *PendingFlow) LegacyRegistrationStatus(
	ctx context.Context,
	session *PendingAuthSession, emailVerificationRequired, forceEmailOnSignup bool,
) (*PendingAuthSession, bool, error) {
	if session == nil {
		return nil, false, infraerrors.BadRequest("PENDING_AUTH_SESSION_INVALID", "pending auth registration context is invalid")
	}

	payload := NormalizePendingOAuthCompletionResponse(MergePendingCompletionResponse(session, nil))
	if step := PendingSessionStringValue(payload, "step"); step != "" {
		return session, true, nil
	}

	if !emailVerificationRequired && !forceEmailOnSignup {
		return session, false, nil
	}

	if !f.Available() {
		return nil, false, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}

	// Persist the newly required choice step with a cleared target so the older
	// completed observation cannot continue as an already selected account
	// during this progression.
	updatedSession, err := f.Database.UpdateProgress(
		ctx,
		session,
		strings.TrimSpace(session.Intent),
		strings.TrimSpace(session.ResolvedEmail),
		nil,
		BuildLegacyCompleteRegistrationPendingResponse(session, forceEmailOnSignup, emailVerificationRequired),
	)
	if err != nil {
		return nil, false, infraerrors.InternalServer("PENDING_AUTH_SESSION_UPDATE_FAILED", "failed to update pending oauth session").WithCause(err)
	}
	return updatedSession, true, nil
}

// TransitionAccountToChoice saves frontend choice controls and a supplied
// positive account target for a nonnil session. Nil or nonpositive target
// data clears the PostgreSQL target; the adapter trims email/intent and
// replaces completion state from the supplied snapshot. Callers assemble the
// database and keep those observations aligned.
func (f *PendingFlow) TransitionAccountToChoice(
	ctx context.Context,
	session *PendingAuthSession,
	targetUser *User,
	email string,
) (*PendingAuthSession, error) {
	completionResponse := PendingOAuthChoiceCompletionResponse(session, email)
	var targetUserID *int64
	if targetUser != nil && targetUser.ID > 0 {
		targetUserID = &targetUser.ID
	}
	session, err := f.Database.UpdateProgress(
		ctx,
		session,
		strings.TrimSpace(session.Intent),
		email,
		targetUserID,
		completionResponse,
	)
	if err != nil {
		return nil, infraerrors.InternalServer("PENDING_AUTH_SESSION_UPDATE_FAILED", "failed to update pending oauth session").WithCause(err)
	}
	return session, nil
}

// SkipAdoptionPrompt reports whether an already associated identity can
// bypass profile choice for an otherwise issuable login completion. Nil
// sessions, empty payloads or blocked issuance return false; absence of
// nonblank upstream display/avatar hints also returns false.
//
// Once those conditions hold, the supplied database checks identity
// association for the positive target account. A true result describes that
// lookup, not session consumption or completed token issuance; query failures
// remain errors. Callers provide the database before this eligible path and
// retain current account/lifetime checks at their surrounding exchange entry
// point.
func (f *PendingFlow) SkipAdoptionPrompt(
	ctx context.Context,
	session *PendingAuthSession,
	payload map[string]any,
) (bool, error) {
	if session == nil || len(payload) == 0 {
		return false, nil
	}
	if !PendingOAuthCompletionCanIssueTokenPair(session, payload) {
		return false, nil
	}
	if PendingSessionStringValue(session.UpstreamIdentityClaims, "suggested_display_name") == "" &&
		PendingSessionStringValue(session.UpstreamIdentityClaims, "suggested_avatar_url") == "" {
		return false, nil
	}

	return f.Database.IdentityExistsForUser(ctx, session, *session.TargetUserID)
}
