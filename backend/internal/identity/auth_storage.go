package identity

import (
	"context"
)

// Authentication writes use storage participants for registered users,
// invitation claims, identity ownership and first-bind grants. The service
// forwards prepared inputs, while the adapter chooses transaction or savepoint
// participation and leaves post-commit cache and session effects to its caller.

// AuthStorage executes authentication writes within transaction and savepoint scopes.
// Storage participants leave post-commit side effects to their callers.
type AuthStorage interface {
	// HasDatabase reports whether storage has a configured database client. The
	// production adapter checks configuration presence rather than probing a live
	// connection; callers still handle operation failures separately.
	HasDatabase() bool
	// ApplyProviderDefaultSettingsOnFirstBind applies the enabled provider grant
	// for a user. The production adapter uses the supplied transaction or creates
	// one, records the first-bind claim before balance, concurrency and
	// subscription writes. A transaction created here commits their successful
	// combined result; an existing transaction remains the caller's commit scope.
	// Duplicate claims skip the grant. Missing database or settings and
	// nonpositive user IDs are successful no-ops, while transaction or grant
	// failures remain errors.
	ApplyProviderDefaultSettingsOnFirstBind(
		ctx context.Context,
		userID int64,
		providerType string,
	) error
	// AuthApplyProviderDefaultSettingsOnFirstBind participates in the caller's
	// first-bind transaction. The production adapter records the unique
	// user/provider/reason claim before applying enabled balance, concurrency and
	// subscription defaults; callers supply the rollback scope covering those
	// writes. An existing claim returns without applying defaults again. Claim
	// insertion and the later funding writes need one commit scope so a failed
	// grant does not leave a committed first-bind marker behind.
	AuthApplyProviderDefaultSettingsOnFirstBind(
		ctx context.Context,
		userID int64,
		providerType string,
	) error
	// AuthCreateRegisteredUser creates the supplied user with current account
	// defaults and registration guards. The production adapter rechecks an
	// enforced domain quota inside its transaction, applies normalized-email
	// locking when configured, creates the user, then consumes a supplied
	// invitation before commit. A nil user is a no-op. Database-free fixtures use
	// their repositories directly, so their rollback behavior differs from the
	// production transaction.
	AuthCreateRegisteredUser(ctx context.Context, user *User, artifacts *AuthRegistrationArtifacts) error
	// AuthEnsureEmailAuthIdentity returns the user-owned email identity and its
	// creation hint. Missing database, reserved email, lookup/write failure or
	// another owner yields nil and false. The production hint records absence
	// during preflight, which is not an insert-ownership claim after a concurrent
	// conflict.
	AuthEnsureEmailAuthIdentity(ctx context.Context, user *User, source string) (*AuthIdentity, bool)
	// AuthEnsureEmailOAuthIdentity creates or refreshes the selected provider
	// tuple for a user. The production adapter rejects another owner, updates
	// matching identity metadata and returns persistence errors; the caller has
	// already validated the profile input.
	AuthEnsureEmailOAuthIdentity(ctx context.Context, userID int64, input EmailOAuthIdentityInput) error
	// AuthFindEmailOAuthIdentityOwner resolves the exact provider type, key and
	// subject to a current user. Absent identities or missing users return nil
	// with no error; other lookup failures remain errors for the login decision.
	AuthFindEmailOAuthIdentityOwner(ctx context.Context, providerType, providerKey, providerSubject string) (*User, error)
	// AuthHasProviderGrantRecord reports whether the supplied user/provider/reason
	// claim exists. Missing database or a nonpositive user ID returns false
	// without error; database failures remain errors so grant callers can choose
	// their conservative result.
	AuthHasProviderGrantRecord(
		ctx context.Context,
		userID int64,
		providerType string,
		grantReason string,
	) (bool, error)
	// AuthLoadOAuthRegistrationInvitation reads the submitted invitation snapshot
	// for account completion. The production adapter joins an existing transaction
	// when available; database-free fixtures use the injected redeem repository
	// and retain its lookup result.
	AuthLoadOAuthRegistrationInvitation(ctx context.Context, invitationCode string) (*RedeemCode, error)
	// AuthRestoreOAuthRegistrationInvitation restores a used invitation owned by
	// the supplied user. Disabled invitations, blank codes, invalid users, missing
	// records or mismatching ownership are no-ops; storage failures remain errors
	// for registration recovery.
	AuthRestoreOAuthRegistrationInvitation(ctx context.Context, invitationCode string, userID int64) error
	// AuthRunFailOpenDBStep runs an optional callback within the caller's
	// persistence scope. A nil callback succeeds, and a missing transaction calls
	// it directly. With a transaction, the production adapter creates a named
	// savepoint, rolls back a failed step and releases the savepoint before
	// returning its error. Callers supply a trusted SQL identifier and decide
	// whether that returned failure should stop their surrounding authentication
	// operation.
	AuthRunFailOpenDBStep(ctx context.Context, savepointName string, fn func(context.Context) error) error
	// AuthShouldApplyEmailFirstBindDefaults selects email first-bind eligibility
	// from a non-nil identity and its creation hint. Login-backfill source
	// suppresses grants; other created hints permit them, while older dual-write
	// identities need a matching owner and no existing grant claim. A failed claim
	// lookup returns false. Callers keep the supplied identity associated with
	// this user; a created hint bypasses the later existing-identity owner branch.
	AuthShouldApplyEmailFirstBindDefaults(
		ctx context.Context,
		userID int64,
		identity *AuthIdentity,
		created bool,
	) bool
	// AuthTouchUserLogin attempts to update login and activity timestamps for a
	// positive user ID. Missing database or invalid IDs are skipped; write
	// failures are diagnosed and leave the authentication caller with no error
	// result.
	AuthTouchUserLogin(ctx context.Context, userID int64)
	// AuthUpdateBoundEmailIdentityTx coordinates account credentials and their
	// email identity in a transaction. The production adapter joins a supplied
	// transaction or opens one, rechecks alias or normalized-email ownership,
	// writes email and password, replaces the email identity and optionally
	// applies first-bind grants before returning. The working currentUser fields
	// are refreshed before commit, so a failed commit can leave that object
	// changed; callers reload after failure before reusing its account snapshot.
	// BindEmailIdentity attempts refresh-session revocation after this operation
	// returns successfully and diagnoses revocation failure. A joined
	// transaction still needs its caller's completion.
	// AuthDependencies.Invalidator and BalanceCache are not invoked by this
	// adapter operation.
	AuthUpdateBoundEmailIdentityTx(
		ctx context.Context,
		currentUser *User,
		email string,
		registrationNormalizedEmail string,
		hashedPassword string,
		applyFirstBindDefaults bool,
	) error
	// AuthUpdateOAuthRegistrationInvitation restores the supplied invitation
	// snapshot through storage. Nil input succeeds; a database transaction uses
	// its participation adapter, while fixture repositories receive their snapshot
	// through the ordinary Update method.
	AuthUpdateOAuthRegistrationInvitation(ctx context.Context, code *RedeemCode) error
	// AuthUpdateOAuthSignupSource attempts a signup-source write isolated by the
	// optional savepoint helper. Missing database, invalid user IDs and blank
	// source skip work; failures are diagnosed while authentication may continue
	// independently.
	AuthUpdateOAuthSignupSource(ctx context.Context, userID int64, signupSource string)
	// AuthUpdateUserSignupSource attempts the supplied signup-source write for a
	// positive user. The production adapter skips missing database or blank source
	// and diagnoses update failure because this method exposes no error result.
	AuthUpdateUserSignupSource(ctx context.Context, userID int64, signupSource string)
	// AuthUseOAuthRegistrationInvitation consumes the invitation for the selected
	// user during completion. Existing database transactions supply the
	// participation adapter; database-free fixtures delegate to Redeem.Use, and a
	// failed claim remains an error.
	AuthUseOAuthRegistrationInvitation(ctx context.Context, invitationID, userID int64) error
}

// ApplyProviderDefaultSettingsOnFirstBind delegates provider grant application
// to configured storage. Callers provide the positive user and selected
// provider after binding; storage owns transaction participation, and the
// returned failure still requires caller handling.
func (s *AuthService) ApplyProviderDefaultSettingsOnFirstBind(
	ctx context.Context,
	userID int64,
	providerType string,
) error {
	return s.Storage.ApplyProviderDefaultSettingsOnFirstBind(ctx, userID, providerType)
}

// AuthApplyProviderDefaultSettingsOnFirstBind delegates the first-bind
// participant operation to storage. Callers keep the surrounding transaction
// available across their binding and grant writes; this forwarding method
// returns the participant error unchanged.
func (s *AuthService) AuthApplyProviderDefaultSettingsOnFirstBind(
	ctx context.Context,
	userID int64,
	providerType string,
) error {
	return s.Storage.AuthApplyProviderDefaultSettingsOnFirstBind(ctx, userID, providerType)
}

// AuthCreateRegisteredUser forwards the planned user and registration artifacts
// to storage. A configured Storage implementation is required; it owns
// current-default and guard checks before creation, with failures returned to
// the registration flow.
func (s *AuthService) AuthCreateRegisteredUser(ctx context.Context, user *User, artifacts *AuthRegistrationArtifacts) error {
	return s.Storage.AuthCreateRegisteredUser(ctx, user, artifacts)
}

// AuthEnsureEmailAuthIdentity forwards email identity backfill and returns its
// identity and hint. Callers use nil identity as a skipped or unsuccessful
// attempt, and leave eligibility decisions to
// AuthShouldApplyEmailFirstBindDefaults after a usable result.
func (s *AuthService) AuthEnsureEmailAuthIdentity(ctx context.Context, user *User, source string) (*AuthIdentity, bool) {
	return s.Storage.AuthEnsureEmailAuthIdentity(ctx, user, source)
}

// AuthEnsureEmailOAuthIdentity forwards provider-identity persistence for the
// selected user and input. Storage must be configured; it owns conflict
// detection and metadata writes, while the caller handles any error before
// continuing authentication.
func (s *AuthService) AuthEnsureEmailOAuthIdentity(ctx context.Context, userID int64, input EmailOAuthIdentityInput) error {
	return s.Storage.AuthEnsureEmailOAuthIdentity(ctx, userID, input)
}

// AuthFindEmailOAuthIdentityOwner forwards the exact external tuple lookup to
// storage. A nil user with nil error means the flow has no current owner to
// reuse; other errors remain part of its login decision.
func (s *AuthService) AuthFindEmailOAuthIdentityOwner(ctx context.Context, providerType, providerKey, providerSubject string) (*User, error) {
	return s.Storage.AuthFindEmailOAuthIdentityOwner(ctx, providerType, providerKey, providerSubject)
}

// AuthHasProviderGrantRecord delegates the grant-claim lookup using the
// supplied user, provider and reason. Callers interpret both its boolean and
// error before deciding eligibility, and this method adds no cached decision.
func (s *AuthService) AuthHasProviderGrantRecord(
	ctx context.Context,
	userID int64,
	providerType string,
	grantReason string,
) (bool, error) {
	return s.Storage.AuthHasProviderGrantRecord(ctx, userID, providerType, grantReason)
}

// AuthLoadOAuthRegistrationInvitation delegates invitation snapshot loading to
// storage for account completion. The caller retains the returned snapshot when
// planning recovery; storage errors are returned before the flow attempts
// invitation consumption.
func (s *AuthService) AuthLoadOAuthRegistrationInvitation(ctx context.Context, invitationCode string) (*RedeemCode, error) {
	return s.Storage.AuthLoadOAuthRegistrationInvitation(ctx, invitationCode)
}

// AuthRestoreOAuthRegistrationInvitation requests storage recovery for a user's
// submitted invitation code. The caller invokes it after its registration
// decision requires restoration, and handles the returned recovery failure
// independently of the initial error.
func (s *AuthService) AuthRestoreOAuthRegistrationInvitation(ctx context.Context, invitationCode string, userID int64) error {
	return s.Storage.AuthRestoreOAuthRegistrationInvitation(ctx, invitationCode, userID)
}

// AuthRunFailOpenDBStep delegates optional persistence work and its trusted
// savepoint name to storage. It returns the step error rather than suppressing
// it; authentication callers decide whether that failure is tolerable.
func (s *AuthService) AuthRunFailOpenDBStep(ctx context.Context, savepointName string, fn func(context.Context) error) error {
	return s.Storage.AuthRunFailOpenDBStep(ctx, savepointName, fn)
}

// AuthShouldApplyEmailFirstBindDefaults forwards grant eligibility for a usable
// non-nil email identity. Callers pass the creation hint from their ensure
// operation; storage owns source and grant-record checks before returning its
// decision.
func (s *AuthService) AuthShouldApplyEmailFirstBindDefaults(
	ctx context.Context,
	userID int64,
	identity *AuthIdentity,
	created bool,
) bool {
	return s.Storage.AuthShouldApplyEmailFirstBindDefaults(ctx, userID, identity, created)
}

// AuthTouchUserLogin requests storage to record the selected user's login
// activity. A configured Storage implementation is required, and its
// diagnostic-only result lets successful authentication proceed when this
// optional timestamp write fails.
func (s *AuthService) AuthTouchUserLogin(ctx context.Context, userID int64) {
	s.Storage.AuthTouchUserLogin(ctx, userID)
}

// AuthUpdateBoundEmailIdentityTx forwards the current user and planned
// email/password binding to storage. The caller provides prepared
// normalized-email and grant decisions; storage owns transaction participation.
// BindEmailIdentity requests refresh-session cleanup after a successful return,
// including when an external transaction owner still needs to commit.
func (s *AuthService) AuthUpdateBoundEmailIdentityTx(
	ctx context.Context,
	currentUser *User,
	email string,
	registrationNormalizedEmail string,
	hashedPassword string,
	applyFirstBindDefaults bool,
) error {
	return s.Storage.AuthUpdateBoundEmailIdentityTx(ctx, currentUser, email, registrationNormalizedEmail, hashedPassword, applyFirstBindDefaults)
}

// AuthUpdateOAuthRegistrationInvitation forwards a saved invitation snapshot
// for persistence recovery. The caller keeps that snapshot until recovery is
// decided, and receives the storage error so it can report incomplete
// restoration.
func (s *AuthService) AuthUpdateOAuthRegistrationInvitation(ctx context.Context, code *RedeemCode) error {
	return s.Storage.AuthUpdateOAuthRegistrationInvitation(ctx, code)
}

// AuthUpdateOAuthSignupSource requests the optional OAuth signup-source write
// through storage. The implementation owns its savepoint and diagnostic
// handling, while this forwarding method exposes no error result to the
// authentication caller.
func (s *AuthService) AuthUpdateOAuthSignupSource(ctx context.Context, userID int64, signupSource string) {
	s.Storage.AuthUpdateOAuthSignupSource(ctx, userID, signupSource)
}

// AuthUpdateUserSignupSource requests the optional user signup-source write
// through configured storage. Its implementation determines skip and diagnostic
// behavior; callers can continue their account flow after this method returns.
func (s *AuthService) AuthUpdateUserSignupSource(ctx context.Context, userID int64, signupSource string) {
	s.Storage.AuthUpdateUserSignupSource(ctx, userID, signupSource)
}

// AuthUseOAuthRegistrationInvitation delegates the invitation claim for the
// selected user. Callers keep transaction participation aligned with their
// registration work and handle this returned failure before treating the
// account completion as successful.
func (s *AuthService) AuthUseOAuthRegistrationInvitation(ctx context.Context, invitationID, userID int64) error {
	return s.Storage.AuthUseOAuthRegistrationInvitation(ctx, invitationID, userID)
}
