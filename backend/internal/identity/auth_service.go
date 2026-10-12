package identity

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

var (
	ErrInvalidCredentials = infraerrors.Unauthorized("INVALID_CREDENTIALS", "invalid email or password")
	ErrUserNotActive      = infraerrors.Forbidden("USER_NOT_ACTIVE", "user is not active")
	ErrEmailExists        = infraerrors.Conflict("EMAIL_EXISTS", "email already exists")
	ErrEmailReserved      = infraerrors.BadRequest("EMAIL_RESERVED", "email is reserved")
	ErrInvalidToken       = infraerrors.Unauthorized("INVALID_TOKEN", "invalid token")
	ErrTokenExpired       = infraerrors.Unauthorized("TOKEN_EXPIRED", "token has expired")

	ErrTokenTooLarge       = infraerrors.BadRequest("TOKEN_TOO_LARGE", "token too large")
	ErrTokenRevoked        = infraerrors.Unauthorized("TOKEN_REVOKED", "token has been revoked")
	ErrRefreshTokenInvalid = infraerrors.Unauthorized("REFRESH_TOKEN_INVALID", "invalid refresh token")
	ErrRefreshTokenExpired = infraerrors.Unauthorized("REFRESH_TOKEN_EXPIRED", "refresh token has expired")

	ErrEmailVerifyRequired          = infraerrors.BadRequest("EMAIL_VERIFY_REQUIRED", "email verification is required")
	ErrEmailSuffixNotAllowed        = infraerrors.BadRequest("EMAIL_SUFFIX_NOT_ALLOWED", "email suffix is not allowed")
	ErrEmailDomainRegistrationLimit = infraerrors.BadRequest(
		"EMAIL_DOMAIN_REGISTRATION_LIMIT",
		"this email domain cannot register another account; use a mainstream email or contact support to add the enterprise domain",
	)
	ErrEmailChangeDisabled     = infraerrors.Forbidden("EMAIL_CHANGE_DISABLED", "email changes are disabled")
	ErrRegDisabled             = infraerrors.Forbidden("REGISTRATION_DISABLED", "registration is currently disabled")
	ErrServiceUnavailable      = infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "service temporarily unavailable")
	ErrInvitationCodeRequired  = infraerrors.BadRequest("INVITATION_CODE_REQUIRED", "invitation code is required")
	ErrInvitationCodeInvalid   = infraerrors.BadRequest("INVITATION_CODE_INVALID", "invalid or used invitation code")
	ErrOAuthInvitationRequired = infraerrors.Forbidden("OAUTH_INVITATION_REQUIRED", "invitation code required to complete oauth registration")
	ErrCaptchaProviderConflict = infraerrors.ServiceUnavailable("CAPTCHA_PROVIDER_CONFLICT", "multiple captcha providers are enabled")

	// ErrPasswordIncorrect reports a failed current-password check.
	ErrPasswordIncorrect = infraerrors.BadRequest("PASSWORD_INCORRECT", "current password is incorrect")
)

type CaptchaProof struct {
	// TurnstileToken carries a Cloudflare token or an Aliyun captchaVerifyParam.
	TurnstileToken string
	TencentTicket  string
	TencentRandstr string
}

type DefaultSubscriptionAssigner interface {
	AssignOrExtendSubscription(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, bool, error)
}

type AuthSignupGrantPlan struct {
	Balance       float64
	Concurrency   int
	Subscriptions []DefaultSubscriptionSetting
}

// SendVerifyCodeResult contains the delay before requesting another verification code.
type SendVerifyCodeResult struct {
	Countdown int `json:"countdown"` // Seconds.
}

type AuthRegistrationArtifacts struct {
	InvitationRedeemCode *RedeemCode
	// EnforceEmailDomainQuota enables non-whitelisted domain quotas for registration and OAuth email completion.
	EnforceEmailDomainQuota bool
}

// RegisterWithVerification registers a user with optional email verification, promo, invitation, and affiliate codes, then returns a token and user.
func (s *AuthService) RegisterWithVerification(ctx context.Context, email, password, verifyCode, promoCode, invitationCode, affiliateCode string) (string, *User, error) {
	// Registration is disabled when settings are unavailable.
	if s.Settings == nil || !s.Settings.IsRegistrationEnabled(ctx) {
		return "", nil, ErrRegDisabled
	}

	// Reserve synthetic OAuth addresses to prevent collisions with local accounts.
	if IsReservedEmail(email) {
		return "", nil, ErrEmailReserved
	}
	existsEmail, err := s.AuthRegistrationEmailExists(ctx, email)
	if err != nil {
		s.Observer.Printf("service.auth", "[Auth] Database error checking email exists: %v", err)
		return "", nil, ErrServiceUnavailable
	}
	if existsEmail {
		return "", nil, ErrEmailExists
	}
	if err := s.AuthValidateRegistrationEmailQuota(ctx, email); err != nil {
		return "", nil, err
	}

	artifacts, err := s.AuthResolveRegistrationArtifacts(ctx, invitationCode, ErrInvitationCodeRequired)
	if err != nil {
		return "", nil, err
	}
	artifacts.EnforceEmailDomainQuota = true

	if s.Settings != nil && s.Settings.IsEmailVerifyEnabled(ctx) {
		// Reject registration when required email verification cannot run.
		if s.Email == nil {
			s.Observer.Printf("service.auth", "%s", "[Auth] Email verification enabled but email service not configured, rejecting registration")
			return "", nil, ErrServiceUnavailable
		}
		if verifyCode == "" {
			return "", nil, ErrEmailVerifyRequired
		}

		if err := s.Email.VerifyCode(ctx, email, verifyCode); err != nil {
			return "", nil, fmt.Errorf("verify code: %w", err)
		}
	}

	hashedPassword, err := s.HashPassword(password)
	if err != nil {
		return "", nil, fmt.Errorf("hash password: %w", err)
	}

	grantPlan := s.AuthResolveSignupGrantPlan(ctx, "email")

	// Snapshot the default RPM limit at signup; zero means unlimited.
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
	}
	if err := s.AuthCreateRegisteredUser(ctx, user, artifacts); err != nil {
		// An email can become occupied after the preflight check.
		switch {
		case errors.Is(err, ErrEmailExists):
			return "", nil, ErrEmailExists
		case errors.Is(err, ErrEmailDomainRegistrationLimit):
			return "", nil, ErrEmailDomainRegistrationLimit
		case errors.Is(err, ErrInvitationCodeInvalid):
			return "", nil, ErrInvitationCodeInvalid
		default:
			s.Observer.Printf("service.auth", "[Auth] Database error creating user: %v", err)
			return "", nil, ErrServiceUnavailable
		}
	}
	s.AuthPostAuthUserBootstrap(ctx, user, "email", true)
	s.AuthAssignSubscriptions(ctx, user.ID, grantPlan.Subscriptions, "auto assigned by signup defaults")
	s.AuthBindRegistrationAffiliate(ctx, user.ID, affiliateCode)

	if promoCode != "" && s.Promo != nil && s.Settings != nil && s.Settings.IsPromoCodeEnabled(ctx) {
		if err := s.Promo.ApplyPromoCode(ctx, user.ID, promoCode); err != nil {
			// Promo failures are logged while registration continues.
			s.Observer.Printf("service.auth", "[Auth] Failed to apply promo code for user %d: %v", user.ID, err)
		} else {
			// Reload the balance after applying the promotion.
			if updatedUser, err := s.Users.GetByID(ctx, user.ID); err == nil {
				user = updatedUser
			}
		}
	}

	token, err := s.GenerateToken(ctx, user)
	if err != nil {
		return "", nil, fmt.Errorf("generate token: %w", err)
	}

	return token, user, nil
}

// SendVerifyCode sends a registration verification email synchronously.
func (s *AuthService) SendVerifyCode(ctx context.Context, email string, locale ...string) error {
	// Registration defaults to disabled.
	if s.Settings == nil || !s.Settings.IsRegistrationEnabled(ctx) {
		return ErrRegDisabled
	}

	if IsReservedEmail(email) {
		return ErrEmailReserved
	}

	existsEmail, err := s.AuthRegistrationEmailExists(ctx, email)
	if err != nil {
		s.Observer.Printf("service.auth", "[Auth] Database error checking email exists: %v", err)
		return ErrServiceUnavailable
	}
	if existsEmail {
		return ErrEmailExists
	}
	if err := s.AuthValidateRegistrationEmailQuota(ctx, email); err != nil {
		return err
	}

	if s.Email == nil {
		return errors.New("email service not configured")
	}

	siteName := "TokenRouter"
	if s.Settings != nil {
		siteName = s.Settings.GetSiteName(ctx)
	}

	return s.Email.SendVerifyCode(ctx, email, siteName, FirstEmailLocale(locale))
}

// SendVerifyCodeAsync queues a registration verification email and returns a retry delay.
func (s *AuthService) SendVerifyCodeAsync(ctx context.Context, email string, locale ...string) (*SendVerifyCodeResult, error) {
	s.Observer.Printf("service.auth", "[Auth] SendVerifyCodeAsync called for email: %s", email)

	// Registration defaults to disabled.
	if s.Settings == nil || !s.Settings.IsRegistrationEnabled(ctx) {
		s.Observer.Printf("service.auth", "%s", "[Auth] Registration is disabled")
		return nil, ErrRegDisabled
	}

	if IsReservedEmail(email) {
		return nil, ErrEmailReserved
	}

	existsEmail, err := s.AuthRegistrationEmailExists(ctx, email)
	if err != nil {
		s.Observer.Printf("service.auth", "[Auth] Database error checking email exists: %v", err)
		return nil, ErrServiceUnavailable
	}
	if existsEmail {
		s.Observer.Printf("service.auth", "[Auth] Email already exists: %s", email)
		return nil, ErrEmailExists
	}
	if err := s.AuthValidateRegistrationEmailQuota(ctx, email); err != nil {
		return nil, err
	}

	if s.EmailQueue == nil {
		s.Observer.Printf("service.auth", "%s", "[Auth] Email queue service not configured")
		return nil, errors.New("email queue service not configured")
	}

	siteName := "TokenRouter"
	if s.Settings != nil {
		siteName = s.Settings.GetSiteName(ctx)
	}

	s.Observer.Printf("service.auth", "[Auth] Enqueueing verify code for: %s", email)
	if err := s.EmailQueue.EnqueueVerifyCode(email, siteName, FirstEmailLocale(locale)); err != nil {
		s.Observer.Printf("service.auth", "[Auth] Failed to enqueue: %v", err)
		return nil, fmt.Errorf("enqueue verify code: %w", err)
	}

	s.Observer.Printf("service.auth", "[Auth] Verify code enqueued successfully for: %s", email)
	return &SendVerifyCodeResult{
		Countdown: 60, // Seconds.
	}, nil
}

// VerifyCaptchaForRegister validates the enabled CAPTCHA for registration.
// When email verification is enabled and a code is supplied, the code-request step
// has already checked the one-time CAPTCHA token, so registration does not consume it again.
func (s *AuthService) VerifyCaptchaForRegister(ctx context.Context, proof CaptchaProof, remoteIP, verifyCode string) error {
	if s.IsEmailVerifyEnabled(ctx) && strings.TrimSpace(verifyCode) != "" {
		s.Observer.Printf("service.auth", "%s", "[Auth] Email verify flow detected, skip duplicate captcha check on register")
		return nil
	}
	return s.VerifyCaptcha(ctx, proof, remoteIP)
}

func (s *AuthService) VerifyCaptcha(ctx context.Context, proof CaptchaProof, remoteIP string) error {
	required := s.Options != nil && s.Options.Server.Mode == "release" && s.Options.Turnstile.Required
	if s.Settings == nil {
		if required {
			return ErrTurnstileNotConfigured
		}
		return nil
	}

	providerConfig, err := s.Settings.GetCaptchaProviderConfig(ctx)
	if err != nil {
		s.Observer.Printf("service.auth", "%s", "[Auth] Failed to read captcha provider settings")
		return ErrServiceUnavailable
	}
	turnstileEnabled := providerConfig.TurnstileEnabled
	tencentEnabled := providerConfig.Tencent.Enabled
	aliyunEnabled := providerConfig.Aliyun.Enabled
	if AuthCaptchaProvidersConflict(turnstileEnabled, tencentEnabled, aliyunEnabled) {
		return ErrCaptchaProviderConflict
	}
	if tencentEnabled {
		if s.Tencent == nil {
			return ErrTencentCaptchaNotConfigured
		}
		return s.Tencent.VerifyTicketWithConfig(ctx, providerConfig.Tencent, proof.TencentTicket, proof.TencentRandstr, remoteIP)
	}
	if aliyunEnabled {
		if s.Aliyun == nil {
			return ErrAliyunCaptchaNotConfigured
		}
		return s.Aliyun.VerifyParamWithConfig(ctx, providerConfig.Aliyun, proof.TurnstileToken)
	}
	if turnstileEnabled {
		if s.Turnstile == nil || strings.TrimSpace(providerConfig.TurnstileSecretKey) == "" {
			return ErrTurnstileNotConfigured
		}
		return s.Turnstile.VerifyTokenWithSecret(ctx, providerConfig.TurnstileSecretKey, proof.TurnstileToken, remoteIP)
	}
	if required {
		return ErrTurnstileNotConfigured
	}
	return nil
}

// AuthCaptchaProvidersConflict reports whether more than one CAPTCHA provider is enabled.
func AuthCaptchaProvidersConflict(enabled ...bool) bool {
	count := 0
	for _, e := range enabled {
		if e {
			count++
		}
	}
	return count > 1
}

// VerifyActionCaptchaIfEnabled checks Tencent or Aliyun CAPTCHA for OAuth and Passkey login starts.
// Cloudflare Turnstile does not guard these action endpoints.
func (s *AuthService) VerifyActionCaptchaIfEnabled(ctx context.Context, proof CaptchaProof, remoteIP string) error {
	if s == nil || s.Settings == nil {
		return ErrServiceUnavailable
	}

	providerConfig, err := s.Settings.GetCaptchaProviderConfig(ctx)
	if err != nil {
		s.Observer.Printf("service.auth", "%s", "[Auth] Failed to read captcha provider settings")
		return ErrServiceUnavailable
	}
	tencentEnabled := providerConfig.Tencent.Enabled
	aliyunEnabled := providerConfig.Aliyun.Enabled
	if !tencentEnabled && !aliyunEnabled {
		return nil
	}
	if AuthCaptchaProvidersConflict(providerConfig.TurnstileEnabled, tencentEnabled, aliyunEnabled) {
		return ErrCaptchaProviderConflict
	}
	if aliyunEnabled {
		if s.Aliyun == nil {
			return ErrAliyunCaptchaNotConfigured
		}
		return s.Aliyun.VerifyParamWithConfig(ctx, providerConfig.Aliyun, proof.TurnstileToken)
	}
	if s.Tencent == nil {
		return ErrTencentCaptchaNotConfigured
	}
	return s.Tencent.VerifyTicketWithConfig(
		ctx,
		providerConfig.Tencent,
		proof.TencentTicket,
		proof.TencentRandstr,
		remoteIP,
	)
}

// IsTurnstileEnabled reports whether Turnstile verification is enabled.
func (s *AuthService) IsTurnstileEnabled(ctx context.Context) bool {
	if s.Turnstile == nil {
		return false
	}
	return s.Turnstile.IsEnabled(ctx)
}

// IsRegistrationEnabled reports whether registration is enabled.
func (s *AuthService) IsRegistrationEnabled(ctx context.Context) bool {
	if s.Settings == nil {
		return false // Missing settings disable registration.
	}
	return s.Settings.IsRegistrationEnabled(ctx)
}

// IsEmailVerifyEnabled reports whether registration requires email verification.
func (s *AuthService) IsEmailVerifyEnabled(ctx context.Context) bool {
	if s.Settings == nil {
		return false
	}
	return s.Settings.IsEmailVerifyEnabled(ctx)
}

// Login verifies password credentials and returns a JWT and the active user.
func (s *AuthService) Login(ctx context.Context, email, password string) (string, *User, error) {
	user, err := s.Users.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return "", nil, ErrInvalidCredentials
		}
		// Log database errors and return a service-unavailable error to the caller.
		s.Observer.Printf("service.auth", "[Auth] Database error during login: %v", err)
		return "", nil, ErrServiceUnavailable
	}

	if !s.CheckPassword(password, user.PasswordHash) {
		return "", nil, ErrInvalidCredentials
	}

	if !user.IsActive() {
		return "", nil, ErrUserNotActive
	}

	token, err := s.GenerateToken(ctx, user)
	if err != nil {
		return "", nil, fmt.Errorf("generate token: %w", err)
	}

	return token, user, nil
}

// AuthCanBypassRegistrationDisabledForOAuth permits DingTalk signup when internal_only
// and dingtalk_connect_bypass_registration are both enabled.
func (s *AuthService) AuthCanBypassRegistrationDisabledForOAuth(ctx context.Context, signupSource string) bool {
	if signupSource != "dingtalk" {
		return false
	}
	cfg, err := s.Settings.GetDingTalkConnectOAuthConfig(ctx)
	if err != nil || !cfg.Enabled || !cfg.BypassRegistration {
		return false
	}
	return cfg.CorpRestrictionPolicy == "internal_only"
}

// LoginOrRegisterOAuthWithTokenPair signs in an OAuth user and returns an access/refresh pair.
// New users are created when registration policy allows it.
// invitationCode and affiliateCode apply to new accounts.
// signupSource selects provider grants and the DingTalk registration exemption.
func (s *AuthService) LoginOrRegisterOAuthWithTokenPair(ctx context.Context, email, username, invitationCode, affiliateCode, signupSource string) (*TokenPair, *User, error) {
	return s.AuthLoginOrRegisterOAuthWithTokenPair(ctx, email, username, invitationCode, affiliateCode, "", signupSource)
}

// LoginOrRegisterOAuthWithTokenPairAndPromoCode signs in an OAuth user with an optional signup promotion.
// The promotion applies only when this call creates the account.
func (s *AuthService) LoginOrRegisterOAuthWithTokenPairAndPromoCode(ctx context.Context, email, username, invitationCode, affiliateCode, promoCode, signupSource string) (*TokenPair, *User, error) {
	return s.AuthLoginOrRegisterOAuthWithTokenPair(ctx, email, username, invitationCode, affiliateCode, promoCode, signupSource)
}

func (s *AuthService) AuthLoginOrRegisterOAuthWithTokenPair(ctx context.Context, email, username, invitationCode, affiliateCode, promoCode, signupSource string) (*TokenPair, *User, error) {
	if s.RefreshTokens == nil {
		return nil, nil, errors.New("refresh token cache not configured")
	}

	email = strings.TrimSpace(email)
	if email == "" || len(email) > 255 {
		return nil, nil, infraerrors.BadRequest("INVALID_EMAIL", "invalid email")
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, nil, infraerrors.BadRequest("INVALID_EMAIL", "invalid email")
	}

	username = strings.TrimSpace(username)
	if len([]rune(username)) > 100 {
		username = string([]rune(username)[:100])
	}

	user, err := s.Users.GetByEmail(ctx, email)
	created := false
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			// A missing OAuth account must satisfy registration policy.
			if s.Settings == nil || (!s.Settings.IsRegistrationEnabled(ctx) && !s.AuthCanBypassRegistrationDisabledForOAuth(ctx, signupSource)) {
				return nil, nil, ErrRegDisabled
			}

			artifacts, err := s.AuthResolveRegistrationArtifacts(ctx, invitationCode, ErrOAuthInvitationRequired)
			if err != nil {
				return nil, nil, err
			}

			randomPassword, err := RandomHexString(32)
			if err != nil {
				s.Observer.Printf("service.auth", "[Auth] Failed to generate random password for oauth signup: %v", err)
				return nil, nil, ErrServiceUnavailable
			}
			hashedPassword, err := s.HashPassword(randomPassword)
			if err != nil {
				return nil, nil, fmt.Errorf("hash password: %w", err)
			}

			// Use the supplied provider for grants, including users with real email addresses.
			// Infer the provider from the address only when signupSource is absent.
			if strings.TrimSpace(signupSource) == "" {
				signupSource = AuthInferLegacySignupSource(email)
			}
			grantPlan := s.AuthResolveSignupGrantPlan(ctx, signupSource)
			var defaultRPMLimit int
			if s.Settings != nil {
				defaultRPMLimit = s.Settings.GetDefaultUserRPMLimit(ctx)
			}

			newUser := &User{
				Email:        email,
				Username:     username,
				PasswordHash: hashedPassword,
				Role:         RoleUser,
				Balance:      grantPlan.Balance,
				Concurrency:  grantPlan.Concurrency,
				RPMLimit:     defaultRPMLimit,
				Status:       StatusActive,
				SignupSource: signupSource,
			}
			if err := s.AuthCreateRegisteredUser(ctx, newUser, artifacts); err != nil {
				if errors.Is(err, ErrEmailExists) {
					user, err = s.Users.GetByEmail(ctx, email)
					if err != nil {
						if errors.Is(err, ErrUserNotFound) {
							return nil, nil, ErrEmailExists
						}
						s.Observer.Printf("service.auth", "[Auth] Database error getting user after conflict: %v", err)
						return nil, nil, ErrServiceUnavailable
					}
				} else if errors.Is(err, ErrInvitationCodeInvalid) {
					return nil, nil, ErrInvitationCodeInvalid
				} else {
					s.Observer.Printf("service.auth", "[Auth] Database error creating oauth user: %v", err)
					return nil, nil, ErrServiceUnavailable
				}
			} else {
				user = newUser
				created = true
				s.AuthPostAuthUserBootstrap(ctx, user, signupSource, false)
				s.AuthAssignSubscriptions(ctx, user.ID, grantPlan.Subscriptions, "auto assigned by signup defaults")
				s.AuthBindRegistrationAffiliate(ctx, user.ID, affiliateCode)
			}
		} else {
			s.Observer.Printf("service.auth", "[Auth] Database error during oauth login: %v", err)
			return nil, nil, ErrServiceUnavailable
		}
	}

	if !user.IsActive() {
		return nil, nil, ErrUserNotActive
	}

	if user.Username == "" && username != "" {
		user.Username = username
		if err := s.Users.Update(ctx, user, UserUpdateFields{Username: true}); err != nil {
			s.Observer.Printf("service.auth", "[Auth] Failed to update username after oauth login: %v", err)
		}
	}
	if created {
		user = s.AuthApplyOAuthSignupPromoCode(ctx, user, promoCode)
	}
	tokenPair, err := s.GenerateTokenPair(ctx, user, "")
	if err != nil {
		return nil, nil, fmt.Errorf("generate token pair: %w", err)
	}
	return tokenPair, user, nil
}

func (s *AuthService) ApplyOAuthSignupPromoCode(ctx context.Context, userID int64, promoCode string) {
	if userID <= 0 {
		return
	}
	s.AuthApplyOAuthSignupPromoCode(ctx, &User{ID: userID}, promoCode)
}

func (s *AuthService) AuthApplyOAuthSignupPromoCode(ctx context.Context, user *User, promoCode string) *User {
	promoCode = strings.TrimSpace(promoCode)
	if user == nil || user.ID <= 0 || promoCode == "" || s.Promo == nil || s.Settings == nil || !s.Settings.IsPromoCodeEnabled(ctx) {
		return user
	}
	if err := s.Promo.ApplyPromoCode(ctx, user.ID, promoCode); err != nil {
		s.Observer.Printf("service.auth", "[Auth] Failed to apply promo code for oauth user %d: %v", user.ID, err)
		return user
	}
	if updatedUser, err := s.Users.GetByID(ctx, user.ID); err == nil {
		return updatedUser
	}
	return user
}

func (s *AuthService) AuthAssignSubscriptions(ctx context.Context, userID int64, items []DefaultSubscriptionSetting, notes string) {
	if s.Settings == nil || s.DefaultSubscriptions == nil || userID <= 0 {
		return
	}
	for _, item := range items {
		err := s.AuthRunFailOpenDBStep(ctx, "auth_default_subscription", func(stepCtx context.Context) error {
			_, _, err := s.DefaultSubscriptions.AssignOrExtendSubscription(stepCtx, &AssignSubscriptionInput{
				UserID: userID,
				PlanID: item.PlanID,
				Notes:  notes,
			})
			return err
		})
		if err != nil {
			s.Observer.Printf("service.auth", "[Auth] Failed to assign default subscription: user_id=%d plan_id=%d err=%v", userID, item.PlanID, err)
		}
	}
}

func (s *AuthService) AuthResolveRegistrationArtifacts(ctx context.Context, invitationCode string, missingInvitationErr error) (*AuthRegistrationArtifacts, error) {
	artifacts := &AuthRegistrationArtifacts{}

	if s.Settings != nil && s.Settings.IsInvitationCodeEnabled(ctx) {
		if invitationCode == "" {
			return nil, missingInvitationErr
		}
		redeemCode, err := s.Redeem.GetByCode(ctx, invitationCode)
		if err != nil {
			s.Observer.Printf("service.auth", "[Auth] Invalid invitation code: %s, error: %v", invitationCode, err)
			return nil, ErrInvitationCodeInvalid
		}
		if redeemCode.Type != RedeemTypeInvitation || !redeemCode.CanUse() {
			s.Observer.Printf("service.auth", "[Auth] Invitation code invalid: type=%s, status=%s", redeemCode.Type, redeemCode.Status)
			return nil, ErrInvitationCodeInvalid
		}
		artifacts.InvitationRedeemCode = redeemCode
	}

	return artifacts, nil
}

func (s *AuthService) AuthBindRegistrationAffiliate(ctx context.Context, userID int64, affiliateCode string) {
	if s == nil || s.Affiliate == nil || userID <= 0 {
		return
	}
	if err := s.AuthRunFailOpenDBStep(ctx, "auth_affiliate_profile", func(stepCtx context.Context) error {
		err := s.Affiliate.EnsureUserAffiliate(stepCtx, userID)
		return err
	}); err != nil {
		s.Observer.Printf("service.auth", "[Auth] Failed to initialize affiliate profile for user %d: %v", userID, err)
		return
	}
	if code := strings.TrimSpace(affiliateCode); code != "" {
		if err := s.AuthRunFailOpenDBStep(ctx, "auth_affiliate_bind_inviter", func(stepCtx context.Context) error {
			return s.Affiliate.BindInviterByCode(stepCtx, userID, code)
		}); err != nil {
			// Affiliate-binding failures are logged while registration continues.
			s.Observer.Printf("service.auth", "[Auth] Failed to bind affiliate inviter for user %d: %v", userID, err)
		}
	}
}

func (s *AuthService) AuthResolveSignupGrantPlan(ctx context.Context, signupSource string) AuthSignupGrantPlan {
	plan := AuthSignupGrantPlan{}
	if s != nil && s.Options != nil {
		plan.Balance = s.Options.Default.UserBalance
		plan.Concurrency = s.Options.Default.UserConcurrency
	}
	if s == nil || s.Settings == nil {
		return plan
	}

	plan.Balance = s.Settings.GetDefaultBalance(ctx)
	plan.Concurrency = s.Settings.GetDefaultConcurrency(ctx)
	plan.Subscriptions = s.Settings.GetDefaultSubscriptions(ctx)

	resolved, enabled, err := s.Settings.ResolveAuthSourceGrantSettings(ctx, signupSource, false)
	if err != nil {
		s.Observer.Printf("service.auth", "[Auth] Failed to load auth source signup defaults for %s: %v", signupSource, err)
		return plan
	}
	if !enabled {
		return plan
	}

	plan.Balance = resolved.Balance
	plan.Concurrency = resolved.Concurrency
	plan.Subscriptions = resolved.Subscriptions

	return plan
}

func AuthAuthSourceSignupSettings(defaults *AuthSourceDefaultSettings, signupSource string) (ProviderDefaultGrantSettings, bool) {
	if defaults == nil {
		return ProviderDefaultGrantSettings{}, false
	}

	switch strings.ToLower(strings.TrimSpace(signupSource)) {
	case "email":
		return defaults.Email, true
	case "linuxdo":
		return defaults.LinuxDo, true
	case "oidc":
		return defaults.OIDC, true
	case "wechat":
		return defaults.WeChat, true
	case "github":
		return defaults.GitHub, true
	case "google":
		return defaults.Google, true
	case "dingtalk":
		return defaults.DingTalk, true
	default:
		return ProviderDefaultGrantSettings{}, false
	}
}

func (s *AuthService) AuthPostAuthUserBootstrap(ctx context.Context, user *User, signupSource string, touchLogin bool) {
	if user == nil || user.ID <= 0 {
		return
	}

	if strings.TrimSpace(signupSource) == "" {
		signupSource = "email"
	}
	s.AuthUpdateUserSignupSource(ctx, user.ID, signupSource)

	if touchLogin {
		s.AuthTouchUserLogin(ctx, user.ID)
	}
}

func (s *AuthService) AuthBackfillEmailIdentityOnSuccessfulLogin(ctx context.Context, user *User) {
	if s == nil || user == nil || user.ID <= 0 {
		return
	}
	identity, created := s.AuthEnsureEmailAuthIdentity(ctx, user, "auth_service_login_backfill")
	if s.AuthShouldApplyEmailFirstBindDefaults(ctx, user.ID, identity, created) {
		if err := s.ApplyProviderDefaultSettingsOnFirstBind(ctx, user.ID, "email"); err != nil {
			s.Observer.Printf("service.auth", "[Auth] Failed to apply email first bind defaults: user_id=%d err=%v", user.ID, err)
		}
	}
}

func AuthEmailAuthIdentitySource(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	raw, ok := metadata["source"]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}

func AuthInferLegacySignupSource(email string) string {
	normalized := strings.ToLower(strings.TrimSpace(email))
	switch {
	case strings.HasSuffix(normalized, DingTalkConnectSyntheticEmailDomain):
		return "dingtalk"
	case strings.HasSuffix(normalized, LinuxDoConnectSyntheticEmailDomain):
		return "linuxdo"
	case strings.HasSuffix(normalized, OIDCConnectSyntheticEmailDomain):
		return "oidc"
	case strings.HasSuffix(normalized, WeChatConnectSyntheticEmailDomain):
		return "wechat"
	default:
		return "email"
	}
}

func (s *AuthService) AuthValidateRegistrationEmailPolicy(ctx context.Context, email string) error {
	if s.Settings == nil {
		return nil
	}
	whitelist := s.Settings.GetRegistrationEmailSuffixWhitelist(ctx)
	if !IsRegistrationEmailSuffixAllowed(email, whitelist) {
		return AuthBuildEmailSuffixNotAllowedError(whitelist)
	}
	return nil
}

// AuthValidateRegistrationEmailQuota allows any domain when the whitelist is empty.
// Otherwise, non-whitelisted domains need an enabled quota and share one slot per registrable domain.
func (s *AuthService) AuthValidateRegistrationEmailQuota(ctx context.Context, email string) error {
	if s == nil || s.Settings == nil {
		return nil
	}
	whitelist := s.Settings.GetRegistrationEmailSuffixWhitelist(ctx)
	if !IsRegistrationEmailSuffixLimited(email, whitelist) {
		return nil
	}
	if !s.Settings.IsRegistrationEmailDomainQuotaEnabled(ctx) {
		return AuthBuildEmailSuffixNotAllowedError(whitelist)
	}
	domain, err := s.AuthRegistrationEmailDomainLimit(ctx, email)
	if err != nil || domain == "" {
		return err
	}
	quotaRepo := s.DomainRegistration
	ok := quotaRepo != nil
	if !ok {
		// Database-backed registration requires atomic quota checks; database-free test stores may omit them.
		if s.HasDatabase() {
			return ErrServiceUnavailable
		}
		return nil
	}
	count, err := quotaRepo.CountUsersByEmailDomain(ctx, domain)
	if err != nil {
		s.Observer.Printf("service.auth", "[Auth] Failed to count registration email domain %s: %v", domain, err)
		return ErrServiceUnavailable
	}
	if count > 0 {
		return ErrEmailDomainRegistrationLimit
	}
	return nil
}

func (s *AuthService) AuthRegistrationEmailDomainLimit(ctx context.Context, email string) (string, error) {
	if s == nil || s.Settings == nil {
		return "", nil
	}
	whitelist := s.Settings.GetRegistrationEmailSuffixWhitelist(ctx)
	if !IsRegistrationEmailSuffixLimited(email, whitelist) {
		return "", nil
	}
	// Recheck inside the transaction in case the quota setting changed after preflight.
	if !s.Settings.IsRegistrationEmailDomainQuotaEnabled(ctx) {
		return "", AuthBuildEmailSuffixNotAllowedError(whitelist)
	}
	domain := RegistrationEmailDomain(email)
	if domain == "" {
		return "", AuthBuildEmailSuffixNotAllowedError(whitelist)
	}
	return domain, nil
}

func (s *AuthService) AuthRegistrationEmailExists(ctx context.Context, email string) (bool, error) {
	exists, err := s.Users.ExistsByEmail(ctx, email)
	if err != nil || exists {
		return exists, err
	}
	if s.Settings != nil && s.Settings.IsRegistrationEmailNormalizationEnabled(ctx) {
		normalizedEmail := NormalizeRegistrationEmailAddress(email)
		if normalizedEmail != "" {
			return s.Users.ExistsByNormalizedEmail(ctx, normalizedEmail)
		}
	}
	return false, nil
}

func AuthBuildEmailSuffixNotAllowedError(whitelist []string) error {
	if len(whitelist) == 0 {
		return ErrEmailSuffixNotAllowed
	}

	allowed := strings.Join(whitelist, ", ")
	return infraerrors.BadRequest(
		"EMAIL_SUFFIX_NOT_ALLOWED",
		fmt.Sprintf("email suffix is not allowed, allowed suffixes: %s", allowed),
	).WithMetadata(map[string]string{
		"allowed_suffixes":     strings.Join(whitelist, ","),
		"allowed_suffix_count": strconv.Itoa(len(whitelist)),
	})
}

// IsPasswordResetEnabled reports whether email verification and password reset are enabled.
func (s *AuthService) IsPasswordResetEnabled(ctx context.Context) bool {
	if s.Settings == nil {
		return false
	}

	if !s.Settings.IsEmailVerifyEnabled(ctx) {
		return false
	}
	return s.Settings.IsPasswordResetEnabled(ctx)
}

// AuthPreparePasswordReset returns the site name, reset URL, and whether to proceed.
// Callers return silent success when shouldProceed is false to limit account enumeration.
func (s *AuthService) AuthPreparePasswordReset(ctx context.Context, email, frontendBaseURL string) (string, string, bool) {
	user, err := s.Users.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			// Keep the missing-account result internal to limit enumeration.
			s.Observer.Printf("service.auth", "[Auth] Password reset requested for non-existent email: %s", email)
			return "", "", false
		}
		s.Observer.Printf("service.auth", "[Auth] Database error checking email for password reset: %v", err)
		return "", "", false
	}

	if !user.IsActive() {
		s.Observer.Printf("service.auth", "[Auth] Password reset requested for inactive user: %s", email)
		return "", "", false
	}

	siteName := "TokenRouter"
	if s.Settings != nil {
		siteName = s.Settings.GetSiteName(ctx)
	}

	resetURL := fmt.Sprintf("%s/reset-password", strings.TrimSuffix(frontendBaseURL, "/"))

	return siteName, resetURL, true
}

// RequestPasswordResetAsync queues a password-reset email.
// Security: Returns the same response regardless of whether the email exists (prevent user enumeration).
func (s *AuthService) RequestPasswordResetAsync(ctx context.Context, email, frontendBaseURL string, locale ...string) error {
	if !s.IsPasswordResetEnabled(ctx) {
		return infraerrors.Forbidden("PASSWORD_RESET_DISABLED", "password reset is not enabled")
	}
	if s.EmailQueue == nil {
		return ErrServiceUnavailable
	}

	siteName, resetURL, shouldProceed := s.AuthPreparePasswordReset(ctx, email, frontendBaseURL)
	if !shouldProceed {
		return nil // Silent success to prevent enumeration
	}

	if err := s.EmailQueue.EnqueuePasswordReset(email, siteName, resetURL, FirstEmailLocale(locale)); err != nil {
		s.Observer.Printf("service.auth", "[Auth] Failed to enqueue password reset email for %s: %v", email, err)
		return nil // Silent success to prevent enumeration
	}

	s.Observer.Printf("service.auth", "[Auth] Password reset email enqueued for: %s", email)
	return nil
}

// ResetPassword verifies the reset token, attempts to consume it, and changes the active user's password.
// Changing the stored password invalidates existing JWTs.
func (s *AuthService) ResetPassword(ctx context.Context, email, token, newPassword string) error {
	if !s.IsPasswordResetEnabled(ctx) {
		return infraerrors.Forbidden("PASSWORD_RESET_DISABLED", "password reset is not enabled")
	}

	if s.Email == nil {
		return ErrServiceUnavailable
	}

	// Verify the reset token before changing credentials.
	if err := s.Email.ConsumePasswordResetToken(ctx, email, token); err != nil {
		return err
	}

	user, err := s.Users.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return ErrInvalidResetToken // Token was valid but user was deleted
		}
		s.Observer.Printf("service.auth", "[Auth] Database error getting user for password reset: %v", err)
		return ErrServiceUnavailable
	}

	if !user.IsActive() {
		return ErrUserNotActive
	}

	hashedPassword, err := s.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	user.PasswordHash = hashedPassword
	user.TokenVersion++

	// TokenVersion has no database column: ResolvedTokenVersion derives it from email and password_hash.
	// Persisting password_hash changes the fingerprint used to validate existing tokens.
	if err := s.Users.Update(ctx, user, UserUpdateFields{PasswordHash: true}); err != nil {
		s.Observer.Printf("service.auth", "[Auth] Database error updating password for user %d: %v", user.ID, err)
		return ErrServiceUnavailable
	}

	// Revoke cached refresh sessions after persisting the password.
	if err := s.RevokeAllUserSessions(ctx, user.ID); err != nil {
		s.Observer.Printf("service.auth", "[Auth] Failed to revoke refresh tokens for user %d: %v", user.ID, err)
		// The password change has succeeded, so revocation errors are logged.
	}

	s.Observer.Printf("service.auth", "[Auth] Password reset successful for user: %s", email)
	return nil
}
