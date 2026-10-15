package postgres

import (
	"context"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
)

// auth_projection.go adapts AuthState operations and stored identity entities
// to the authentication service interfaces.
// Authentication storage receives identity domain values while AuthState
// persists Ent entities and account workflow writes. This facade delegates
// those operations to one retained state instance, converting email identities
// at the service interface while retaining optional pointers and metadata
// ownership.

// AuthRepository exposes authentication persistence backed by the retained
// AuthState instance.
type AuthRepository struct {
	// State retains shared authentication dependencies. Assembly assigns its
	// Rules service before workflows invoke state methods that perform
	// registration or binding validation.
	State *AuthState
}

// HasDatabase reports whether the retained state has an Ent client configured.
// Nil repository or state returns false; this presence check does not
// establish live connection availability for later writes.
func (s *AuthRepository) HasDatabase() bool { return s != nil && s.State.HasDatabase() }

// ApplyProviderDefaultSettingsOnFirstBind delegates enabled provider grants
// after first binding. State joins a context transaction or owns one, with
// duplicate claims skipping grant writes and errors retained for the caller.
func (s *AuthRepository) ApplyProviderDefaultSettingsOnFirstBind(
	ctx context.Context,
	userID int64,
	providerType string,
) error {
	return s.State.ApplyProviderDefaultSettingsOnFirstBind(ctx, userID, providerType)
}

// AuthApplyProviderDefaultSettingsOnFirstBind delegates first-bind grants
// within the caller persistence scope. The caller supplies the transaction
// covering claim and entitlement writes; the state returns failures instead of
// committing partial grants itself.
func (s *AuthRepository) AuthApplyProviderDefaultSettingsOnFirstBind(
	ctx context.Context,
	userID int64,
	providerType string,
) error {
	return s.State.AuthApplyProviderDefaultSettingsOnFirstBind(ctx, userID, providerType)
}

// AuthCreateRegisteredUser delegates current account defaults, registration
// guards, and invitation consumption. State owns its configured transaction,
// while database-free fixture dependencies retain their ordinary write
// behavior and errors.
func (s *AuthRepository) AuthCreateRegisteredUser(ctx context.Context, user *identitycore.User, artifacts *identitycore.AuthRegistrationArtifacts) error {
	return s.State.AuthCreateRegisteredUser(ctx, user, artifacts)
}

// AuthEnsureEmailAuthIdentity maps the user-owned email identity and its
// creation hint from state. Nil results and false cover skipped or failed
// backfill; the hint reflects initial absence, not insert ownership.
func (s *AuthRepository) AuthEnsureEmailAuthIdentity(ctx context.Context, user *identitycore.User, source string) (*identitycore.AuthIdentity, bool) {
	v, created := s.State.AuthEnsureEmailAuthIdentity(ctx, user, source)
	return AuthIdentityFromEntity(v), created
}

// AuthEnsureEmailOAuthIdentity delegates prepared provider-tuple creation or
// metadata refresh for the supplied account. The state checks ownership and
// retains storage errors; the caller validates the provider profile first.
func (s *AuthRepository) AuthEnsureEmailOAuthIdentity(ctx context.Context, userID int64, input identitycore.EmailOAuthIdentityInput) error {
	return s.State.AuthEnsureEmailOAuthIdentity(ctx, userID, input)
}

// AuthFindEmailOAuthIdentityOwner delegates exact provider tuple lookup for a
// current account. Missing identities or accounts return nil with no error,
// and other lookup failures remain errors for authentication.
func (s *AuthRepository) AuthFindEmailOAuthIdentityOwner(ctx context.Context, providerType, providerKey, providerSubject string) (*identitycore.User, error) {
	return s.State.AuthFindEmailOAuthIdentityOwner(ctx, providerType, providerKey, providerSubject)
}

// AuthHasProviderGrantRecord delegates lookup of one account/provider/reason
// grant marker. Missing storage and nonpositive account IDs report false with
// no error; database failures remain visible to grant selection callers.
func (s *AuthRepository) AuthHasProviderGrantRecord(
	ctx context.Context,
	userID int64,
	providerType string,
	grantReason string,
) (bool, error) {
	return s.State.AuthHasProviderGrantRecord(ctx, userID, providerType, grantReason)
}

// AuthLoadOAuthRegistrationInvitation delegates the submitted invitation
// lookup in the available persistence scope. Context transactions choose their
// invitation participant; database-free fixtures use the retained redeem
// repository and its result.
func (s *AuthRepository) AuthLoadOAuthRegistrationInvitation(ctx context.Context, invitationCode string) (*identitycore.RedeemCode, error) {
	return s.State.AuthLoadOAuthRegistrationInvitation(ctx, invitationCode)
}

// AuthRestoreOAuthRegistrationInvitation delegates recovery of a used
// invitation owned by the supplied account. Disabled or mismatched recovery
// inputs skip work; the state returns storage failures for registration
// compensation.
func (s *AuthRepository) AuthRestoreOAuthRegistrationInvitation(ctx context.Context, invitationCode string, userID int64) error {
	return s.State.AuthRestoreOAuthRegistrationInvitation(ctx, invitationCode, userID)
}

// AuthRunFailOpenDBStep delegates optional writes with a trusted SQL savepoint
// identifier. State rolls back and releases failed transactional steps before
// returning the error; the caller decides whether authentication can continue.
func (s *AuthRepository) AuthRunFailOpenDBStep(ctx context.Context, savepointName string, fn func(context.Context) error) error {
	return s.State.AuthRunFailOpenDBStep(ctx, savepointName, fn)
}

// AuthShouldApplyEmailFirstBindDefaults maps a nonnil identity for state grant
// selection. The creation hint, metadata source, owner, and prior grant
// observation determine eligibility; callers retain identity ownership
// consistency.
func (s *AuthRepository) AuthShouldApplyEmailFirstBindDefaults(
	ctx context.Context,
	userID int64,
	identity *identitycore.AuthIdentity,
	created bool,
) bool {
	return s.State.AuthShouldApplyEmailFirstBindDefaults(ctx, userID, AuthIdentityToEntity(identity), created)
}

// AuthTouchUserLogin delegates recording of login and account activity
// observations. The state skips missing storage or invalid IDs and diagnoses
// update failure because this method returns no error.
func (s *AuthRepository) AuthTouchUserLogin(ctx context.Context, userID int64) {
	s.State.AuthTouchUserLogin(ctx, userID)
}

// AuthUpdateBoundEmailIdentityTx delegates account credential and
// email-identity replacement in the available transaction. The working account
// is refreshed before commit; callers reload it after a failure before reusing
// that snapshot.
func (s *AuthRepository) AuthUpdateBoundEmailIdentityTx(
	ctx context.Context,
	currentUser *identitycore.User,
	email string,
	registrationNormalizedEmail string,
	hashedPassword string,
	applyFirstBindDefaults bool,
) error {
	return s.State.AuthUpdateBoundEmailIdentityTx(ctx, currentUser, email, registrationNormalizedEmail, hashedPassword, applyFirstBindDefaults)
}

// AuthUpdateOAuthRegistrationInvitation delegates restoration of the supplied
// invitation snapshot. Nil snapshots succeed, while context transactions
// select their participant and database-free fixtures use the ordinary
// repository update.
func (s *AuthRepository) AuthUpdateOAuthRegistrationInvitation(ctx context.Context, code *identitycore.RedeemCode) error {
	return s.State.AuthUpdateOAuthRegistrationInvitation(ctx, code)
}

// AuthUpdateOAuthSignupSource delegates an optional signup-source write
// through state savepoint handling. Missing storage or unusable input skips
// work; write failures are diagnosed while the authentication caller can
// continue.
func (s *AuthRepository) AuthUpdateOAuthSignupSource(ctx context.Context, userID int64, signupSource string) {
	s.State.AuthUpdateOAuthSignupSource(ctx, userID, signupSource)
}

// AuthUpdateUserSignupSource delegates a signup-source observation for a
// positive account. Missing storage or blank source skips the update, and
// state diagnoses failure because the facade exposes no error result.
func (s *AuthRepository) AuthUpdateUserSignupSource(ctx context.Context, userID int64, signupSource string) {
	s.State.AuthUpdateUserSignupSource(ctx, userID, signupSource)
}

// AuthUseOAuthRegistrationInvitation delegates invitation consumption for the
// selected account during signup. Context transactions select their
// participant, database-free fixtures use their redeem repository, and
// unsuccessful claims remain errors.
func (s *AuthRepository) AuthUseOAuthRegistrationInvitation(ctx context.Context, invitationID, userID int64) error {
	return s.State.AuthUseOAuthRegistrationInvitation(ctx, invitationID, userID)
}

// AuthIdentityFromEntity maps an Ent identity into the domain value, returning
// nil for nil input. Metadata and optional verification and issuer pointers
// remain shared with the supplied entity.
func AuthIdentityFromEntity(v *dbent.AuthIdentity) *identitycore.AuthIdentity {
	if v == nil {
		return nil
	}
	return &identitycore.AuthIdentity{ID: v.ID, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, UserID: v.UserID, ProviderType: v.ProviderType, ProviderKey: v.ProviderKey, ProviderSubject: v.ProviderSubject, VerifiedAt: v.VerifiedAt, Issuer: v.Issuer, Metadata: v.Metadata}
}

// AuthIdentityToEntity maps a domain identity into an Ent value, returning nil
// for nil input. Metadata and optional verification and issuer pointers remain
// shared with the supplied identity.
func AuthIdentityToEntity(v *identitycore.AuthIdentity) *dbent.AuthIdentity {
	if v == nil {
		return nil
	}
	return &dbent.AuthIdentity{ID: v.ID, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, UserID: v.UserID, ProviderType: v.ProviderType, ProviderKey: v.ProviderKey, ProviderSubject: v.ProviderSubject, VerifiedAt: v.VerifiedAt, Issuer: v.Issuer, Metadata: v.Metadata}
}
