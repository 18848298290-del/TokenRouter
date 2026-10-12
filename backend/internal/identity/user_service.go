package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"log/slog"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/sync/singleflight"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

const (
	ProfileMaxNotifyEmails      = 3 // Maximum number of notification emails per user
	ProfileMaxInlineAvatarBytes = 100 * 1024
	ProfileTargetAvatarBytes    = 20 * 1024

	// User-level rate limiting for notify email verification codes.
	ProfileNotifyCodeUserRateLimit  = 5
	ProfileNotifyCodeUserRateWindow = 10 * time.Minute

	ProfileDefaultUserIdentityRedirect = "/settings/profile"
	ProfileUserLastActiveMinTouch      = 10 * time.Minute
	ProfileUserLastActiveFailBackoff   = 30 * time.Second

	ProfileUserIdentityNoteEmailManagedByBinding   = "profile.authBindings.notes.emailManagedByBinding"
	ProfileUserIdentityNoteCanUnbind               = "profile.authBindings.notes.canUnbind"
	ProfileUserIdentityNoteBindAnotherBeforeUnbind = "profile.authBindings.notes.bindAnotherBeforeUnbind"

	SettingKeyDingTalkConnectEnabled     = "dingtalk_connect_enabled"
	SettingKeyLinuxDoConnectEnabled      = "linuxdo_connect_enabled"
	SettingKeyOIDCConnectEnabled         = "oidc_connect_enabled"
	SettingKeySiteName                   = "site_name"
	SettingKeyWeChatConnectEnabled       = "wechat_connect_enabled"
	SettingKeyWeChatConnectMPEnabled     = "wechat_connect_mp_enabled"
	SettingKeyWeChatConnectMobileEnabled = "wechat_connect_mobile_enabled"
	SettingKeyWeChatConnectMode          = "wechat_connect_mode"
	SettingKeyWeChatConnectOpenEnabled   = "wechat_connect_open_enabled"
)

var (
	ProfileAvatarScaleSteps   = []float64{1, 0.92, 0.84, 0.76, 0.68, 0.6, 0.52, 0.44, 0.36}
	ProfileAvatarQualitySteps = []int{88, 80, 72, 64, 56, 48, 40, 32}

	ErrBalanceNegative             = billing.ErrBalanceNegative
	ErrInsufficientPerms           = infraerrors.Forbidden("INSUFFICIENT_PERMISSIONS", "insufficient permissions")
	ErrNotifyCodeUserRateLimit     = infraerrors.TooManyRequests("NOTIFY_CODE_USER_RATE_LIMIT", "too many verification codes requested, please try again later")
	ErrAvatarInvalid               = infraerrors.BadRequest("AVATAR_INVALID", "avatar must be a valid image data URL or http(s) URL")
	ErrAvatarTooLarge              = infraerrors.BadRequest("AVATAR_TOO_LARGE", "avatar image must be 100KB or smaller")
	ErrAvatarNotImage              = infraerrors.BadRequest("AVATAR_NOT_IMAGE", "avatar content must be an image")
	ErrProfileEmailChangeForbidden = infraerrors.BadRequest("EMAIL_PROFILE_UPDATE_FORBIDDEN", "email must be changed through verified email binding")
	ErrIdentityProviderInvalid     = infraerrors.BadRequest("IDENTITY_PROVIDER_INVALID", "identity provider is invalid")
	ErrIdentityRedirectInvalid     = infraerrors.BadRequest("IDENTITY_REDIRECT_INVALID", "identity redirect path is invalid")
	ErrUserAPIKeyLimitInvalid      = infraerrors.BadRequest("INVALID_API_KEY_LIMIT", fmt.Sprintf("api key limit must be between 0 and %d", MaxUserAPIKeyLimit))
	ErrIdentityUnbindLastMethod    = infraerrors.Conflict(
		"IDENTITY_UNBIND_LAST_METHOD",
		"bind another sign-in method before unbinding this provider",
	)
)

type ProfileUserProfileIdentityTxRunner interface {
	WithUserProfileIdentityTx(ctx context.Context, fn func(txCtx context.Context) error) error
}

// UserService manages user profiles, credentials, and account updates.
type UserService struct {
	operationClock
	runBackground        func(string, func()) bool
	userRepo             UserRepository
	settingRepo          ProfileSettings
	authCacheInvalidator UserAuthInvalidator
	billingCache         UserBalanceCache
	lastActiveTouchL1    sync.Map
	lastActiveTouchSF    singleflight.Group
}

// ProfileSettings reads runtime settings used by the profile page.
type ProfileSettings interface {
	GetMultiple(context.Context, []string) (map[string]string, error)
	GetValue(context.Context, string) (string, error)
}

type (
	UserAuthInvalidator interface{ InvalidateAuthCacheByUserID(context.Context, int64) }
	UserBalanceCache    interface {
		InvalidateUserBalance(context.Context, int64) error
	}
)

// NotifyVerificationNotice contains the recipient and verification code prepared for delivery.
type NotifyVerificationNotice struct {
	UserID                        int64
	Email, Code, Locale, SiteName string
}

type NotifyVerificationSender interface {
	GenerateVerifyCode() (string, error)
	SendNotifyVerification(context.Context, NotifyVerificationNotice) error
}

// IsValidUserAPIKeyLimit reports whether the limit fits the supported database range.
func IsValidUserAPIKeyLimit(limit int) bool {
	return limit >= 0 && limit <= MaxUserAPIKeyLimit
}

func NewUserService(users UserRepository, settings ProfileSettings, auth UserAuthInvalidator, balance UserBalanceCache, background func(string, func()) bool, clocks ...func() time.Time) *UserService {
	if background == nil {
		background = func(_ string, fn func()) bool { fn(); return true }
	}
	return &UserService{operationClock: clockFromOptional(clocks), userRepo: users, settingRepo: settings, authCacheInvalidator: auth, billingCache: balance, runBackground: background}
}

// GetFirstAdmin returns the first administrator for Admin API Key authentication.
func (s *UserService) GetFirstAdmin(ctx context.Context) (*User, error) {
	admin, err := s.userRepo.GetFirstAdmin(ctx)
	if err != nil {
		return nil, fmt.Errorf("get first admin: %w", err)
	}
	return admin, nil
}

// GetProfile loads the user, credential fingerprint, and avatar.
func (s *UserService) GetProfile(ctx context.Context, userID int64) (*User, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	ProfileNormalizeLoadedUserTokenVersion(user)
	if err := s.ProfileHydrateUserAvatar(ctx, user); err != nil {
		return nil, fmt.Errorf("get user avatar: %w", err)
	}
	return user, nil
}

func (s *UserService) GetProfileIdentitySummaries(ctx context.Context, userID int64, user *User) (UserIdentitySummarySet, error) {
	if user == nil {
		var err error
		user, err = s.userRepo.GetByID(ctx, userID)
		if err != nil {
			return UserIdentitySummarySet{}, fmt.Errorf("get user: %w", err)
		}
	}

	records, err := s.ProfileListUserAuthIdentities(ctx, userID)
	if err != nil {
		return UserIdentitySummarySet{}, err
	}

	summaries := UserIdentitySummarySet{
		Email:    s.ProfileBuildEmailIdentitySummary(user, records),
		LinuxDo:  s.ProfileBuildProviderIdentitySummary("linuxdo", user, records),
		OIDC:     s.ProfileBuildProviderIdentitySummary("oidc", user, records),
		WeChat:   s.ProfileBuildProviderIdentitySummary("wechat", user, records),
		DingTalk: s.ProfileBuildProviderIdentitySummary("dingtalk", user, records),
	}

	s.ProfileApplyExplicitProviderAvailability(ctx, &summaries)
	return summaries, nil
}

func (s *UserService) ProfileApplyExplicitProviderAvailability(ctx context.Context, summaries *UserIdentitySummarySet) {
	if s == nil || summaries == nil || s.settingRepo == nil {
		return
	}

	settings, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyLinuxDoConnectEnabled,
		SettingKeyOIDCConnectEnabled,
		SettingKeyWeChatConnectEnabled,
		SettingKeyWeChatConnectOpenEnabled,
		SettingKeyWeChatConnectMPEnabled,
		SettingKeyWeChatConnectMobileEnabled,
		SettingKeyWeChatConnectMode,
		SettingKeyDingTalkConnectEnabled,
	})
	if err != nil {
		return
	}

	if raw, ok := settings[SettingKeyLinuxDoConnectEnabled]; ok && strings.TrimSpace(raw) != "" && raw != "true" {
		ProfileDisableIdentityBindAction(&summaries.LinuxDo)
	}
	if raw, ok := settings[SettingKeyDingTalkConnectEnabled]; ok && strings.TrimSpace(raw) != "" && raw != "true" {
		ProfileDisableIdentityBindAction(&summaries.DingTalk)
	}
	if raw, ok := settings[SettingKeyOIDCConnectEnabled]; ok && strings.TrimSpace(raw) != "" && raw != "true" {
		ProfileDisableIdentityBindAction(&summaries.OIDC)
	}
	if raw, ok := settings[SettingKeyWeChatConnectEnabled]; ok && strings.TrimSpace(raw) != "" {
		if raw != "true" {
			ProfileDisableIdentityBindAction(&summaries.WeChat)
			return
		}
		openEnabled, mpEnabled, _ := ParseWeChatConnectCapabilitySettings(settings, true, settings[SettingKeyWeChatConnectMode])
		if !openEnabled && !mpEnabled {
			ProfileDisableIdentityBindAction(&summaries.WeChat)
		}
	}
}

func ProfileDisableIdentityBindAction(summary *UserIdentitySummary) {
	if summary == nil || summary.Bound {
		return
	}
	summary.CanBind = false
	summary.BindStartPath = ""
}

func (s *UserService) PrepareIdentityBindingStart(_ context.Context, req StartUserIdentityBindingRequest) (*StartUserIdentityBindingResult, error) {
	provider := ProfileNormalizeUserIdentityProvider(req.Provider)
	if provider == "" {
		return nil, ErrIdentityProviderInvalid
	}

	authorizeURL, err := ProfileBuildUserIdentityBindAuthorizeURL(provider, req.RedirectTo)
	if err != nil {
		return nil, err
	}

	return &StartUserIdentityBindingResult{
		Provider:           provider,
		AuthorizeURL:       authorizeURL,
		Method:             "GET",
		UseBrowserRedirect: true,
	}, nil
}

func (s *UserService) UnbindUserAuthProvider(ctx context.Context, userID int64, provider string) (*User, error) {
	user, _, err := s.UnbindUserAuthProviderWithResult(ctx, userID, provider)
	return user, err
}

func (s *UserService) UnbindUserAuthProviderWithResult(ctx context.Context, userID int64, provider string) (*User, bool, error) {
	provider = ProfileNormalizeUserIdentityProvider(provider)
	if provider == "" || provider == "email" {
		return nil, false, ErrIdentityProviderInvalid
	}

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, false, fmt.Errorf("get user: %w", err)
	}

	records, err := s.ProfileListUserAuthIdentities(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	if len(ProfileFilterUserAuthIdentities(records, provider)) == 0 {
		return user, false, nil
	}
	if !s.ProfileCanUnbindProvider(provider, user, records) {
		return nil, false, ErrIdentityUnbindLastMethod
	}

	if err := s.userRepo.UnbindUserAuthProvider(ctx, userID, provider); err != nil {
		return nil, false, err
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}

	updatedUser, err := s.GetProfile(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	return updatedUser, true, nil
}

// UpdateProfile updates the requested profile fields and invalidates changed concurrency data.
func (s *UserService) UpdateProfile(ctx context.Context, userID int64, req UpdateProfileRequest) (*User, error) {
	if txRunner, ok := s.userRepo.(ProfileUserProfileIdentityTxRunner); ok {
		var (
			updated        *User
			oldConcurrency int
		)
		if err := txRunner.WithUserProfileIdentityTx(ctx, func(txCtx context.Context) error {
			var err error
			updated, oldConcurrency, err = s.ProfileUpdateProfile(txCtx, userID, req)
			return err
		}); err != nil {
			return nil, err
		}
		if s.authCacheInvalidator != nil && updated != nil && updated.Concurrency != oldConcurrency {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
		}
		return updated, nil
	}

	updated, oldConcurrency, err := s.ProfileUpdateProfile(ctx, userID, req)
	if err != nil {
		return nil, err
	}
	if s.authCacheInvalidator != nil && updated.Concurrency != oldConcurrency {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	return updated, nil
}

func (s *UserService) ProfileUpdateProfile(ctx context.Context, userID int64, req UpdateProfileRequest) (*User, int, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, 0, fmt.Errorf("get user: %w", err)
	}
	oldConcurrency := user.Concurrency

	// Declare requested fields so a stale user snapshot cannot overwrite concurrent updates.
	var fields UserUpdateFields
	if req.ClearPreferredLocale {
		user.PreferredLocale = nil
		fields.PreferredLocale = true
	}
	if req.PreferredLocale != nil {
		code := locale.Normalize(*req.PreferredLocale)
		if code == "" {
			return nil, oldConcurrency, infraerrors.BadRequest("INVALID_LOCALE", "Language is not supported.")
		}
		user.PreferredLocale = &code
		fields.PreferredLocale = true
	}

	// The login email must be changed through verified identity binding.
	if req.Email != nil {
		return nil, oldConcurrency, ErrProfileEmailChangeForbidden
	}

	if req.Username != nil {
		user.Username = *req.Username
		fields.Username = true
	}

	if req.AvatarURL != nil {
		avatar, err := s.SetAvatar(ctx, userID, *req.AvatarURL)
		if err != nil {
			return nil, oldConcurrency, err
		}
		ProfileApplyUserAvatar(user, avatar)
	}

	if req.Concurrency != nil {
		user.Concurrency = *req.Concurrency
		fields.Concurrency = true
	}

	if req.BalanceNotifyEnabled != nil {
		user.BalanceNotifyEnabled = *req.BalanceNotifyEnabled
		fields.BalanceNotifySettings = true
	}
	if req.BalanceNotifyThreshold != nil {
		if *req.BalanceNotifyThreshold <= 0 {
			user.BalanceNotifyThreshold = nil // clear to system default
		} else {
			user.BalanceNotifyThreshold = req.BalanceNotifyThreshold
		}
		fields.BalanceNotifySettings = true
	}

	if err := s.userRepo.Update(ctx, user, fields); err != nil {
		return nil, oldConcurrency, fmt.Errorf("update user: %w", err)
	}

	return user, oldConcurrency, nil
}

func (s *UserService) SetAvatar(ctx context.Context, userID int64, raw string) (*UserAvatar, error) {
	avatarValue := strings.TrimSpace(raw)
	if avatarValue == "" {
		if err := s.userRepo.DeleteUserAvatar(ctx, userID); err != nil {
			return nil, fmt.Errorf("delete avatar: %w", err)
		}
		return nil, nil
	}

	avatarInput, err := ProfileNormalizeUserAvatarInput(avatarValue)
	if err != nil {
		return nil, err
	}

	avatar, err := s.userRepo.UpsertUserAvatar(ctx, userID, avatarInput)
	if err != nil {
		return nil, fmt.Errorf("upsert avatar: %w", err)
	}
	return avatar, nil
}

func ProfileApplyUserAvatar(user *User, avatar *UserAvatar) {
	if user == nil {
		return
	}
	if avatar == nil {
		user.AvatarURL = ""
		user.AvatarSource = ""
		user.AvatarMIME = ""
		user.AvatarByteSize = 0
		user.AvatarSHA256 = ""
		return
	}

	user.AvatarURL = avatar.URL
	user.AvatarSource = avatar.StorageProvider
	user.AvatarMIME = avatar.ContentType
	user.AvatarByteSize = avatar.ByteSize
	user.AvatarSHA256 = avatar.SHA256
}

func ProfileNormalizeUserAvatarInput(raw string) (UpsertUserAvatarInput, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return UpsertUserAvatarInput{}, ErrAvatarInvalid
	}
	if strings.HasPrefix(raw, "data:") {
		return ProfileNormalizeInlineUserAvatarInput(raw)
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return UpsertUserAvatarInput{}, ErrAvatarInvalid
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return UpsertUserAvatarInput{}, ErrAvatarInvalid
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return UpsertUserAvatarInput{}, ErrAvatarInvalid
	}

	return UpsertUserAvatarInput{
		StorageProvider: "remote_url",
		URL:             raw,
	}, nil
}

func ValidateUserAvatar(raw string) error {
	_, err := ProfileNormalizeUserAvatarInput(raw)
	return err
}

func ProfileNormalizeInlineUserAvatarInput(raw string) (UpsertUserAvatarInput, error) {
	body := strings.TrimPrefix(raw, "data:")
	meta, encoded, ok := strings.Cut(body, ",")
	if !ok {
		return UpsertUserAvatarInput{}, ErrAvatarInvalid
	}
	meta = strings.TrimSpace(meta)
	encoded = strings.TrimSpace(encoded)
	if !strings.HasSuffix(strings.ToLower(meta), ";base64") {
		return UpsertUserAvatarInput{}, ErrAvatarInvalid
	}

	contentType := strings.TrimSpace(meta[:len(meta)-len(";base64")])
	if contentType == "" || !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return UpsertUserAvatarInput{}, ErrAvatarNotImage
	}

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return UpsertUserAvatarInput{}, ErrAvatarInvalid
	}
	if len(decoded) > ProfileMaxInlineAvatarBytes {
		return UpsertUserAvatarInput{}, ErrAvatarTooLarge
	}

	if len(decoded) > ProfileTargetAvatarBytes {
		decoded, contentType, err = ProfileCompressInlineAvatar(decoded)
		if err != nil {
			return UpsertUserAvatarInput{}, err
		}
		raw = "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(decoded)
	}

	sum := sha256.Sum256(decoded)
	return UpsertUserAvatarInput{
		StorageProvider: "inline",
		URL:             raw,
		ContentType:     contentType,
		ByteSize:        len(decoded),
		SHA256:          hex.EncodeToString(sum[:]),
	}, nil
}

func ProfileCompressInlineAvatar(decoded []byte) ([]byte, string, error) {
	src, _, err := image.Decode(bytes.NewReader(decoded))
	if err != nil {
		return nil, "", ErrAvatarInvalid
	}

	srcBounds := src.Bounds()
	if srcBounds.Empty() {
		return nil, "", ErrAvatarInvalid
	}

	for _, scale := range ProfileAvatarScaleSteps {
		width := max(1, int(float64(srcBounds.Dx())*scale))
		height := max(1, int(float64(srcBounds.Dy())*scale))
		dst := image.NewRGBA(image.Rect(0, 0, width, height))
		stddraw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, stddraw.Src)
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, srcBounds, stddraw.Over, nil)

		for _, quality := range ProfileAvatarQualitySteps {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
				return nil, "", ErrAvatarInvalid
			}
			if buf.Len() <= ProfileTargetAvatarBytes {
				return buf.Bytes(), "image/jpeg", nil
			}
		}
	}

	return nil, "", ErrAvatarTooLarge
}

func (s *UserService) ProfileBuildEmailIdentitySummary(user *User, records []UserAuthIdentityRecord) UserIdentitySummary {
	summary := UserIdentitySummary{
		Provider:  "email",
		CanBind:   false,
		CanUnbind: false,
		NoteKey:   ProfileUserIdentityNoteEmailManagedByBinding,
		Note:      "Primary account email is managed through verified email binding.",
	}
	if user == nil {
		return summary
	}

	filtered := ProfileFilterUserAuthIdentities(records, "email")
	if len(filtered) > 0 {
		primary := ProfileSelectPrimaryUserAuthIdentity(filtered)
		email := strings.TrimSpace(ProfileFirstStringIdentityValue(primary.Metadata, "email"))
		if email == "" {
			email = strings.TrimSpace(primary.ProviderSubject)
		}
		if email == "" || IsReservedEmail(email) {
			email = strings.TrimSpace(user.Email)
		}
		if email == "" || IsReservedEmail(email) {
			email = strings.TrimSpace(primary.ProviderKey)
		}

		summary.Bound = true
		summary.BoundCount = len(filtered)
		summary.DisplayName = email
		summary.SubjectHint = ProfileMaskEmailIdentity(email)
		summary.ProviderKey = strings.TrimSpace(primary.ProviderKey)
		summary.VerifiedAt = primary.VerifiedAt
		return summary
	}

	// Compatibility fallback for legacy normal-email users that predate auth_identities backfill.
	email := strings.TrimSpace(user.Email)
	if email == "" || IsReservedEmail(email) {
		return summary
	}
	summary.Bound = true
	summary.BoundCount = 1
	summary.DisplayName = email
	summary.SubjectHint = ProfileMaskEmailIdentity(email)
	summary.ProviderKey = "email"
	return summary
}

func (s *UserService) ProfileBuildProviderIdentitySummary(provider string, user *User, records []UserAuthIdentityRecord) UserIdentitySummary {
	summary := UserIdentitySummary{
		Provider:  provider,
		CanUnbind: false,
	}
	filtered := ProfileFilterUserAuthIdentities(records, provider)
	if len(filtered) == 0 {
		summary.CanBind = true
		bindStartPath, err := ProfileBuildUserIdentityBindAuthorizeURL(provider, "")
		if err == nil {
			summary.BindStartPath = bindStartPath
		}
		return summary
	}

	primary := ProfileSelectPrimaryUserAuthIdentity(filtered)
	summary.Bound = true
	summary.BoundCount = len(filtered)
	summary.DisplayName = ProfileUserAuthIdentityDisplayName(primary)
	summary.AvatarURL = strings.TrimSpace(ProfileFirstStringIdentityValue(primary.Metadata, "avatar_url", "suggested_avatar_url", "headimgurl"))
	summary.SubjectHint = ProfileMaskOpaqueIdentity(primary.ProviderSubject)
	summary.ProviderKey = strings.TrimSpace(primary.ProviderKey)
	summary.VerifiedAt = primary.VerifiedAt
	summary.CanUnbind = s.ProfileCanUnbindProvider(provider, user, records)
	if summary.CanUnbind {
		summary.NoteKey = ProfileUserIdentityNoteCanUnbind
		summary.Note = "You can unbind this sign-in method."
	} else {
		summary.NoteKey = ProfileUserIdentityNoteBindAnotherBeforeUnbind
		summary.Note = "Bind another sign-in method before unbinding."
	}
	return summary
}

func (s *UserService) ProfileCanUnbindProvider(provider string, user *User, records []UserAuthIdentityRecord) bool {
	if provider == "" || provider == "email" || len(ProfileFilterUserAuthIdentities(records, provider)) == 0 {
		return false
	}

	if s.ProfileCanUseEmailAsSignInMethod(user, records) {
		return true
	}

	for _, candidate := range []string{"linuxdo", "oidc", "wechat", "dingtalk"} {
		if candidate == provider {
			continue
		}
		if len(ProfileFilterUserAuthIdentities(records, candidate)) > 0 {
			return true
		}
	}

	return false
}

func (s *UserService) ProfileCanUseEmailAsSignInMethod(user *User, records []UserAuthIdentityRecord) bool {
	if user == nil {
		return false
	}

	email := strings.ToLower(strings.TrimSpace(user.Email))
	if email == "" || IsReservedEmail(email) {
		return false
	}

	if ProfileEmailSignupSourceAllowsLogin(user.SignupSource) {
		return true
	}

	return slices.ContainsFunc(ProfileFilterUserAuthIdentities(records, "email"), ProfileEmailIdentitySupportsSignIn)
}

func ProfileEmailSignupSourceAllowsLogin(signupSource string) bool {
	signupSource = strings.ToLower(strings.TrimSpace(signupSource))
	return signupSource == "" || signupSource == "email"
}

func ProfileEmailIdentitySupportsSignIn(record UserAuthIdentityRecord) bool {
	source := strings.TrimSpace(ProfileFirstStringIdentityValue(record.Metadata, "source"))
	switch source {
	case "auth_service_email_bind", "auth_service_login_backfill", "auth_service_dual_write":
		return true
	default:
		return false
	}
}

func (s *UserService) ProfileListUserAuthIdentities(ctx context.Context, userID int64) ([]UserAuthIdentityRecord, error) {
	if userID <= 0 || s == nil || s.userRepo == nil {
		return nil, nil
	}
	return s.userRepo.ListUserAuthIdentities(ctx, userID)
}

func ProfileBuildUserIdentityBindAuthorizeURL(provider, redirectTo string) (string, error) {
	provider = ProfileNormalizeUserIdentityProvider(provider)
	if provider == "" || provider == "email" {
		return "", ErrIdentityProviderInvalid
	}

	redirectTo, err := ProfileNormalizeUserIdentityRedirect(redirectTo)
	if err != nil {
		return "", err
	}

	path := ""
	switch provider {
	case "linuxdo":
		path = "/api/v1/auth/oauth/linuxdo/bind/start"
	case "oidc":
		path = "/api/v1/auth/oauth/oidc/bind/start"
	case "wechat":
		path = "/api/v1/auth/oauth/wechat/bind/start"
	case "dingtalk":
		path = "/api/v1/auth/oauth/dingtalk/bind/start"
	default:
		return "", ErrIdentityProviderInvalid
	}

	query := url.Values{}
	query.Set("redirect", redirectTo)
	query.Set("intent", "bind_current_user")
	return path + "?" + query.Encode(), nil
}

func ProfileNormalizeUserIdentityProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "linuxdo":
		return "linuxdo"
	case "oidc":
		return "oidc"
	case "wechat":
		return "wechat"
	case "dingtalk":
		return "dingtalk"
	case "email":
		return "email"
	default:
		return ""
	}
}

func ProfileNormalizeUserIdentityRedirect(raw string) (string, error) {
	redirect := strings.TrimSpace(raw)
	if redirect == "" {
		return ProfileDefaultUserIdentityRedirect, nil
	}
	if len(redirect) > 2048 || !strings.HasPrefix(redirect, "/") || strings.HasPrefix(redirect, "//") {
		return "", ErrIdentityRedirectInvalid
	}
	return redirect, nil
}

func ProfileFilterUserAuthIdentities(records []UserAuthIdentityRecord, provider string) []UserAuthIdentityRecord {
	if len(records) == 0 {
		return nil
	}
	filtered := make([]UserAuthIdentityRecord, 0, len(records))
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.ProviderType), provider) {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func ProfileSelectPrimaryUserAuthIdentity(records []UserAuthIdentityRecord) UserAuthIdentityRecord {
	if len(records) == 0 {
		return UserAuthIdentityRecord{}
	}
	sort.SliceStable(records, func(i, j int) bool {
		left := ProfileUserAuthIdentitySortTime(records[i])
		right := ProfileUserAuthIdentitySortTime(records[j])
		if !left.Equal(right) {
			return left.After(right)
		}
		return records[i].ProviderKey < records[j].ProviderKey
	})
	return records[0]
}

func ProfileUserAuthIdentitySortTime(record UserAuthIdentityRecord) time.Time {
	if record.VerifiedAt != nil && !record.VerifiedAt.IsZero() {
		return record.VerifiedAt.UTC()
	}
	if !record.UpdatedAt.IsZero() {
		return record.UpdatedAt.UTC()
	}
	if !record.CreatedAt.IsZero() {
		return record.CreatedAt.UTC()
	}
	return time.Time{}
}

func ProfileUserAuthIdentityDisplayName(record UserAuthIdentityRecord) string {
	if displayName := ProfileFirstStringIdentityValue(record.Metadata,
		"display_name",
		"suggested_display_name",
		"username",
		"name",
		"nickname",
		"email",
	); displayName != "" {
		return displayName
	}
	if subject := strings.TrimSpace(record.ProviderSubject); subject != "" {
		return subject
	}
	return strings.TrimSpace(record.ProviderType)
}

func ProfileFirstStringIdentityValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		case fmt.Stringer:
			if trimmed := strings.TrimSpace(value.String()); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func ProfileMaskEmailIdentity(email string) string {
	local, domain, ok := strings.Cut(strings.TrimSpace(email), "@")
	if !ok || local == "" || domain == "" {
		return ProfileMaskOpaqueIdentity(email)
	}
	runes := []rune(local)
	if len(runes) == 1 {
		return string(runes[0]) + "***@" + domain
	}
	return string(runes[0]) + "***" + string(runes[len(runes)-1]) + "@" + domain
}

func ProfileMaskOpaqueIdentity(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	switch {
	case len(runes) == 0:
		return ""
	case len(runes) <= 4:
		return string(runes[0]) + "***"
	case len(runes) <= 8:
		return string(runes[:2]) + "***" + string(runes[len(runes)-1:])
	default:
		return string(runes[:3]) + "***" + string(runes[len(runes)-3:])
	}
}

// ChangePassword verifies the current password and persists the new password hash.
// The changed credential fingerprint invalidates previously issued tokens.
func (s *UserService) ChangePassword(ctx context.Context, userID int64, req ChangePasswordRequest) error {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}

	if !user.CheckPassword(req.CurrentPassword) {
		return ErrPasswordIncorrect
	}

	if err := user.SetPassword(req.NewPassword); err != nil {
		return fmt.Errorf("set password: %w", err)
	}

	// Update the in-memory version; persistence below changes the credential fingerprint.
	user.TokenVersion++

	// TokenVersion has no database column. ResolvedTokenVersion derives it from
	// email and password_hash, so persisting the new hash invalidates old tokens.
	if err := s.userRepo.Update(ctx, user, UserUpdateFields{PasswordHash: true}); err != nil {
		return fmt.Errorf("update user: %w", err)
	}

	return nil
}

// GetByID loads a user with the credential fingerprint and avatar for administrative use.
func (s *UserService) GetByID(ctx context.Context, id int64) (*User, error) {
	user, err := s.userRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	ProfileNormalizeLoadedUserTokenVersion(user)
	if err := s.ProfileHydrateUserAvatar(ctx, user); err != nil {
		return nil, fmt.Errorf("get user avatar: %w", err)
	}
	return user, nil
}

func ProfileNormalizeLoadedUserTokenVersion(user *User) {
	if user == nil || user.TokenVersionResolved {
		return
	}
	user.TokenVersion = ResolvedTokenVersion(user)
	user.TokenVersionResolved = true
}

// TouchLastActive throttles writes to users.last_active_at on authentication requests.
// Load and write failures are logged and do not fail the request.
func (s *UserService) TouchLastActive(ctx context.Context, userID int64) {
	if s == nil || s.userRepo == nil || userID <= 0 {
		return
	}

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		slog.Debug("skip touch user last active after load failure", "user_id", userID, "error", err)
		return
	}
	s.TouchLastActiveForUser(ctx, user)
}

// TouchLastActiveForUser uses an already loaded user to avoid another database read.
func (s *UserService) TouchLastActiveForUser(ctx context.Context, user *User) {
	if s == nil || s.userRepo == nil || user == nil || user.ID <= 0 {
		return
	}

	now := s.now()
	if ProfileUserLastActiveFresh(user.LastActiveAt, now) {
		return
	}
	if v, ok := s.lastActiveTouchL1.Load(user.ID); ok {
		if nextAllowedAt, ok := v.(time.Time); ok && now.Before(nextAllowedAt) {
			return
		}
	}

	_, err, _ := s.lastActiveTouchSF.Do(strconv.FormatInt(user.ID, 10), func() (any, error) {
		latest := s.now()
		if v, ok := s.lastActiveTouchL1.Load(user.ID); ok {
			if nextAllowedAt, ok := v.(time.Time); ok && latest.Before(nextAllowedAt) {
				return nil, nil
			}
		}
		if ProfileUserLastActiveFresh(user.LastActiveAt, latest) {
			return nil, nil
		}
		if err := s.userRepo.UpdateUserLastActiveAt(ctx, user.ID, latest); err != nil {
			s.lastActiveTouchL1.Store(user.ID, latest.Add(ProfileUserLastActiveFailBackoff))
			return nil, fmt.Errorf("touch user last active: %w", err)
		}
		s.lastActiveTouchL1.Store(user.ID, latest.Add(ProfileUserLastActiveMinTouch))
		return nil, nil
	})
	if err != nil {
		slog.Warn("touch user last active failed", "user_id", user.ID, "error", err)
	}
}

func ProfileUserLastActiveFresh(lastActiveAt *time.Time, now time.Time) bool {
	if lastActiveAt == nil {
		return false
	}
	return now.Before(lastActiveAt.Add(ProfileUserLastActiveMinTouch))
}

func (s *UserService) ProfileHydrateUserAvatar(ctx context.Context, user *User) error {
	if s == nil || s.userRepo == nil || user == nil || user.ID == 0 {
		return nil
	}

	avatar, err := s.userRepo.GetUserAvatar(ctx, user.ID)
	if err != nil {
		return err
	}
	ProfileApplyUserAvatar(user, avatar)
	return nil
}

// List returns a page of users for administrative use.
func (s *UserService) List(ctx context.Context, params pagination.PaginationParams) ([]User, *pagination.PaginationResult, error) {
	users, pagination, err := s.userRepo.List(ctx, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list users: %w", err)
	}
	return users, pagination, nil
}

// UpdateBalance updates the balance and invalidates authentication and billing caches.
func (s *UserService) UpdateBalance(ctx context.Context, userID int64, amount float64) error {
	if err := s.userRepo.UpdateBalance(ctx, userID, amount); err != nil {
		return fmt.Errorf("update balance: %w", err)
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if s.billingCache != nil {
		s.runBackground("service/user_service.go:UpdateBalance", func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic in balance cache invalidation", "user_id", userID, "recover", r)
				}
			}()
			cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.billingCache.InvalidateUserBalance(cacheCtx, userID); err != nil {
				slog.Error("invalidate user balance cache failed", "user_id", userID, "error", err)
			}
		})
	}
	return nil
}

// UpdateConcurrency updates the concurrency limit and invalidates authentication caches.
func (s *UserService) UpdateConcurrency(ctx context.Context, userID int64, concurrency int) error {
	if err := s.userRepo.UpdateConcurrency(ctx, userID, concurrency); err != nil {
		return fmt.Errorf("update concurrency: %w", err)
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	return nil
}

// UpdateStatus updates account status and invalidates authentication caches.
func (s *UserService) UpdateStatus(ctx context.Context, userID int64, status string) error {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}

	user.Status = status

	if err := s.userRepo.Update(ctx, user, UserUpdateFields{Status: true}); err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}

	return nil
}

// Delete invalidates authentication caches before deleting the user.
func (s *UserService) Delete(ctx context.Context, userID int64) error {
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if err := s.userRepo.Delete(ctx, userID); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

// SendNotifyEmailCode sends a verification code to the extra notification email.
func (s *UserService) SendNotifyEmailCode(ctx context.Context, userID int64, email string, emailService NotifyVerificationSender, cache EmailCache, locale ...string) error {
	if err := ProfileCheckNotifyCodeRateLimitWithClock(ctx, cache, userID, email, s.now); err != nil {
		return err
	}

	code, err := emailService.GenerateVerifyCode()
	if err != nil {
		return fmt.Errorf("generate code: %w", err)
	}

	// Send before caching or incrementing counters so failed delivery does not
	// consume the recipient's cooldown or user-level allowance.
	if err := s.SendNotifyVerifyEmail(ctx, emailService, userID, email, code, FirstEmailLocale(locale)); err != nil {
		return err
	}

	if err := ProfileSaveNotifyVerifyCodeWithClock(ctx, cache, email, code, s.now); err != nil {
		return err
	}

	// Increment user-level counter after successful save
	if _, err := cache.IncrNotifyCodeUserRate(ctx, userID, ProfileNotifyCodeUserRateWindow); err != nil {
		slog.Error("failed to increment notify code user rate", "user_id", userID, "error", err)
	}

	return nil
}

// ProfileCheckNotifyCodeRateLimitWithClock checks email cooldown and per-user limits using now.
func ProfileCheckNotifyCodeRateLimitWithClock(ctx context.Context, cache EmailCache, userID int64, email string, now func() time.Time) error {
	existing, err := cache.GetNotifyVerifyCode(ctx, email)
	if err == nil && existing != nil {
		if now().Sub(existing.CreatedAt) < VerifyCodeCooldown {
			return ErrVerifyCodeTooFrequent
		}
	}
	count, err := cache.GetNotifyCodeUserRate(ctx, userID)
	if err == nil && count >= ProfileNotifyCodeUserRateLimit {
		return ErrNotifyCodeUserRateLimit
	}
	return nil
}

// ProfileSaveNotifyVerifyCodeWithClock stores a code with timestamps from now.
func ProfileSaveNotifyVerifyCodeWithClock(ctx context.Context, cache EmailCache, email, code string, now func() time.Time) error {
	data := &VerificationCodeData{
		Code:      code,
		Attempts:  0,
		CreatedAt: now(),
		ExpiresAt: now().Add(VerifyCodeTTL),
	}
	if err := cache.SetNotifyVerifyCode(ctx, email, data, VerifyCodeTTL); err != nil {
		return fmt.Errorf("save verify code: %w", err)
	}
	return nil
}

// VerifyAndAddNotifyEmail verifies the code and adds the email to user's extra emails.
func (s *UserService) VerifyAndAddNotifyEmail(ctx context.Context, userID int64, email, code string, cache EmailCache) error {
	if err := ProfileVerifyNotifyCodeWithClock(ctx, cache, email, code, s.now); err != nil {
		return err
	}
	_ = cache.DeleteNotifyVerifyCode(ctx, email)
	return s.ProfileAddOrVerifyNotifyEmail(ctx, userID, email)
}

// ProfileVerifyNotifyCodeWithClock checks the code and uses now to retain the expiry on failed attempts.
func ProfileVerifyNotifyCodeWithClock(ctx context.Context, cache EmailCache, email, code string, now func() time.Time) error {
	data, err := cache.GetNotifyVerifyCode(ctx, email)
	if err != nil || data == nil {
		return ErrInvalidVerifyCode
	}
	if data.Attempts >= MaxVerifyCodeAttempts {
		return ErrVerifyCodeMaxAttempts
	}
	if subtle.ConstantTimeCompare([]byte(data.Code), []byte(code)) != 1 {
		data.Attempts++
		remaining := data.ExpiresAt.Sub(now())
		if remaining <= 0 {
			return ErrInvalidVerifyCode
		}
		if err := cache.SetNotifyVerifyCode(ctx, email, data, remaining); err != nil {
			slog.Error("failed to update notify verify code attempts", "email", email, "error", err)
		}
		if data.Attempts >= MaxVerifyCodeAttempts {
			return ErrVerifyCodeMaxAttempts
		}
		return ErrInvalidVerifyCode
	}
	return nil
}

// ProfileAddOrVerifyNotifyEmail adds the email to user's extra notification emails or marks it as verified.
// Concurrent changes to BalanceNotifyExtraEmails can overwrite each other because
// this read-modify-write replaces the whole list.
func (s *UserService) ProfileAddOrVerifyNotifyEmail(ctx context.Context, userID int64, email string) error {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	for i, e := range user.BalanceNotifyExtraEmails {
		if strings.EqualFold(e.Email, email) {
			if !e.Verified {
				user.BalanceNotifyExtraEmails[i].Verified = true
				return s.userRepo.Update(ctx, user, UserUpdateFields{BalanceNotifyExtraEmails: true})
			}
			return nil // Already verified
		}
	}
	if len(user.BalanceNotifyExtraEmails) >= ProfileMaxNotifyEmails {
		return infraerrors.BadRequest("TOO_MANY_NOTIFY_EMAILS", fmt.Sprintf("maximum %d notification emails allowed", ProfileMaxNotifyEmails))
	}
	user.BalanceNotifyExtraEmails = append(user.BalanceNotifyExtraEmails, NotifyEmailEntry{
		Email:    email,
		Disabled: false,
		Verified: true,
	})
	return s.userRepo.Update(ctx, user, UserUpdateFields{BalanceNotifyExtraEmails: true})
}

// RemoveNotifyEmail removes an email from user's extra notification emails.
func (s *UserService) RemoveNotifyEmail(ctx context.Context, userID int64, email string) error {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return err
	}

	filtered := make([]NotifyEmailEntry, 0, len(user.BalanceNotifyExtraEmails))
	found := false
	for _, e := range user.BalanceNotifyExtraEmails {
		if strings.EqualFold(e.Email, email) {
			found = true
		} else {
			filtered = append(filtered, e)
		}
	}
	if !found {
		return infraerrors.BadRequest("EMAIL_NOT_FOUND", "notification email not found")
	}
	user.BalanceNotifyExtraEmails = filtered
	return s.userRepo.Update(ctx, user, UserUpdateFields{BalanceNotifyExtraEmails: true})
}

// ToggleNotifyEmail sets the disabled state of a notification email entry.
func (s *UserService) ToggleNotifyEmail(ctx context.Context, userID int64, email string, disabled bool) error {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return err
	}

	found := false
	for i, e := range user.BalanceNotifyExtraEmails {
		if strings.EqualFold(e.Email, email) {
			user.BalanceNotifyExtraEmails[i].Disabled = disabled
			found = true
			break
		}
	}
	if !found {
		return infraerrors.BadRequest("EMAIL_NOT_FOUND", "notification email not found")
	}

	return s.userRepo.Update(ctx, user, UserUpdateFields{BalanceNotifyExtraEmails: true})
}

func (s *UserService) SendNotifyVerifyEmail(ctx context.Context, sender NotifyVerificationSender, userID int64, email, code, locale string) error {
	siteName := "TokenRouter"
	if s.settingRepo != nil {
		if name, err := s.settingRepo.GetValue(ctx, SettingKeySiteName); err == nil && name != "" {
			siteName = name
		}
	}
	return sender.SendNotifyVerification(ctx, NotifyVerificationNotice{UserID: userID, Email: email, Code: code, Locale: locale, SiteName: siteName})
}

func NormalizeWeChatConnectModeSetting(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "mp":
		return "mp"
	case "mobile":
		return "mobile"
	default:
		return "open"
	}
}

func ParseWeChatConnectCapabilitySettings(settings map[string]string, enabled bool, mode string) (bool, bool, bool) {
	mode = NormalizeWeChatConnectModeSetting(mode)
	rawOpen, hasOpen := settings[SettingKeyWeChatConnectOpenEnabled]
	rawMP, hasMP := settings[SettingKeyWeChatConnectMPEnabled]
	rawMobile, hasMobile := settings[SettingKeyWeChatConnectMobileEnabled]
	openConfigured := hasOpen && strings.TrimSpace(rawOpen) != ""
	mpConfigured := hasMP && strings.TrimSpace(rawMP) != ""
	mobileConfigured := hasMobile && strings.TrimSpace(rawMobile) != ""

	if openConfigured || mpConfigured || mobileConfigured {
		openEnabled := strings.TrimSpace(rawOpen) == "true"
		mpEnabled := strings.TrimSpace(rawMP) == "true"
		mobileEnabled := strings.TrimSpace(rawMobile) == "true"
		return openEnabled, mpEnabled, mobileEnabled
	}

	if !enabled {
		return false, false, false
	}
	if mode == "mp" {
		return false, true, false
	}
	if mode == "mobile" {
		return false, false, true
	}
	return true, false, false
}
