package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Pending provider signup can verify a local mailbox code or use a mailbox
// proof already established upstream. Both paths create an account before its
// provider identity and browser session finish, and preserve the account ID
// for later finalization or compensation.
//
// Invitation admission initially reads eligibility without consuming it.
// FinalizeOAuthEmailAccount rechecks and claims the invitation, then applies
// current plan/affiliate effects; balance and concurrency came from the
// earlier creation snapshot. Token generation can fail after creation and
// attempts best-effort account cleanup. General rollback restores an
// invitation before deleting the account, so those separately completed
// effects and their returned failures matter to the surrounding pending flow.

// AuthNormalizeOAuthSignupSource returns a recognized lowercased/trimmed
// signup provider label. Blank, email and unknown text become email; callers
// use the result for stored source and grant selection rather than
// provider-token verification.
func AuthNormalizeOAuthSignupSource(signupSource string) string {
	signupSource = strings.TrimSpace(strings.ToLower(signupSource))
	switch signupSource {
	case "", "email":
		return "email"
	case "linuxdo", "wechat", "oidc", "github", "google", "dingtalk":
		return signupSource
	default:
		return "email"
	}
}

// SendPendingOAuthVerifyCode sends a local challenge after mailbox syntax,
// reserved-address and registration-domain quota checks. It requires the
// email service but does not require the public registration switch; the
// returned cooldown describes challenge request timing rather than
// account-creation admission.
func (s *AuthService) SendPendingOAuthVerifyCode(ctx context.Context, email string, locale ...string) (*SendVerifyCodeResult, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return nil, ErrEmailVerifyRequired
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, ErrEmailVerifyRequired
	}
	if IsReservedEmail(email) {
		return nil, ErrEmailReserved
	}
	if s == nil || s.Email == nil {
		return nil, ErrServiceUnavailable
	}
	if err := s.AuthValidateRegistrationEmailQuota(ctx, email); err != nil {
		return nil, err
	}

	siteName := "TokenRouter"
	if s.Settings != nil {
		siteName = s.Settings.GetSiteName(ctx)
	}
	if err := s.Email.SendVerifyCode(ctx, email, siteName, FirstEmailLocale(locale)); err != nil {
		return nil, err
	}
	return &SendVerifyCodeResult{
		// Expose the challenge request cooldown in whole seconds for frontend timing.
		Countdown: int(VerifyCodeCooldown / time.Second),
	}, nil
}

// AuthValidateOAuthRegistrationInvitation returns a usable invitation
// snapshot when current policy requires it. Missing service/settings or
// disabled policy returns nil; active policy trims the code and maps lookup
// or unusable-state failure to invitation rejection without claiming the code
// yet.
func (s *AuthService) AuthValidateOAuthRegistrationInvitation(ctx context.Context, invitationCode string) (*RedeemCode, error) {
	if s == nil || s.Settings == nil || !s.Settings.IsInvitationCodeEnabled(ctx) {
		return nil, nil
	}
	if s.Redeem == nil && !s.HasDatabase() {
		return nil, ErrServiceUnavailable
	}

	invitationCode = strings.TrimSpace(invitationCode)
	if invitationCode == "" {
		return nil, ErrInvitationCodeRequired
	}

	// Read invitation eligibility for this pending creation without claiming it
	// yet; finalization repeats the check and performs conditional redemption
	// for the created account.
	redeemCode, err := s.AuthLoadOAuthRegistrationInvitation(ctx, invitationCode)
	if err != nil {
		return nil, ErrInvitationCodeInvalid
	}
	if redeemCode.Type != RedeemTypeInvitation || !redeemCode.CanUse() {
		return nil, ErrInvitationCodeInvalid
	}
	return redeemCode, nil
}

// VerifyOAuthEmailCode verifies trimmed mailbox/code text through the local
// challenge service. Empty input is required-proof failure and missing
// service is unavailable; this proof is requested for pending signup/binding
// independently of the global ordinary-registration verification switch.
func (s *AuthService) VerifyOAuthEmailCode(ctx context.Context, email, verifyCode string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	verifyCode = strings.TrimSpace(verifyCode)

	if email == "" {
		return ErrEmailVerifyRequired
	}
	if verifyCode == "" {
		return ErrEmailVerifyRequired
	}
	if s == nil || s.Email == nil {
		return ErrServiceUnavailable
	}
	return s.Email.VerifyCode(ctx, email, verifyCode)
}

// RegisterOAuthEmailAccount creates a local account after a submitted mailbox
// challenge and current registration/invitation/domain policy checks. The
// DingTalk bypass predicate uses the supplied source before later
// normalization; account grants snapshot balance/concurrency while this
// code-verification path leaves RPM at its zero value.
//
// Creation uses domain and optional normalized-address guards but defers
// invitation consumption and later plan/affiliate effects to
// FinalizeOAuthEmailAccount. Token-pair generation follows account creation;
// failure attempts account deletion with no invitation code and suppresses
// cleanup failure. Callers continue pending identity finalization after a
// usable account result and compensate later failures using the matching
// account/invitation information.
func (s *AuthService) RegisterOAuthEmailAccount(
	ctx context.Context,
	email string,
	password string,
	verifyCode string,
	invitationCode string,
	signupSource string,
) (*TokenPair, *User, error) {
	if s == nil {
		return nil, nil, ErrServiceUnavailable
	}
	if s.Settings == nil || (!s.Settings.IsRegistrationEnabled(ctx) && !s.AuthCanBypassRegistrationDisabledForOAuth(ctx, signupSource)) {
		return nil, nil, ErrRegDisabled
	}

	email = strings.TrimSpace(strings.ToLower(email))
	if IsReservedEmail(email) {
		return nil, nil, ErrEmailReserved
	}
	if err := s.VerifyOAuthEmailCode(ctx, email, verifyCode); err != nil {
		slog.Error("oauth email register: verify code failed", "email", email, "error", err.Error())
		return nil, nil, err
	}

	if _, err := s.AuthValidateOAuthRegistrationInvitation(ctx, invitationCode); err != nil {
		slog.Error("oauth email register: invitation failed", "email", email, "error", err.Error())
		return nil, nil, err
	}

	existsEmail, err := s.Users.ExistsByEmail(ctx, email)
	if err != nil {
		slog.Error("oauth email register: ExistsByEmail failed", "email", email, "error", err.Error())
		return nil, nil, ErrServiceUnavailable
	}
	if existsEmail {
		return nil, nil, ErrEmailExists
	}
	if err := s.AuthValidateRegistrationEmailQuota(ctx, email); err != nil {
		slog.Error("oauth email register: policy rejected", "email", email, "error", err.Error())
		return nil, nil, err
	}

	hashedPassword, err := s.HashPassword(password)
	if err != nil {
		return nil, nil, fmt.Errorf("hash password: %w", err)
	}

	signupSource = AuthNormalizeOAuthSignupSource(signupSource)
	grantPlan := s.AuthResolveSignupGrantPlan(ctx, signupSource)

	user := &User{
		Email:        email,
		PasswordHash: hashedPassword,
		Role:         RoleUser,
		Balance:      grantPlan.Balance,
		Concurrency:  grantPlan.Concurrency,
		Status:       StatusActive,
		SignupSource: signupSource,
	}
	if err := s.AuthCreateOAuthEmailAccountUser(ctx, user); err != nil {
		switch {
		case errors.Is(err, ErrEmailExists):
			return nil, nil, ErrEmailExists
		case errors.Is(err, ErrEmailDomainRegistrationLimit):
			return nil, nil, ErrEmailDomainRegistrationLimit
		default:
			slog.Error("oauth email register: userRepo.Create failed", "email", email, "signup_source", signupSource, "error", err.Error())
			return nil, nil, ErrServiceUnavailable
		}
	}

	tokenPair, err := s.GenerateTokenPair(ctx, user, "")
	if err != nil {
		// Token generation failed after account creation; attempt deleting that
		// account and suppress cleanup failure so the wrapped token-generation
		// error remains the response cause.
		_ = s.RollbackOAuthEmailAccountCreation(ctx, user.ID, "")
		return nil, nil, fmt.Errorf("generate token pair: %w", err)
	}
	return tokenPair, user, nil
}

// RegisterVerifiedOAuthEmailAccount creates a local account from
// caller-established provider mailbox proof. It validates normalized syntax
// within 255 bytes, rejects blank password/reserved mailboxes and checks
// current registration, invitation and domain policy; this path snapshots
// default RPM in addition to signup balance/concurrency.
//
// It does not request a local mailbox code. Guarded creation precedes
// token-pair generation and defers invitation consumption/plan/affiliate
// completion to the later finalization step. Token failure attempts account
// deletion and suppresses cleanup failure, so callers retain their upstream
// proof and pending context when completing or compensating the created
// account.
func (s *AuthService) RegisterVerifiedOAuthEmailAccount(
	ctx context.Context,
	email string,
	password string,
	invitationCode string,
	signupSource string,
) (*TokenPair, *User, error) {
	if s == nil {
		return nil, nil, ErrServiceUnavailable
	}
	if s.Settings == nil || (!s.Settings.IsRegistrationEnabled(ctx) && !s.AuthCanBypassRegistrationDisabledForOAuth(ctx, signupSource)) {
		return nil, nil, ErrRegDisabled
	}

	email = strings.TrimSpace(strings.ToLower(email))
	// Apply the stored mailbox byte budget before accepting provider-proven text.
	if email == "" || len(email) > 255 {
		return nil, nil, ErrEmailVerifyRequired
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, nil, ErrEmailVerifyRequired
	}
	if IsReservedEmail(email) {
		return nil, nil, ErrEmailReserved
	}
	if strings.TrimSpace(password) == "" {
		return nil, nil, infraerrors.BadRequest("PASSWORD_REQUIRED", "password is required")
	}
	if _, err := s.AuthValidateOAuthRegistrationInvitation(ctx, invitationCode); err != nil {
		return nil, nil, err
	}

	existsEmail, err := s.Users.ExistsByEmail(ctx, email)
	if err != nil {
		return nil, nil, ErrServiceUnavailable
	}
	if existsEmail {
		return nil, nil, ErrEmailExists
	}
	if err := s.AuthValidateRegistrationEmailQuota(ctx, email); err != nil {
		return nil, nil, err
	}

	hashedPassword, err := s.HashPassword(password)
	if err != nil {
		return nil, nil, fmt.Errorf("hash password: %w", err)
	}

	signupSource = AuthNormalizeOAuthSignupSource(signupSource)
	grantPlan := s.AuthResolveSignupGrantPlan(ctx, signupSource)
	var defaultRPMLimit int
	if s.Settings != nil {
		defaultRPMLimit = s.Settings.GetDefaultUserRPMLimit(ctx)
	}
	user := &User{
		Email:        email,
		PasswordHash: hashedPassword,
		Role:         RoleUser,
		Balance:      grantPlan.Balance,
		Concurrency:  grantPlan.Concurrency,
		RPMLimit:     defaultRPMLimit,
		Status:       StatusActive,
		SignupSource: signupSource,
	}
	if err := s.AuthCreateOAuthEmailAccountUser(ctx, user); err != nil {
		switch {
		case errors.Is(err, ErrEmailExists):
			return nil, nil, ErrEmailExists
		case errors.Is(err, ErrEmailDomainRegistrationLimit):
			return nil, nil, ErrEmailDomainRegistrationLimit
		default:
			return nil, nil, ErrServiceUnavailable
		}
	}

	tokenPair, err := s.GenerateTokenPair(ctx, user, "")
	if err != nil {
		// Token generation failed after account creation; attempt deleting that
		// account and suppress cleanup failure so the wrapped token-generation
		// error remains the response cause.
		_ = s.RollbackOAuthEmailAccountCreation(ctx, user.ID, "")
		return nil, nil, fmt.Errorf("generate token pair: %w", err)
	}
	return tokenPair, user, nil
}

// AuthCreateOAuthEmailAccountUser delegates guarded account creation with
// domain quota enforcement and no invitation-consumption artifact. Callers
// pass the planned account; nil service/user is unavailable, and a failed
// create with an assigned positive ID attempts cleanup except for
// email-conflict failure.
//
// Cleanup failure is suppressed and the creation error is returned.
// Successful creation leaves invitation finalization for a later operation,
// so callers retain the account ID for compensation if pending binding or
// token preparation fails. The helper does not establish completion of
// subscriptions, affiliation or browser-session consumption.
func (s *AuthService) AuthCreateOAuthEmailAccountUser(ctx context.Context, user *User) error {
	if s == nil || user == nil {
		return ErrServiceUnavailable
	}

	// Create with address/domain guards now, leaving invitation redemption for
	// finalization after the pending identity sequence can associate this
	// account with its provider.
	err := s.AuthCreateRegisteredUser(ctx, user, &AuthRegistrationArtifacts{EnforceEmailDomainQuota: true})
	if err != nil && user.ID > 0 && !errors.Is(err, ErrEmailExists) {
		// A failed create assigned an account ID; attempt removing that partial
		// account, except email conflicts, and suppress cleanup failure while
		// retaining the creation error.
		_ = s.RollbackOAuthEmailAccountCreation(ctx, user.ID, "")
	}
	return err
}

// FinalizeOAuthEmailAccount completes signup effects for a created positive
// account. It normalizes the source, revalidates the invitation under current
// policy and consumes a required invitation before source update, current
// grant-plan resolution, plan assignment and affiliate attribution.
//
// The current plan is resolved again, but this operation applies its
// subscription selections while balance/concurrency keep their earlier
// creation values. Optional source, plan and affiliate helpers diagnose
// errors while proceeding; invitation-claim failure returns before those
// effects. PendingFlow supplies its binding transaction where applicable and
// owns account compensation after later phase failure, so callers keep the
// account/source/invitation observations aligned.
func (s *AuthService) FinalizeOAuthEmailAccount(
	ctx context.Context,
	user *User,
	invitationCode string,
	signupSource string,
	affiliateCode string,
) error {
	if s == nil || user == nil || user.ID <= 0 {
		return ErrServiceUnavailable
	}

	signupSource = AuthNormalizeOAuthSignupSource(signupSource)
	InvitationRedeemCode, err := s.AuthValidateOAuthRegistrationInvitation(ctx, invitationCode)
	if err != nil {
		return err
	}
	// Claim the revalidated invitation before signup source/plan/affiliate
	// effects; a failed conditional claim returns here so those later effects
	// are not attempted.
	if InvitationRedeemCode != nil {
		if err := s.AuthUseOAuthRegistrationInvitation(ctx, InvitationRedeemCode.ID, user.ID); err != nil {
			return ErrInvitationCodeInvalid
		}
	}

	s.AuthUpdateOAuthSignupSource(ctx, user.ID, signupSource)
	grantPlan := s.AuthResolveSignupGrantPlan(ctx, signupSource)
	s.AuthAssignSubscriptions(ctx, user.ID, grantPlan.Subscriptions, "auto assigned by signup defaults")
	s.AuthBindRegistrationAffiliate(ctx, user.ID, affiliateCode)
	return nil
}

// RollbackOAuthEmailAccountCreation restores an invitation used by the
// identified account before deleting that created account. It requires a
// configured user store and positive account ID; invitation restoration
// failure returns before deletion is attempted.
//
// The operations participate according to their own storage/context rules.
// When restoration performs its own completed write, a later delete failure
// can leave that write in place and returns a wrapped deletion error. Callers
// retain this recovery error where their flow reports compensation failures,
// while selected token-generation paths suppress it in favor of the original
// failure.
func (s *AuthService) RollbackOAuthEmailAccountCreation(ctx context.Context, userID int64, invitationCode string) error {
	if s == nil || s.Users == nil || userID <= 0 {
		return ErrServiceUnavailable
	}
	// Restore this account's invitation before deletion; failed restoration
	// stops cleanup, while a later delete error can follow a successfully
	// restored invitation record.
	if err := s.AuthRestoreOAuthRegistrationInvitation(ctx, invitationCode, userID); err != nil {
		return err
	}
	if err := s.Users.Delete(ctx, userID); err != nil {
		return fmt.Errorf("delete created oauth user: %w", err)
	}
	return nil
}

// ValidatePasswordCredentials checks an active account's local password for
// pending identity adoption without issuing platform tokens. Missing account
// or wrong password becomes invalid-credentials; other lookup failures become
// service-unavailable, and the returned account still needs the surrounding
// pending/browser checks.
func (s *AuthService) ValidatePasswordCredentials(ctx context.Context, email, password string) (*User, error) {
	if s == nil {
		return nil, ErrServiceUnavailable
	}

	user, err := s.Users.GetByEmail(ctx, strings.TrimSpace(strings.ToLower(email)))
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, ErrServiceUnavailable
	}
	if !user.IsActive() {
		return nil, ErrUserNotActive
	}
	if !s.CheckPassword(password, user.PasswordHash) {
		return nil, ErrInvalidCredentials
	}
	return user, nil
}

// RecordSuccessfulLogin attempts local-email backfill from a readable
// nonreserved current account before requesting login activity recording.
// Account read failure skips backfill but still reaches AuthTouchUserLogin;
// callers supply assembled storage and a positive target, and backfill
// failure handling belongs to its delegated helper.
func (s *AuthService) RecordSuccessfulLogin(ctx context.Context, userID int64) {
	if s != nil && s.Users != nil && userID > 0 {
		user, err := s.Users.GetByID(ctx, userID)
		// Unreadable account data supplies no mailbox to backfill; skip that
		// observation while leaving the following activity write to the configured
		// storage helper.
		if err == nil && user != nil && !IsReservedEmail(user.Email) {
			s.AuthBackfillEmailIdentityOnSuccessfulLogin(ctx, user)
		}
	}
	s.AuthTouchUserLogin(ctx, userID)
}
