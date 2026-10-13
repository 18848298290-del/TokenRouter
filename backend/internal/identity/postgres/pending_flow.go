package postgres

import (
	"context"
	"strings"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/ent/identityadoptiondecision"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Pending account completion combines identity binding, signup finalization,
// and browser-session consumption around an already created local account. The
// database adapter supplies entity conversion and current-row lookup while the
// identity flow decides which account and adoption choices reach those writes.
//
// FinalizeCreatedAccount starts its own transaction, then passes that
// transaction context through binding, signup completion, consumption, and the
// optional commit hook. Decision preparation instead receives the original
// context, so its persistence scope is selected by that callback.
// Phase-specific errors let the identity flow compensate earlier account
// creation. Rollback is attempted on exit, and committing completes the
// database stages before HTTP post-registration work runs.

// PendingFlowDatabase supplies database reads and transactional writes for
// identity pending completion.
type PendingFlowDatabase struct {
	Client   *dbent.Client             // pending authentication Ent client
	Auth     *identitycore.AuthService // signup and binding grant service
	Profiles *identitycore.UserService // profile adoption account service
}

// HasDatabase reports whether this pending database adapter has an Ent client
// configured.
func (d *PendingFlowDatabase) HasDatabase() bool { return d != nil && d.Client != nil }

// LoadAdoptionDecision reads the saved profile choice for the pending session
// ID. A missing decision returns nil with no error, while other lookup
// failures remain errors for the identity flow.
func (d *PendingFlowDatabase) LoadAdoptionDecision(ctx context.Context, id int64) (*identitycore.IdentityAdoptionDecision, error) {
	v, err := d.Client.IdentityAdoptionDecision.Query().Where(identityadoptiondecision.PendingAuthSessionIDEQ(id)).Only(ctx)
	// Absent adoption choice is a usable pending state; return no decision so the
	// flow can request or prepare profile selection when needed.
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	return IdentityAdoptionDecisionFromEntity(v), err
}

// FindUserByNormalizedEmail resolves one trimmed, case-folded account email
// and maps its entity result. Missing or ambiguous accounts retain their
// domain lookup errors; this read does not prove email ownership.
func (d *PendingFlowDatabase) FindUserByNormalizedEmail(ctx context.Context, email string) (*identitycore.User, error) {
	v, e := FindUserByNormalizedEmail(ctx, d.Client, email)
	return UserFromEntity(v), e
}

// UpdateProgress saves pending intent, email, target, and completion state
// through the database helper. Entity conversion remains shallow, and its
// stored result or error passes back to the pending flow.
func (d *PendingFlowDatabase) UpdateProgress(ctx context.Context, p *identitycore.PendingAuthSession, intent, email string, target *int64, state map[string]any) (*identitycore.PendingAuthSession, error) {
	v, e := UpdatePendingOAuthSessionProgress(ctx, d.Client, PendingAuthSessionToEntity(p), intent, email, target, state)
	return PendingAuthSessionFromEntity(v), e
}

// EnsureRegistrationIdentityAvailable delegates the provider tuple precheck
// before pending account creation. Existing active ownership is rejected, but
// registration completion still performs its own transactional binding check.
func (d *PendingFlowDatabase) EnsureRegistrationIdentityAvailable(ctx context.Context, p *identitycore.PendingAuthSession) error {
	return EnsurePendingOAuthRegistrationIdentityAvailable(ctx, d.Client, PendingAuthSessionToEntity(p))
}

// IdentityExistsForUser delegates provider binding inspection for the supplied
// account and pending tuple. Missing inputs return false according to the
// helper, while storage lookup errors stop the flow decision.
func (d *PendingFlowDatabase) IdentityExistsForUser(ctx context.Context, p *identitycore.PendingAuthSession, id int64) (bool, error) {
	return PendingOAuthIdentityExistsForUser(ctx, d.Client, PendingAuthSessionToEntity(p), id)
}

// ApplyBinding delegates prepared account binding and selected profile choices
// after entity conversion. The helper joins a supplied transaction or owns
// one; force and first-bind options come from the identity flow.
func (d *PendingFlowDatabase) ApplyBinding(ctx context.Context, p identitycore.PendingBinding) error {
	return ApplyPendingOAuthBinding(ctx, d.Client, d.Auth, d.Profiles, PendingAuthSessionToEntity(p.Session), IdentityAdoptionDecisionToEntity(p.Decision), p.OverrideUserID, p.ForceBind, p.ApplyFirstBindDefaults)
}

// ApplyAdoption delegates saved profile choices for the supplied pending state
// and optional target override. The helper chooses binding from intent and
// decision, with transaction completion owned by its participation path.
func (d *PendingFlowDatabase) ApplyAdoption(ctx context.Context, p *identitycore.PendingAuthSession, choice *identitycore.IdentityAdoptionDecision, id *int64) error {
	return ApplyPendingOAuthAdoption(ctx, d.Client, d.Auth, d.Profiles, PendingAuthSessionToEntity(p), IdentityAdoptionDecisionToEntity(choice), id)
}

// ApplyAdoptionAndConsume delegates adoption and browser consumption within
// one owned transaction. The supplied target account must be positive, and any
// binding or consumption failure prevents that transaction from committing.
func (d *PendingFlowDatabase) ApplyAdoptionAndConsume(ctx context.Context, p *identitycore.PendingAuthSession, choice *identitycore.IdentityAdoptionDecision, id int64) error {
	return ApplyPendingOAuthAdoptionAndConsumeSession(ctx, d.Client, d.Auth, d.Profiles, PendingAuthSessionToEntity(p), IdentityAdoptionDecisionToEntity(choice), id)
}

// FinalizeCreatedAccount completes binding, signup, and consumption for a
// validated already-created account and pending session. It returns
// PendingWriteError with the failing database phase for flow compensation.
//
// An owned transaction is opened before optional decision preparation, but
// PrepareDecision receives the original context. Binding, email-account
// finalization, browser consumption, and BeforeCommit then receive the
// injected transaction context. Their success is committed together; rollback
// errors are discarded on exit. Callers retain the separate compensation
// responsibility for account creation and any callback work outside this
// transaction.
func (d *PendingFlowDatabase) FinalizeCreatedAccount(ctx context.Context, p identitycore.PendingAccountFinalization) error {
	failure := func(phase string, err error) error { return &identitycore.PendingWriteError{Phase: phase, Cause: err} }
	tx, err := d.Client.Tx(ctx)
	if err != nil {
		return failure("begin", err)
	}
	// Rollback covers any failing binding, signup, consumption, or commit-hook
	// stage. Its cleanup error is discarded while the original phase error
	// remains the compensation input.
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	// Decision preparation uses the original context after the transaction
	// begins. Its callback selects persistence participation independently, while
	// subsequent completion writes use the injected transaction context.
	if p.PrepareDecision != nil {
		decision, err := p.PrepareDecision(ctx)
		if err != nil {
			return failure("adoption", err)
		}
		p.Decision = decision
	}
	session, choice := PendingAuthSessionToEntity(p.Session), IdentityAdoptionDecisionToEntity(p.Decision)
	if err := ApplyPendingOAuthBinding(txCtx, d.Client, d.Auth, d.Profiles, session, choice, &p.User.ID, true, false); err != nil {
		return failure("binding", err)
	}
	if err := d.Auth.FinalizeOAuthEmailAccount(txCtx, p.User, strings.TrimSpace(p.InvitationCode), strings.TrimSpace(p.Session.ProviderType), strings.TrimSpace(p.AffiliateCode)); err != nil {
		return failure("finalize", err)
	}
	if err := ConsumePendingOAuthBrowserSessionTx(txCtx, tx, session); err != nil {
		return failure("consume", err)
	}
	if p.BeforeCommit != nil {
		if err := p.BeforeCommit(txCtx, p.Session); err != nil {
			return failure("hook", err)
		}
	}
	// Commit after binding, signup completion, session consumption, and the
	// optional hook succeed, so database failure still reaches the identity flow
	// as a finalization phase error.
	if err := tx.Commit(); err != nil {
		return failure("commit", err)
	}
	return nil
}

// FindOAuthIdentityUser resolves a trimmed provider tuple to its current
// active account. Missing identities return nil with no error, absent storage
// is unavailable, and owner lookup can reject an inactive account.
func (d *PendingFlowDatabase) FindOAuthIdentityUser(ctx context.Context, identity identitycore.PendingAuthIdentityKey) (*identitycore.User, error) {
	client := d.Client
	if client == nil {
		return nil, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}

	record, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ(strings.TrimSpace(identity.ProviderType)),
			authidentity.ProviderKeyEQ(strings.TrimSpace(identity.ProviderKey)),
			authidentity.ProviderSubjectEQ(strings.TrimSpace(identity.ProviderSubject)),
		).
		Only(ctx)
	if err != nil {
		// Identity absence leaves no existing provider owner for callback account
		// choice or registration. Other lookup failures return an application error
		// for the surrounding continuation to handle.
		if dbent.IsNotFound(err) {
			return nil, nil
		}
		return nil, infraerrors.InternalServer("AUTH_IDENTITY_LOOKUP_FAILED", "failed to inspect auth identity ownership").WithCause(err)
	}
	v, e := FindActiveUserByID(ctx, client, record.UserID)
	return UserFromEntity(v), e
}
