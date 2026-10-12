package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

// Persistent identity setting keys and the administrator credential prefix.
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

// RuntimeSettingsStore reads, writes, and deletes identity settings.
type RuntimeSettingsStore interface {
	GetValue(context.Context, string) (string, error)
	GetMultiple(context.Context, []string) (map[string]string, error)
	Set(context.Context, string, string) error
	Delete(context.Context, string) error
}

// RuntimeSettings reads registration, security, and captcha settings.
type RuntimeSettings struct {
	settingRepo RuntimeSettingsStore
	notFound    error
}

// NewRuntimeSettings binds the store; getters read runtime values on each call.
func NewRuntimeSettings(repo RuntimeSettingsStore, notFound error) *RuntimeSettings {
	return &RuntimeSettings{settingRepo: repo, notFound: notFound}
}

// IsRegistrationEnabled reads the registration switch and returns false on read failure.
func (s *RuntimeSettings) IsRegistrationEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRegistrationEnabled)
	if err != nil {
		return false
	}
	return value == "true"
}

// IsEmailVerifyEnabled reads the email verification switch and returns false on read failure.
func (s *RuntimeSettings) IsEmailVerifyEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyEmailVerifyEnabled)
	if err != nil {
		return false
	}
	return value == "true"
}

// IsRegistrationEmailDomainQuotaEnabled reads the domain quota switch and returns false on read failure.
func (s *RuntimeSettings) IsRegistrationEmailDomainQuotaEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRegistrationEmailDomainQuotaEnabled)
	if err != nil {
		return false
	}
	return value == "true"
}

// IsUserEmailChangeEnabled reads the email-change switch and returns false on read failure.
func (s *RuntimeSettings) IsUserEmailChangeEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyUserEmailChangeEnabled)
	if err != nil {
		return false
	}
	return value == "true"
}

// GetRegistrationEmailSuffixWhitelist reads allowed email suffixes or returns an empty list on failure.
func (s *RuntimeSettings) GetRegistrationEmailSuffixWhitelist(ctx context.Context) []string {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRegistrationEmailSuffixWhitelist)
	if err != nil {
		return []string{}
	}
	return ParseRegistrationEmailSuffixWhitelist(value)
}

// IsPasswordResetEnabled reports whether both email verification and password reset are enabled.
func (s *RuntimeSettings) IsPasswordResetEnabled(ctx context.Context) bool {
	if !s.IsEmailVerifyEnabled(ctx) {
		return false
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyPasswordResetEnabled)
	if err != nil {
		return false // Disabled when settings cannot be read.
	}
	return value == "true"
}

// IsTotpEnabled reads the TOTP switch and returns false on read failure.
func (s *RuntimeSettings) IsTotpEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyTotpEnabled)
	if err != nil {
		return false // Disabled when settings cannot be read.
	}
	return value == "true"
}

// IsSessionBindingEnabled reads the session binding switch and returns false on read failure.
func (s *RuntimeSettings) IsSessionBindingEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeySessionBindingEnabled)
	if err != nil {
		return false // Disabled when settings cannot be read.
	}
	return value == "true"
}

// IsStepUpEnabled reads the step-up switch and returns false on read failure.
func (s *RuntimeSettings) IsStepUpEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyStepUpEnabled)
	if err != nil {
		return false // Disabled when settings cannot be read.
	}
	return value == "true"
}

// GetDefaultUserRPMLimit returns the default RPM limit, or zero for a missing or invalid value.
func (s *RuntimeSettings) GetDefaultUserRPMLimit(ctx context.Context) int {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultUserRPMLimit)
	if err != nil || value == "" {
		return 0
	}
	if v, err := strconv.Atoi(value); err == nil && v >= 0 {
		return v
	}
	return 0
}

// GetDefaultUserAPIKeyLimit returns the configured limit or DefaultUserAPIKeyLimit when unavailable or invalid.
func (s *RuntimeSettings) GetDefaultUserAPIKeyLimit(ctx context.Context) int {
	if s == nil || s.settingRepo == nil {
		return DefaultUserAPIKeyLimit
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultUserAPIKeyLimit)
	if err != nil || value == "" {
		return DefaultUserAPIKeyLimit
	}
	if limit, err := strconv.Atoi(value); err == nil && IsValidUserAPIKeyLimit(limit) {
		return limit
	}
	return DefaultUserAPIKeyLimit
}

// IsTurnstileEnabled reads the Turnstile switch and returns false on read failure.
func (s *RuntimeSettings) IsTurnstileEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyTurnstileEnabled)
	if err != nil {
		return false
	}
	return value == "true"
}

// GetTurnstileSecretKey reads the Turnstile secret or returns an empty string on failure.
func (s *RuntimeSettings) GetTurnstileSecretKey(ctx context.Context) string {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyTurnstileSecretKey)
	if err != nil {
		return ""
	}
	return value
}

// GetCaptchaProviderConfig reads and parses captcha provider settings in one batch.
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

// GetTencentCaptchaConfig returns Tencent captcha settings or a zero value on read failure.
func (s *RuntimeSettings) GetTencentCaptchaConfig(ctx context.Context) TencentCaptchaConfig {
	config, err := s.GetCaptchaProviderConfig(ctx)
	if err != nil {
		return TencentCaptchaConfig{}
	}
	return config.Tencent
}

// GenerateAdminAPIKey generates and stores a random key with the admin- prefix.
func (s *RuntimeSettings) GenerateAdminAPIKey(ctx context.Context) (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}

	key := AdminAPIKeyPrefix + hex.EncodeToString(bytes)

	if err := s.settingRepo.Set(ctx, SettingKeyAdminAPIKey, key); err != nil {
		return "", fmt.Errorf("save admin api key: %w", err)
	}

	return key, nil
}

// GetAdminAPIKeyStatus reports key presence and masks values longer than 14 characters.
func (s *RuntimeSettings) GetAdminAPIKeyStatus(ctx context.Context) (maskedKey string, exists bool, err error) {
	key, err := s.settingRepo.GetValue(ctx, SettingKeyAdminAPIKey)
	if err != nil {
		if errors.Is(err, s.notFound) {
			return "", false, nil
		}
		return "", false, err
	}
	if key == "" {
		return "", false, nil
	}

	if len(key) > 14 {
		maskedKey = key[:10] + "..." + key[len(key)-4:]
	} else {
		maskedKey = key
	}

	return maskedKey, true, nil
}

// GetAdminAPIKey reads the administrator key or returns an empty string when unconfigured.
func (s *RuntimeSettings) GetAdminAPIKey(ctx context.Context) (string, error) {
	key, err := s.settingRepo.GetValue(ctx, SettingKeyAdminAPIKey)
	if err != nil {
		if errors.Is(err, s.notFound) {
			return "", nil // No key is configured.
		}
		return "", err // Propagate other storage failures.
	}
	return key, nil
}

// DeleteAdminAPIKey removes the stored administrator key.
func (s *RuntimeSettings) DeleteAdminAPIKey(ctx context.Context) error {
	return s.settingRepo.Delete(ctx, SettingKeyAdminAPIKey)
}

// IsRegistrationEmailNormalizationEnabled reads the normalization switch and returns false on read failure.
func (s *RuntimeSettings) IsRegistrationEmailNormalizationEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRegistrationEmailNormalization)
	if err != nil {
		return false
	}
	return value == "true"
}
