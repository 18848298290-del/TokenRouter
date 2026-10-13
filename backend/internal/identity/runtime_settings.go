package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

// Authentication feature checks interpret persisted text each time they run.
// Most switches enable a feature only for the exact value true and select
// disabled after a store failure, while numeric account limits use their own
// defaults when input is missing or unusable. Password reset additionally
// depends on email verification, with the two switches read separately.
//
// CAPTCHA verification reads its switches and server credentials through one
// GetMultiple call, keeping provider dispatch on one supplied batch value.
// Administrator key management distinguishes an absent setting from other store
// failures and returns a newly generated raw key after persistence succeeds.
// Status reads apply a display mask; authentication reads the stored key
// itself.

// These keys name persistent identity switches, credentials and default account
// limits; AdminAPIKeyPrefix identifies generated keys accepted by administrator
// authentication.
const (
	AdminAPIKeyPrefix                             = "admin-"
	SettingKeyAdminAPIKey                         = "admin_api_key"
	SettingKeyAliyunCaptchaAccessKeyID            = "aliyun_captcha_access_key_id"
	SettingKeyAliyunCaptchaAccessKeySecret        = "aliyun_captcha_access_key_secret"
	SettingKeyAliyunCaptchaEnabled                = "aliyun_captcha_enabled"
	SettingKeyAliyunCaptchaRegion                 = "aliyun_captcha_region"
	SettingKeyAliyunCaptchaSceneID                = "aliyun_captcha_scene_id"
	SettingKeyDefaultUserAPIKeyLimit              = "default_user_api_key_limit"
	SettingKeyDefaultUserRPMLimit                 = "default_user_rpm_limit"
	SettingKeyEmailVerifyEnabled                  = "email_verify_enabled"
	SettingKeyPasswordResetEnabled                = "password_reset_enabled"
	SettingKeyRegistrationEmailDomainQuotaEnabled = "registration_email_domain_quota_enabled"
	SettingKeyRegistrationEmailNormalization      = "registration_email_normalization"
	SettingKeyRegistrationEmailSuffixWhitelist    = "registration_email_suffix_whitelist"
	SettingKeyRegistrationEnabled                 = "registration_enabled"
	SettingKeySessionBindingEnabled               = "session_binding_enabled"
	SettingKeyStepUpEnabled                       = "step_up_enabled"
	SettingKeyTencentCaptchaAppID                 = "tencent_captcha_app_id"
	SettingKeyTencentCaptchaAppSecretKey          = "tencent_captcha_app_secret_key"
	SettingKeyTencentCaptchaCloudSecretID         = "tencent_captcha_cloud_secret_id"
	SettingKeyTencentCaptchaCloudSecretKey        = "tencent_captcha_cloud_secret_key"
	SettingKeyTencentCaptchaEnabled               = "tencent_captcha_enabled"
	SettingKeyTencentCaptchaRegion                = "tencent_captcha_region"
	SettingKeyTotpEnabled                         = "totp_enabled"
	SettingKeyTurnstileEnabled                    = "turnstile_enabled"
	SettingKeyTurnstileSecretKey                  = "turnstile_secret_key"
	SettingKeyUserEmailChangeEnabled              = "user_email_change_enabled"
)

// RuntimeSettingsStore supplies persistent value access for identity runtime
// policy reads.
type RuntimeSettingsStore interface {
	// GetValue reads one stored setting as text for the identity interpreter.
	// Missing settings use the configured not-found error; other read failures
	// remain errors for each getter's own default or rejection rule.
	GetValue(context.Context, string) (string, error)
	// GetMultiple reads the requested settings into one key-to-text batch value.
	// Absent keys stay absent from the map, while a batch failure is returned
	// before CAPTCHA callers can select their provider or credentials.
	GetMultiple(context.Context, []string) (map[string]string, error)
	// Set persists one supplied setting value and returns its storage result.
	// Administrator key generation returns its raw credential only after this
	// operation succeeds; persistence failures remain visible to that caller.
	Set(context.Context, string, string) error
	// Delete removes the requested stored identity setting and returns the result.
	// Administrator key removal delegates to this operation; the store owns the
	// database delete and any failure returned to the management caller.
	Delete(context.Context, string) error
}

// RuntimeSettings interprets current registration, security, CAPTCHA and
// administrator key settings.
type RuntimeSettings struct {
	// settingRepo is used for current reads and administrator credential writes.
	// Configure it before calling getters other than the nil-tolerant API Key
	// limit getter.
	settingRepo RuntimeSettingsStore
	// notFound identifies an absent key for administrator status and credential
	// reads. Other read failures stay errors on those two methods.
	notFound error
}

// NewRuntimeSettings binds identity setting reads to the supplied store and
// missing-value error. Configure a usable store before invoking its getters;
// the instance retains both inputs and reads current values per operation.
func NewRuntimeSettings(repo RuntimeSettingsStore, notFound error) *RuntimeSettings {
	return &RuntimeSettings{settingRepo: repo, notFound: notFound}
}

// IsRegistrationEnabled reads the current registration switch.
// Only the exact stored text true enables it. The getter returns false on a
// failed read, so the caller receives no enabled registration flag.
func (s *RuntimeSettings) IsRegistrationEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRegistrationEnabled)
	if err != nil {
		// A failed registration lookup supplies no permission to create an account,
		// so the calling registration gate receives its rejecting disabled result.
		return false
	}
	return value == "true"
}

// IsEmailVerifyEnabled reads the current email verification switch.
// Only the exact stored text true enables it. The getter returns false when
// reading fails, so the caller receives no enabled verification requirement.
func (s *RuntimeSettings) IsEmailVerifyEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyEmailVerifyEnabled)
	if err != nil {
		// The verification requirement is enabled by an affirmative stored value. A
		// failed lookup returns false to callers deciding whether to require an
		// email code.
		return false
	}
	return value == "true"
}

// IsRegistrationEmailDomainQuotaEnabled reads the current registration domain
// quota switch.
// Only the exact stored text true enables it. The getter returns false when
// reading fails, leaving callers on their ordinary whitelist policy.
func (s *RuntimeSettings) IsRegistrationEmailDomainQuotaEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRegistrationEmailDomainQuotaEnabled)
	if err != nil {
		// Without an affirmative quota switch, out-of-whitelist registration stays
		// on its ordinary suffix restriction; a failed lookup cannot enable the
		// separate quota allowance.
		return false
	}
	return value == "true"
}

// IsUserEmailChangeEnabled reads the current existing-user email change switch.
// Only the exact stored text true enables it. The getter returns false when
// reading fails, so callers cannot obtain an enabled change permission.
func (s *RuntimeSettings) IsUserEmailChangeEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyUserEmailChangeEnabled)
	if err != nil {
		// A failed settings lookup cannot grant existing-email change permission, so
		// callers checking this switch receive false before allowing a different
		// primary address.
		return false
	}
	return value == "true"
}

// GetRegistrationEmailSuffixWhitelist reads and parses the stored registration
// suffix policy. A failed read returns an allocated empty list, and tolerant
// parsing can also return empty; suffix checks treat that result as
// unrestricted.
func (s *RuntimeSettings) GetRegistrationEmailSuffixWhitelist(ctx context.Context) []string {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRegistrationEmailSuffixWhitelist)
	if err != nil {
		// No usable whitelist is available after a read failure, so supply the
		// empty policy consumed by the registration suffix-check layer's acceptance
		// rule.
		return []string{}
	}
	return ParseRegistrationEmailSuffixWhitelist(value)
}

// IsPasswordResetEnabled reports whether both required reset switches are
// enabled.
// Email verification is checked first, and a disabled or unreadable value stops
// the password-reset lookup. The reset setting then requires the exact text
// true.
//
// These are separate store reads, so the result uses the values observed during
// this operation rather than a shared settings snapshot. Either read failure
// produces false. Callers checking password reset therefore receive an enabled
// result only after both feature checks have supplied affirmative values.
func (s *RuntimeSettings) IsPasswordResetEnabled(ctx context.Context) bool {
	if !s.IsEmailVerifyEnabled(ctx) {
		return false
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyPasswordResetEnabled)
	if err != nil {
		// A reset-specific read failure supplies no enabled reset value after the
		// email check, so the combined feature result stays disabled for this call.
		return false
	}
	return value == "true"
}

// IsTotpEnabled reads the current global TOTP feature switch.
// Only the exact stored text true enables it. The getter returns false after a
// failed read, leaving its caller with a disabled feature result.
func (s *RuntimeSettings) IsTotpEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyTotpEnabled)
	if err != nil {
		// Read failure leaves TOTP disabled for feature availability checks, so this
		// getter cannot supply an enabling value from an unavailable stored setting.
		return false
	}
	return value == "true"
}

// IsSessionBindingEnabled reads the current session fingerprint binding switch.
// Only the exact stored text true enables it. The getter returns false when
// reading fails, so the caller receives no enabled binding check flag.
func (s *RuntimeSettings) IsSessionBindingEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeySessionBindingEnabled)
	if err != nil {
		// When the lookup fails, session callers receive a disabled binding flag and
		// skip checks gated by it; the current fingerprint itself remains a separate
		// context value.
		return false
	}
	return value == "true"
}

// IsStepUpEnabled reads the current administrator step-up switch.
// Only the exact stored text true enables it. The getter returns false when
// reading fails, so the caller receives no enabled step-up requirement.
func (s *RuntimeSettings) IsStepUpEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyStepUpEnabled)
	if err != nil {
		// The step-up setting is opt-in at this getter. A failed lookup exposes
		// false to callers deciding whether the configured administrator challenge
		// requirement applies.
		return false
	}
	return value == "true"
}

// GetDefaultUserRPMLimit reads the default account request limit per minute.
// Missing, empty, negative or malformed values return zero, which is the user
// limit's unlimited value; nonnegative integers are returned as stored.
func (s *RuntimeSettings) GetDefaultUserRPMLimit(ctx context.Context) int {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultUserRPMLimit)
	if err != nil || value == "" {
		// With no usable stored value, new account defaults receive the unlimited
		// user RPM ceiling represented by zero rather than a failed settings read.
		return 0
	}
	if v, err := strconv.Atoi(value); err == nil && v >= 0 {
		return v
	}
	// Invalid numeric text cannot supply a usable nonnegative ceiling, so keep
	// the same unlimited default used when this setting is absent or unreadable.
	return 0
}

// GetDefaultUserAPIKeyLimit reads the default API Key count limit for accounts.
// An absent service or store, failed read, empty value or integer outside the
// database range selects DefaultUserAPIKeyLimit; a valid zero remains unlimited.
func (s *RuntimeSettings) GetDefaultUserAPIKeyLimit(ctx context.Context) int {
	if s == nil || s.settingRepo == nil {
		return DefaultUserAPIKeyLimit
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultUserAPIKeyLimit)
	if err != nil || value == "" {
		// Missing or unreadable settings retain the built-in account Key allowance,
		// so new account creation can receive a usable count limit despite this
		// lookup.
		return DefaultUserAPIKeyLimit
	}
	if limit, err := strconv.Atoi(value); err == nil && IsValidUserAPIKeyLimit(limit) {
		return limit
	}
	// Failed parsing or a value outside the database range cannot configure the
	// account allowance, so the caller receives the same built-in count limit.
	return DefaultUserAPIKeyLimit
}

// IsTurnstileEnabled reads the current Turnstile verification switch.
// Only the exact stored text true enables it. The getter returns false when
// reading fails, so feature checks receive a disabled provider result.
func (s *RuntimeSettings) IsTurnstileEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyTurnstileEnabled)
	if err != nil {
		// A single-switch lookup failure disables this feature query; the batch
		// CAPTCHA dispatch path independently rejects a failed configuration read
		// before selecting a provider.
		return false
	}
	return value == "true"
}

// GetTurnstileSecretKey reads the server-side Turnstile secret as stored text.
// Read failures become an empty string; verification callers use that missing
// secret result to reject an enabled provider that cannot be configured.
func (s *RuntimeSettings) GetTurnstileSecretKey(ctx context.Context) string {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyTurnstileSecretKey)
	if err != nil {
		// No usable server secret is available after this lookup fails, so expose
		// the empty credential value used by callers' verification configuration
		// checks.
		return ""
	}
	return value
}

// GetCaptchaProviderConfig reads CAPTCHA switches and credentials in one batch.
// Missing entries produce disabled switches or empty credentials, and unknown
// regional values select their provider's default. A failed batch read remains
// an error so AuthService can stop before provider dispatch.
func (s *RuntimeSettings) GetCaptchaProviderConfig(ctx context.Context) (CaptchaProviderConfig, error) {
	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyTurnstileEnabled,
		SettingKeyTurnstileSecretKey,
		SettingKeyTencentCaptchaEnabled,
		SettingKeyTencentCaptchaAppID,
		SettingKeyTencentCaptchaAppSecretKey,
		SettingKeyTencentCaptchaCloudSecretID,
		SettingKeyTencentCaptchaCloudSecretKey,
		SettingKeyTencentCaptchaRegion,
		SettingKeyAliyunCaptchaEnabled,
		SettingKeyAliyunCaptchaAccessKeyID,
		SettingKeyAliyunCaptchaAccessKeySecret,
		SettingKeyAliyunCaptchaSceneID,
		SettingKeyAliyunCaptchaRegion,
	})
	if err != nil {
		return CaptchaProviderConfig{}, fmt.Errorf("read captcha provider settings: %w", err)
	}
	return CaptchaProviderConfig{
		TurnstileEnabled:   values[SettingKeyTurnstileEnabled] == "true",
		TurnstileSecretKey: values[SettingKeyTurnstileSecretKey],
		Tencent: TencentCaptchaConfig{
			Enabled:        values[SettingKeyTencentCaptchaEnabled] == "true",
			AppID:          values[SettingKeyTencentCaptchaAppID],
			AppSecretKey:   values[SettingKeyTencentCaptchaAppSecretKey],
			CloudSecretID:  values[SettingKeyTencentCaptchaCloudSecretID],
			CloudSecretKey: values[SettingKeyTencentCaptchaCloudSecretKey],
			Region:         NormalizeTencentCaptchaRegion(values[SettingKeyTencentCaptchaRegion]),
		},
		Aliyun: AliyunCaptchaConfig{
			Enabled:         values[SettingKeyAliyunCaptchaEnabled] == "true",
			AccessKeyID:     values[SettingKeyAliyunCaptchaAccessKeyID],
			AccessKeySecret: values[SettingKeyAliyunCaptchaAccessKeySecret],
			SceneID:         values[SettingKeyAliyunCaptchaSceneID],
			Region:          NormalizeAliyunCaptchaRegion(values[SettingKeyAliyunCaptchaRegion]),
		},
	}, nil
}

// GetTencentCaptchaConfig extracts Tencent settings from the shared CAPTCHA
// batch.
// A batch read failure becomes a zero configuration with verification disabled
// and empty credentials, while a successful read retains the normalized region.
func (s *RuntimeSettings) GetTencentCaptchaConfig(ctx context.Context) TencentCaptchaConfig {
	config, err := s.GetCaptchaProviderConfig(ctx)
	if err != nil {
		// This compatibility getter has no error result, so a failed batch read
		// supplies disabled Tencent settings for callers that consume only this
		// value.
		return TencentCaptchaConfig{}
	}
	return config.Tencent
}

// GenerateAdminAPIKey creates and persists a random administrator credential.
// Thirty-two random bytes become lowercase hex after AdminAPIKeyPrefix; the raw
// key is returned only when the setting write succeeds, and generation or
// persistence failures are returned to the administrator.
func (s *RuntimeSettings) GenerateAdminAPIKey(ctx context.Context) (string, error) {
	// Thirty-two random bytes produce sixty-four hexadecimal credential
	// characters.
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}

	key := AdminAPIKeyPrefix + hex.EncodeToString(bytes)

	// Persist the complete generated credential before returning it to management,
	// so a failed setting write cannot be reported as a usable new administrator
	// key.
	if err := s.settingRepo.Set(ctx, SettingKeyAdminAPIKey, key); err != nil {
		return "", fmt.Errorf("save admin api key: %w", err)
	}

	return key, nil
}

// GetAdminAPIKeyStatus reports whether an administrator credential is stored
// and returns its display value. Missing settings and empty values report false
// with no error; other read failures remain errors. Values longer than fourteen
// bytes show their first ten and final four bytes around an ellipsis.
func (s *RuntimeSettings) GetAdminAPIKeyStatus(ctx context.Context) (maskedKey string, exists bool, err error) {
	key, err := s.settingRepo.GetValue(ctx, SettingKeyAdminAPIKey)
	if err != nil {
		if errors.Is(err, s.notFound) {
			// A missing credential is a normal unconfigured state for management;
			// report absent status while other storage failures still return errors.
			return "", false, nil
		}
		return "", false, err
	}
	if key == "" {
		return "", false, nil
	}

	// Longer credentials reveal the prefix and final four bytes for
	// identification.
	if len(key) > 14 {
		maskedKey = key[:10] + "..." + key[len(key)-4:]
	} else {
		maskedKey = key
	}

	return maskedKey, true, nil
}

// GetAdminAPIKey reads the stored raw credential used for administrator
// authentication.
// The configured missing-setting error becomes an empty string with no error;
// other failures remain errors, and stored text is returned unchanged.
func (s *RuntimeSettings) GetAdminAPIKey(ctx context.Context) (string, error) {
	key, err := s.settingRepo.GetValue(ctx, SettingKeyAdminAPIKey)
	if err != nil {
		if errors.Is(err, s.notFound) {
			// A missing setting denotes an unconfigured management credential, so its
			// absence is a usable empty result rather than a storage failure for
			// callers.
			return "", nil
		}
		return "", err
	}
	return key, nil
}

// DeleteAdminAPIKey removes the administrator credential through the configured
// settings store.
func (s *RuntimeSettings) DeleteAdminAPIKey(ctx context.Context) error {
	return s.settingRepo.Delete(ctx, SettingKeyAdminAPIKey)
}

// IsRegistrationEmailNormalizationEnabled reads the current registration
// mailbox normalization switch.
// Only the exact stored text true enables it. The getter returns false when
// reading fails, so registration callers receive no enabled normalization flag.
func (s *RuntimeSettings) IsRegistrationEmailNormalizationEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRegistrationEmailNormalization)
	if err != nil {
		// A failed lookup supplies no affirmative normalization choice, so
		// registration callers keep this optional mailbox uniqueness normalization
		// disabled for their current decision.
		return false
	}
	return value == "true"
}
