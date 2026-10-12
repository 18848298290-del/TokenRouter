package identity

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

type AuthNormalizedEmailBindingConflictChecker interface {
	ExistsByNormalizedEmailExcluding(ctx context.Context, normalizedEmail string, excludedUserID int64) (bool, error)
}

// AuthEmailIdentityAliasGuardRepository provides transactional primary-email replacement with alias checks.
// The optional interface allows test repositories to operate without a database.
type AuthEmailIdentityAliasGuardRepository interface {
	UpdateEmailWithAliasGuard(ctx context.Context, userID int64, email string, passwordHash string) error
}

// BindEmailIdentity verifies and binds a local email/password identity to the
// current user, or replaces the existing bound primary email.
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

	if s.HasDatabase() {
		if err := s.AuthUpdateBoundEmailIdentityTx(ctx, currentUser, normalizedEmail, registrationNormalizedEmail, hashedPassword, firstRealEmailBind); err != nil {
			return nil, err
		}
		s.AuthRevokeEmailIdentitySessions(ctx, userID)
		return currentUser, nil
	}

	currentUser.Email = normalizedEmail
	currentUser.PasswordHash = hashedPassword
	fields := UserUpdateFields{Email: true, PasswordHash: true}
	updateUser := s.Users.Update
	if registrationNormalizedEmail != "" {
		// Email binding uses the same normalized-address uniqueness guard as registration.
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

// SendEmailIdentityBindCode sends a verification code for authenticated email binding flows.
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

// AuthEnsureUserEmailChangeAllowed checks the change policy when replacing a real email address.
// Initial binding and verification of the existing address remain allowed.
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

func AuthNormalizeEmailForIdentityBinding(email string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || len(normalized) > 255 {
		return "", infraerrors.BadRequest("INVALID_EMAIL", "invalid email")
	}
	if _, err := mail.ParseAddress(normalized); err != nil {
		return "", infraerrors.BadRequest("INVALID_EMAIL", "invalid email")
	}
	return normalized, nil
}

func AuthHasBindableEmailIdentitySubject(email string) bool {
	normalized := strings.ToLower(strings.TrimSpace(email))
	return normalized != "" && !IsReservedEmail(normalized)
}

func (s *AuthService) AuthNormalizeRegistrationEmailForBinding(ctx context.Context, email string) string {
	if s == nil || s.Settings == nil || !s.Settings.IsRegistrationEmailNormalizationEnabled(ctx) {
		return ""
	}
	return NormalizeRegistrationEmailAddress(email)
}

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

// AuthEnsureEmailAliasTargetAvailable checks whether another user owns the mailbox alias.
// Owner-aware stores allow another alias of the current user's mailbox.
// Boolean-only stores reject occupied aliases because they cannot identify the owner.
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
		// Reject occupancy when the store cannot distinguish the current owner.
		return ErrEmailExists
	}
	return nil
}

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
		// The boolean lookup cannot exclude this user, so it cannot establish a conflict.
		return false, nil
	}

	return s.Users.ExistsByNormalizedEmail(ctx, registrationNormalizedEmail)
}

func (s *AuthService) AuthRevokeEmailIdentitySessions(ctx context.Context, userID int64) {
	if err := s.RevokeAllUserSessions(ctx, userID); err != nil {
		s.Observer.Printf("service.auth", "[Auth] Failed to revoke refresh sessions after email identity bind for user %d: %v", userID, err)
	}
}

func AuthNormalizeBoundEmailAuthIdentitySubject(email string) string {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || IsReservedEmail(normalized) {
		return ""
	}
	return normalized
}
