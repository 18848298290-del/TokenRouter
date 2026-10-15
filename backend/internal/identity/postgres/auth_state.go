package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// auth_state.go persists registration, email binding, invitation use, and
// first-bind grants for authentication flows.
// Registration and email binding keep account credentials, provider identity
// rows, invitation use, and first-bind grants in their selected persistence
// scope. Authentication supplies prepared account and provider inputs;
// AuthState invokes the retained core Rules for business checks before the
// database stages that depend on them.
//
// Transaction selection differs by operation. First-bind grants and
// bound-email updates join an injected Ent transaction or own one, while
// registered-user creation opens its configured transaction and database-free
// fixtures call their repositories directly. Optional steps use savepoints
// when a transaction is injected so their failures can be returned after
// recovery. Email backfill and timestamp/source observations deliberately
// diagnose or suppress selected failures, leaving callers to distinguish those
// results from required authentication writes.

// AuthState persists authentication workflow data with shared dependencies and
// core business checks.
type AuthState struct {
	// AuthDependencies is retained from assembly and supplies account, settings,
	// entitlement, and diagnostic participants. Configure the dependencies
	// required by each workflow before invoking storage operations.
	*identitycore.AuthDependencies
	entClient *dbent.Client // configured authentication storage client
	// Rules is assigned after AuthService construction using this state adapter.
	// Registration and normalized-email binding paths call it for business checks
	// before committing prepared account writes.
	Rules *identitycore.AuthService
}

// NewAuthState retains the storage client and shared authentication dependency
// pointer. Assembly sets Rules after constructing AuthService; callers
// configure required dependencies before invoking registration and
// email-binding workflows.
func NewAuthState(client *dbent.Client, deps *identitycore.AuthDependencies) *AuthState {
	return &AuthState{AuthDependencies: deps, entClient: client}
}

// ApplyProviderDefaultSettingsOnFirstBind applies enabled provider defaults
// under a first-bind claim. Missing state, client, settings, or positive
// account ID skips the operation.
//
// An injected Ent transaction is joined directly; otherwise the method owns a
// transaction and attempts rollback on exit. Grant insertion precedes balance,
// concurrency, and subscription writes so duplicate account/provider/reason
// claims skip repeat application. The owned transaction commits only after
// those configured grant participants succeed, and supplied participants must
// honor its context for combined completion.
func (s *AuthState) ApplyProviderDefaultSettingsOnFirstBind(
	ctx context.Context,
	userID int64,
	providerType string,
) error {
	if s == nil || s.entClient == nil || s.Settings == nil || userID <= 0 {
		return nil
	}

	if dbent.TxFromContext(ctx) != nil {
		return s.AuthApplyProviderDefaultSettingsOnFirstBind(ctx, userID, providerType)
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin first bind defaults transaction: %w", err)
	}
	// An owned first-bind transaction rolls back the marker when a later
	// entitlement fails. Cleanup errors are discarded while that grant failure
	// remains the caller result.
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := s.AuthApplyProviderDefaultSettingsOnFirstBind(txCtx, userID, providerType); err != nil {
		return err
	}
	// Commit the first-bind marker with all configured successful entitlement
	// writes, so a saved marker does not suppress a grant that this transaction
	// rolled back.
	return tx.Commit()
}

// AuthApplyProviderDefaultSettingsOnFirstBind records the enabled first-bind
// claim before applying provider entitlements. Supply the caller transaction
// covering the marker and later grant participants.
//
// The unique account/provider/first_bind insert uses do-nothing conflict
// handling; zero affected rows skip every grant write. A newly inserted claim
// permits configured balance, concurrency, and subscription defaults in
// sequence. This method selects the injected transaction client but creates no
// rollback scope itself, so callers keep claim and entitlement errors inside
// their shared transaction.
func (s *AuthState) AuthApplyProviderDefaultSettingsOnFirstBind(
	ctx context.Context,
	userID int64,
	providerType string,
) error {
	providerDefaults, enabled, err := s.Settings.ResolveAuthSourceGrantSettings(ctx, providerType, true)
	if err != nil {
		return fmt.Errorf("load auth source defaults: %w", err)
	}
	if !enabled {
		return nil
	}

	client := s.entClient
	if tx := dbent.TxFromContext(ctx); tx != nil {
		client = tx.Client()
	}

	// Insert the unique first-bind marker before entitlements and inspect its
	// affected count. A duplicate marker skips all grants, and a later grant
	// failure needs the caller transaction rollback.
	var result entsql.Result
	if err := client.Driver().Exec(
		ctx,
		`INSERT INTO user_provider_default_grants (user_id, provider_type, grant_reason)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, provider_type, grant_reason) DO NOTHING`,
		[]any{userID, strings.TrimSpace(providerType), "first_bind"},
		&result,
	); err != nil {
		return fmt.Errorf("record first bind provider grant: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read first bind provider grant result: %w", err)
	}
	if affected == 0 {
		return nil
	}

	if providerDefaults.Balance != 0 {
		if err := billingpostgres.NewBalanceStore(client).AddBalance(ctx, userID, providerDefaults.Balance); err != nil {
			return fmt.Errorf("apply first bind balance default: %w", err)
		}
	}
	if providerDefaults.Concurrency != 0 {
		if err := NewConcurrencyStore(client).UpdateConcurrency(ctx, userID, providerDefaults.Concurrency); err != nil {
			return fmt.Errorf("apply first bind concurrency default: %w", err)
		}
	}
	if s.DefaultSubscriptions != nil {
		for _, item := range providerDefaults.Subscriptions {
			if _, _, err := s.DefaultSubscriptions.AssignOrExtendSubscription(ctx, &identitycore.AssignSubscriptionInput{
				UserID: userID,
				PlanID: item.PlanID,
				Notes:  "auto assigned by first bind defaults",
			}); err != nil {
				return fmt.Errorf("apply first bind subscription default: %w", err)
			}
		}
	}

	return nil
}

// AuthCreateRegisteredUser captures the current key-count default and creates
// the supplied user with registration guards and an optional invitation. Nil
// user input is a successful no-op.
//
// The work rechecks enforced domain policy and normalized-email availability
// in its operation context before account creation and invitation use. A
// configured Ent client opens an owned transaction; database-free fixtures run
// the supplied repositories directly. Domain quota requires the atomic
// repository capability when a database is configured, while its absent
// fixture capability uses ordinary creation. Repository writes can mutate the
// supplied user object before a later invitation or commit failure, so callers
// treat error results as requiring recovery.
func (s *AuthState) AuthCreateRegisteredUser(ctx context.Context, user *identitycore.User, artifacts *identitycore.AuthRegistrationArtifacts) error {
	if user == nil {
		return nil
	}
	// Snapshot the current default at registration; later setting changes do not
	// update existing users.
	user.APIKeyLimit = identitycore.DefaultUserAPIKeyLimit
	if s.Settings != nil {
		user.APIKeyLimit = s.Settings.GetDefaultUserAPIKeyLimit(ctx)
	}
	if artifacts == nil {
		artifacts = &identitycore.AuthRegistrationArtifacts{}
	}
	normalizedEmail := ""
	if s.Settings != nil && s.Settings.IsRegistrationEmailNormalizationEnabled(ctx) {
		normalizedEmail = identitycore.NormalizeRegistrationEmailAddress(user.Email)
	}

	run := func(runCtx context.Context) error {
		domainLimit := ""
		if artifacts.EnforceEmailDomainQuota {
			var err error
			domainLimit, err = s.Rules.AuthRegistrationEmailDomainLimit(runCtx, user.Email)
			if err != nil {
				return err
			}
		}

		if normalizedEmail != "" {
			if dbent.TxFromContext(runCtx) != nil {
				if err := s.Users.LockRegistrationEmail(runCtx, normalizedEmail); err != nil {
					return err
				}
			}

			existsEmail, err := s.Rules.AuthRegistrationEmailExists(runCtx, user.Email)
			if err != nil {
				return err
			}
			if existsEmail {
				return identitycore.ErrEmailExists
			}
		}

		if domainLimit != "" {
			quotaRepo := s.DomainRegistration
			ok := quotaRepo != nil
			if !ok {
				// A configured Ent client requires the guarded repository to keep the
				// domain quota check with account creation. Client-free state uses the
				// ordinary user repository for this branch.
				if s.entClient != nil {
					return identitycore.ErrServiceUnavailable
				}
				if err := s.Users.Create(runCtx, user); err != nil {
					return err
				}
			} else {
				if err := quotaRepo.CreateWithRegistrationEmailGuards(runCtx, user, normalizedEmail, domainLimit); err != nil {
					return err
				}
			}
		} else if err := s.Users.Create(runCtx, user); err != nil {
			return err
		}

		if artifacts.InvitationRedeemCode != nil {
			if err := s.Redeem.Use(runCtx, artifacts.InvitationRedeemCode.ID, user.ID); err != nil {
				return identitycore.ErrInvitationCodeInvalid
			}
		}

		return nil
	}

	if s.entClient == nil {
		return run(ctx)
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	// Rollback covers the new account and supplied invitation use when
	// registration fails after creation. Cleanup errors are discarded, and
	// callers retain recovery of the mutated account object.
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := run(txCtx); err != nil {
		return err
	}
	// Commit after guarded account creation and any invitation consumption
	// succeed, keeping the stored account and invitation claim within the same
	// owned registration result.
	return tx.Commit()
}

// AuthEnsureEmailAuthIdentity returns a user-owned email identity and an
// initial-absence creation hint. Missing storage, unusable account input,
// blank or reserved email, and diagnosed persistence failures return nil and
// false.
//
// It checks existence, attempts conflict-tolerant creation when absent, then
// reloads the exact email tuple and checks its owner. The creation hint comes
// from the first existence read, so a concurrent same-owner insert can still
// yield true; it is not an inserted-row ownership count. SQL no-row conflict
// results are suppressed, and the caller treats skipped backfill separately
// from required account authentication.
func (s *AuthState) AuthEnsureEmailAuthIdentity(ctx context.Context, user *identitycore.User, source string) (*dbent.AuthIdentity, bool) {
	if s == nil || s.entClient == nil || user == nil || user.ID <= 0 {
		return nil, false
	}

	email := strings.ToLower(strings.TrimSpace(user.Email))
	if email == "" || identitycore.IsReservedEmail(email) {
		return nil, false
	}
	if strings.TrimSpace(source) == "" {
		source = "auth_service_dual_write"
	}

	client := s.entClient
	if tx := dbent.TxFromContext(ctx); tx != nil {
		client = tx.Client()
	}

	buildQuery := func() *dbent.AuthIdentityQuery {
		return client.AuthIdentity.Query().Where(
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ(email),
		)
	}

	// Optional email backfill starts with an existence observation. If that query
	// fails, report no usable identity or creation hint so account-based
	// authentication can handle the skipped write.
	existed, err := buildQuery().Exist(ctx)
	if err != nil {
		s.Observer.Printf("service.auth", "[Auth] Failed to inspect email auth identity: user_id=%d email=%s err=%v", user.ID, email, err)
		return nil, false
	}

	if !existed {
		if err = client.AuthIdentity.Create().
			SetUserID(user.ID).
			SetProviderType("email").
			SetProviderKey("email").
			SetProviderSubject(email).
			SetVerifiedAt(time.Now().UTC()).
			SetMetadata(map[string]any{
				"source": strings.TrimSpace(source),
			}).
			OnConflictColumns(
				authidentity.FieldProviderType,
				authidentity.FieldProviderKey,
				authidentity.FieldProviderSubject,
			).
			DoNothing().
			Exec(ctx); err != nil {
			// A conflict-tolerant insert can report no SQL row. Treat that outcome as
			// skipped backfill rather than returning a creation hint without a reloaded
			// owned identity.
			if isSQLNoRowsError(err) {
				return nil, false
			}
		}
		if err != nil {
			s.Observer.Printf("service.auth", "[Auth] Failed to ensure email auth identity: user_id=%d email=%s err=%v", user.ID, email, err)
			return nil, false
		}
	}

	// Reload the selected tuple before reporting ownership and the preflight
	// hint. A failed reload leaves backfill unavailable; initial absence alone
	// cannot establish the resulting identity owner.
	identity, err := buildQuery().Only(ctx)
	if err != nil {
		s.Observer.Printf("service.auth", "[Auth] Failed to reload email auth identity: user_id=%d email=%s err=%v", user.ID, email, err)
		return nil, false
	}
	if identity.UserID != user.ID {
		s.Observer.Printf("service.auth", "[Auth] Email auth identity ownership mismatch: user_id=%d email=%s owner_id=%d", user.ID, email, identity.UserID)
		return nil, false
	}

	return identity, !existed
}

// AuthEnsureEmailOAuthIdentity creates or refreshes one provider tuple for the
// supplied account through the configured Ent client. Callers provide a
// validated provider profile and a client participating in any required
// transaction; this method uses that client directly.
//
// The type is normalized while key and subject are trimmed before ownership
// lookup. A same-owner row receives replacement metadata, and another owner
// rejects the operation. Metadata starts with normalized email and
// verification fields, then upstream entries overwrite them before supplied
// name and avatar hints are applied. New rows receive those selected values
// and the requested account ID.
func (s *AuthState) AuthEnsureEmailOAuthIdentity(ctx context.Context, userID int64, input identitycore.EmailOAuthIdentityInput) error {
	metadata := map[string]any{
		"email":          strings.TrimSpace(strings.ToLower(input.Email)),
		"email_verified": input.EmailVerified,
	}
	maps.Copy(metadata, input.UpstreamMetadata)
	if strings.TrimSpace(input.Username) != "" {
		metadata["username"] = strings.TrimSpace(input.Username)
	}
	if strings.TrimSpace(input.DisplayName) != "" {
		metadata["display_name"] = strings.TrimSpace(input.DisplayName)
	}
	if strings.TrimSpace(input.AvatarURL) != "" {
		metadata["avatar_url"] = strings.TrimSpace(input.AvatarURL)
	}

	providerType := identitycore.AuthNormalizeOAuthSignupSource(input.ProviderType)
	providerKey := strings.TrimSpace(input.ProviderKey)
	providerSubject := strings.TrimSpace(input.ProviderSubject)
	identity, err := s.entClient.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ(providerType),
			authidentity.ProviderKeyEQ(providerKey),
			authidentity.ProviderSubjectEQ(providerSubject),
		).
		Only(ctx)
	if err != nil && !dbent.IsNotFound(err) {
		return infraerrors.InternalServer("AUTH_IDENTITY_LOOKUP_FAILED", "failed to inspect auth identity ownership").WithCause(err)
	}
	if identity != nil {
		if identity.UserID != userID {
			return infraerrors.Conflict("AUTH_IDENTITY_OWNERSHIP_CONFLICT", "auth identity already belongs to another user")
		}
		_, err = s.entClient.AuthIdentity.UpdateOneID(identity.ID).
			SetMetadata(metadata).
			Save(ctx)
		return err
	}
	_, err = s.entClient.AuthIdentity.Create().
		SetUserID(userID).
		SetProviderType(providerType).
		SetProviderKey(providerKey).
		SetProviderSubject(providerSubject).
		SetMetadata(metadata).
		Save(ctx)
	return err
}

// AuthFindEmailOAuthIdentityOwner resolves the exact supplied provider tuple
// through the configured client and user repository. Missing identity or
// account returns nil with no error; other account read failures become
// service unavailable.
func (s *AuthState) AuthFindEmailOAuthIdentityOwner(ctx context.Context, providerType, providerKey, providerSubject string) (*identitycore.User, error) {
	identity, err := s.entClient.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ(providerType),
			authidentity.ProviderKeyEQ(providerKey),
			authidentity.ProviderSubjectEQ(providerSubject),
		).
		Only(ctx)
	if err != nil {
		// Identity absence represents an unresolved provider account, so the email
		// OAuth flow can choose its registration requirements rather than receiving
		// an infrastructure lookup failure.
		if dbent.IsNotFound(err) {
			return nil, nil
		}
		return nil, infraerrors.InternalServer("AUTH_IDENTITY_LOOKUP_FAILED", "failed to inspect auth identity ownership").WithCause(err)
	}
	user, err := s.Users.GetByID(ctx, identity.UserID)
	if err != nil {
		// An identity whose account cannot be loaded yields no usable owner. Leave
		// that account-resolution result to the flow while unrelated account-read
		// failures remain service errors.
		if errors.Is(err, identitycore.ErrUserNotFound) {
			return nil, nil
		}
		return nil, identitycore.ErrServiceUnavailable
	}
	return user, nil
}

// AuthHasProviderGrantRecord reads one account/provider/reason marker through
// the configured client. Missing state or client and nonpositive IDs return
// false with no error; query or iteration failures remain errors.
func (s *AuthState) AuthHasProviderGrantRecord(
	ctx context.Context,
	userID int64,
	providerType string,
	grantReason string,
) (bool, error) {
	if s == nil || s.entClient == nil || userID <= 0 {
		return false, nil
	}

	rows, err := s.entClient.QueryContext(
		ctx,
		`SELECT 1 FROM user_provider_default_grants WHERE user_id = $1 AND provider_type = $2 AND grant_reason = $3 LIMIT 1`,
		userID,
		strings.TrimSpace(providerType),
		strings.TrimSpace(grantReason),
	)
	if err != nil {
		return false, err
	}
	// Close the marker query after the existence observation, including an
	// iteration error. Cleanup errors are discarded while the observed row and
	// iterator failure determine the result.
	defer func() { _ = rows.Close() }()
	return rows.Next(), rows.Err()
}

// AuthLoadOAuthRegistrationInvitation selects a transaction-aware database
// invitation reader when available. Otherwise the retained redeem repository
// performs the lookup; callers provide that fixture capability when the
// configured client is absent.
func (s *AuthState) AuthLoadOAuthRegistrationInvitation(ctx context.Context, invitationCode string) (*identitycore.RedeemCode, error) {
	if client := s.AuthOauthEmailFlowClient(ctx); client != nil {
		return registrationInvitationsForContext(ctx, client).Load(ctx, invitationCode)
	}
	return s.Redeem.GetByCode(ctx, invitationCode)
}

// AuthOauthEmailFlowClient returns the injected Ent transaction client or
// configured authentication client. Nil state or missing configured client
// returns nil even when context carries a transaction.
func (s *AuthState) AuthOauthEmailFlowClient(ctx context.Context) *dbent.Client {
	if s == nil || s.entClient == nil {
		return nil
	}
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return s.entClient
}

// AuthRestoreOAuthRegistrationInvitation restores a used invitation owned by
// the supplied account after failed registration. Current invitation policy,
// positive account ID, matching type, used status, and saved owner determine
// whether recovery runs; missing records or mismatched state leave it
// untouched.
//
// Recovery clears status, usage count, user, and time on the loaded snapshot
// before its repository update. A failed write returns recovery context but
// does not restore that snapshot's fields. The selected participant follows
// the available transaction or fixture repository, and its owner controls any
// encompassing rollback.
func (s *AuthState) AuthRestoreOAuthRegistrationInvitation(ctx context.Context, invitationCode string, userID int64) error {
	if s == nil || s.Settings == nil || !s.Settings.IsInvitationCodeEnabled(ctx) {
		return nil
	}
	if s.Redeem == nil && s.AuthOauthEmailFlowClient(ctx) == nil {
		return identitycore.ErrServiceUnavailable
	}

	invitationCode = strings.TrimSpace(invitationCode)
	if invitationCode == "" || userID <= 0 {
		return nil
	}

	redeemCode, err := s.AuthLoadOAuthRegistrationInvitation(ctx, invitationCode)
	if err != nil {
		if errors.Is(err, identitycore.ErrRedeemCodeNotFound) {
			return nil
		}
		return fmt.Errorf("load invitation code: %w", err)
	}
	if redeemCode.Type != identitycore.RedeemTypeInvitation || redeemCode.Status != identitycore.StatusUsed || redeemCode.UsedBy == nil || *redeemCode.UsedBy != userID {
		return nil
	}

	// Restore invitation status with its used count, user, and time fields
	// together. Leaving those ownership fields populated would attach reusable
	// invitation state to the preceding failed registration account.
	redeemCode.Status = identitycore.StatusUnused
	redeemCode.UsedCount = 0
	redeemCode.UsedBy = nil
	redeemCode.UsedAt = nil
	if err := s.AuthUpdateOAuthRegistrationInvitation(ctx, redeemCode); err != nil {
		return fmt.Errorf("restore invitation code: %w", err)
	}
	return nil
}

// AuthRunFailOpenDBStep runs optional work with a trusted SQL savepoint
// identifier when context holds a transaction. Nil callbacks succeed;
// otherwise a missing transaction invokes the callback directly.
//
// Within a transaction it creates a savepoint before the callback, rolls back
// a failed callback, and releases that savepoint before returning its failure.
// Rollback or release failures supersede the callback error with recovery
// context. Successful callbacks also release the savepoint. The caller decides
// whether the returned failure stops authentication and makes callback writes
// honor the supplied transaction context.
func (s *AuthState) AuthRunFailOpenDBStep(ctx context.Context, savepointName string, fn func(context.Context) error) error {
	if fn == nil {
		return nil
	}
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		return fn(ctx)
	}

	client := tx.Client()
	// Isolate optional registration writes with a savepoint inside the parent transaction.
	// A failed PostgreSQL statement otherwise leaves the transaction aborted.
	if _, err := client.ExecContext(ctx, "SAVEPOINT "+savepointName); err != nil {
		return fmt.Errorf("create savepoint %s: %w", savepointName, err)
	}
	// A failed optional statement can abort the parent PostgreSQL transaction.
	// Roll back to this savepoint and release it before returning the callback
	// failure to the authentication caller.
	if err := fn(ctx); err != nil {
		if _, rollbackErr := client.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepointName); rollbackErr != nil {
			return fmt.Errorf("rollback savepoint %s after %v: %w", savepointName, err, rollbackErr)
		}
		if _, releaseErr := client.ExecContext(ctx, "RELEASE SAVEPOINT "+savepointName); releaseErr != nil {
			return fmt.Errorf("release savepoint %s after %v: %w", savepointName, err, releaseErr)
		}
		return err
	}
	if _, err := client.ExecContext(ctx, "RELEASE SAVEPOINT "+savepointName); err != nil {
		return fmt.Errorf("release savepoint %s: %w", savepointName, err)
	}
	return nil
}

// AuthShouldApplyEmailFirstBindDefaults selects first-bind eligibility from a
// nonnil email identity, creation hint, and prior grant state. Callers keep
// the identity associated with the supplied account before invoking it.
//
// Login-backfill metadata suppresses eligibility before the creation hint is
// considered. Other created hints allow grants immediately, while existing
// identities require a usable state/client, matching owner, dual-write source,
// and no recorded first-bind claim. Grant lookup failures are diagnosed and
// return false. Metadata is read before the later nil-identity guard, so nil
// input is not a supported calling case.
func (s *AuthState) AuthShouldApplyEmailFirstBindDefaults(
	ctx context.Context,
	userID int64,
	identity *dbent.AuthIdentity,
	created bool,
) bool {
	source := identitycore.AuthEmailAuthIdentitySource(identity.Metadata)
	if source == "auth_service_login_backfill" {
		return false
	}
	if created {
		return true
	}
	if s == nil || s.entClient == nil || userID <= 0 || identity == nil || identity.UserID != userID {
		return false
	}
	if source != "auth_service_dual_write" {
		return false
	}

	hasGrant, err := s.AuthHasProviderGrantRecord(ctx, userID, "email", "first_bind")
	// An unavailable grant observation cannot establish that a first-bind claim
	// is absent. Diagnose it and decline this optional grant selection until its
	// stored state can be read.
	if err != nil {
		s.Observer.Printf("service.auth", "[Auth] Failed to inspect email first bind grant state: user_id=%d err=%v", userID, err)
		return false
	}
	return !hasGrant
}

// AuthTouchUserLogin records login and activity with one current UTC
// observation for a positive account. Missing state/client or invalid ID skips
// work; failures are diagnosed while the authentication caller receives no
// error result.
func (s *AuthState) AuthTouchUserLogin(ctx context.Context, userID int64) {
	if s == nil || s.entClient == nil || userID <= 0 {
		return
	}
	now := time.Now().UTC()
	// Login timestamp persistence is an observation after account authentication.
	// Diagnose failure here instead of returning it as a login rejection from
	// this no-error-result operation.
	if err := s.entClient.User.UpdateOneID(userID).
		SetLastLoginAt(now).
		SetLastActiveAt(now).
		Exec(ctx); err != nil {
		s.Observer.Printf("service.auth", "[Auth] Failed to touch login timestamps: user_id=%d err=%v", userID, err)
	}
}

// AuthUpdateBoundEmailIdentityTx replaces account credentials and their email
// identity in the injected transaction or an owned one. Transaction startup or
// commit failure becomes service unavailable.
//
// The delegated write rechecks alias or configured normalized-email ownership,
// persists credentials, replaces identity, and optionally applies first-bind
// grants. It refreshes the supplied currentUser before an owned commit, so
// commit failure can leave that object changed despite storage rollback.
// Callers reload the account after error before reusing it; a joined
// transaction retains caller completion ownership.
func (s *AuthState) AuthUpdateBoundEmailIdentityTx(
	ctx context.Context,
	currentUser *identitycore.User,
	email string,
	registrationNormalizedEmail string,
	hashedPassword string,
	applyFirstBindDefaults bool,
) error {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return s.AuthUpdateBoundEmailIdentityWithClient(ctx, tx.Client(), currentUser, email, registrationNormalizedEmail, hashedPassword, applyFirstBindDefaults)
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return identitycore.ErrServiceUnavailable
	}
	// Rollback covers credential, identity, and grant writes on a failed owned
	// email update. The currentUser snapshot may already have refreshed fields
	// and needs caller reload after failure.
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := s.AuthUpdateBoundEmailIdentityWithClient(txCtx, tx.Client(), currentUser, email, registrationNormalizedEmail, hashedPassword, applyFirstBindDefaults); err != nil {
		return err
	}
	// Commit bound credentials, replacement email identity, and selected grants
	// before returning success. Commit failure leaves the already refreshed
	// in-memory account snapshot for caller recovery.
	if err := tx.Commit(); err != nil {
		return identitycore.ErrServiceUnavailable
	}
	return nil
}

// AuthUpdateBoundEmailIdentityWithClient writes bound email and password
// through the supplied valid client and current account. The caller provides
// the transaction scope covering its credential, identity, and optional
// entitlement stages.
//
// An alias guard performs its own locked ownership recheck; fixtures lacking
// it use configured normalized-email locking before direct credential
// persistence. The new email identity is ensured before the preceding subject
// is removed. Optional first-bind grants follow replacement, then the stored
// account fields refresh the supplied snapshot before return. A later failure
// relies on caller rollback and does not restore already modified in-memory
// account fields.
func (s *AuthState) AuthUpdateBoundEmailIdentityWithClient(
	ctx context.Context,
	client *dbent.Client,
	currentUser *identitycore.User,
	email string,
	registrationNormalizedEmail string,
	hashedPassword string,
	applyFirstBindDefaults bool,
) error {
	if client == nil || currentUser == nil || currentUser.ID <= 0 {
		return identitycore.ErrServiceUnavailable
	}

	oldEmail := currentUser.Email
	if guard := s.EmailAliasGuard; guard != nil {
		// Lock and recheck alias ownership in this transaction to close the gap
		// after the service precheck.
		if err := guard.UpdateEmailWithAliasGuard(ctx, currentUser.ID, email, hashedPassword); err != nil {
			return err
		}
	} else {
		// Without the alias capability, this path uses the supplied normalized-email
		// guard before direct credential storage. The caller transaction still
		// covers later identity replacement and grant writes.
		updatedUser := *currentUser
		updatedUser.Email = email
		updatedUser.PasswordHash = hashedPassword
		if registrationNormalizedEmail != "" {
			if err := s.Users.LockRegistrationEmail(ctx, registrationNormalizedEmail); err != nil {
				return identitycore.ErrServiceUnavailable
			}
			exists, err := s.Rules.AuthHasNormalizedEmailBindingConflict(ctx, currentUser, registrationNormalizedEmail)
			if err != nil {
				return identitycore.ErrServiceUnavailable
			}
			if exists {
				return identitycore.ErrEmailExists
			}
		}
		if _, err := client.User.UpdateOneID(currentUser.ID).
			SetEmail(updatedUser.Email).
			SetPasswordHash(updatedUser.PasswordHash).
			Save(ctx); err != nil {
			if dbent.IsConstraintError(err) || errors.Is(err, identitycore.ErrEmailExists) {
				return identitycore.ErrEmailExists
			}
			return identitycore.ErrServiceUnavailable
		}
	}

	if err := AuthReplaceBoundEmailAuthIdentityWithClient(ctx, client, currentUser.ID, oldEmail, email, "auth_service_email_bind"); err != nil {
		if errors.Is(err, identitycore.ErrEmailExists) {
			return identitycore.ErrEmailExists
		}
		return identitycore.ErrServiceUnavailable
	}

	if applyFirstBindDefaults {
		if err := s.ApplyProviderDefaultSettingsOnFirstBind(ctx, currentUser.ID, "email"); err != nil {
			return fmt.Errorf("apply email first bind defaults: %w", err)
		}
	}

	refreshedUser, err := client.User.Get(ctx, currentUser.ID)
	if err != nil {
		return identitycore.ErrServiceUnavailable
	}
	currentUser.Email = refreshedUser.Email
	currentUser.PasswordHash = refreshedUser.PasswordHash
	currentUser.Balance = refreshedUser.Balance
	currentUser.Concurrency = refreshedUser.Concurrency
	currentUser.UpdatedAt = refreshedUser.UpdatedAt
	return nil
}

// AuthUpdateOAuthRegistrationInvitation restores a supplied invitation
// snapshot through the selected database participant or fixture repository.
// Nil snapshots succeed; callers retain transaction completion and recovery
// handling for persistence errors.
func (s *AuthState) AuthUpdateOAuthRegistrationInvitation(ctx context.Context, code *identitycore.RedeemCode) error {
	if code == nil {
		return nil
	}
	if client := s.AuthOauthEmailFlowClient(ctx); client != nil {
		return registrationInvitationsForContext(ctx, client).RestoreSnapshot(ctx, code)
	}
	return s.Redeem.Update(ctx, code)
}

// AuthUpdateOAuthSignupSource attempts the supplied account source write
// through optional savepoint handling. Missing client, invalid ID, or blank
// source skips work; failed writes are logged so authentication may continue.
func (s *AuthState) AuthUpdateOAuthSignupSource(ctx context.Context, userID int64, signupSource string) {
	client := s.AuthOauthEmailFlowClient(ctx)
	if client == nil || userID <= 0 || strings.TrimSpace(signupSource) == "" {
		return
	}
	// Signup source is optional attribution after account work. Isolate its
	// transactional failure and log the returned error so the authentication
	// caller can complete its required stages.
	if err := s.AuthRunFailOpenDBStep(ctx, "auth_oauth_signup_source", func(stepCtx context.Context) error {
		stepClient := s.AuthOauthEmailFlowClient(stepCtx)
		if stepClient == nil {
			return identitycore.ErrServiceUnavailable
		}
		return stepClient.User.UpdateOneID(userID).SetSignupSource(signupSource).Exec(stepCtx)
	}); err != nil {
		slog.Warn("[Auth] update oauth signup source failed (fail-open)", "user_id", userID, "source", signupSource, "error", err)
	}
}

// AuthUpdateUserSignupSource attempts source persistence through the
// configured client for a positive account. Missing state/client or blank
// source skips work; diagnosed failures have no returned error for
// authentication.
func (s *AuthState) AuthUpdateUserSignupSource(ctx context.Context, userID int64, signupSource string) {
	if s == nil || s.entClient == nil || userID <= 0 {
		return
	}
	if strings.TrimSpace(signupSource) == "" {
		return
	}
	// This source observation exposes no error result to authentication. Diagnose
	// its failed update while keeping the required account workflow decision with
	// the surrounding caller.
	if err := s.entClient.User.UpdateOneID(userID).
		SetSignupSource(signupSource).
		Exec(ctx); err != nil {
		s.Observer.Printf("service.auth", "[Auth] Failed to update signup source: user_id=%d source=%s err=%v", userID, signupSource, err)
	}
}

// AuthUseOAuthRegistrationInvitation consumes the selected invitation through
// the transaction-aware database participant when available. Database-free
// fixtures use Redeem.Use, and claim failure remains an error for signup
// completion.
func (s *AuthState) AuthUseOAuthRegistrationInvitation(ctx context.Context, invitationID, userID int64) error {
	if client := s.AuthOauthEmailFlowClient(ctx); client != nil {
		return registrationInvitationsForContext(ctx, client).Consume(ctx, invitationID, userID)
	}
	return s.Redeem.Use(ctx, invitationID, userID)
}

// HasDatabase reports whether authentication state has a configured Ent
// database client.
func (s *AuthState) HasDatabase() bool { return s != nil && s.entClient != nil }

// AuthReplaceBoundEmailAuthIdentityWithClient attempts the new normalized
// email subject before deleting the preceding owned subject. Callers supply a
// valid client and the transaction spanning account credentials and both
// identity operations.
//
// An empty new subject can make the ensure step a no-op, and an absent
// readback is also accepted by that helper. The old subject is still removed
// when nonempty and different. A later delete failure requires caller rollback
// of earlier work; equal or empty old subjects finish after the attempted new-
// subject check.
func AuthReplaceBoundEmailAuthIdentityWithClient(
	ctx context.Context,
	client *dbent.Client,
	userID int64,
	oldEmail string,
	newEmail string,
	source string,
) error {
	newSubject := identitycore.AuthNormalizeBoundEmailAuthIdentitySubject(newEmail)
	if err := AuthEnsureBoundEmailAuthIdentityWithClient(ctx, client, userID, newSubject, source); err != nil {
		return err
	}

	oldSubject := identitycore.AuthNormalizeBoundEmailAuthIdentitySubject(oldEmail)
	if oldSubject == "" || oldSubject == newSubject {
		return nil
	}

	_, err := client.AuthIdentity.Delete().
		Where(
			authidentity.UserIDEQ(userID),
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ(oldSubject),
		).
		Exec(ctx)
	return err
}

// AuthEnsureBoundEmailAuthIdentityWithClient attempts a normalized email
// identity insert and checks its owner when readback finds a row. Missing
// client, nonpositive account ID, or empty subject skips work; callers supply
// the normalized subject and transaction-backed client when these stages need
// shared completion.
//
// The insert uses do-nothing conflict handling and records the current UTC
// verification timestamp with source metadata. Recognized SQL no-row results
// continue to readback, where another owner returns ErrEmailExists. An absent
// readback also succeeds, so nil error can represent either the requested
// owned row or no row.
func AuthEnsureBoundEmailAuthIdentityWithClient(
	ctx context.Context,
	client *dbent.Client,
	userID int64,
	subject string,
	source string,
) error {
	if client == nil || userID <= 0 || subject == "" {
		return nil
	}

	if strings.TrimSpace(source) == "" {
		source = "auth_service_email_bind"
	}

	if err := client.AuthIdentity.Create().
		SetUserID(userID).
		SetProviderType("email").
		SetProviderKey("email").
		SetProviderSubject(subject).
		SetVerifiedAt(time.Now().UTC()).
		SetMetadata(map[string]any{"source": strings.TrimSpace(source)}).
		OnConflictColumns(
			authidentity.FieldProviderType,
			authidentity.FieldProviderKey,
			authidentity.FieldProviderSubject,
		).
		DoNothing().
		Exec(ctx); err != nil {
		// Do-nothing insertion can report no returned SQL row. Continue to ownership
		// readback for that result, allowing a preexisting row to determine whether
		// this email binding conflicts.
		if !isSQLNoRowsError(err) {
			return err
		}
	}

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ(subject),
		).
		Only(ctx)
	if err != nil {
		// Readback absence is accepted after this conflict-tolerant insert. Callers
		// therefore cannot infer that an email identity row exists solely from this
		// helper returning no error.
		if dbent.IsNotFound(err) {
			return nil
		}
		return err
	}
	// Conflict-tolerant insertion can select a preexisting subject row. Reload
	// its owner before accepting the binding so another account email identity is
	// rejected.
	if identity.UserID != userID {
		return identitycore.ErrEmailExists
	}
	return nil
}

// registrationInvitationsForContext selects the injected transaction
// invitation participant when present. Otherwise it retains the supplied Ent
// client for ordinary invitation persistence, with transaction completion left
// to the selected caller scope.
func registrationInvitationsForContext(ctx context.Context, client *dbent.Client) *billingpostgres.RegistrationInvitations {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return billingpostgres.RegistrationInvitationsInTx(tx)
	}
	return billingpostgres.NewRegistrationInvitations(client)
}
