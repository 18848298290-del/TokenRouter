package identity

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// auth_email_binding.go validates mailbox ownership and replaces local email
// credentials for account binding.
// Binding a local mailbox uses its own email challenge even when ordinary
// registration verification is disabled. The current account's mailbox
// determines whether binding accepts a newly selected password or requires
// proof of the existing password before replacement.
//
// Destination checks distinguish direct email ownership, mailbox aliases and
// optional normalized registration addresses. PostgreSQL rechecks those
// observations while writing credentials and their email identity; a
// database-free fallback has separate account and grant writes.
// Refresh-session revocation follows a successful storage return and is best
// effort. Missing optional preflight capability cannot be treated as proof of
// a free alias, and working account fields can change before later write or
// grant failures.

// AuthNormalizedEmailBindingConflictChecker supplies owner-excluding
// occupancy reads for normalized binding destinations.
type AuthNormalizedEmailBindingConflictChecker interface {
	// ExistsByNormalizedEmailExcluding checks destination occupancy while
	// omitting the supplied account. Binding uses it when the destination
	// normalizes to that account's current mailbox, allowing its own alias while
	// rejecting another owner; storage failures remain errors.
	ExistsByNormalizedEmailExcluding(ctx context.Context, normalizedEmail string, excludedUserID int64) (bool, error)
}

// AuthEmailIdentityAliasGuardRepository writes primary credentials after
// transaction-scoped destination alias checks.
type AuthEmailIdentityAliasGuardRepository interface {
	// UpdateEmailWithAliasGuard replaces the account email/password after
	// destination and mailbox-alias ownership checks. PostgreSQL locks and
	// rechecks those values using its transaction participation, while the
	// enclosing AuthState operation separately replaces the email identity.
	//
	// Callers supply aligned account, email and password-hash data and keep
	// their intended transaction available through the guard. A failed ownership
	// check prevents this credential write; a successful return still needs
	// completion by an external transaction owner when one is joined. Identity
	// replacement, grants and refresh-session cleanup belong to the surrounding
	// binding sequence.
	UpdateEmailWithAliasGuard(ctx context.Context, userID int64, email string, passwordHash string) error
}

// BindEmailIdentity verifies a mailbox challenge and binds local
// email/password credentials to the current account. It rejects reserved
// mailboxes and applies strict suffix policy; first nonreserved email binding
// requires at least six password bytes, while an existing mailbox requires
// the current password, which is rehashed for this write.
//
// A database-backed path delegates ownership guards, credentials, identity
// replacement and first-bind grants to storage before attempting
// refresh-session revocation. The database-free fallback mutates the working
// account before its masked update and requests first-bind grants afterward,
// so a grant failure can follow completed credential persistence. Both paths
// diagnose revocation failure after successful writes. Callers supply
// authenticated account context and keep the returned working account
// separate from proof that every optional cache/session effect completed.
func (s *AuthService) BindEmailIdentity(
	ctx context.Context,
	userID int64,
	email string,
	verifyCode string,
	password string,
) (*User, error) {
	if s == nil {
		return nil, ErrServiceUnavailable
	}

	normalizedEmail, err := AuthNormalizeEmailForIdentityBinding(email)
	if err != nil {
		return nil, err
	}
	if IsReservedEmail(normalizedEmail) {
		return nil, ErrEmailReserved
	}
	if strings.TrimSpace(password) == "" {
		return nil, ErrPasswordRequired
	}
	currentUser, err := s.Users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := s.AuthEnsureUserEmailChangeAllowed(ctx, currentUser, normalizedEmail); err != nil {
		return nil, err
	}
	if err := s.VerifyOAuthEmailCode(ctx, normalizedEmail, verifyCode); err != nil {
		return nil, err
	}
	if err := s.AuthValidateRegistrationEmailPolicy(ctx, normalizedEmail); err != nil {
		return nil, err
	}
	firstRealEmailBind := !AuthHasBindableEmailIdentitySubject(currentUser.Email)
	// Count password bytes for this first-binding minimum, including multibyte input.
	if firstRealEmailBind && len(password) < 6 {
		return nil, infraerrors.BadRequest("PASSWORD_TOO_SHORT", "password must be at least 6 characters")
	}
	if !firstRealEmailBind && !s.CheckPassword(password, currentUser.PasswordHash) {
		return nil, ErrPasswordIncorrect
	}

	registrationNormalizedEmail := s.AuthNormalizeRegistrationEmailForBinding(ctx, normalizedEmail)
	if err := s.AuthEnsureEmailBindingTargetAvailable(ctx, currentUser, normalizedEmail, registrationNormalizedEmail); err != nil {
		return nil, err
	}

	hashedPassword, err := s.HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	// Use the adapter's guarded credential/identity sequence when
	// database-backed; refresh-session cleanup follows its successful return,
	// while externally joined transactions keep caller-owned completion.
	if s.HasDatabase() {
		if err := s.AuthUpdateBoundEmailIdentityTx(ctx, currentUser, normalizedEmail, registrationNormalizedEmail, hashedPassword, firstRealEmailBind); err != nil {
			return nil, err
		}
		s.AuthRevokeEmailIdentitySessions(ctx, userID)
		return currentUser, nil
	}

	// The fallback edits the working account before persistence; its grant
	// request follows the masked credential write and can fail after those
	// credentials are saved.
	currentUser.Email = normalizedEmail
	currentUser.PasswordHash = hashedPassword
	fields := UserUpdateFields{Email: true, PasswordHash: true}
	updateUser := s.Users.Update
	if registrationNormalizedEmail != "" {
		// Select registration's normalized-address write guard for this submitted
		// mailbox, keeping the additional uniqueness check alongside the credential
		// update rather than relying on preflight alone.
		updateUser = func(updateCtx context.Context, updateUser *User, updateFields UserUpdateFields) error {
			return s.Users.UpdateWithNormalizedEmailGuard(updateCtx, updateUser, registrationNormalizedEmail, updateFields)
		}
	}
	if err := updateUser(ctx, currentUser, fields); err != nil {
		if errors.Is(err, ErrEmailExists) {
			return nil, ErrEmailExists
		}
		return nil, ErrServiceUnavailable
	}

	if firstRealEmailBind {
		if err := s.ApplyProviderDefaultSettingsOnFirstBind(ctx, userID, "email"); err != nil {
			return nil, fmt.Errorf("apply email first bind defaults: %w", err)
		}
	}

	s.AuthRevokeEmailIdentitySessions(ctx, userID)
	return currentUser, nil
}

// SendEmailIdentityBindCode sends a local challenge after checking the
// current account's mailbox-change policy, strict suffix policy and
// destination occupancy. It requires email delivery, returns lookup/delivery
// failures and passes the selected locale/site text without changing account
// credentials.
func (s *AuthService) SendEmailIdentityBindCode(ctx context.Context, userID int64, email string, locale ...string) error {
	if s == nil {
		return ErrServiceUnavailable
	}

	normalizedEmail, err := AuthNormalizeEmailForIdentityBinding(email)
	if err != nil {
		return err
	}
	if IsReservedEmail(normalizedEmail) {
		return ErrEmailReserved
	}
	currentUser, err := s.Users.GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return ErrUserNotFound
		}
		return ErrServiceUnavailable
	}
	if err := s.AuthEnsureUserEmailChangeAllowed(ctx, currentUser, normalizedEmail); err != nil {
		return err
	}
	if err := s.AuthValidateRegistrationEmailPolicy(ctx, normalizedEmail); err != nil {
		return err
	}
	if s.Email == nil {
		return ErrServiceUnavailable
	}
	registrationNormalizedEmail := s.AuthNormalizeRegistrationEmailForBinding(ctx, normalizedEmail)
	if err := s.AuthEnsureEmailBindingTargetAvailable(ctx, currentUser, normalizedEmail, registrationNormalizedEmail); err != nil {
		return err
	}

	siteName := "TokenRouter"
	if s.Settings != nil {
		siteName = s.Settings.GetSiteName(ctx)
	}
	return s.Email.SendVerifyCode(ctx, normalizedEmail, siteName, FirstEmailLocale(locale))
}

// AuthEnsureUserEmailChangeAllowed admits initial nonreserved-mailbox binding
// and confirmation of the current mailbox. A nil account returns
// ErrUserNotFound; an existing nonreserved mailbox changed to a different
// address requires the current user-email-change switch.
//
// The comparison trims the stored mailbox and ignores case, while targetEmail
// is supplied by the binding normalizer. Missing settings rejects an actual
// replacement. This guard does not validate password or mailbox challenge
// proof; BindEmailIdentity performs those later checks before ownership and
// credential persistence, so policy admission alone cannot authorize
// replacing an account mailbox.
func (s *AuthService) AuthEnsureUserEmailChangeAllowed(ctx context.Context, currentUser *User, targetEmail string) error {
	if currentUser == nil {
		return ErrUserNotFound
	}
	if !AuthHasBindableEmailIdentitySubject(currentUser.Email) || strings.EqualFold(strings.TrimSpace(currentUser.Email), targetEmail) {
		return nil
	}
	if s.Settings == nil || !s.Settings.IsUserEmailChangeEnabled(ctx) {
		return ErrEmailChangeDisabled
	}
	return nil
}

// AuthNormalizeEmailForIdentityBinding trims/lowercases input and validates
// nonempty text within a 255-byte budget through mail.ParseAddress. It
// retains the normalized input text rather than extracting parsed.Address, so
// accepted display-address syntax keeps its representation for subsequent
// policy and lookup.
func AuthNormalizeEmailForIdentityBinding(email string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	// The mailbox validation budget applies to encoded bytes, not rune count.
	if normalized == "" || len(normalized) > 255 {
		return "", infraerrors.BadRequest("INVALID_EMAIL", "invalid email")
	}
	if _, err := mail.ParseAddress(normalized); err != nil {
		return "", infraerrors.BadRequest("INVALID_EMAIL", "invalid email")
	}
	return normalized, nil
}

// AuthHasBindableEmailIdentitySubject reports whether trimmed/lowercased text
// is nonblank and nonreserved. This classifies an existing local mailbox for
// first-binding/password selection; it does not validate mailbox syntax,
// stored email proof or the account's password.
func AuthHasBindableEmailIdentitySubject(email string) bool {
	normalized := strings.ToLower(strings.TrimSpace(email))
	return normalized != "" && !IsReservedEmail(normalized)
}

// AuthNormalizeRegistrationEmailForBinding returns the mailbox-normalized
// guard value when runtime normalization is enabled. Missing service/settings
// or disabled normalization returns empty text, telling binding callers to
// omit this additional registration-address guard.
func (s *AuthService) AuthNormalizeRegistrationEmailForBinding(ctx context.Context, email string) string {
	if s == nil || s.Settings == nil || !s.Settings.IsRegistrationEmailNormalizationEnabled(ctx) {
		return ""
	}
	return NormalizeRegistrationEmailAddress(email)
}

// AuthEnsureEmailBindingTargetAvailable checks direct email, optional alias
// ownership and a supplied normalized-address guard for the current account.
// Another direct owner is rejected, missing direct email permits later
// checks, and lookup failures become service-unavailable.
//
// Owner-aware alias checks can allow a current account's own alternate alias,
// while boolean-only occupancy cannot identify that owner. A normalized match
// to the current mailbox uses the excluding capability when present; without
// it the helper cannot establish another owner. These reads are preflight
// observations, and PostgreSQL binding guards repeat destination ownership
// checks during its later credential transaction.
func (s *AuthService) AuthEnsureEmailBindingTargetAvailable(
	ctx context.Context,
	currentUser *User,
	email string,
	registrationNormalizedEmail string,
) error {
	existingUser, err := s.Users.GetByEmail(ctx, email)
	switch {
	case err == nil:
		if existingUser != nil && (currentUser == nil || existingUser.ID != currentUser.ID) {
			return ErrEmailExists
		}
	case err != nil && !errors.Is(err, ErrUserNotFound):
		return ErrServiceUnavailable
	}

	if err := s.AuthEnsureEmailAliasTargetAvailable(ctx, currentUser, email); err != nil {
		return err
	}

	if registrationNormalizedEmail == "" {
		return nil
	}

	exists, err := s.AuthHasNormalizedEmailBindingConflict(ctx, currentUser, registrationNormalizedEmail)
	if err != nil {
		return ErrServiceUnavailable
	}
	if exists {
		return ErrEmailExists
	}
	return nil
}

// AuthEnsureEmailAliasTargetAvailable rejects another account's mailbox alias
// before binding. Owner-aware lookup takes precedence and permits the current
// owner; if absent, a positive boolean-only lookup rejects occupancy because
// it cannot distinguish that account.
//
// Nil current account returns ErrUserNotFound and configured lookup failure
// becomes service-unavailable. When neither capability is supplied, this
// preflight returns success without an alias observation. The database
// binding guard still owns its later write-time check, so callers retain that
// distinction between omitted preflight capability and a verified available
// alias.
func (s *AuthService) AuthEnsureEmailAliasTargetAvailable(ctx context.Context, currentUser *User, email string) error {
	if currentUser == nil {
		return ErrUserNotFound
	}
	if lookup := s.AliasOwner; lookup != nil {
		ownerID, exists, err := lookup.EmailAliasOwnerID(ctx, email, currentUser.ID)
		if err != nil {
			return ErrServiceUnavailable
		}
		if exists && ownerID != currentUser.ID {
			return ErrEmailExists
		}
		return nil
	}
	// Without owner-aware data, boolean alias occupancy is the available
	// observation; an occupied result cannot prove this account owns it and is
	// rejected.
	lookup := s.AliasLookup
	ok := lookup != nil
	if !ok {
		return nil
	}
	exists, err := lookup.ExistsByEmailAlias(ctx, email)
	if err != nil {
		return ErrServiceUnavailable
	}
	if exists {
		return ErrEmailExists
	}
	return nil
}

// AuthHasNormalizedEmailBindingConflict selects the occupancy read for a
// normalized binding destination. Empty guard text returns false, while a
// positive current user whose mailbox normalizes to that destination requires
// owner-excluding lookup to identify a different account.
//
// If that capability is absent, the same-mailbox case returns false because
// boolean occupancy would include the current owner. Other destinations use
// the ordinary normalized-email existence read. Errors stay errors for the
// surrounding availability guard, and a successful result is still a
// preflight observation before the storage transaction rechecks ownership.
func (s *AuthService) AuthHasNormalizedEmailBindingConflict(
	ctx context.Context,
	currentUser *User,
	registrationNormalizedEmail string,
) (bool, error) {
	if registrationNormalizedEmail == "" {
		return false, nil
	}

	currentUserID := int64(0)
	currentNormalizedEmail := ""
	if currentUser != nil {
		currentUserID = currentUser.ID
		currentNormalizedEmail = NormalizeRegistrationEmailAddress(currentUser.Email)
	}
	if currentUserID > 0 && currentNormalizedEmail == registrationNormalizedEmail {
		if checker := s.NormalizedEmailConflict; checker != nil {
			return checker.ExistsByNormalizedEmailExcluding(ctx, registrationNormalizedEmail, currentUserID)
		}
		// The destination normalizes to the current mailbox, but no excluding
		// reader identifies another owner; a boolean occupied result would count
		// this account itself.
		return false, nil
	}

	return s.Users.ExistsByNormalizedEmail(ctx, registrationNormalizedEmail)
}

// AuthRevokeEmailIdentitySessions attempts all-user refresh-session cleanup
// after binding receives a successful credential-write result. A joined
// transaction can still await its caller's commit; this helper diagnoses cleanup
// errors and returns no error, so binding success does not confirm removal of
// every cached refresh session.
func (s *AuthService) AuthRevokeEmailIdentitySessions(ctx context.Context, userID int64) {
	// Binding has received a successful write result within its storage scope.
	// Diagnose cleanup failure while allowing that caller to continue; revocation
	// does not commit or roll back any transaction joined by the credential write.
	if err := s.RevokeAllUserSessions(ctx, userID); err != nil {
		s.Observer.Printf("service.auth", "[Auth] Failed to revoke refresh sessions after email identity bind for user %d: %v", userID, err)
	}
}

// AuthNormalizeBoundEmailAuthIdentitySubject returns trimmed/lowercased text
// for a nonreserved mailbox. Blank or synthetic addresses return empty text
// so persistent email-identity helpers skip that subject; syntax and
// credential proof belong to the binding flow.
func AuthNormalizeBoundEmailAuthIdentitySubject(email string) string {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || IsReservedEmail(normalized) {
		return ""
	}
	return normalized
}
