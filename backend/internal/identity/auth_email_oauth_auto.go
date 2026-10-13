package identity

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// A verified provider mailbox does not replace the stable provider subject as
// account identity. GitHub, Google and OIDC login first resolve that
// subject's owner, reject an email mismatch and only then consider
// email-based account lookup or creation.
//
// The caller supplies trusted upstream proof through EmailVerified and stable
// identity/profile observations. Account creation and identity writes finish
// before login tokens are generated, leaving persistent results when a later
// step fails. The creation helper can recover an existing account after a
// uniqueness conflict, but the caller records that helper branch as created;
// promotion and first-bind selection therefore follow the branch hint rather
// than a guaranteed new-row result.

// EmailOAuthIdentityInput carries caller-verified provider identity and
// mailbox observations for local account selection.
type EmailOAuthIdentityInput struct {
	// ProviderType, ProviderKey and ProviderSubject identify the same upstream
	// identity. Callers align that tuple with the verified mailbox and profile
	// hints before local owner resolution or persistence.
	ProviderType string // GitHub, Google or OIDC provider
	// ProviderKey scopes the subject within its provider. Verified-email login
	// trims it and uses ProviderType when blank before owner lookup and
	// persistence.
	ProviderKey     string
	ProviderSubject string // stable provider subject value
	Email           string // provider-observed account mailbox text
	// EmailVerified asserts upstream mailbox proof supplied by the caller.
	// Verified-email login checks this flag but does not reverify the provider
	// token here.
	EmailVerified bool
	Username      string // suggested local account username
	DisplayName   string // suggested provider display name
	AvatarURL     string // suggested provider avatar address
	// UpstreamMetadata supplies additional saved provider observations.
	// AuthState overlays its entries after email/proof defaults, so those keys
	// can replace defaults and nested values retain sharing.
	UpstreamMetadata map[string]any
}

// LoginOrRegisterVerifiedEmailOAuthWithSignupCodes delegates verified-email
// provider login with signup invitation, affiliate and promotion text.
// Callers establish upstream proof in EmailOAuthIdentityInput; the
// implementation accepts GitHub, Google or OIDC and handles account/identity
// persistence before token issuance.
func (s *AuthService) LoginOrRegisterVerifiedEmailOAuthWithSignupCodes(
	ctx context.Context,
	input EmailOAuthIdentityInput,
	invitationCode string,
	affiliateCode string,
	promoCode string,
) (*TokenPair, *User, error) {
	return s.AuthLoginOrRegisterVerifiedEmailOAuth(ctx, input, invitationCode, affiliateCode, promoCode)
}

// AuthLoginOrRegisterVerifiedEmailOAuth resolves a verified provider subject
// to a current account, or uses its normalized email for existing-account
// lookup and creation. Callers prove the upstream email before setting
// EmailVerified; this method validates the flag, syntax, reserved addresses
// and strict suffix policy, then rejects a stored subject owner with a
// different email.
//
// Identity persistence precedes optional username update, grant/promotion
// selection, login recording and token issuance. The creation-helper branch
// sets created=true even when that helper returns an account recovered from a
// uniqueness conflict, so this hint can select promotion and skip the
// existing-account first-bind request. Later write/token failures can follow
// completed account or identity updates; this flow performs no general
// account rollback.
func (s *AuthService) AuthLoginOrRegisterVerifiedEmailOAuth(
	ctx context.Context,
	input EmailOAuthIdentityInput,
	invitationCode string,
	affiliateCode string,
	promoCode string,
) (*TokenPair, *User, error) {
	if s == nil || s.Users == nil || !s.HasDatabase() {
		return nil, nil, ErrServiceUnavailable
	}

	providerType := AuthNormalizeOAuthSignupSource(input.ProviderType)
	if providerType != "github" && providerType != "google" && providerType != "oidc" {
		return nil, nil, infraerrors.BadRequest("OAUTH_PROVIDER_INVALID", "oauth provider is invalid")
	}
	providerKey := strings.TrimSpace(input.ProviderKey)
	if providerKey == "" {
		providerKey = providerType
	}
	providerSubject := strings.TrimSpace(input.ProviderSubject)
	if providerSubject == "" {
		return nil, nil, infraerrors.BadRequest("OAUTH_SUBJECT_MISSING", "oauth subject is missing")
	}
	if !input.EmailVerified {
		return nil, nil, infraerrors.Forbidden("OAUTH_EMAIL_NOT_VERIFIED", "oauth email is not verified")
	}

	email := strings.TrimSpace(strings.ToLower(input.Email))
	// Apply the stored mailbox byte budget before parser and ownership checks.
	if email == "" || len(email) > 255 {
		return nil, nil, infraerrors.BadRequest("INVALID_EMAIL", "invalid email")
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, nil, infraerrors.BadRequest("INVALID_EMAIL", "invalid email")
	}
	if IsReservedEmail(email) {
		return nil, nil, ErrEmailReserved
	}
	if err := s.AuthValidateRegistrationEmailPolicy(ctx, email); err != nil {
		return nil, nil, err
	}

	identityUser, err := s.AuthFindEmailOAuthIdentityOwner(ctx, providerType, providerKey, providerSubject)
	if err != nil {
		return nil, nil, err
	}
	if identityUser != nil && !strings.EqualFold(strings.TrimSpace(identityUser.Email), email) {
		return nil, nil, infraerrors.Conflict("AUTH_IDENTITY_EMAIL_MISMATCH", "oauth identity belongs to a different email")
	}

	user := identityUser
	created := false
	if user == nil {
		user, err = s.Users.GetByEmail(ctx, email)
		if err != nil {
			if errors.Is(err, ErrUserNotFound) {
				user, err = s.AuthCreateEmailOAuthUser(ctx, email, input.Username, providerType, invitationCode, affiliateCode)
				if err != nil {
					return nil, nil, err
				}
				// Record the creation-helper branch as signup state even when its
				// uniqueness recovery returned an existing account; later
				// promotion/first-bind selection follows this flag.
				created = true
			} else {
				s.Observer.Printf("service.auth", "[Auth] Database error during %s oauth login: %v", providerType, err)
				return nil, nil, ErrServiceUnavailable
			}
		}
	}

	if !user.IsActive() {
		return nil, nil, ErrUserNotActive
	}
	// Save the stable provider relationship before optional profile/grant work
	// and login issuance; failures afterward can leave the completed identity
	// write in place.
	if err := s.AuthEnsureEmailOAuthIdentity(ctx, user.ID, EmailOAuthIdentityInput{
		ProviderType:     providerType,
		ProviderKey:      providerKey,
		ProviderSubject:  providerSubject,
		Email:            email,
		EmailVerified:    input.EmailVerified,
		Username:         input.Username,
		DisplayName:      input.DisplayName,
		AvatarURL:        input.AvatarURL,
		UpstreamMetadata: input.UpstreamMetadata,
	}); err != nil {
		return nil, nil, err
	}

	if user.Username == "" && strings.TrimSpace(input.Username) != "" {
		user.Username = strings.TrimSpace(input.Username)
		// A missing account name accepts the supplied hint, but a save failure is
		// diagnostic after identity binding and does not stop token issuance.
		if err := s.Users.Update(ctx, user, UserUpdateFields{Username: true}); err != nil {
			s.Observer.Printf("service.auth", "[Auth] Failed to update username after %s oauth login: %v", providerType, err)
		}
	}
	if !created {
		// Existing-account branch requests idempotent first-bind grants; diagnose
		// grant failure while continuing login after the provider identity has
		// already been saved.
		if err := s.ApplyProviderDefaultSettingsOnFirstBind(ctx, user.ID, providerType); err != nil {
			s.Observer.Printf("service.auth", "[Auth] Failed to apply %s first bind defaults: %v", providerType, err)
		}
	} else {
		user = s.AuthApplyOAuthSignupPromoCode(ctx, user, promoCode)
	}
	s.RecordSuccessfulLogin(ctx, user.ID)

	tokenPair, err := s.GenerateTokenPair(ctx, user, "")
	if err != nil {
		return nil, nil, fmt.Errorf("generate token pair: %w", err)
	}
	return tokenPair, user, nil
}

// AuthCreateEmailOAuthUser creates an account under the provider's signup
// defaults and invitation policy. It snapshots RPM and generates a local
// random-password hash before guarded persistence; callers provide the
// validated email/provider context used by the surrounding verified-email
// flow.
//
// A uniqueness conflict reloads and returns the current account without a
// separate created flag. Successful creation alone runs source bootstrap,
// plan assignment and affiliate attribution. Other persistence failures
// become service-unavailable except invalid invitation, so callers
// distinguish a usable returned account from proof that this helper created
// it when choosing later signup-only effects.
func (s *AuthService) AuthCreateEmailOAuthUser(ctx context.Context, email, username, providerType, invitationCode, affiliateCode string) (*User, error) {
	if s.Settings == nil || !s.Settings.IsRegistrationEnabled(ctx) {
		return nil, ErrRegDisabled
	}

	artifacts, err := s.AuthResolveRegistrationArtifacts(ctx, invitationCode, ErrOAuthInvitationRequired)
	if err != nil {
		return nil, err
	}

	randomPassword, err := RandomHexString(32)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	hashedPassword, err := s.HashPassword(randomPassword)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	grantPlan := s.AuthResolveSignupGrantPlan(ctx, providerType)
	var defaultRPMLimit int
	if s.Settings != nil {
		defaultRPMLimit = s.Settings.GetDefaultUserRPMLimit(ctx)
	}
	user := &User{
		Email:        email,
		Username:     strings.TrimSpace(username),
		PasswordHash: hashedPassword,
		Role:         RoleUser,
		Balance:      grantPlan.Balance,
		Concurrency:  grantPlan.Concurrency,
		RPMLimit:     defaultRPMLimit,
		Status:       StatusActive,
		SignupSource: providerType,
	}
	if err := s.AuthCreateRegisteredUser(ctx, user, artifacts); err != nil {
		// Another create can occupy this email after lookup; reload its current
		// account for the caller without rerunning this helper's
		// successful-creation bootstrap effects.
		if errors.Is(err, ErrEmailExists) {
			existing, loadErr := s.Users.GetByEmail(ctx, email)
			if loadErr != nil {
				return nil, ErrServiceUnavailable
			}
			return existing, nil
		}
		if errors.Is(err, ErrInvitationCodeInvalid) {
			return nil, ErrInvitationCodeInvalid
		}
		return nil, ErrServiceUnavailable
	}
	// Only successful account creation reaches source bootstrap, plan assignment
	// and affiliate attribution here; uniqueness recovery returns earlier with
	// the current account.
	s.AuthPostAuthUserBootstrap(ctx, user, providerType, false)
	s.AuthAssignSubscriptions(ctx, user.ID, grantPlan.Subscriptions, "auto assigned by signup defaults")
	s.AuthBindRegistrationAffiliate(ctx, user.ID, affiliateCode)
	return user, nil
}
