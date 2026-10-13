package identity

import (
	"context"
	"fmt"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// A pending OAuth account can be created before identity binding and
// browser-session consumption finish. Failure handling therefore needs the
// created account and invitation information as well as a storage phase, so
// HTTP callers can report an error and perform the appropriate cookie
// cleanup.
//
// Profile choices preserve the difference between omitted and explicitly
// false flags, while forced binding and first-binding grants are separate
// controls. The PostgreSQL adapter owns binding transactions and invokes
// decision preparation through the original context. PendingFlow adds
// compensation around those operations, with distinct cleanup behavior for
// general finalization and verified-email finalization; a separate
// second-factor path can finish binding before its browser consume fails.

// PendingDatabase supplies pending account lookups and writes with
// adapter-owned transaction participation.
type PendingDatabase interface {
	// FindLinuxDoCompatEmailUser resolves a nonreserved email hint against
	// current local accounts. The PostgreSQL adapter trims/lowercases input,
	// returns nil for no match and rejects ambiguous normalized matches; a match
	// still needs surrounding account proof.
	FindLinuxDoCompatEmailUser(context.Context, string) (*User, error)
	// FindOIDCCompatEmailUser resolves a nonreserved email hint for OIDC account
	// choice. Missing accounts return nil, lookup failures remain errors, and
	// the returned account is an observation for later binding checks rather
	// than email ownership proof.
	FindOIDCCompatEmailUser(context.Context, string) (*User, error)
	// FindDingTalkCompatEmailUser resolves a nonreserved email hint for DingTalk
	// account choice. The PostgreSQL adapter rejects multiple normalized matches
	// and returns nil for no match; the caller verifies the provider and
	// local-account intent separately.
	FindDingTalkCompatEmailUser(context.Context, string) (*User, error)
	// FindOAuthBindTarget loads the account named by the authenticated binding
	// request. The PostgreSQL adapter reports missing users as unauthorized and
	// other failures as internal errors; surrounding handlers supply current
	// authentication and account-state checks.
	FindOAuthBindTarget(context.Context, int64) (*User, error)
	// EnsureWeChatBindOwnership inspects canonical/legacy WeChat subjects and,
	// when supplied, channel mode/application/openid ownership. Conflicting
	// active owners return errors; absent channel identifiers skip that channel
	// lookup, and this preflight performs no reservation for the later write.
	EnsureWeChatBindOwnership(context.Context, int64, string, WeChatIdentityChannel, string) error
	// FindWeChatUserByLegacyOpenID resolves a WeChat account using stable
	// subject, channel identity, then legacy openid candidates. Conflicting
	// owners are errors and a selected owner is checked for activity; the caller
	// keeps channel and provider observations aligned.
	FindWeChatUserByLegacyOpenID(context.Context, PendingAuthIdentityKey, WeChatIdentityChannel, string) (*User, error)
	// EnsureWeChatRuntimeIdentityBinding repairs the supplied account's WeChat
	// identity relationship from verified provider input. PostgreSQL owns its
	// transaction and canonical/channel uniqueness handling; claims remain
	// caller-stable during the operation and failures return before successful
	// commit.
	EnsureWeChatRuntimeIdentityBinding(context.Context, int64, PendingAuthIdentityKey, map[string]any) error

	// HasDatabase reports whether the adapter has its required database
	// connection.
	HasDatabase() bool
	// LoadAdoptionDecision reads the saved profile choice for one pending
	// session. Missing decisions return nil without error; storage failures
	// remain errors, allowing AdoptionDecision to distinguish a first choice
	// from an unavailable observation.
	LoadAdoptionDecision(context.Context, int64) (*IdentityAdoptionDecision, error)
	// FindOAuthIdentityUser resolves the trimmed provider type/key/subject to
	// its current active account. No identity returns nil, while lookup or
	// inactive-owner failures remain errors; callers use the result before
	// separately checking registration and email policy.
	FindOAuthIdentityUser(context.Context, PendingAuthIdentityKey) (*User, error)
	// FindUserByNormalizedEmail reads the local account under normalized email
	// matching. Missing input matches return ErrUserNotFound, ambiguous matches
	// remain errors, and callers provide verified email context before using
	// this observation to choose creation or binding.
	FindUserByNormalizedEmail(context.Context, string) (*User, error)
	// UpdateProgress saves intent, resolved email, target and completion state
	// from a pending snapshot. The PostgreSQL adapter clears a nil target and
	// replaces completion inside copied outer flow state; callers retain
	// consumption/lifetime validation around this separate write.
	UpdateProgress(context.Context, *PendingAuthSession, string, string, *int64, map[string]any) (*PendingAuthSession, error)
	// EnsureRegistrationIdentityAvailable rejects a pending provider subject
	// already owned by an active account. A successful preflight does not
	// reserve the subject; account finalization repeats ownership handling
	// inside its later transaction.
	EnsureRegistrationIdentityAvailable(context.Context, *PendingAuthSession) error
	// IdentityExistsForUser reports whether the pending provider subject belongs
	// to the supplied account. PostgreSQL includes WeChat compatibility keys,
	// rejects unusable input with false and preserves query errors; this read
	// supports prompt selection without consuming state.
	IdentityExistsForUser(context.Context, *PendingAuthSession, int64) (bool, error)
	// ApplyBinding applies the selected provider binding and profile choice to a
	// pending account target. A positive override wins over the session target;
	// ForceBind bypasses intent/choice selection, while ApplyFirstBindDefaults
	// requests the provider's independently idempotent first-binding grants.
	//
	// The PostgreSQL adapter joins a transaction carried by context or owns one
	// for the binding sequence. It checks identity ownership, updates selected
	// profile/metadata and decision links, and delegates applicable
	// grants/avatar work before returning. Browser-session consumption belongs
	// to a later operation, so callers handle any failure after this binding has
	// completed separately.
	ApplyBinding(context.Context, PendingBinding) error
	// ApplyAdoption delegates choice-selected binding/profile updates for the
	// pending account. A positive override supplies its target; PostgreSQL
	// requests first-binding defaults for bind_current_user intent, and
	// surrounding flows own browser consumption and later token issuance.
	ApplyAdoption(context.Context, *PendingAuthSession, *IdentityAdoptionDecision, *int64) error
	// ApplyAdoptionAndConsume applies a saved profile choice and consumes the
	// browser session for a positive account ID. PostgreSQL begins a
	// transaction, performs adoption through that transaction and checks the
	// pending session again while consuming it before commit.
	//
	// A failed adoption, consumption or commit remains an error. The caller
	// supplies matching session/account observations and performs credential
	// issuance separately after this operation succeeds. Delegated profile/grant
	// effects obey their own transaction participation, so a successful database
	// commit is the completion point for these persistent writes.
	ApplyAdoptionAndConsume(context.Context, *PendingAuthSession, *IdentityAdoptionDecision, int64) error
	// FinalizeCreatedAccount binds an already created account, completes signup
	// effects and consumes its pending browser session. PostgreSQL begins a
	// binding transaction, invokes optional PrepareDecision with the original
	// context, then binds with first-binding defaults disabled before signup
	// finalization, consumption and BeforeCommit.
	//
	// PrepareDecision can therefore persist outside the new binding transaction.
	// BeforeCommit receives its transaction context, while external hook effects
	// retain their own lifetime. Failures identify begin, adoption, binding,
	// finalize, consume, hook or commit phases; the PendingFlow caller owns
	// compensating the separately created account after those failures.
	FinalizeCreatedAccount(context.Context, PendingAccountFinalization) error
}

// PendingBinding selects a pending identity target, profile decision and
// grant behavior.
type PendingBinding struct {
	// Session supplies provider and browser observations for binding. Callers
	// retain stable matching state through the adapter's ownership and target
	// checks.
	Session *PendingAuthSession
	// Decision selects display-name/avatar adoption and may hold the identity
	// reference. ForceBind permits binding when this choice is absent or
	// declines both hints.
	Decision *IdentityAdoptionDecision
	// OverrideUserID wins when it points to a positive account ID. Otherwise the
	// PostgreSQL adapter resolves the target from the pending session.
	OverrideUserID *int64
	// ForceBind and ApplyFirstBindDefaults control different phases: forced
	// binding selects the identity operation even without an adoption choice,
	// while grants still depend on provider settings and their persisted
	// first-binding record.
	// ForceBind: bypass intent-based binding selection. ApplyFirstBindDefaults:
	// request idempotent provider grants.
	ForceBind, ApplyFirstBindDefaults bool
}

// PendingAccountFinalization carries a created account and callbacks for its
// pending signup completion.
type PendingAccountFinalization struct {
	// PrepareDecision overrides Decision when supplied. PostgreSQL calls it
	// after beginning the binding transaction but passes the original context,
	// allowing a separate decision write.
	PrepareDecision func(context.Context) (*IdentityAdoptionDecision, error)

	// Session identifies the pending identity/browser state to bind and consume.
	// Finalization requires nonnil state aligned with the already created
	// account.
	Session  *PendingAuthSession
	Decision *IdentityAdoptionDecision // saved profile adoption choice
	// User identifies the already created signup account. Finalization uses its
	// ID for forced binding and later compensation after a failed database
	// operation.
	User *User
	// InvitationCode and AffiliateCode feed different signup effects during
	// FinalizeOAuthEmailAccount. The adapter trims both; compensation uses
	// InvitationCode to restore a used invitation before deleting the created
	// account.
	// InvitationCode: signup invitation redemption text. AffiliateCode: signup
	// affiliate attribution text.
	InvitationCode, AffiliateCode string
	// BeforeCommit runs after browser consumption with the PostgreSQL
	// transaction context and session. Its error aborts commit; external
	// callback effects follow their own recovery rules.
	BeforeCommit func(context.Context, *PendingAuthSession) error
}

// PendingWriteError pairs a failed pending-write phase with the underlying
// response cause.
type PendingWriteError struct {
	Phase string // failed pending operation phase
	// Cause supplies Error and Unwrap for HTTP error handling. Every usable
	// PendingWriteError requires a nonnil cause because Error directly calls its
	// message.
	Cause error
}

// OAuthAdoptionChoice distinguishes omitted profile selections from
// explicitly accepted or declined suggestions.
type OAuthAdoptionChoice struct {
	// AdoptDisplayName and AdoptAvatar retain omission with nil pointers. Each
	// nonnil pointer supplies its own boolean, so a false choice still counts as
	// an explicit decision.
	// AdoptDisplayName: provider name adoption choice. AdoptAvatar: provider
	// avatar adoption choice.
	AdoptDisplayName, AdoptAvatar *bool
}

// PendingFlow coordinates saved adoption choices, account finalization and
// signup failure compensation.
type PendingFlow struct {
	// Store reads browser sessions and persists profile choices. Available does
	// not check it, so callers assemble it before decision or second-factor
	// operations.
	Store    PendingStore
	Database PendingDatabase // pending account database operations
	// Auth supplies created-account cleanup and signup operations. Compensation
	// paths require this service when their supplied user has a positive ID.
	Auth     *AuthService
	Profiles *UserService // selected profile update service
}

// WeChatIdentityChannel identifies the provider channel used to interpret an
// application-local subject.
type WeChatIdentityChannel struct {
	// Mode and AppID identify the same WeChat channel namespace. Ownership
	// lookups combine that pair with channelSubject, keeping application-local
	// openids distinct from global provider subjects.
	// Mode: selected authorization channel mode. AppID: selected channel
	// application identifier.
	Mode, AppID string
}

// Error returns the underlying cause's display text for pending response
// handling. The receiver and Cause are required; a nil cause cannot supply
// this message, so constructors pair every phase with an error.
func (e *PendingWriteError) Error() string { return e.Cause.Error() }

// Unwrap returns the pending failure cause for standard error-chain
// inspection.
func (e *PendingWriteError) Unwrap() error { return e.Cause }

// HasDecision reports whether either adoption flag was supplied, including
// false.
func (c OAuthAdoptionChoice) HasDecision() bool {
	return c.AdoptDisplayName != nil || c.AdoptAvatar != nil
}

// Available reports whether this flow has a database-backed adapter. It
// accepts a nil flow and does not validate Store, Auth or Profiles; callers
// assemble those additional dependencies before the operations that use them.
func (f *PendingFlow) Available() bool {
	return f != nil && f.Database != nil && f.Database.HasDatabase()
}

// AdoptionDecision merges supplied profile flags with the saved choice for
// one session. An omitted flag retains its earlier value, including an
// existing identity reference; explicit false replaces a previous true just
// as explicit true does.
//
// Without a supplied choice, an existing decision is returned directly, or
// absence is returned when ensure is false. ensure=true creates an absent
// false/false decision so later binding has a saved choice. Loading and
// upserting remain separate operations, so callers handle storage errors and
// the store's own concurrent-update behavior rather than treating this merge
// as a reserved snapshot.
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
	// Retain earlier flags and identity linkage before overlaying supplied
	// pointers; an omitted field cannot erase a stored decision for the other
	// profile hint.
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

// FinalizeCreatedAccount delegates binding/signup/consumption for an already
// created account and compensates a failed result. Callers provide the
// database and authentication services, a matching session and account, and
// invitation information needed to undo that separate creation.
//
// A positive account ID triggers RollbackOAuthEmailAccountCreation after any
// database-finalization failure. Successful cleanup retains the original
// phase/error; cleanup failure becomes a compensation phase with the original
// error still wrapped. A missing or nonpositive account skips cleanup, so
// this wrapper cannot infer or recover an account that the caller did not
// identify.
func (f *PendingFlow) FinalizeCreatedAccount(ctx context.Context, p PendingAccountFinalization) error {
	if err := f.Database.FinalizeCreatedAccount(ctx, p); err != nil {
		return f.compensateCreatedAccount(ctx, p, err)
	}
	return nil
}

// CompleteSecondFactorBinding reloads the browser-bound pending session after
// the caller proves the supplied account's second factor. It ensures a saved
// default adoption choice, then forces binding to userID with first-binding
// grants requested before separately consuming the browser session.
//
// The database-backed readiness check does not establish Store or Auth
// readiness. Decision persistence and binding can complete before consumption
// fails, because the final consume is a separate store call. Callers
// therefore preserve the returned failure and do not treat it as proof that
// no identity relationship was written.
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
	// Ensure the choice before forced binding, then consume separately; a later
	// consume failure can follow completed identity updates and first-binding
	// grant requests.
	if e = f.Database.ApplyBinding(ctx, PendingBinding{Session: p, Decision: choice, OverrideUserID: &userID, ForceBind: true, ApplyFirstBindDefaults: true}); e != nil {
		return infraerrors.InternalServer("PENDING_AUTH_BIND_APPLY_FAILED", "failed to bind pending oauth identity").WithCause(e)
	}
	_, e = f.Store.ConsumeBrowserSession(ctx, p.SessionToken, p.BrowserSessionKey)
	return e
}

// FinalizeCreatedAccountWithChoice saves the selected profile flags before
// finalizing an already created account. Callers supply a nonnil session and
// matching account; omitted flags retain existing choices, while ensure=true
// gives finalization a stored default decision when none exists.
//
// A failed decision save triggers account compensation before database
// binding is attempted. Once a decision is available, FinalizeCreatedAccount
// owns the finalization failure/cleanup result. The decision write is
// separate from that binding transaction, so an error or compensated account
// creation can leave a previously saved choice for the pending session.
func (f *PendingFlow) FinalizeCreatedAccountWithChoice(ctx context.Context, p PendingAccountFinalization, choice OAuthAdoptionChoice) error {
	// Save the submitted choice before binding the separately created account; a
	// save failure must enter account compensation because creation already
	// happened upstream.
	decision, err := f.AdoptionDecision(ctx, p.Session.ID, choice, true)
	if err != nil {
		return f.compensateCreatedAccount(ctx, p, err)
	}
	p.Decision = decision
	return f.FinalizeCreatedAccount(ctx, p)
}

// compensateCreatedAccount attempts signup cleanup for the supplied positive
// account ID after an earlier failure. It delegates invitation restoration
// followed by account deletion to AuthService and returns the earlier error
// when cleanup succeeds or no account is identified.
//
// A cleanup failure replaces the response phase with compensation. Its cause
// wraps the earlier error through %w and includes cleanup text through %v, so
// standard error inspection still reaches the original cause but not the
// rollback error independently. Callers retain the account/invitation
// identifiers needed by this recovery path.
func (f *PendingFlow) compensateCreatedAccount(ctx context.Context, p PendingAccountFinalization, original error) error {
	// Only an identified created account can be cleaned up; restoration/deletion
	// failures retain the original cause while changing the response phase to
	// compensation.
	if p.User != nil && p.User.ID > 0 {
		if rollbackErr := f.Auth.RollbackOAuthEmailAccountCreation(ctx, p.User.ID, strings.TrimSpace(p.InvitationCode)); rollbackErr != nil {
			return &PendingWriteError{Phase: "compensation", Cause: infraerrors.InternalServer("PENDING_AUTH_ACCOUNT_ROLLBACK_FAILED", "failed to rollback pending oauth account creation").WithCause(fmt.Errorf("original error: %w; rollback error: %v", original, rollbackErr))}
		}
	}
	return original
}

// FinalizeVerifiedAccount finalizes a created local-email account using a
// default saved profile decision. Missing database readiness returns
// not_ready immediately; that early return performs no account compensation.
// Ready callers provide nonnil session/account data and the
// store/authentication dependencies used by decision preparation and
// recovery.
//
// The operation copies the session and outer claims map, adds a trimmed
// invitation hint when present, and replaces PrepareDecision with its
// default-decision callback. Nested claims and the session's other referenced
// values remain shared. PostgreSQL invokes that callback with the original
// context after beginning its binding transaction. A later finalization
// failure attempts account cleanup but suppresses cleanup failure and returns
// the original database phase/error.
func (f *PendingFlow) FinalizeVerifiedAccount(ctx context.Context, p PendingAccountFinalization) error {
	if !f.Available() {
		return &PendingWriteError{Phase: "not_ready", Cause: infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")}
	}
	// Copy the session and outer claim map before adding the invitation hint,
	// keeping caller-owned map entries intact while retaining nested claim
	// sharing.
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
		// Database finalization has already failed after account creation; attempt
		// invitation/account cleanup, suppress its failure and keep the original
		// phase for HTTP response handling.
		_ = f.Auth.RollbackOAuthEmailAccountCreation(ctx, p.User.ID, strings.TrimSpace(p.InvitationCode))
		return err
	}
	return nil
}
