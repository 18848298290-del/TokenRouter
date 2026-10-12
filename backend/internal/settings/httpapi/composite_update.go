package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"

	"github.com/TokenFlux/TokenRouter/internal/audit"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billinghttp "github.com/TokenFlux/TokenRouter/internal/billing/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/authconfig"
	"github.com/TokenFlux/TokenRouter/internal/identity/contact"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	identitydto "github.com/TokenFlux/TokenRouter/internal/identity/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	runtimesettings "github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/settings/composite"
	settingsdto "github.com/TokenFlux/TokenRouter/internal/settings/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

var (
	// semverPattern accepts three dot-separated numeric version components.
	semverPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

	// menuItemIDPattern accepts ASCII letters, digits, hyphens, and underscores.
	menuItemIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

	// markdownMenuSlugPattern matches the slug rules used by the Markdown page reader.
	markdownMenuSlugPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

	// settingKeyJSONAliases maps request JSON names that differ from their stored setting keys.
	// Other UpdateSettingsRequest fields use their setting keys as JSON names.
	settingKeyJSONAliases = map[string]string{
		"smtp_from_email": notification.SettingKeySMTPFrom,
	}

	// settingKeyByJSONName maps non-pointer top-level request fields to stored setting keys.
	// It is built once from struct tags so added fields participate automatically.
	//
	// UpdateSettings merges pointer fields with stored values, so they are excluded here.
	// Some pointer fields are rewritten on every save to normalize invalid security settings;
	// see TestUpdateSettingsMalformedForwardedClientIPHeadersRemainFailClosedWhenOmitted.
	settingKeyByJSONName = buildSettingKeyByJSONName()
)

// UpdateSettingsRequest contains the combined settings update payload.
type UpdateSettingsRequest = settingsdto.UpdateSettingsRequest

// generateMenuItemID returns a random hexadecimal ID for a custom menu item.
func generateMenuItemID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate menu item ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// scopesContainOpenID checks whitespace-separated scopes for openid, ignoring case.
func scopesContainOpenID(scopes string) bool {
	return slices.Contains(strings.Fields(strings.ToLower(strings.TrimSpace(scopes))), "openid")
}

// firstNonEmpty returns the first nonempty trimmed value, or an empty string.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// ensureActorTotpForStepUp checks that the actor can enable step-up verification.
// The actor needs an admin session with TOTP enabled; admin API keys are rejected.
// Failure writes an error response and returns false.
func (h *Handler) ensureActorTotpForStepUp(c *gin.Context) bool {
	if c.GetString("auth_method") == audit.AuditAuthMethodAdminAPIKey {
		httpx.ErrorWithDetails(c, http.StatusForbidden,
			"Admin API key cannot enable step-up verification; use an admin session with TOTP enabled",
			"STEP_UP_ADMIN_API_KEY_FORBIDDEN", nil)
		return false
	}
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		httpx.ErrorWithDetails(c, http.StatusForbidden,
			"Enabling step-up verification requires an authenticated admin session",
			"STEP_UP_ENABLE_REQUIRES_TOTP", nil)
		return false
	}
	if h.userService == nil {
		httpx.InternalError(c, "Step-up precondition check unavailable")
		return false
	}
	user, err := h.userService.GetByID(c.Request.Context(), subject.UserID)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return false
	}
	if !user.TotpEnabled {
		httpx.ErrorWithDetails(c, http.StatusBadRequest,
			"Enable two-factor authentication (TOTP) for your account before turning on step-up verification",
			"STEP_UP_ENABLE_REQUIRES_TOTP", nil)
		return false
	}
	return true
}

func buildSettingKeyByJSONName() map[string]string {
	t := reflect.TypeFor[UpdateSettingsRequest]()
	out := make(map[string]string, t.NumField())
	for field := range t.Fields() {
		if field.Type.Kind() == reflect.Pointer {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		if alias, ok := settingKeyJSONAliases[name]; ok {
			out[name] = alias
			continue
		}
		out[name] = name
	}
	return out
}

// omittedSettingKeys returns stored keys for non-pointer request fields absent from the payload.
// Dropping these keys from the prepared update prevents partial requests from resetting omitted fields to
// zero values.
func omittedSettingKeys(sentFields map[string]json.RawMessage) runtimesettings.OmittedKeys {
	omitted := make(runtimesettings.OmittedKeys, len(settingKeyByJSONName))
	for jsonName, settingKey := range settingKeyByJSONName {
		if _, sent := sentFields[jsonName]; !sent {
			omitted[settingKey] = struct{}{}
		}
	}
	return omitted
}

func SettingsAuditRequest(req UpdateSettingsRequest) UpdateSettingsRequest {
	req.TencentCaptchaAppSecretKey = strings.TrimSpace(req.TencentCaptchaAppSecretKey)
	req.TencentCaptchaCloudSecretID = strings.TrimSpace(req.TencentCaptchaCloudSecretID)
	req.TencentCaptchaCloudSecretKey = strings.TrimSpace(req.TencentCaptchaCloudSecretKey)
	req.AliyunCaptchaAccessKeySecret = strings.TrimSpace(req.AliyunCaptchaAccessKeySecret)
	return req
}

func (h *Handler) UpdateSettings(c *gin.Context) {
	var sentFields map[string]json.RawMessage
	if err := c.ShouldBindBodyWith(&sentFields, binding.JSON); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	// Reject the removed field so clients cannot assume automatic cross-model mapping is active.
	if _, exists := sentFields["grok_cross_client_model_map_enabled"]; exists {
		httpx.BadRequest(c, "grok_cross_client_model_map_enabled has been removed; configure explicit model_mapping instead")
		return
	}
	for _, field := range []string{"site_name_zh", "site_name_en", "site_title_zh", "site_title_en", "site_subtitle_zh", "site_subtitle_en"} {
		if _, exists := sentFields[field]; exists {
			httpx.ErrorWithDetails(c, http.StatusBadRequest, "Use site_texts to edit translations.", "REMOVED_SETTING_FIELD", map[string]string{"field": field})
			return
		}
	}
	if rejectRemovedUngroupedKeySchedulingField(c, sentFields) || rejectRemovedPlatformQuotaFields(c, sentFields) || rejectDeprecatedAdvancedSchedulerRequestFields(c, sentFields) {
		return
	}
	var req UpdateSettingsRequest
	if err := c.ShouldBindBodyWith(&req, binding.JSON); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	update, err := h.settingService.BeginSettingsUpdate(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	defer update.Close()
	c.Request = c.Request.WithContext(update.Context())

	// The admin allowlist is intersected with provider capabilities when building the execution catalog.
	if req.CreativeModelSettings != nil {
		if sanitizer, ok := h.creativeModelReader.(interface {
			NormalizeCreativeModelSettingsForSave(context.Context, []creative.CreativeModelSetting) ([]creative.CreativeModelSetting, error)
		}); ok {
			normalized, normalizeErr := sanitizer.NormalizeCreativeModelSettingsForSave(c.Request.Context(), *req.CreativeModelSettings)
			if normalizeErr != nil {
				httpx.BadRequest(c, "Invalid creative model settings: "+normalizeErr.Error())
				return
			}
			req.CreativeModelSettings = &normalized
		}
	}
	auditReq := SettingsAuditRequest(req)
	omitted := omittedSettingKeys(sentFields)

	previousSettings, err := h.settingService.GetAllSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	previousAuthSourceDefaults, err := h.settingService.GetAuthSourceDefaultSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	// Pointer fields distinguish omission from false so older clients keep the stored switch values.
	registrationEmailDomainQuotaEnabled := previousSettings.RegistrationEmailDomainQuotaEnabled
	if req.RegistrationEmailDomainQuotaEnabled != nil {
		registrationEmailDomainQuotaEnabled = *req.RegistrationEmailDomainQuotaEnabled
	}
	userEmailChangeEnabled := previousSettings.UserEmailChangeEnabled
	if req.UserEmailChangeEnabled != nil {
		userEmailChangeEnabled = *req.UserEmailChangeEnabled
	}
	// Omitted security switches retain their stored values.
	sessionBindingEnabled := previousSettings.SessionBindingEnabled
	if req.SessionBindingEnabled != nil {
		sessionBindingEnabled = *req.SessionBindingEnabled
	}
	stepUpEnabled := previousSettings.StepUpEnabled
	if req.StepUpEnabled != nil {
		stepUpEnabled = *req.StepUpEnabled
	}
	forwardedClientIPHeaders := append([]string(nil), previousSettings.ForwardedClientIPHeaders...)
	if req.ForwardedClientIPHeaders != nil {
		forwardedClientIPHeaders = append([]string(nil), *req.ForwardedClientIPHeaders...)
	}

	// Enabling step-up requires the actor's own TOTP to prevent locking the administrator out
	// of sensitive operations. Check the precondition on the false-to-true transition;
	// routine saves with step-up already enabled do not require this check.
	if stepUpEnabled && !previousSettings.StepUpEnabled {
		if !h.ensureActorTotpForStepUp(c) {
			return
		}
	}
	// Disabling step-up requires step-up verification to prevent a stolen session from removing the protection.
	// The previous snapshot establishes that the switch is enabled. Use the unconditional check
	// so a second settings read cannot fail open; the frontend handles STEP_UP_REQUIRED and retries.
	if !stepUpEnabled && previousSettings.StepUpEnabled {
		if !identityhttp.EnforceStepUp(c, h.totpService, h.userService, nil) {
			return
		}
	}

	if req.DefaultConcurrency < 1 {
		req.DefaultConcurrency = 1
	}
	if req.DefaultBalance < 0 {
		req.DefaultBalance = 0
	}
	if req.DefaultUserAPIKeyLimit != nil && !identity.IsValidUserAPIKeyLimit(*req.DefaultUserAPIKeyLimit) {
		httpx.ErrorFrom(c, identity.ErrUserAPIKeyLimitInvalid)
		return
	}
	normalizedPromotion := promotion.NormalizeAdminSettings(promotion.AdminSettings{AffiliateRebateRate: req.AffiliateRebateRate, AffiliateRebateFreezeHours: req.AffiliateRebateFreezeHours, AffiliateRebateDurationDays: req.AffiliateRebateDurationDays, AffiliateRebatePerInviteeCap: req.AffiliateRebatePerInviteeCap})
	req.AffiliateRebateRate = normalizedPromotion.AffiliateRebateRate
	req.AffiliateRebateFreezeHours = normalizedPromotion.AffiliateRebateFreezeHours
	req.AffiliateRebateDurationDays = normalizedPromotion.AffiliateRebateDurationDays
	req.AffiliateRebatePerInviteeCap = normalizedPromotion.AffiliateRebatePerInviteeCap
	if req.ReasoningPointRMBUnitPrice != nil && *req.ReasoningPointRMBUnitPrice < 0 {
		*req.ReasoningPointRMBUnitPrice = 0
	}
	if req.USDExchangeRate != nil && *req.USDExchangeRate < 0 {
		*req.USDExchangeRate = 0
	}
	adminRechargeRebateEnabled := previousSettings.AdminRechargeRebateEnabled
	if req.AdminRechargeRebateEnabled != nil {
		adminRechargeRebateEnabled = *req.AdminRechargeRebateEnabled
	}
	// Keep saved table settings when older clients omit them.
	if req.TableDefaultPageSize <= 0 {
		req.TableDefaultPageSize = previousSettings.TableDefaultPageSize
	}
	if req.TablePageSizeOptions == nil {
		req.TablePageSizeOptions = previousSettings.TablePageSizeOptions
	}
	// Usage resolves omitted fields and validates ranking and display rules.
	usageRanking, rankingErr := usage.ResolveRankingSettings(usage.UsageRankingSettings{
		Enabled: previousSettings.UsageRankingEnabled, SortBy: usage.UsageRankingSortBy(previousSettings.UsageRankingSortBy), ShowTotalTokens: previousSettings.UsageRankingShowTotalTokens, ShowRequests: previousSettings.UsageRankingShowRequests, ShowActualCost: previousSettings.UsageRankingShowActualCost, Limit: previousSettings.UsageRankingLimit,
	}, usage.RankingSettingsUpdate{Limit: req.UsageRankingLimit, Enabled: req.UsageRankingEnabled, SortBy: req.UsageRankingSortBy, ShowTotalTokens: req.UsageRankingShowTotalTokens, ShowRequests: req.UsageRankingShowRequests, ShowActualCost: req.UsageRankingShowActualCost})
	if rankingErr != nil {
		httpx.BadRequest(c, rankingErr.Error())
		return
	}
	req.UsageRankingLimit = usageRanking.Limit
	// Notification owns SMTP normalization and the empty-host retention rule.
	smtp := notification.NormalizeAdminSMTPSettings(notification.AdminSMTPSettings{SMTPHost: previousSettings.SMTPHost, SMTPPort: previousSettings.SMTPPort, SMTPUsername: previousSettings.SMTPUsername, SMTPPassword: previousSettings.SMTPPassword, SMTPFrom: previousSettings.SMTPFrom, SMTPFromName: previousSettings.SMTPFromName, SMTPUseTLS: previousSettings.SMTPUseTLS}, notification.AdminSMTPSettings{SMTPHost: req.SMTPHost, SMTPPort: req.SMTPPort, SMTPUsername: req.SMTPUsername, SMTPPassword: req.SMTPPassword, SMTPFrom: req.SMTPFrom, SMTPFromName: req.SMTPFromName, SMTPUseTLS: req.SMTPUseTLS})
	req.SMTPHost = smtp.SMTPHost
	req.SMTPPort = smtp.SMTPPort
	req.SMTPUsername = smtp.SMTPUsername
	req.SMTPPassword = smtp.SMTPPassword
	req.SMTPFrom = smtp.SMTPFrom
	req.SMTPFromName = smtp.SMTPFromName
	req.SMTPUseTLS = smtp.SMTPUseTLS
	req.BalanceUnitName = strings.TrimSpace(req.BalanceUnitName)
	req.BalanceUnitSymbol = strings.TrimSpace(req.BalanceUnitSymbol)
	req.BalanceIconSVG = strings.TrimSpace(req.BalanceIconSVG)
	if req.BalanceUnitName == "" {
		req.BalanceUnitName = "USD"
	}
	if req.BalanceUnitSymbol == "" {
		req.BalanceUnitSymbol = "$"
	}
	req.TencentCaptchaAppID = strings.TrimSpace(req.TencentCaptchaAppID)
	req.TencentCaptchaAppSecretKey = strings.TrimSpace(req.TencentCaptchaAppSecretKey)
	req.TencentCaptchaCloudSecretID = strings.TrimSpace(req.TencentCaptchaCloudSecretID)
	req.TencentCaptchaCloudSecretKey = strings.TrimSpace(req.TencentCaptchaCloudSecretKey)
	req.DefaultSubscriptions = normalizeDefaultSubscriptions(req.DefaultSubscriptions)
	req.AuthSourceDefaultEmailSubscriptions = normalizeOptionalDefaultSubscriptions(req.AuthSourceDefaultEmailSubscriptions)
	req.AuthSourceDefaultLinuxDoSubscriptions = normalizeOptionalDefaultSubscriptions(req.AuthSourceDefaultLinuxDoSubscriptions)
	req.AuthSourceDefaultOIDCSubscriptions = normalizeOptionalDefaultSubscriptions(req.AuthSourceDefaultOIDCSubscriptions)
	req.AuthSourceDefaultWeChatSubscriptions = normalizeOptionalDefaultSubscriptions(req.AuthSourceDefaultWeChatSubscriptions)
	req.AuthSourceDefaultGitHubSubscriptions = normalizeOptionalDefaultSubscriptions(req.AuthSourceDefaultGitHubSubscriptions)
	req.AuthSourceDefaultGoogleSubscriptions = normalizeOptionalDefaultSubscriptions(req.AuthSourceDefaultGoogleSubscriptions)
	req.AuthSourceDefaultDingTalkSubscriptions = normalizeOptionalDefaultSubscriptions(req.AuthSourceDefaultDingTalkSubscriptions)

	turnstileEnabled := req.TurnstileEnabled
	if _, sent := sentFields["turnstile_enabled"]; !sent {
		turnstileEnabled = previousSettings.TurnstileEnabled
	}
	tencentCaptchaEnabled := req.TencentCaptchaEnabled
	if _, sent := sentFields["tencent_captcha_enabled"]; !sent {
		tencentCaptchaEnabled = previousSettings.TencentCaptchaEnabled
	}
	aliyunCaptchaEnabled := req.AliyunCaptchaEnabled
	if _, sent := sentFields["aliyun_captcha_enabled"]; !sent {
		aliyunCaptchaEnabled = previousSettings.AliyunCaptchaEnabled
	}
	enabledCaptchaProviders := 0
	for _, enabled := range []bool{turnstileEnabled, tencentCaptchaEnabled, aliyunCaptchaEnabled} {
		if enabled {
			enabledCaptchaProviders++
		}
	}
	if enabledCaptchaProviders > 1 {
		httpx.BadRequest(c, "Multiple captcha providers (Cloudflare Turnstile / Tencent Captcha / Aliyun Captcha) cannot be enabled at the same time")
		return
	}
	// An omitted Aliyun region uses the saved value; values other than sgp normalize to cn.
	if _, sent := sentFields["aliyun_captcha_region"]; !sent {
		req.AliyunCaptchaRegion = previousSettings.AliyunCaptchaRegion
	}
	if req.AliyunCaptchaRegion != identity.AliyunCaptchaRegionSGP {
		req.AliyunCaptchaRegion = identity.AliyunCaptchaRegionCN
	}
	// An omitted Tencent region uses the saved value; values other than intl normalize to cn.
	if _, sent := sentFields["tencent_captcha_region"]; !sent {
		req.TencentCaptchaRegion = previousSettings.TencentCaptchaRegion
	}
	if req.TencentCaptchaRegion != identity.TencentCaptchaRegionINTL {
		req.TencentCaptchaRegion = identity.TencentCaptchaRegionCN
	}

	// Validate enabled Turnstile configuration.
	if req.TurnstileEnabled {
		if req.TurnstileSiteKey == "" {
			httpx.BadRequest(c, "Turnstile Site Key is required when enabled")
			return
		}
		// An empty secret retains the saved value.
		if req.TurnstileSecretKey == "" {
			if previousSettings.TurnstileSecretKey == "" {
				httpx.BadRequest(c, "Turnstile Secret Key is required when enabled")
				return
			}
			req.TurnstileSecretKey = previousSettings.TurnstileSecretKey
		}

		// Validate changed keys before saving to prevent a bad configuration from blocking login.
		siteKeyChanged := previousSettings.TurnstileSiteKey != req.TurnstileSiteKey
		secretKeyChanged := previousSettings.TurnstileSecretKey != req.TurnstileSecretKey
		if siteKeyChanged || secretKeyChanged {
			if err := h.turnstileService.ValidateSecretKey(c.Request.Context(), req.TurnstileSecretKey); err != nil {
				httpx.ErrorFrom(c, err)
				return
			}
		}
	}

	if tencentCaptchaEnabled {
		if _, sent := sentFields["tencent_captcha_app_id"]; !sent {
			req.TencentCaptchaAppID = previousSettings.TencentCaptchaAppID
		}
		appID, err := strconv.ParseUint(req.TencentCaptchaAppID, 10, 64)
		if err != nil || appID == 0 {
			httpx.BadRequest(c, "Tencent Captcha CaptchaAppId must be a positive integer when enabled")
			return
		}
		if req.TencentCaptchaAppSecretKey == "" {
			req.TencentCaptchaAppSecretKey = previousSettings.TencentCaptchaAppSecretKey
		}
		if req.TencentCaptchaCloudSecretID == "" {
			req.TencentCaptchaCloudSecretID = previousSettings.TencentCaptchaCloudSecretID
		}
		if req.TencentCaptchaCloudSecretKey == "" {
			req.TencentCaptchaCloudSecretKey = previousSettings.TencentCaptchaCloudSecretKey
		}
		if req.TencentCaptchaAppSecretKey == "" {
			httpx.BadRequest(c, "Tencent Captcha AppSecretKey is required when enabled")
			return
		}
		if req.TencentCaptchaCloudSecretID == "" {
			httpx.BadRequest(c, "Tencent Cloud SecretId is required when Tencent Captcha is enabled")
			return
		}
		if req.TencentCaptchaCloudSecretKey == "" {
			httpx.BadRequest(c, "Tencent Cloud SecretKey is required when Tencent Captcha is enabled")
			return
		}
	}

	// Validate enabled Aliyun Captcha 2.0 configuration.
	if aliyunCaptchaEnabled {
		if _, sent := sentFields["aliyun_captcha_scene_id"]; !sent {
			req.AliyunCaptchaSceneID = previousSettings.AliyunCaptchaSceneID
		}
		if _, sent := sentFields["aliyun_captcha_prefix"]; !sent {
			req.AliyunCaptchaPrefix = previousSettings.AliyunCaptchaPrefix
		}
		if _, sent := sentFields["aliyun_captcha_access_key_id"]; !sent {
			req.AliyunCaptchaAccessKeyID = previousSettings.AliyunCaptchaAccessKeyID
		}
		if req.AliyunCaptchaSceneID == "" {
			httpx.BadRequest(c, "Aliyun Captcha Scene ID is required when enabled")
			return
		}
		if req.AliyunCaptchaPrefix == "" {
			httpx.BadRequest(c, "Aliyun Captcha Prefix is required when enabled")
			return
		}
		if req.AliyunCaptchaAccessKeyID == "" {
			httpx.BadRequest(c, "Aliyun Captcha AccessKey ID is required when enabled")
			return
		}
		// An empty AccessKey Secret retains the saved value.
		if req.AliyunCaptchaAccessKeySecret == "" {
			if previousSettings.AliyunCaptchaAccessKeySecret == "" {
				httpx.BadRequest(c, "Aliyun Captcha AccessKey Secret is required when enabled")
				return
			}
			req.AliyunCaptchaAccessKeySecret = previousSettings.AliyunCaptchaAccessKeySecret
		}

		// Validate changed credentials with Aliyun before saving to prevent login lockout.
		credentialsChanged := previousSettings.AliyunCaptchaAccessKeyID != req.AliyunCaptchaAccessKeyID ||
			previousSettings.AliyunCaptchaAccessKeySecret != req.AliyunCaptchaAccessKeySecret ||
			previousSettings.AliyunCaptchaSceneID != req.AliyunCaptchaSceneID ||
			previousSettings.AliyunCaptchaRegion != req.AliyunCaptchaRegion
		if credentialsChanged {
			if err := h.aliyunCaptchaService.ValidateCredentials(c.Request.Context(), req.AliyunCaptchaAccessKeyID, req.AliyunCaptchaAccessKeySecret, req.AliyunCaptchaSceneID, req.AliyunCaptchaRegion); err != nil {
				httpx.ErrorFrom(c, err)
				return
			}
		}
	}

	// Enabling TOTP requires a configured encryption key so encrypted credentials remain recoverable across restarts.
	if req.TotpEnabled && !previousSettings.TotpEnabled {
		if !h.settingService.IsTotpEncryptionKeyConfigured() {
			httpx.BadRequest(c, "Cannot enable TOTP: TOTP_ENCRYPTION_KEY environment variable must be configured first. Generate a key with 'openssl rand -hex 32' and set it in your environment.")
			return
		}
	}
	loginAgreementMode := strings.ToLower(strings.TrimSpace(req.LoginAgreementMode))
	if loginAgreementMode == "" {
		loginAgreementMode = strings.ToLower(strings.TrimSpace(previousSettings.LoginAgreementMode))
	}
	switch loginAgreementMode {
	case "", "modal":
		loginAgreementMode = "modal"
	case "checkbox":
	default:
		httpx.BadRequest(c, "Login agreement mode must be modal or checkbox")
		return
	}
	loginAgreementUpdatedAt := strings.TrimSpace(req.LoginAgreementUpdatedAt)
	if loginAgreementUpdatedAt == "" {
		loginAgreementUpdatedAt = strings.TrimSpace(previousSettings.LoginAgreementUpdatedAt)
	}
	loginAgreementDocuments := loginAgreementDocumentsToService(req.LoginAgreementDocuments)
	if len(loginAgreementDocuments) == 0 {
		loginAgreementDocuments = previousSettings.LoginAgreementDocuments
	}
	for _, doc := range loginAgreementDocuments {
		if strings.TrimSpace(doc.Title) == "" {
			httpx.BadRequest(c, "Login agreement document title is required")
			return
		}
		if len(doc.Title) > 80 {
			httpx.BadRequest(c, "Login agreement document title is too long (max 80 characters)")
			return
		}
		if len(doc.ContentMD) > 200*1024 {
			httpx.BadRequest(c, "Login agreement document content is too large (max 200KB)")
			return
		}
	}
	if req.LoginAgreementEnabled && len(loginAgreementDocuments) == 0 {
		httpx.BadRequest(c, "Login agreement documents are required when enabled")
		return
	}

	// Validate enabled LinuxDo Connect configuration.
	if req.LinuxDoConnectEnabled {
		req.LinuxDoConnectClientID = strings.TrimSpace(req.LinuxDoConnectClientID)
		req.LinuxDoConnectClientSecret = strings.TrimSpace(req.LinuxDoConnectClientSecret)
		req.LinuxDoConnectRedirectURL = strings.TrimSpace(req.LinuxDoConnectRedirectURL)

		if req.LinuxDoConnectClientID == "" {
			httpx.BadRequest(c, "LinuxDo Client ID is required when enabled")
			return
		}
		if req.LinuxDoConnectRedirectURL == "" {
			httpx.BadRequest(c, "LinuxDo Redirect URL is required when enabled")
			return
		}
		if err := authconfig.ValidateAbsoluteHTTPURL(req.LinuxDoConnectRedirectURL); err != nil {
			httpx.BadRequest(c, "LinuxDo Redirect URL must be an absolute http(s) URL")
			return
		}

		// An empty client_secret retains the saved value.
		if req.LinuxDoConnectClientSecret == "" {
			if previousSettings.LinuxDoConnectClientSecret == "" {
				httpx.BadRequest(c, "LinuxDo Client Secret is required when enabled")
				return
			}
			req.LinuxDoConnectClientSecret = previousSettings.LinuxDoConnectClientSecret
		}
	}

	// Coerce the retired corp_restriction_policy=whitelist value to none
	// so direct API clients cannot write the unsupported policy back to storage.
	req.DingTalkConnectCorpRestrictionPolicy = identity.SettingsCoerceDingTalkCorpPolicyForWrite(req.DingTalkConnectCorpRestrictionPolicy)

	if req.DingTalkConnectEnabled {
		req.DingTalkConnectClientID = strings.TrimSpace(req.DingTalkConnectClientID)
		req.DingTalkConnectClientSecret = strings.TrimSpace(req.DingTalkConnectClientSecret)
		req.DingTalkConnectRedirectURL = strings.TrimSpace(req.DingTalkConnectRedirectURL)
		req.DingTalkConnectCorpRestrictionPolicy = strings.TrimSpace(req.DingTalkConnectCorpRestrictionPolicy)
		req.DingTalkConnectInternalCorpID = strings.TrimSpace(req.DingTalkConnectInternalCorpID)

		if req.DingTalkConnectClientID == "" {
			httpx.BadRequest(c, "DingTalk Client ID is required when enabled")
			return
		}
		if req.DingTalkConnectRedirectURL == "" {
			httpx.BadRequest(c, "DingTalk Redirect URL is required when enabled")
			return
		}
		if err := authconfig.ValidateAbsoluteHTTPURL(req.DingTalkConnectRedirectURL); err != nil {
			httpx.BadRequest(c, "DingTalk Redirect URL must be an absolute http(s) URL")
			return
		}

		// An empty client_secret retains the saved value.
		if req.DingTalkConnectClientSecret == "" {
			if previousSettings.DingTalkConnectClientSecret == "" {
				httpx.BadRequest(c, "DingTalk Client Secret is required when enabled")
				return
			}
			req.DingTalkConnectClientSecret = previousSettings.DingTalkConnectClientSecret
		}

		// Validate the corporation policy before persisting it.
		dingTalkCfg := authconfig.DingTalkConnectConfig{
			Enabled:               true,
			DingTalkAppKind:       "internal_app", // the settings API supports internal_app
			AppType:               "internal",     // default for internal_only
			CorpRestrictionPolicy: req.DingTalkConnectCorpRestrictionPolicy,
			InternalCorpID:        req.DingTalkConnectInternalCorpID,
		}
		// An empty policy retains the saved configuration.
		if dingTalkCfg.CorpRestrictionPolicy == "" {
			dingTalkCfg.CorpRestrictionPolicy = previousSettings.DingTalkConnectCorpRestrictionPolicy
		}
		// The internal_only policy requires an internal app type.
		if dingTalkCfg.CorpRestrictionPolicy == "internal_only" {
			dingTalkCfg.AppType = "internal"
		} else {
			dingTalkCfg.AppType = "public"
		}
		if err := authconfig.ValidateDingTalkConfig(dingTalkCfg); err != nil {
			httpx.ErrorWithDetails(c, http.StatusBadRequest, err.Error(), mapDingTalkValidateError(err), nil)
			return
		}

		// Registration bypass applies only to internal_only. Clear it for other policies
		// so switching policies cannot leave bypass enabled.
		if dingTalkCfg.CorpRestrictionPolicy != "internal_only" {
			req.DingTalkConnectBypassRegistration = false
			// Identity synchronization also requires internal_only.
			req.DingTalkConnectSyncCorpEmail = false
			req.DingTalkConnectSyncDisplayName = false
			req.DingTalkConnectSyncDept = false
		}
		// Trim attribute keys and supply defaults for empty values.
		req.DingTalkConnectSyncCorpEmailAttrKey = strings.TrimSpace(req.DingTalkConnectSyncCorpEmailAttrKey)
		if req.DingTalkConnectSyncCorpEmailAttrKey == "" {
			req.DingTalkConnectSyncCorpEmailAttrKey = "dingtalk_email"
		}
		req.DingTalkConnectSyncDisplayNameAttrKey = strings.TrimSpace(req.DingTalkConnectSyncDisplayNameAttrKey)
		if req.DingTalkConnectSyncDisplayNameAttrKey == "" {
			req.DingTalkConnectSyncDisplayNameAttrKey = "dingtalk_name"
		}
		req.DingTalkConnectSyncDeptAttrKey = strings.TrimSpace(req.DingTalkConnectSyncDeptAttrKey)
		if req.DingTalkConnectSyncDeptAttrKey == "" {
			req.DingTalkConnectSyncDeptAttrKey = "dingtalk_department"
		}
		// Trim attribute display names and supply the default Chinese labels when empty.
		req.DingTalkConnectSyncCorpEmailAttrName = strings.TrimSpace(req.DingTalkConnectSyncCorpEmailAttrName)
		if req.DingTalkConnectSyncCorpEmailAttrName == "" {
			req.DingTalkConnectSyncCorpEmailAttrName = "钉钉企业邮箱"
		}
		req.DingTalkConnectSyncDisplayNameAttrName = strings.TrimSpace(req.DingTalkConnectSyncDisplayNameAttrName)
		if req.DingTalkConnectSyncDisplayNameAttrName == "" {
			req.DingTalkConnectSyncDisplayNameAttrName = "钉钉姓名"
		}
		req.DingTalkConnectSyncDeptAttrName = strings.TrimSpace(req.DingTalkConnectSyncDeptAttrName)
		if req.DingTalkConnectSyncDeptAttrName == "" {
			req.DingTalkConnectSyncDeptAttrName = "钉钉部门"
		}
	}

	if req.WeChatConnectEnabled {
		req.WeChatConnectAppID = strings.TrimSpace(req.WeChatConnectAppID)
		req.WeChatConnectAppSecret = strings.TrimSpace(req.WeChatConnectAppSecret)
		req.WeChatConnectOpenAppID = strings.TrimSpace(req.WeChatConnectOpenAppID)
		req.WeChatConnectOpenAppSecret = strings.TrimSpace(req.WeChatConnectOpenAppSecret)
		req.WeChatConnectMPAppID = strings.TrimSpace(req.WeChatConnectMPAppID)
		req.WeChatConnectMPAppSecret = strings.TrimSpace(req.WeChatConnectMPAppSecret)
		req.WeChatConnectMobileAppID = strings.TrimSpace(req.WeChatConnectMobileAppID)
		req.WeChatConnectMobileAppSecret = strings.TrimSpace(req.WeChatConnectMobileAppSecret)
		req.WeChatConnectMode = strings.ToLower(strings.TrimSpace(req.WeChatConnectMode))
		req.WeChatConnectScopes = strings.TrimSpace(req.WeChatConnectScopes)
		req.WeChatConnectRedirectURL = strings.TrimSpace(req.WeChatConnectRedirectURL)
		req.WeChatConnectFrontendRedirectURL = strings.TrimSpace(req.WeChatConnectFrontendRedirectURL)
		req.WeChatConnectAppID = strings.TrimSpace(firstNonEmpty(req.WeChatConnectAppID, previousSettings.WeChatConnectAppID))
		req.WeChatConnectRedirectURL = strings.TrimSpace(firstNonEmpty(req.WeChatConnectRedirectURL, previousSettings.WeChatConnectRedirectURL))
		req.WeChatConnectFrontendRedirectURL = strings.TrimSpace(firstNonEmpty(req.WeChatConnectFrontendRedirectURL, previousSettings.WeChatConnectFrontendRedirectURL))
		if req.WeChatConnectMode == "" {
			req.WeChatConnectMode = strings.ToLower(strings.TrimSpace(previousSettings.WeChatConnectMode))
		}
		if req.WeChatConnectScopes == "" {
			req.WeChatConnectScopes = strings.TrimSpace(previousSettings.WeChatConnectScopes)
		}

		if req.WeChatConnectMPEnabled && req.WeChatConnectMobileEnabled {
			httpx.BadRequest(c, "WeChat Official Account and Mobile App cannot be enabled at the same time")
			return
		}
		if req.WeChatConnectMode != "" {
			switch req.WeChatConnectMode {
			case "open", "mp", "mobile":
			default:
				httpx.BadRequest(c, "WeChat mode must be open, mp, or mobile")
				return
			}
		}
		if !req.WeChatConnectOpenEnabled && !req.WeChatConnectMPEnabled && !req.WeChatConnectMobileEnabled {
			switch req.WeChatConnectMode {
			case "mp":
				req.WeChatConnectMPEnabled = true
			case "mobile":
				req.WeChatConnectMobileEnabled = true
			default:
				req.WeChatConnectOpenEnabled = true
			}
		}
		if req.WeChatConnectMode == "" {
			if req.WeChatConnectMPEnabled {
				req.WeChatConnectMode = "mp"
			} else if req.WeChatConnectMobileEnabled {
				req.WeChatConnectMode = "mobile"
			} else {
				req.WeChatConnectMode = "open"
			}
		}

		req.WeChatConnectOpenAppID = strings.TrimSpace(firstNonEmpty(req.WeChatConnectOpenAppID, req.WeChatConnectAppID, previousSettings.WeChatConnectOpenAppID, previousSettings.WeChatConnectAppID))
		req.WeChatConnectMPAppID = strings.TrimSpace(firstNonEmpty(req.WeChatConnectMPAppID, req.WeChatConnectAppID, previousSettings.WeChatConnectMPAppID, previousSettings.WeChatConnectAppID))
		req.WeChatConnectMobileAppID = strings.TrimSpace(firstNonEmpty(req.WeChatConnectMobileAppID, req.WeChatConnectAppID, previousSettings.WeChatConnectMobileAppID, previousSettings.WeChatConnectAppID))

		if req.WeChatConnectOpenAppSecret == "" {
			req.WeChatConnectOpenAppSecret = strings.TrimSpace(firstNonEmpty(previousSettings.WeChatConnectOpenAppSecret, previousSettings.WeChatConnectAppSecret, req.WeChatConnectAppSecret))
		}
		if req.WeChatConnectMPAppSecret == "" {
			req.WeChatConnectMPAppSecret = strings.TrimSpace(firstNonEmpty(previousSettings.WeChatConnectMPAppSecret, previousSettings.WeChatConnectAppSecret, req.WeChatConnectAppSecret))
		}
		if req.WeChatConnectMobileAppSecret == "" {
			req.WeChatConnectMobileAppSecret = strings.TrimSpace(firstNonEmpty(previousSettings.WeChatConnectMobileAppSecret, previousSettings.WeChatConnectAppSecret, req.WeChatConnectAppSecret))
		}
		if req.WeChatConnectAppSecret == "" {
			req.WeChatConnectAppSecret = strings.TrimSpace(firstNonEmpty(req.WeChatConnectOpenAppSecret, req.WeChatConnectMPAppSecret, req.WeChatConnectMobileAppSecret, previousSettings.WeChatConnectAppSecret))
		}

		if req.WeChatConnectOpenEnabled {
			if req.WeChatConnectOpenAppID == "" {
				httpx.BadRequest(c, "WeChat PC App ID is required when enabled")
				return
			}
			if req.WeChatConnectOpenAppSecret == "" {
				httpx.BadRequest(c, "WeChat PC App Secret is required when enabled")
				return
			}
		}
		if req.WeChatConnectMPEnabled {
			if req.WeChatConnectMPAppID == "" {
				httpx.BadRequest(c, "WeChat Official Account App ID is required when enabled")
				return
			}
			if req.WeChatConnectMPAppSecret == "" {
				httpx.BadRequest(c, "WeChat Official Account App Secret is required when enabled")
				return
			}
		}
		if req.WeChatConnectMobileEnabled {
			if req.WeChatConnectMobileAppID == "" {
				httpx.BadRequest(c, "WeChat Mobile App ID is required when enabled")
				return
			}
			if req.WeChatConnectMobileAppSecret == "" {
				httpx.BadRequest(c, "WeChat Mobile App Secret is required when enabled")
				return
			}
		}

		if req.WeChatConnectScopes == "" {
			if req.WeChatConnectMPEnabled {
				req.WeChatConnectScopes = identity.SettingsDefaultWeChatConnectScopesForMode("mp")
			} else {
				req.WeChatConnectScopes = identity.SettingsDefaultWeChatConnectScopesForMode(req.WeChatConnectMode)
			}
		}
		if req.WeChatConnectOpenEnabled || req.WeChatConnectMPEnabled {
			if req.WeChatConnectRedirectURL == "" {
				httpx.BadRequest(c, "WeChat Redirect URL is required when web oauth is enabled")
				return
			}
			if err := authconfig.ValidateAbsoluteHTTPURL(req.WeChatConnectRedirectURL); err != nil {
				httpx.BadRequest(c, "WeChat Redirect URL must be an absolute http(s) URL")
				return
			}
			if req.WeChatConnectFrontendRedirectURL == "" {
				req.WeChatConnectFrontendRedirectURL = "/auth/wechat/callback"
			}
			if err := authconfig.ValidateFrontendRedirectURL(req.WeChatConnectFrontendRedirectURL); err != nil {
				httpx.BadRequest(c, "WeChat Frontend Redirect URL is invalid")
				return
			}
		}
	}

	// Resolve and validate generic OIDC settings.
	oidcUsePKCE, oidcValidateIDToken, err := h.settingService.OIDCSecurityWriteDefaults(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	if req.OIDCConnectEnabled {
		req.OIDCConnectProviderName = strings.TrimSpace(req.OIDCConnectProviderName)
		req.OIDCConnectClientID = strings.TrimSpace(req.OIDCConnectClientID)
		req.OIDCConnectClientSecret = strings.TrimSpace(req.OIDCConnectClientSecret)
		req.OIDCConnectIssuerURL = strings.TrimSpace(req.OIDCConnectIssuerURL)
		req.OIDCConnectDiscoveryURL = strings.TrimSpace(req.OIDCConnectDiscoveryURL)
		req.OIDCConnectAuthorizeURL = strings.TrimSpace(req.OIDCConnectAuthorizeURL)
		req.OIDCConnectTokenURL = strings.TrimSpace(req.OIDCConnectTokenURL)
		req.OIDCConnectUserInfoURL = strings.TrimSpace(req.OIDCConnectUserInfoURL)
		req.OIDCConnectJWKSURL = strings.TrimSpace(req.OIDCConnectJWKSURL)
		req.OIDCConnectScopes = strings.TrimSpace(req.OIDCConnectScopes)
		req.OIDCConnectRedirectURL = strings.TrimSpace(req.OIDCConnectRedirectURL)
		req.OIDCConnectFrontendRedirectURL = strings.TrimSpace(req.OIDCConnectFrontendRedirectURL)
		req.OIDCConnectTokenAuthMethod = strings.ToLower(strings.TrimSpace(req.OIDCConnectTokenAuthMethod))
		req.OIDCConnectAllowedSigningAlgs = strings.TrimSpace(req.OIDCConnectAllowedSigningAlgs)
		req.OIDCConnectUserInfoEmailPath = strings.TrimSpace(req.OIDCConnectUserInfoEmailPath)
		req.OIDCConnectUserInfoIDPath = strings.TrimSpace(req.OIDCConnectUserInfoIDPath)
		req.OIDCConnectUserInfoUsernamePath = strings.TrimSpace(req.OIDCConnectUserInfoUsernamePath)
		req.OIDCConnectProviderName = strings.TrimSpace(firstNonEmpty(req.OIDCConnectProviderName, previousSettings.OIDCConnectProviderName, "OIDC"))
		req.OIDCConnectClientID = strings.TrimSpace(firstNonEmpty(req.OIDCConnectClientID, previousSettings.OIDCConnectClientID))
		req.OIDCConnectIssuerURL = strings.TrimSpace(firstNonEmpty(req.OIDCConnectIssuerURL, previousSettings.OIDCConnectIssuerURL))
		req.OIDCConnectDiscoveryURL = strings.TrimSpace(firstNonEmpty(req.OIDCConnectDiscoveryURL, previousSettings.OIDCConnectDiscoveryURL))
		req.OIDCConnectAuthorizeURL = strings.TrimSpace(firstNonEmpty(req.OIDCConnectAuthorizeURL, previousSettings.OIDCConnectAuthorizeURL))
		req.OIDCConnectTokenURL = strings.TrimSpace(firstNonEmpty(req.OIDCConnectTokenURL, previousSettings.OIDCConnectTokenURL))
		req.OIDCConnectUserInfoURL = strings.TrimSpace(firstNonEmpty(req.OIDCConnectUserInfoURL, previousSettings.OIDCConnectUserInfoURL))
		req.OIDCConnectJWKSURL = strings.TrimSpace(firstNonEmpty(req.OIDCConnectJWKSURL, previousSettings.OIDCConnectJWKSURL))
		req.OIDCConnectScopes = strings.TrimSpace(firstNonEmpty(req.OIDCConnectScopes, previousSettings.OIDCConnectScopes, "openid email profile"))
		req.OIDCConnectRedirectURL = strings.TrimSpace(firstNonEmpty(req.OIDCConnectRedirectURL, previousSettings.OIDCConnectRedirectURL))
		req.OIDCConnectFrontendRedirectURL = strings.TrimSpace(firstNonEmpty(req.OIDCConnectFrontendRedirectURL, previousSettings.OIDCConnectFrontendRedirectURL, "/auth/oidc/callback"))
		req.OIDCConnectTokenAuthMethod = strings.ToLower(strings.TrimSpace(firstNonEmpty(req.OIDCConnectTokenAuthMethod, previousSettings.OIDCConnectTokenAuthMethod, "client_secret_post")))
		req.OIDCConnectAllowedSigningAlgs = strings.TrimSpace(firstNonEmpty(req.OIDCConnectAllowedSigningAlgs, previousSettings.OIDCConnectAllowedSigningAlgs, "RS256,ES256,PS256"))
		req.OIDCConnectUserInfoEmailPath = strings.TrimSpace(firstNonEmpty(req.OIDCConnectUserInfoEmailPath, previousSettings.OIDCConnectUserInfoEmailPath))
		req.OIDCConnectUserInfoIDPath = strings.TrimSpace(firstNonEmpty(req.OIDCConnectUserInfoIDPath, previousSettings.OIDCConnectUserInfoIDPath))
		req.OIDCConnectUserInfoUsernamePath = strings.TrimSpace(firstNonEmpty(req.OIDCConnectUserInfoUsernamePath, previousSettings.OIDCConnectUserInfoUsernamePath))
		if req.OIDCConnectUsePKCE != nil {
			oidcUsePKCE = *req.OIDCConnectUsePKCE
		}
		if req.OIDCConnectValidateIDToken != nil {
			oidcValidateIDToken = *req.OIDCConnectValidateIDToken
		}
		if req.OIDCConnectClockSkewSeconds == 0 {
			req.OIDCConnectClockSkewSeconds = previousSettings.OIDCConnectClockSkewSeconds
			if req.OIDCConnectClockSkewSeconds == 0 {
				req.OIDCConnectClockSkewSeconds = 120
			}
		}

		if req.OIDCConnectClientID == "" {
			httpx.BadRequest(c, "OIDC Client ID is required when enabled")
			return
		}
		if req.OIDCConnectIssuerURL == "" {
			httpx.BadRequest(c, "OIDC Issuer URL is required when enabled")
			return
		}
		if err := authconfig.ValidateAbsoluteHTTPURL(req.OIDCConnectIssuerURL); err != nil {
			httpx.BadRequest(c, "OIDC Issuer URL must be an absolute http(s) URL")
			return
		}
		if req.OIDCConnectDiscoveryURL != "" {
			if err := authconfig.ValidateAbsoluteHTTPURL(req.OIDCConnectDiscoveryURL); err != nil {
				httpx.BadRequest(c, "OIDC Discovery URL must be an absolute http(s) URL")
				return
			}
		}
		if req.OIDCConnectAuthorizeURL != "" {
			if err := authconfig.ValidateAbsoluteHTTPURL(req.OIDCConnectAuthorizeURL); err != nil {
				httpx.BadRequest(c, "OIDC Authorize URL must be an absolute http(s) URL")
				return
			}
		}
		if req.OIDCConnectTokenURL != "" {
			if err := authconfig.ValidateAbsoluteHTTPURL(req.OIDCConnectTokenURL); err != nil {
				httpx.BadRequest(c, "OIDC Token URL must be an absolute http(s) URL")
				return
			}
		}
		if req.OIDCConnectUserInfoURL != "" {
			if err := authconfig.ValidateAbsoluteHTTPURL(req.OIDCConnectUserInfoURL); err != nil {
				httpx.BadRequest(c, "OIDC UserInfo URL must be an absolute http(s) URL")
				return
			}
		}
		if req.OIDCConnectRedirectURL == "" {
			httpx.BadRequest(c, "OIDC Redirect URL is required when enabled")
			return
		}
		if err := authconfig.ValidateAbsoluteHTTPURL(req.OIDCConnectRedirectURL); err != nil {
			httpx.BadRequest(c, "OIDC Redirect URL must be an absolute http(s) URL")
			return
		}
		if req.OIDCConnectFrontendRedirectURL == "" {
			httpx.BadRequest(c, "OIDC Frontend Redirect URL is required when enabled")
			return
		}
		if err := authconfig.ValidateFrontendRedirectURL(req.OIDCConnectFrontendRedirectURL); err != nil {
			httpx.BadRequest(c, "OIDC Frontend Redirect URL is invalid")
			return
		}
		if !scopesContainOpenID(req.OIDCConnectScopes) {
			httpx.BadRequest(c, "OIDC scopes must contain openid")
			return
		}
		switch req.OIDCConnectTokenAuthMethod {
		case "", "client_secret_post", "client_secret_basic", "none":
		default:
			httpx.BadRequest(c, "OIDC Token Auth Method must be one of client_secret_post/client_secret_basic/none")
			return
		}
		if req.OIDCConnectClockSkewSeconds < 0 || req.OIDCConnectClockSkewSeconds > 600 {
			httpx.BadRequest(c, "OIDC clock skew seconds must be between 0 and 600")
			return
		}
		if oidcValidateIDToken && req.OIDCConnectAllowedSigningAlgs == "" {
			httpx.BadRequest(c, "OIDC Allowed Signing Algs is required when validate_id_token=true")
			return
		}
		if req.OIDCConnectJWKSURL != "" {
			if err := authconfig.ValidateAbsoluteHTTPURL(req.OIDCConnectJWKSURL); err != nil {
				httpx.BadRequest(c, "OIDC JWKS URL must be an absolute http(s) URL")
				return
			}
		}
		if req.OIDCConnectTokenAuthMethod == "" || req.OIDCConnectTokenAuthMethod == "client_secret_post" || req.OIDCConnectTokenAuthMethod == "client_secret_basic" {
			if req.OIDCConnectClientSecret == "" {
				if previousSettings.OIDCConnectClientSecret == "" {
					httpx.BadRequest(c, "OIDC Client Secret is required when enabled")
					return
				}
				req.OIDCConnectClientSecret = previousSettings.OIDCConnectClientSecret
			}
		}
	}

	req.GitHubOAuthClientID = strings.TrimSpace(req.GitHubOAuthClientID)
	req.GitHubOAuthClientSecret = strings.TrimSpace(req.GitHubOAuthClientSecret)
	req.GitHubOAuthRedirectURL = strings.TrimSpace(req.GitHubOAuthRedirectURL)
	req.GitHubOAuthFrontendRedirectURL = strings.TrimSpace(req.GitHubOAuthFrontendRedirectURL)
	if req.GitHubOAuthEnabled {
		req.GitHubOAuthClientID = strings.TrimSpace(firstNonEmpty(req.GitHubOAuthClientID, previousSettings.GitHubOAuthClientID))
		req.GitHubOAuthRedirectURL = strings.TrimSpace(firstNonEmpty(req.GitHubOAuthRedirectURL, previousSettings.GitHubOAuthRedirectURL))
		req.GitHubOAuthFrontendRedirectURL = strings.TrimSpace(firstNonEmpty(req.GitHubOAuthFrontendRedirectURL, previousSettings.GitHubOAuthFrontendRedirectURL, "/auth/oauth/callback"))
		if req.GitHubOAuthClientID == "" {
			httpx.BadRequest(c, "GitHub OAuth Client ID is required when enabled")
			return
		}
		if req.GitHubOAuthClientSecret == "" {
			if previousSettings.GitHubOAuthClientSecret == "" {
				httpx.BadRequest(c, "GitHub OAuth Client Secret is required when enabled")
				return
			}
			req.GitHubOAuthClientSecret = previousSettings.GitHubOAuthClientSecret
		}
		if req.GitHubOAuthRedirectURL == "" {
			httpx.BadRequest(c, "GitHub OAuth Redirect URL is required when enabled")
			return
		}
		if err := authconfig.ValidateAbsoluteHTTPURL(req.GitHubOAuthRedirectURL); err != nil {
			httpx.BadRequest(c, "GitHub OAuth Redirect URL must be an absolute http(s) URL")
			return
		}
		if req.GitHubOAuthFrontendRedirectURL == "" {
			httpx.BadRequest(c, "GitHub OAuth Frontend Redirect URL is required when enabled")
			return
		}
		if err := authconfig.ValidateFrontendRedirectURL(req.GitHubOAuthFrontendRedirectURL); err != nil {
			httpx.BadRequest(c, "GitHub OAuth Frontend Redirect URL is invalid")
			return
		}
	}

	req.GoogleOAuthClientID = strings.TrimSpace(req.GoogleOAuthClientID)
	req.GoogleOAuthClientSecret = strings.TrimSpace(req.GoogleOAuthClientSecret)
	req.GoogleOAuthRedirectURL = strings.TrimSpace(req.GoogleOAuthRedirectURL)
	req.GoogleOAuthFrontendRedirectURL = strings.TrimSpace(req.GoogleOAuthFrontendRedirectURL)
	if req.GoogleOAuthEnabled {
		req.GoogleOAuthClientID = strings.TrimSpace(firstNonEmpty(req.GoogleOAuthClientID, previousSettings.GoogleOAuthClientID))
		req.GoogleOAuthRedirectURL = strings.TrimSpace(firstNonEmpty(req.GoogleOAuthRedirectURL, previousSettings.GoogleOAuthRedirectURL))
		req.GoogleOAuthFrontendRedirectURL = strings.TrimSpace(firstNonEmpty(req.GoogleOAuthFrontendRedirectURL, previousSettings.GoogleOAuthFrontendRedirectURL, "/auth/oauth/callback"))
		if req.GoogleOAuthClientID == "" {
			httpx.BadRequest(c, "Google OAuth Client ID is required when enabled")
			return
		}
		if req.GoogleOAuthClientSecret == "" {
			if previousSettings.GoogleOAuthClientSecret == "" {
				httpx.BadRequest(c, "Google OAuth Client Secret is required when enabled")
				return
			}
			req.GoogleOAuthClientSecret = previousSettings.GoogleOAuthClientSecret
		}
		if req.GoogleOAuthRedirectURL == "" {
			httpx.BadRequest(c, "Google OAuth Redirect URL is required when enabled")
			return
		}
		if err := authconfig.ValidateAbsoluteHTTPURL(req.GoogleOAuthRedirectURL); err != nil {
			httpx.BadRequest(c, "Google OAuth Redirect URL must be an absolute http(s) URL")
			return
		}
		if req.GoogleOAuthFrontendRedirectURL == "" {
			httpx.BadRequest(c, "Google OAuth Frontend Redirect URL is required when enabled")
			return
		}
		if err := authconfig.ValidateFrontendRedirectURL(req.GoogleOAuthFrontendRedirectURL); err != nil {
			httpx.BadRequest(c, "Google OAuth Frontend Redirect URL is invalid")
			return
		}
	}

	// Merge subscription purchase page settings.
	purchaseEnabled := previousSettings.PurchaseSubscriptionEnabled
	if req.PurchaseSubscriptionEnabled != nil {
		purchaseEnabled = *req.PurchaseSubscriptionEnabled
	}
	purchaseURL := previousSettings.PurchaseSubscriptionURL
	if req.PurchaseSubscriptionURL != nil {
		purchaseURL = strings.TrimSpace(*req.PurchaseSubscriptionURL)
	}

	// An enabled purchase page requires a nonempty valid URL.
	// When disabled, an empty URL is allowed; a supplied URL still needs validation.
	if purchaseEnabled {
		if purchaseURL == "" {
			httpx.BadRequest(c, "Purchase Subscription URL is required when enabled")
			return
		}
		if err := authconfig.ValidateAbsoluteHTTPURL(purchaseURL); err != nil {
			httpx.BadRequest(c, "Purchase Subscription URL must be an absolute http(s) URL")
			return
		}
	} else if purchaseURL != "" {
		if err := authconfig.ValidateAbsoluteHTTPURL(purchaseURL); err != nil {
			httpx.BadRequest(c, "Purchase Subscription URL must be an absolute http(s) URL")
			return
		}
	}

	// Validate the frontend URL when supplied.
	req.FrontendURL = strings.TrimSpace(req.FrontendURL)
	if req.FrontendURL != "" {
		if err := authconfig.ValidateAbsoluteHTTPURL(req.FrontendURL); err != nil {
			httpx.BadRequest(c, "Frontend URL must be an absolute http(s) URL")
			return
		}
	}

	// Validate custom menu items.
	const (
		maxCustomMenuItems    = 20
		maxMenuItemLabelLen   = 50
		maxMenuItemURLLen     = 2048
		maxMenuItemIconSVGLen = 10 * 1024 // 10KB
		maxMenuItemIDLen      = 32
	)

	customMenuJSON := previousSettings.CustomMenuItems
	if req.CustomMenuItems != nil {
		items := *req.CustomMenuItems
		if len(items) > maxCustomMenuItems {
			httpx.BadRequest(c, "Too many custom menu items (max 20)")
			return
		}
		for i, item := range items {
			if strings.TrimSpace(item.Label) == "" {
				httpx.BadRequest(c, "Custom menu item label is required")
				return
			}
			if len(item.Label) > maxMenuItemLabelLen {
				httpx.BadRequest(c, "Custom menu item label is too long (max 50 characters)")
				return
			}
			urlTrimmed := strings.TrimSpace(item.URL)
			if slug, ok := strings.CutPrefix(urlTrimmed, "md:"); ok {
				// Markdown entries use md:<slug> with the same slug rules as /api/v1/pages/:slug.
				if slug == "" {
					httpx.BadRequest(c, "Custom menu item markdown slug cannot be empty (use md:slug format)")
					return
				}
				if len(slug) > 64 || !markdownMenuSlugPattern.MatchString(slug) {
					httpx.BadRequest(c, "Custom menu item markdown slug contains invalid characters")
					return
				}
			} else {
				if urlTrimmed == "" {
					httpx.BadRequest(c, "Custom menu item URL is required (use md:slug for markdown pages)")
					return
				}
				if len(urlTrimmed) > maxMenuItemURLLen {
					httpx.BadRequest(c, "Custom menu item URL is too long (max 2048 characters)")
					return
				}
				if err := authconfig.ValidateAbsoluteHTTPURL(urlTrimmed); err != nil {
					httpx.BadRequest(c, "Custom menu item URL must be an absolute http(s) URL or md:<slug>")
					return
				}
			}
			// Store the trimmed URL so leading whitespace cannot prevent md: recognition at runtime.
			items[i].URL = urlTrimmed
			if item.Visibility != "user" && item.Visibility != "admin" {
				httpx.BadRequest(c, "Custom menu item visibility must be 'user' or 'admin'")
				return
			}
			if len(item.IconSVG) > maxMenuItemIconSVGLen {
				httpx.BadRequest(c, "Custom menu item icon SVG is too large (max 10KB)")
				return
			}
			// Auto-generate ID if missing
			if strings.TrimSpace(item.ID) == "" {
				id, err := generateMenuItemID()
				if err != nil {
					httpx.Error(c, http.StatusInternalServerError, "Failed to generate menu item ID")
					return
				}
				items[i].ID = id
			} else if len(item.ID) > maxMenuItemIDLen {
				httpx.BadRequest(c, "Custom menu item ID is too long (max 32 characters)")
				return
			} else if !menuItemIDPattern.MatchString(item.ID) {
				httpx.BadRequest(c, "Custom menu item ID contains invalid characters (only a-z, A-Z, 0-9, - and _ are allowed)")
				return
			}
		}
		// ID uniqueness check
		seen := make(map[string]struct{}, len(items))
		for _, item := range items {
			if _, exists := seen[item.ID]; exists {
				httpx.BadRequest(c, "Duplicate custom menu item ID: "+item.ID)
				return
			}
			seen[item.ID] = struct{}{}
		}
		menuBytes, err := json.Marshal(items)
		if err != nil {
			httpx.BadRequest(c, "Failed to serialize custom menu items")
			return
		}
		customMenuJSON = string(menuBytes)
	}

	// Validate custom endpoints.
	const (
		maxCustomEndpoints        = 10
		maxEndpointNameLen        = 50
		maxEndpointURLLen         = 2048
		maxEndpointDescriptionLen = 200
	)

	customEndpointsJSON := previousSettings.CustomEndpoints
	if req.CustomEndpoints != nil {
		endpoints := *req.CustomEndpoints
		if len(endpoints) > maxCustomEndpoints {
			httpx.BadRequest(c, "Too many custom endpoints (max 10)")
			return
		}
		for _, ep := range endpoints {
			if strings.TrimSpace(ep.Name) == "" {
				httpx.BadRequest(c, "Custom endpoint name is required")
				return
			}
			if len(ep.Name) > maxEndpointNameLen {
				httpx.BadRequest(c, "Custom endpoint name is too long (max 50 characters)")
				return
			}
			if strings.TrimSpace(ep.Endpoint) == "" {
				httpx.BadRequest(c, "Custom endpoint URL is required")
				return
			}
			if len(ep.Endpoint) > maxEndpointURLLen {
				httpx.BadRequest(c, "Custom endpoint URL is too long (max 2048 characters)")
				return
			}
			if err := authconfig.ValidateAbsoluteHTTPURL(strings.TrimSpace(ep.Endpoint)); err != nil {
				httpx.BadRequest(c, "Custom endpoint URL must be an absolute http(s) URL")
				return
			}
			if len(ep.Description) > maxEndpointDescriptionLen {
				httpx.BadRequest(c, "Custom endpoint description is too long (max 200 characters)")
				return
			}
		}
		endpointBytes, err := json.Marshal(endpoints)
		if err != nil {
			httpx.BadRequest(c, "Failed to serialize custom endpoints")
			return
		}
		customEndpointsJSON = string(endpointBytes)
	}

	// Validate footer link groups.
	const (
		maxFooterGroups      = 6
		maxFooterLinksPerGrp = 10
		maxFooterLabelLen    = 50
		maxFooterURLLen      = 2048
		maxFooterTextLen     = 500
	)

	footerLinksJSON := previousSettings.FooterLinks
	if req.FooterLinks != nil {
		groups := *req.FooterLinks
		if len(groups) > maxFooterGroups {
			httpx.BadRequest(c, "Too many footer link groups (max 6)")
			return
		}
		for _, group := range groups {
			if strings.TrimSpace(group.Title) == "" {
				httpx.BadRequest(c, "Footer link group title is required")
				return
			}
			if len(group.Title) > maxFooterLabelLen {
				httpx.BadRequest(c, "Footer link group title is too long (max 50 characters)")
				return
			}
			if len(group.Links) > maxFooterLinksPerGrp {
				httpx.BadRequest(c, "Too many links in footer group (max 10)")
				return
			}
			for _, link := range group.Links {
				if strings.TrimSpace(link.Label) == "" {
					httpx.BadRequest(c, "Footer link label is required")
					return
				}
				if len(link.Label) > maxFooterLabelLen {
					httpx.BadRequest(c, "Footer link label is too long (max 50 characters)")
					return
				}
				trimmedURL := strings.TrimSpace(link.URL)
				if trimmedURL == "" {
					httpx.BadRequest(c, "Footer link URL is required")
					return
				}
				if len(trimmedURL) > maxFooterURLLen {
					httpx.BadRequest(c, "Footer link URL is too long (max 2048 characters)")
					return
				}
				// Accept absolute HTTP(S) URLs or values starting with /.
				if !strings.HasPrefix(trimmedURL, "/") {
					if err := authconfig.ValidateAbsoluteHTTPURL(trimmedURL); err != nil {
						httpx.BadRequest(c, "Footer link URL must be an absolute http(s) URL or a path starting with /")
						return
					}
				}
			}
		}
		groupBytes, err := json.Marshal(groups)
		if err != nil {
			httpx.BadRequest(c, "Failed to serialize footer links")
			return
		}
		footerLinksJSON = string(groupBytes)
	}

	// Limit, trim, and deduplicate featured models while retaining their configured order.
	const maxHomeFeaturedModels = 12
	const maxHomeFeaturedModelIDLen = 200

	homeFeaturedModelsJSON := previousSettings.HomeFeaturedModels
	if req.HomeFeaturedModels != nil {
		models := *req.HomeFeaturedModels
		if len(models) > maxHomeFeaturedModels {
			httpx.BadRequest(c, "Too many home featured models (max 12)")
			return
		}
		seen := make(map[string]struct{}, len(models))
		normalized := make([]string, 0, len(models))
		for _, model := range models {
			trimmed := strings.TrimSpace(model)
			if trimmed == "" {
				httpx.BadRequest(c, "Home featured model ID is required")
				return
			}
			if len(trimmed) > maxHomeFeaturedModelIDLen {
				httpx.BadRequest(c, "Home featured model ID is too long (max 200 characters)")
				return
			}
			if _, duplicate := seen[trimmed]; duplicate {
				continue
			}
			seen[trimmed] = struct{}{}
			normalized = append(normalized, trimmed)
		}
		modelBytes, err := json.Marshal(normalized)
		if err != nil {
			httpx.BadRequest(c, "Failed to serialize home featured models")
			return
		}
		homeFeaturedModelsJSON = string(modelBytes)
	}

	footerText := previousSettings.FooterText
	if req.FooterText != nil {
		trimmed := strings.TrimSpace(*req.FooterText)
		if len(trimmed) > maxFooterTextLen {
			httpx.BadRequest(c, "Footer text is too long (max 500 characters)")
			return
		}
		footerText = trimmed
	}

	// Clamp the metrics collection interval to 60-3600 seconds.
	if req.OpsMetricsIntervalSeconds != nil {
		v := min(max(*req.OpsMetricsIntervalSeconds, 60), 3600)
		req.OpsMetricsIntervalSeconds = &v
	}
	defaultSubscriptions := make([]billing.DefaultSubscriptionSetting, 0, len(req.DefaultSubscriptions))
	for _, sub := range req.DefaultSubscriptions {
		defaultSubscriptions = append(defaultSubscriptions, billing.DefaultSubscriptionSetting{
			PlanID: sub.PlanID,
		})
	}

	// An empty minimum version disables the lower bound; otherwise require three numeric components.
	if req.MinClaudeCodeVersion != "" {
		if !semverPattern.MatchString(req.MinClaudeCodeVersion) {
			httpx.Error(c, http.StatusBadRequest, "min_claude_code_version must be empty or a valid semver (e.g. 2.1.63)")
			return
		}
	}

	// An empty maximum version disables the upper bound; otherwise require three numeric components.
	if req.MaxClaudeCodeVersion != "" {
		if !semverPattern.MatchString(req.MaxClaudeCodeVersion) {
			httpx.Error(c, http.StatusBadRequest, "max_claude_code_version must be empty or a valid semver (e.g. 3.0.0)")
			return
		}
	}
	if req.AntigravityUserAgentVersion != nil {
		normalized := strings.TrimSpace(*req.AntigravityUserAgentVersion)
		req.AntigravityUserAgentVersion = &normalized
		if normalized != "" && !semverPattern.MatchString(normalized) {
			httpx.Error(c, http.StatusBadRequest, "antigravity_user_agent_version must be empty or a valid semver (e.g. 1.23.2)")
			return
		}
	}
	if req.OpenAICodexUserAgent != nil {
		normalized := strings.TrimSpace(*req.OpenAICodexUserAgent)
		req.OpenAICodexUserAgent = &normalized
		// Limit the header length while allowing operators to choose the Codex version format.
		if len(normalized) > 512 {
			httpx.Error(c, http.StatusBadRequest, "openai_codex_user_agent must be at most 512 characters")
			return
		}
	}

	// When both bounds are supplied, the maximum must be at least the minimum.
	if req.MinClaudeCodeVersion != "" && req.MaxClaudeCodeVersion != "" {
		if clientmeta.CompareVersions(req.MaxClaudeCodeVersion, req.MinClaudeCodeVersion) < 0 {
			httpx.Error(c, http.StatusBadRequest, "max_claude_code_version must be greater than or equal to min_claude_code_version")
			return
		}
	}
	if req.CyberSessionBlockTTLSeconds != nil && *req.CyberSessionBlockTTLSeconds <= 0 {
		httpx.BadRequest(c, "cyber_session_block_ttl_seconds must be > 0")
		return
	}
	if req.CreativeWorkerCount != nil && *req.CreativeWorkerCount <= 0 {
		httpx.BadRequest(c, "creative_worker_count must be > 0")
		return
	}

	settings := &composite.Snapshot{
		ProviderSchedulingThresholds: req.ProviderSchedulingThresholds,

		RegistrationEnabled:                 req.RegistrationEnabled,
		EmailVerifyEnabled:                  req.EmailVerifyEnabled,
		RegistrationEmailSuffixWhitelist:    req.RegistrationEmailSuffixWhitelist,
		RegistrationEmailNormalization:      req.RegistrationEmailNormalization,
		RegistrationEmailDomainQuotaEnabled: registrationEmailDomainQuotaEnabled,
		UserEmailChangeEnabled:              userEmailChangeEnabled,
		PromoCodeEnabled:                    req.PromoCodeEnabled,
		PasswordResetEnabled:                req.PasswordResetEnabled,
		FrontendURL:                         req.FrontendURL,
		InvitationCodeEnabled:               req.InvitationCodeEnabled,
		TotpEnabled:                         req.TotpEnabled,
		SessionBindingEnabled:               sessionBindingEnabled,
		StepUpEnabled:                       stepUpEnabled,
		AuditLogRetentionDays:               req.AuditLogRetentionDays,
		LoginAgreementEnabled:               req.LoginAgreementEnabled,
		LoginAgreementMode:                  loginAgreementMode,
		LoginAgreementUpdatedAt:             loginAgreementUpdatedAt,
		LoginAgreementDocuments:             loginAgreementDocuments,
		SMTPHost:                            req.SMTPHost,
		SMTPPort:                            req.SMTPPort,
		SMTPUsername:                        req.SMTPUsername,
		SMTPPassword:                        req.SMTPPassword,
		SMTPFrom:                            req.SMTPFrom,
		SMTPFromName:                        req.SMTPFromName,
		SMTPUseTLS:                          req.SMTPUseTLS,
		TurnstileEnabled:                    req.TurnstileEnabled,
		TurnstileSiteKey:                    req.TurnstileSiteKey,
		TurnstileSecretKey:                  req.TurnstileSecretKey,
		TencentCaptchaEnabled:               req.TencentCaptchaEnabled,
		TencentCaptchaAppID:                 req.TencentCaptchaAppID,
		TencentCaptchaAppSecretKey:          req.TencentCaptchaAppSecretKey,
		TencentCaptchaCloudSecretID:         req.TencentCaptchaCloudSecretID,
		TencentCaptchaCloudSecretKey:        req.TencentCaptchaCloudSecretKey,
		TencentCaptchaRegion:                req.TencentCaptchaRegion,
		AliyunCaptchaEnabled:                req.AliyunCaptchaEnabled,
		AliyunCaptchaAccessKeyID:            req.AliyunCaptchaAccessKeyID,
		AliyunCaptchaAccessKeySecret:        req.AliyunCaptchaAccessKeySecret,
		AliyunCaptchaSceneID:                req.AliyunCaptchaSceneID,
		AliyunCaptchaPrefix:                 req.AliyunCaptchaPrefix,
		AliyunCaptchaRegion:                 req.AliyunCaptchaRegion,
		APIKeyACLTrustForwardedIP: func() bool {
			if req.APIKeyACLTrustForwardedIP != nil {
				return *req.APIKeyACLTrustForwardedIP
			}
			return previousSettings.APIKeyACLTrustForwardedIP
		}(),
		ForwardedClientIPHeaders:               forwardedClientIPHeaders,
		LinuxDoConnectEnabled:                  req.LinuxDoConnectEnabled,
		LinuxDoConnectClientID:                 req.LinuxDoConnectClientID,
		LinuxDoConnectClientSecret:             req.LinuxDoConnectClientSecret,
		LinuxDoConnectRedirectURL:              req.LinuxDoConnectRedirectURL,
		DingTalkConnectEnabled:                 req.DingTalkConnectEnabled,
		DingTalkConnectClientID:                req.DingTalkConnectClientID,
		DingTalkConnectClientSecret:            req.DingTalkConnectClientSecret,
		DingTalkConnectRedirectURL:             req.DingTalkConnectRedirectURL,
		DingTalkConnectCorpRestrictionPolicy:   req.DingTalkConnectCorpRestrictionPolicy,
		DingTalkConnectInternalCorpID:          req.DingTalkConnectInternalCorpID,
		DingTalkConnectBypassRegistration:      req.DingTalkConnectBypassRegistration,
		DingTalkConnectSyncCorpEmail:           req.DingTalkConnectSyncCorpEmail,
		DingTalkConnectSyncDisplayName:         req.DingTalkConnectSyncDisplayName,
		DingTalkConnectSyncDept:                req.DingTalkConnectSyncDept,
		DingTalkConnectSyncCorpEmailAttrKey:    req.DingTalkConnectSyncCorpEmailAttrKey,
		DingTalkConnectSyncDisplayNameAttrKey:  req.DingTalkConnectSyncDisplayNameAttrKey,
		DingTalkConnectSyncDeptAttrKey:         req.DingTalkConnectSyncDeptAttrKey,
		DingTalkConnectSyncCorpEmailAttrName:   req.DingTalkConnectSyncCorpEmailAttrName,
		DingTalkConnectSyncDisplayNameAttrName: req.DingTalkConnectSyncDisplayNameAttrName,
		DingTalkConnectSyncDeptAttrName:        req.DingTalkConnectSyncDeptAttrName,
		WeChatConnectEnabled:                   req.WeChatConnectEnabled,
		WeChatConnectAppID:                     req.WeChatConnectAppID,
		WeChatConnectAppSecret:                 req.WeChatConnectAppSecret,
		WeChatConnectOpenAppID:                 req.WeChatConnectOpenAppID,
		WeChatConnectOpenAppSecret:             req.WeChatConnectOpenAppSecret,
		WeChatConnectMPAppID:                   req.WeChatConnectMPAppID,
		WeChatConnectMPAppSecret:               req.WeChatConnectMPAppSecret,
		WeChatConnectMobileAppID:               req.WeChatConnectMobileAppID,
		WeChatConnectMobileAppSecret:           req.WeChatConnectMobileAppSecret,
		WeChatConnectOpenEnabled:               req.WeChatConnectOpenEnabled,
		WeChatConnectMPEnabled:                 req.WeChatConnectMPEnabled,
		WeChatConnectMobileEnabled:             req.WeChatConnectMobileEnabled,
		WeChatConnectMode:                      req.WeChatConnectMode,
		WeChatConnectScopes:                    req.WeChatConnectScopes,
		WeChatConnectRedirectURL:               req.WeChatConnectRedirectURL,
		WeChatConnectFrontendRedirectURL:       req.WeChatConnectFrontendRedirectURL,
		OIDCConnectEnabled:                     req.OIDCConnectEnabled,
		OIDCConnectProviderName:                req.OIDCConnectProviderName,
		OIDCConnectClientID:                    req.OIDCConnectClientID,
		OIDCConnectClientSecret:                req.OIDCConnectClientSecret,
		OIDCConnectIssuerURL:                   req.OIDCConnectIssuerURL,
		OIDCConnectDiscoveryURL:                req.OIDCConnectDiscoveryURL,
		OIDCConnectAuthorizeURL:                req.OIDCConnectAuthorizeURL,
		OIDCConnectTokenURL:                    req.OIDCConnectTokenURL,
		OIDCConnectUserInfoURL:                 req.OIDCConnectUserInfoURL,
		OIDCConnectJWKSURL:                     req.OIDCConnectJWKSURL,
		OIDCConnectScopes:                      req.OIDCConnectScopes,
		OIDCConnectRedirectURL:                 req.OIDCConnectRedirectURL,
		OIDCConnectFrontendRedirectURL:         req.OIDCConnectFrontendRedirectURL,
		OIDCConnectTokenAuthMethod:             req.OIDCConnectTokenAuthMethod,
		OIDCConnectUsePKCE:                     oidcUsePKCE,
		OIDCConnectValidateIDToken:             oidcValidateIDToken,
		OIDCConnectAllowedSigningAlgs:          req.OIDCConnectAllowedSigningAlgs,
		OIDCConnectClockSkewSeconds:            req.OIDCConnectClockSkewSeconds,
		OIDCConnectRequireEmailVerified:        req.OIDCConnectRequireEmailVerified,
		OIDCConnectUserInfoEmailPath:           req.OIDCConnectUserInfoEmailPath,
		OIDCConnectUserInfoIDPath:              req.OIDCConnectUserInfoIDPath,
		OIDCConnectUserInfoUsernamePath:        req.OIDCConnectUserInfoUsernamePath,
		GitHubOAuthEnabled:                     req.GitHubOAuthEnabled,
		GitHubOAuthClientID:                    req.GitHubOAuthClientID,
		GitHubOAuthClientSecret:                req.GitHubOAuthClientSecret,
		GitHubOAuthRedirectURL:                 req.GitHubOAuthRedirectURL,
		GitHubOAuthFrontendRedirectURL:         req.GitHubOAuthFrontendRedirectURL,
		GoogleOAuthEnabled:                     req.GoogleOAuthEnabled,
		GoogleOneTapEnabled:                    req.GoogleOneTapEnabled,
		GoogleOAuthClientID:                    req.GoogleOAuthClientID,
		GoogleOAuthClientSecret:                req.GoogleOAuthClientSecret,
		GoogleOAuthRedirectURL:                 req.GoogleOAuthRedirectURL,
		GoogleOAuthFrontendRedirectURL:         req.GoogleOAuthFrontendRedirectURL,
		LocalizedSettings:                      req.LocalizedSettings,
		SiteTexts:                              req.SiteTexts,
		DefaultLocale:                          req.DefaultLocale,
		SiteName:                               req.SiteName,
		SiteLogo:                               req.SiteLogo,
		SiteSubtitle:                           req.SiteSubtitle,
		APIBaseURL:                             req.APIBaseURL,
		ContactInfo:                            req.ContactInfo,
		DocURL:                                 req.DocURL,
		HomeContent:                            req.HomeContent,
		HideCcsImportButton:                    req.HideCcsImportButton,
		PurchaseSubscriptionEnabled:            purchaseEnabled,
		PurchaseSubscriptionURL:                purchaseURL,
		TableDefaultPageSize:                   req.TableDefaultPageSize,
		TablePageSizeOptions:                   req.TablePageSizeOptions,
		UsageRankingLimit:                      usageRanking.Limit,
		UsageRankingEnabled:                    usageRanking.Enabled,
		UsageRankingSortBy:                     string(usageRanking.SortBy),
		UsageRankingShowTotalTokens:            usageRanking.ShowTotalTokens,
		UsageRankingShowRequests:               usageRanking.ShowRequests,
		UsageRankingShowActualCost:             usageRanking.ShowActualCost,
		CustomMenuItems:                        customMenuJSON,
		CustomEndpoints:                        customEndpointsJSON,
		FooterLinks:                            footerLinksJSON,
		FooterText:                             footerText,
		HomeFeaturedModels:                     homeFeaturedModelsJSON,
		DefaultConcurrency:                     req.DefaultConcurrency,
		DefaultBalance:                         req.DefaultBalance,
		AffiliateEnabled:                       req.AffiliateEnabled,
		AffiliateRebateRate:                    req.AffiliateRebateRate,
		AffiliateRebateFreezeHours:             req.AffiliateRebateFreezeHours,
		AffiliateRebateDurationDays:            req.AffiliateRebateDurationDays,
		AffiliateRebatePerInviteeCap:           req.AffiliateRebatePerInviteeCap,
		AdminRechargeRebateEnabled:             adminRechargeRebateEnabled,
		TeamEnabled: func() bool {
			if req.TeamEnabled != nil {
				return *req.TeamEnabled
			}
			return previousSettings.TeamEnabled
		}(),
		CreativeEnabled: func() bool {
			if req.CreativeEnabled != nil {
				return *req.CreativeEnabled
			}
			return previousSettings.CreativeEnabled
		}(),
		CreativeModelSettings: func() []creative.CreativeModelSetting {
			if req.CreativeModelSettings != nil {
				return *req.CreativeModelSettings
			}
			return previousSettings.CreativeModelSettings
		}(),
		CreativeWorkerCount: func() int {
			if req.CreativeWorkerCount != nil {
				return *req.CreativeWorkerCount
			}
			return previousSettings.CreativeWorkerCount
		}(),
		RiskControlEnabled: func() bool {
			if req.RiskControlEnabled != nil {
				return *req.RiskControlEnabled
			}
			return previousSettings.RiskControlEnabled
		}(),
		CyberSessionBlockEnabled: func() bool {
			if req.CyberSessionBlockEnabled != nil {
				return *req.CyberSessionBlockEnabled
			}
			return previousSettings.CyberSessionBlockEnabled
		}(),
		CyberSessionBlockTTLSeconds: func() int {
			if req.CyberSessionBlockTTLSeconds != nil {
				return *req.CyberSessionBlockTTLSeconds
			}
			return previousSettings.CyberSessionBlockTTLSeconds
		}(),
		DefaultUserRPMLimit:                  req.DefaultUserRPMLimit,
		DefaultUserAPIKeyLimit:               intValueOrDefault(req.DefaultUserAPIKeyLimit, previousSettings.DefaultUserAPIKeyLimit),
		DefaultSubscriptions:                 defaultSubscriptions,
		BalanceUnitName:                      req.BalanceUnitName,
		BalanceUnitSymbol:                    req.BalanceUnitSymbol,
		BalanceIconSVG:                       req.BalanceIconSVG,
		ReasoningPointRMBUnitPrice:           float64ValueOrDefault(req.ReasoningPointRMBUnitPrice, previousSettings.ReasoningPointRMBUnitPrice),
		USDExchangeRate:                      float64ValueOrDefault(req.USDExchangeRate, previousSettings.USDExchangeRate),
		MarketplaceAvailabilityWindowDays:    intValueOrDefault(req.MarketplaceAvailabilityWindowDays, previousSettings.MarketplaceAvailabilityWindowDays),
		MarketplaceAvailabilityBucketMinutes: intValueOrDefault(req.MarketplaceAvailabilityBucketMinutes, previousSettings.MarketplaceAvailabilityBucketMinutes),
		EnableModelFallback:                  req.EnableModelFallback,
		FallbackModelAnthropic:               req.FallbackModelAnthropic,
		FallbackModelOpenAI:                  req.FallbackModelOpenAI,
		FallbackModelGemini:                  req.FallbackModelGemini,
		FallbackModelAntigravity:             req.FallbackModelAntigravity,
		GrokDefaultTextModel: func() string {
			if req.GrokDefaultTextModel != nil {
				return strings.TrimSpace(*req.GrokDefaultTextModel)
			}
			return previousSettings.GrokDefaultTextModel
		}(),
		GrokDefaultBaseURLMode: func() string {
			if req.GrokDefaultBaseURLMode != nil {
				return strings.TrimSpace(*req.GrokDefaultBaseURLMode)
			}
			return previousSettings.GrokDefaultBaseURLMode
		}(),
		EnableIdentityPatch:  req.EnableIdentityPatch,
		IdentityPatchPrompt:  req.IdentityPatchPrompt,
		MinClaudeCodeVersion: req.MinClaudeCodeVersion,
		MaxClaudeCodeVersion: req.MaxClaudeCodeVersion,
		BackendModeEnabled:   req.BackendModeEnabled,
		OpenAITTFTMode: func() string {
			if req.OpenAITTFTMode != nil {
				return *req.OpenAITTFTMode
			}
			return previousSettings.OpenAITTFTMode
		}(),
		AllowUserViewErrorRequests: func() bool {
			if req.AllowUserViewErrorRequests != nil {
				return *req.AllowUserViewErrorRequests
			}
			return previousSettings.AllowUserViewErrorRequests
		}(),
		OpsMonitoringEnabled: func() bool {
			if req.OpsMonitoringEnabled != nil {
				return *req.OpsMonitoringEnabled
			}
			return previousSettings.OpsMonitoringEnabled
		}(),
		OpsRealtimeMonitoringEnabled: func() bool {
			if req.OpsRealtimeMonitoringEnabled != nil {
				return *req.OpsRealtimeMonitoringEnabled
			}
			return previousSettings.OpsRealtimeMonitoringEnabled
		}(),
		OpsMetricsIntervalSeconds: func() int {
			if req.OpsMetricsIntervalSeconds != nil {
				return *req.OpsMetricsIntervalSeconds
			}
			return previousSettings.OpsMetricsIntervalSeconds
		}(),
		EnableFingerprintUnification: func() bool {
			if req.EnableFingerprintUnification != nil {
				return *req.EnableFingerprintUnification
			}
			return previousSettings.EnableFingerprintUnification
		}(),
		EnableMetadataPassthrough: func() bool {
			if req.EnableMetadataPassthrough != nil {
				return *req.EnableMetadataPassthrough
			}
			return previousSettings.EnableMetadataPassthrough
		}(),
		EnableCCHSigning: func() bool {
			if req.EnableCCHSigning != nil {
				return *req.EnableCCHSigning
			}
			return previousSettings.EnableCCHSigning
		}(),
		EnableClaudeOAuthSystemPromptInjection: func() bool {
			if req.EnableClaudeOAuthSystemPromptInjection != nil {
				return *req.EnableClaudeOAuthSystemPromptInjection
			}
			return previousSettings.EnableClaudeOAuthSystemPromptInjection
		}(),
		ClaudeOAuthSystemPrompt: func() string {
			if req.ClaudeOAuthSystemPrompt != nil {
				return *req.ClaudeOAuthSystemPrompt
			}
			return previousSettings.ClaudeOAuthSystemPrompt
		}(),
		ClaudeOAuthSystemPromptBlocks: func() string {
			if req.ClaudeOAuthSystemPromptBlocks != nil {
				return *req.ClaudeOAuthSystemPromptBlocks
			}
			return previousSettings.ClaudeOAuthSystemPromptBlocks
		}(),
		EnableAnthropicCacheTTL1hInjection: func() bool {
			if req.EnableAnthropicCacheTTL1hInjection != nil {
				return *req.EnableAnthropicCacheTTL1hInjection
			}
			return previousSettings.EnableAnthropicCacheTTL1hInjection
		}(),
		RewriteMessageCacheControl: func() bool {
			if req.RewriteMessageCacheControl != nil {
				return *req.RewriteMessageCacheControl
			}
			return previousSettings.RewriteMessageCacheControl
		}(),
		EnableClientDatelineNormalization: func() bool {
			if req.EnableClientDatelineNormalization != nil {
				return *req.EnableClientDatelineNormalization
			}
			return previousSettings.EnableClientDatelineNormalization
		}(),
		AntigravityUserAgentVersion: func() string {
			if req.AntigravityUserAgentVersion != nil {
				return *req.AntigravityUserAgentVersion
			}
			return previousSettings.AntigravityUserAgentVersion
		}(),
		OpenAICodexUserAgent: func() string {
			if req.OpenAICodexUserAgent != nil {
				return *req.OpenAICodexUserAgent
			}
			return previousSettings.OpenAICodexUserAgent
		}(),
		OpenAIAllowClaudeCodeCodexPlugin: func() bool {
			if req.OpenAIAllowClaudeCodeCodexPlugin != nil {
				return *req.OpenAIAllowClaudeCodeCodexPlugin
			}
			return previousSettings.OpenAIAllowClaudeCodeCodexPlugin
		}(),
		UserPromptReplacementConfig: func() *promptpolicy.UserPromptReplacementConfig {
			if req.UserPromptReplacementConfig != nil {
				return req.UserPromptReplacementConfig
			}
			return previousSettings.UserPromptReplacementConfig
		}(),
		PaymentVisibleMethodAlipaySource: func() string {
			if req.PaymentVisibleMethodAlipaySource != nil {
				return strings.TrimSpace(*req.PaymentVisibleMethodAlipaySource)
			}
			return previousSettings.PaymentVisibleMethodAlipaySource
		}(),
		PaymentVisibleMethodWxpaySource: func() string {
			if req.PaymentVisibleMethodWxpaySource != nil {
				return strings.TrimSpace(*req.PaymentVisibleMethodWxpaySource)
			}
			return previousSettings.PaymentVisibleMethodWxpaySource
		}(),
		PaymentVisibleMethodAlipayEnabled: func() bool {
			if req.PaymentVisibleMethodAlipayEnabled != nil {
				return *req.PaymentVisibleMethodAlipayEnabled
			}
			return previousSettings.PaymentVisibleMethodAlipayEnabled
		}(),
		PaymentVisibleMethodWxpayEnabled: func() bool {
			if req.PaymentVisibleMethodWxpayEnabled != nil {
				return *req.PaymentVisibleMethodWxpayEnabled
			}
			return previousSettings.PaymentVisibleMethodWxpayEnabled
		}(),
		AdvancedSchedulerStickyWeightedEnabled: func() bool {
			if req.AdvancedSchedulerStickyWeightedEnabled != nil {
				return *req.AdvancedSchedulerStickyWeightedEnabled
			}
			return previousSettings.AdvancedSchedulerStickyWeightedEnabled
		}(),
		AdvancedSchedulerSubscriptionPriorityEnabled: func() bool {
			if req.AdvancedSchedulerSubscriptionPriorityEnabled != nil {
				return *req.AdvancedSchedulerSubscriptionPriorityEnabled
			}
			return previousSettings.AdvancedSchedulerSubscriptionPriorityEnabled
		}(),
		AdvancedSchedulerEWMAErrorRateAlpha: stringSetting(req.AdvancedSchedulerEWMAErrorRateAlpha, previousSettings.AdvancedSchedulerEWMAErrorRateAlpha),
		AdvancedSchedulerEWMATTFTAlpha:      stringSetting(req.AdvancedSchedulerEWMATTFTAlpha, previousSettings.AdvancedSchedulerEWMATTFTAlpha),
		AdvancedSchedulerStickyEscapeEnabled: func() bool {
			if req.AdvancedSchedulerStickyEscapeEnabled != nil {
				return *req.AdvancedSchedulerStickyEscapeEnabled
			}
			return previousSettings.AdvancedSchedulerStickyEscapeEnabled
		}(),
		AdvancedSchedulerStickyEscapeEnabledSet: func() bool {
			if req.AdvancedSchedulerStickyEscapeEnabled != nil {
				return true
			}
			return previousSettings.AdvancedSchedulerStickyEscapeEnabledSet
		}(),
		AdvancedSchedulerStickyEscapeTTFTMs:     stringSetting(req.AdvancedSchedulerStickyEscapeTTFTMs, previousSettings.AdvancedSchedulerStickyEscapeTTFTMs),
		AdvancedSchedulerStickyEscapeErrorRate:  stringSetting(req.AdvancedSchedulerStickyEscapeErrorRate, previousSettings.AdvancedSchedulerStickyEscapeErrorRate),
		AdvancedSchedulerLBTopK:                 stringSetting(req.AdvancedSchedulerLBTopK, previousSettings.AdvancedSchedulerLBTopK),
		AdvancedSchedulerWeightPriority:         stringSetting(req.AdvancedSchedulerWeightPriority, previousSettings.AdvancedSchedulerWeightPriority),
		AdvancedSchedulerWeightLoad:             stringSetting(req.AdvancedSchedulerWeightLoad, previousSettings.AdvancedSchedulerWeightLoad),
		AdvancedSchedulerWeightQueue:            stringSetting(req.AdvancedSchedulerWeightQueue, previousSettings.AdvancedSchedulerWeightQueue),
		AdvancedSchedulerWeightErrorRate:        stringSetting(req.AdvancedSchedulerWeightErrorRate, previousSettings.AdvancedSchedulerWeightErrorRate),
		AdvancedSchedulerWeightTTFT:             stringSetting(req.AdvancedSchedulerWeightTTFT, previousSettings.AdvancedSchedulerWeightTTFT),
		AdvancedSchedulerWeightReset:            stringSetting(req.AdvancedSchedulerWeightReset, previousSettings.AdvancedSchedulerWeightReset),
		AdvancedSchedulerWeightQuotaHeadroom:    stringSetting(req.AdvancedSchedulerWeightQuotaHeadroom, previousSettings.AdvancedSchedulerWeightQuotaHeadroom),
		AdvancedSchedulerWeightPreviousResponse: stringSetting(req.AdvancedSchedulerWeightPreviousResponse, previousSettings.AdvancedSchedulerWeightPreviousResponse),
		AdvancedSchedulerWeightSessionSticky:    stringSetting(req.AdvancedSchedulerWeightSessionSticky, previousSettings.AdvancedSchedulerWeightSessionSticky),
		OpenAIQuotaAutoPauseSettings: func() provider.QuotaAutoPauseSettings {
			if req.OpenAIQuotaAutoPauseSettings != nil {
				return *req.OpenAIQuotaAutoPauseSettings
			}
			return previousSettings.OpenAIQuotaAutoPauseSettings
		}(),
		OpenAIQuotaAutoPauseSettingsSet: req.OpenAIQuotaAutoPauseSettings != nil,
		BalanceLowNotifyEnabled: func() bool {
			if req.BalanceLowNotifyEnabled != nil {
				return *req.BalanceLowNotifyEnabled
			}
			return previousSettings.BalanceLowNotifyEnabled
		}(),
		BalanceLowNotifyThreshold: func() float64 {
			if req.BalanceLowNotifyThreshold != nil {
				return *req.BalanceLowNotifyThreshold
			}
			return previousSettings.BalanceLowNotifyThreshold
		}(),
		BalanceLowNotifyRechargeURL: func() string {
			if req.BalanceLowNotifyRechargeURL != nil {
				return *req.BalanceLowNotifyRechargeURL
			}
			return previousSettings.BalanceLowNotifyRechargeURL
		}(),
		SubscriptionExpiryNotifyEnabled: func() bool {
			if req.SubscriptionExpiryNotifyEnabled != nil {
				return *req.SubscriptionExpiryNotifyEnabled
			}
			return previousSettings.SubscriptionExpiryNotifyEnabled
		}(),
		ProviderQuotaNotifyEnabled: func() bool {
			if req.ProviderQuotaNotifyEnabled != nil {
				return *req.ProviderQuotaNotifyEnabled
			}
			return previousSettings.ProviderQuotaNotifyEnabled
		}(),
		ProviderQuotaNotifyEmails: func() []contact.Entry {
			if req.ProviderQuotaNotifyEmails != nil {
				return identitydto.NotifyEmailEntriesToIdentity(*req.ProviderQuotaNotifyEmails)
			}
			return previousSettings.ProviderQuotaNotifyEmails
		}(),
	}

	authSourceDefaults := &identity.AuthSourceDefaultSettings{
		Email: identity.ProviderDefaultGrantSettings{
			Balance:          float64ValueOrDefault(req.AuthSourceDefaultEmailBalance, previousAuthSourceDefaults.Email.Balance),
			Concurrency:      intValueOrDefault(req.AuthSourceDefaultEmailConcurrency, previousAuthSourceDefaults.Email.Concurrency),
			Subscriptions:    defaultSubscriptionsValueOrDefault(req.AuthSourceDefaultEmailSubscriptions, previousAuthSourceDefaults.Email.Subscriptions),
			GrantOnSignup:    boolValueOrDefault(req.AuthSourceDefaultEmailGrantOnSignup, previousAuthSourceDefaults.Email.GrantOnSignup),
			GrantOnFirstBind: boolValueOrDefault(req.AuthSourceDefaultEmailGrantOnFirstBind, previousAuthSourceDefaults.Email.GrantOnFirstBind),
		},
		LinuxDo: identity.ProviderDefaultGrantSettings{
			Balance:          float64ValueOrDefault(req.AuthSourceDefaultLinuxDoBalance, previousAuthSourceDefaults.LinuxDo.Balance),
			Concurrency:      intValueOrDefault(req.AuthSourceDefaultLinuxDoConcurrency, previousAuthSourceDefaults.LinuxDo.Concurrency),
			Subscriptions:    defaultSubscriptionsValueOrDefault(req.AuthSourceDefaultLinuxDoSubscriptions, previousAuthSourceDefaults.LinuxDo.Subscriptions),
			GrantOnSignup:    boolValueOrDefault(req.AuthSourceDefaultLinuxDoGrantOnSignup, previousAuthSourceDefaults.LinuxDo.GrantOnSignup),
			GrantOnFirstBind: boolValueOrDefault(req.AuthSourceDefaultLinuxDoGrantOnFirstBind, previousAuthSourceDefaults.LinuxDo.GrantOnFirstBind),
		},
		OIDC: identity.ProviderDefaultGrantSettings{
			Balance:          float64ValueOrDefault(req.AuthSourceDefaultOIDCBalance, previousAuthSourceDefaults.OIDC.Balance),
			Concurrency:      intValueOrDefault(req.AuthSourceDefaultOIDCConcurrency, previousAuthSourceDefaults.OIDC.Concurrency),
			Subscriptions:    defaultSubscriptionsValueOrDefault(req.AuthSourceDefaultOIDCSubscriptions, previousAuthSourceDefaults.OIDC.Subscriptions),
			GrantOnSignup:    boolValueOrDefault(req.AuthSourceDefaultOIDCGrantOnSignup, previousAuthSourceDefaults.OIDC.GrantOnSignup),
			GrantOnFirstBind: boolValueOrDefault(req.AuthSourceDefaultOIDCGrantOnFirstBind, previousAuthSourceDefaults.OIDC.GrantOnFirstBind),
		},
		WeChat: identity.ProviderDefaultGrantSettings{
			Balance:          float64ValueOrDefault(req.AuthSourceDefaultWeChatBalance, previousAuthSourceDefaults.WeChat.Balance),
			Concurrency:      intValueOrDefault(req.AuthSourceDefaultWeChatConcurrency, previousAuthSourceDefaults.WeChat.Concurrency),
			Subscriptions:    defaultSubscriptionsValueOrDefault(req.AuthSourceDefaultWeChatSubscriptions, previousAuthSourceDefaults.WeChat.Subscriptions),
			GrantOnSignup:    boolValueOrDefault(req.AuthSourceDefaultWeChatGrantOnSignup, previousAuthSourceDefaults.WeChat.GrantOnSignup),
			GrantOnFirstBind: boolValueOrDefault(req.AuthSourceDefaultWeChatGrantOnFirstBind, previousAuthSourceDefaults.WeChat.GrantOnFirstBind),
		},
		GitHub: identity.ProviderDefaultGrantSettings{
			Balance:          float64ValueOrDefault(req.AuthSourceDefaultGitHubBalance, previousAuthSourceDefaults.GitHub.Balance),
			Concurrency:      intValueOrDefault(req.AuthSourceDefaultGitHubConcurrency, previousAuthSourceDefaults.GitHub.Concurrency),
			Subscriptions:    defaultSubscriptionsValueOrDefault(req.AuthSourceDefaultGitHubSubscriptions, previousAuthSourceDefaults.GitHub.Subscriptions),
			GrantOnSignup:    boolValueOrDefault(req.AuthSourceDefaultGitHubGrantOnSignup, previousAuthSourceDefaults.GitHub.GrantOnSignup),
			GrantOnFirstBind: boolValueOrDefault(req.AuthSourceDefaultGitHubGrantOnFirstBind, previousAuthSourceDefaults.GitHub.GrantOnFirstBind),
		},
		Google: identity.ProviderDefaultGrantSettings{
			Balance:          float64ValueOrDefault(req.AuthSourceDefaultGoogleBalance, previousAuthSourceDefaults.Google.Balance),
			Concurrency:      intValueOrDefault(req.AuthSourceDefaultGoogleConcurrency, previousAuthSourceDefaults.Google.Concurrency),
			Subscriptions:    defaultSubscriptionsValueOrDefault(req.AuthSourceDefaultGoogleSubscriptions, previousAuthSourceDefaults.Google.Subscriptions),
			GrantOnSignup:    boolValueOrDefault(req.AuthSourceDefaultGoogleGrantOnSignup, previousAuthSourceDefaults.Google.GrantOnSignup),
			GrantOnFirstBind: boolValueOrDefault(req.AuthSourceDefaultGoogleGrantOnFirstBind, previousAuthSourceDefaults.Google.GrantOnFirstBind),
		},
		DingTalk: identity.ProviderDefaultGrantSettings{
			Balance:          float64ValueOrDefault(req.AuthSourceDefaultDingTalkBalance, previousAuthSourceDefaults.DingTalk.Balance),
			Concurrency:      intValueOrDefault(req.AuthSourceDefaultDingTalkConcurrency, previousAuthSourceDefaults.DingTalk.Concurrency),
			Subscriptions:    defaultSubscriptionsValueOrDefault(req.AuthSourceDefaultDingTalkSubscriptions, previousAuthSourceDefaults.DingTalk.Subscriptions),
			GrantOnSignup:    boolValueOrDefault(req.AuthSourceDefaultDingTalkGrantOnSignup, previousAuthSourceDefaults.DingTalk.GrantOnSignup),
			GrantOnFirstBind: boolValueOrDefault(req.AuthSourceDefaultDingTalkGrantOnFirstBind, previousAuthSourceDefaults.DingTalk.GrantOnFirstBind),
		},
		ForceEmailOnThirdPartySignup: boolValueOrDefault(req.ForceEmailOnThirdPartySignup, previousAuthSourceDefaults.ForceEmailOnThirdPartySignup),
	}
	values, err := h.settingService.PrepareSettingsWithAuthSourceDefaults(c.Request.Context(), settings, nil, omitted)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	changes := []runtimesettings.PreparedChange{{Module: "system-values", Values: values}}
	if native, ok := h.settingService.(interface {
		ApplicationChanges() []runtimesettings.PreparedChange
	}); ok {
		changes = append(changes, native.ApplicationChanges()...)
	} else {
		changes = append(changes, runtimesettings.PreparedChange{Module: "system", Apply: func(ctx context.Context) error {
			if err := h.settingService.ApplyPersistedSettings(ctx); err != nil {
				return err
			}
			if h.opsService != nil {
				h.opsService.SetMonitoringEnabled(settings.OpsMonitoringEnabled)
			}
			return nil
		}})
	}

	// Prepare every registered participant before the single batch commit.
	rawInput, err := json.Marshal(req)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	var fields runtimesettings.Fields
	if err = json.Unmarshal(rawInput, &fields); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	// Drop omitted non-pointer fields; pointer fields use the merged and normalized values.
	for name := range settingKeyByJSONName {
		if _, sent := sentFields[name]; !sent {
			delete(fields, name)
		}
	}
	// Write the merged rebate switch on every save, including requests that omit it.
	fields["affiliate_admin_recharge_enabled"], _ = json.Marshal(settings.AdminRechargeRebateEnabled)
	fields["usage_ranking_enabled"], _ = json.Marshal(settings.UsageRankingEnabled)
	fields["usage_ranking_sort_by"], _ = json.Marshal(settings.UsageRankingSortBy)
	fields["usage_ranking_show_total_tokens"], _ = json.Marshal(settings.UsageRankingShowTotalTokens)
	fields["usage_ranking_show_requests"], _ = json.Marshal(settings.UsageRankingShowRequests)
	fields["usage_ranking_show_actual_cost"], _ = json.Marshal(settings.UsageRankingShowActualCost)
	fields["allow_user_view_error_requests"], _ = json.Marshal(settings.AllowUserViewErrorRequests)
	// Use identity values after permission checks and merging; skip omitted non-pointer fields.
	identityRaw, err := json.Marshal(settings.IdentityAdminSettings())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	var identityFields runtimesettings.Fields
	if err = json.Unmarshal(identityRaw, &identityFields); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	maps.Copy(identityFields, identity.AuthSourceParticipantFields(authSourceDefaults))
	for name, value := range identityFields {
		if _, nonPointer := settingKeyByJSONName[name]; nonPointer {
			if _, sent := sentFields[name]; !sent {
				continue
			}
		}
		fields[name] = value
	}
	fields["team_enabled"], _ = json.Marshal(settings.TeamEnabled)
	fields["risk_control_enabled"], _ = json.Marshal(settings.RiskControlEnabled)
	fields["cyber_session_block_enabled"], _ = json.Marshal(settings.CyberSessionBlockEnabled)
	fields["cyber_session_block_ttl_seconds"], _ = json.Marshal(settings.CyberSessionBlockTTLSeconds)
	fields["creative_enabled"], _ = json.Marshal(settings.CreativeEnabled)
	fields["creative_worker_count"], _ = json.Marshal(settings.CreativeWorkerCount)
	fields["creative_model_settings"], _ = json.Marshal(settings.CreativeModelSettings)
	fields["api_key_acl_trust_forwarded_ip"], _ = json.Marshal(settings.APIKeyACLTrustForwardedIP)
	fields["forwarded_client_ip_headers"], _ = json.Marshal(settings.ForwardedClientIPHeaders)
	if err := mergeNormalizedSettingsFields(fields, settings.BillingAdminSettings(), sentFields); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	schedulerFields, err := settings.SchedulerAdminSettings().ParticipantFields()
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	delete(fields, "advanced_scheduler_sticky_escape_enabled")
	for name, value := range schedulerFields {
		if _, nonPointer := settingKeyByJSONName[name]; nonPointer {
			if _, sent := sentFields[name]; !sent {
				continue
			}
		}
		fields[name] = value
	}
	if err := mergeNormalizedSettingsFields(fields, settings.RoutingAdminSettings(), sentFields); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	if err := mergeNormalizedSettingsFields(fields, settings.SiteAdminSettings(), sentFields); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	for name, value := range map[string]any{"provider_quota_notify_enabled": settings.ProviderQuotaNotifyEnabled, "provider_quota_notify_emails": settings.ProviderQuotaNotifyEmails, "provider_scheduling_thresholds": settings.ProviderSchedulingThresholds} {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			httpx.ErrorFrom(c, marshalErr)
			return
		}
		fields[name] = raw
	}
	for name, value := range map[string]any{
		"payment_visible_method_alipay_source": settings.PaymentVisibleMethodAlipaySource, "payment_visible_method_wxpay_source": settings.PaymentVisibleMethodWxpaySource, "payment_visible_method_alipay_enabled": settings.PaymentVisibleMethodAlipayEnabled, "payment_visible_method_wxpay_enabled": settings.PaymentVisibleMethodWxpayEnabled,
		"ops_monitoring_enabled": settings.OpsMonitoringEnabled, "ops_realtime_monitoring_enabled": settings.OpsRealtimeMonitoringEnabled, "ops_metrics_interval_seconds": settings.OpsMetricsIntervalSeconds,
	} {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			httpx.ErrorFrom(c, marshalErr)
			return
		}
		fields[name] = raw
	}
	delete(fields, "openai_provider_quota_auto_pause")
	if settings.OpenAIQuotaAutoPauseSettingsSet {
		fields["openai_provider_quota_auto_pause"], _ = json.Marshal(settings.OpenAIQuotaAutoPauseSettings)
	}
	if err := mergeNormalizedSettingsFields(fields, settings.GatewayAdminSettings(), sentFields); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	prepared, err := h.preparedParticipants(update.Context(), fields, values, previousSettings.StoredValues)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	changes = append(changes, prepared...)
	if err := update.Commit(changes...); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	h.auditSettingsUpdate(c, previousSettings, settings, previousAuthSourceDefaults, authSourceDefaults, auditReq)

	// Read committed values for the response.
	updatedSettings, err := h.settingService.GetAllSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	h.ensureDingTalkSyncAttributes(c.Request.Context(), updatedSettings)
	updatedAuthSourceDefaults, err := h.settingService.GetAuthSourceDefaultSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	updatedDefaultSubscriptions := make([]billinghttp.DefaultSubscriptionSetting, 0, len(updatedSettings.DefaultSubscriptions))
	for _, sub := range updatedSettings.DefaultSubscriptions {
		updatedDefaultSubscriptions = append(updatedDefaultSubscriptions, billinghttp.DefaultSubscriptionSetting{
			PlanID: sub.PlanID,
		})
	}

	// Reload payment config for response
	var updatedPaymentCfg *payment.PaymentConfig
	if h.paymentConfigService != nil {
		updatedPaymentCfg, err = h.paymentConfigService.GetPaymentConfig(c.Request.Context())
		if err != nil {
			httpx.ErrorFrom(c, err)
			return
		}
	}
	if updatedPaymentCfg == nil {
		updatedPaymentCfg = &payment.PaymentConfig{}
	}

	payload := buildSystemSettingsResponse(updatedSettings, updatedPaymentCfg, updatedDefaultSubscriptions, h.settingService.IsTotpEncryptionKeyConfigured())
	payload.OpsMonitoringEnabled = updatedSettings.OpsMonitoringEnabled
	if fastPolicy, err := h.settingService.GetOpenAIFastPolicySettings(c.Request.Context()); err != nil {
		slog.Error("openai_fast_policy_settings_get_failed", "error", err)
	} else if fastPolicy != nil {
		payload.OpenAIFastPolicySettings = openaiFastPolicySettingsToDTO(fastPolicy)
	}

	httpx.Success(c, systemSettingsResponseData(payload, updatedAuthSourceDefaults))
}

// mergeNormalizedSettingsFields merges normalized values and skips omitted non-pointer fields.
func mergeNormalizedSettingsFields(fields runtimesettings.Fields, normalized any, sentFields map[string]json.RawMessage) error {
	raw, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	var normalizedFields runtimesettings.Fields
	if err := json.Unmarshal(raw, &normalizedFields); err != nil {
		return err
	}
	for name, value := range normalizedFields {
		if _, nonPointer := settingKeyByJSONName[name]; nonPointer {
			if _, sent := sentFields[name]; !sent {
				continue
			}
		}
		fields[name] = value
	}
	return nil
}

// rejectDeprecatedAdvancedSchedulerRequestFields rejects obsolete experimental scheduler fields.
// Groups select the scheduler; this endpoint accepts shared advanced_scheduler_* parameters.
func rejectDeprecatedAdvancedSchedulerRequestFields(c *gin.Context, sentFields map[string]json.RawMessage) bool {
	for field := range sentFields {
		if field == "advanced_scheduler_enabled" || strings.HasPrefix(field, "openai_advanced_scheduler_") {
			httpx.ErrorWithDetails(c, http.StatusBadRequest,
				"Deprecated advanced scheduler setting: "+field+"; select the scheduler type on each group and use advanced_scheduler_* settings",
				"DEPRECATED_ADVANCED_SCHEDULER_SETTING", map[string]string{"field": field})
			return true
		}
	}
	return false
}

// mapDingTalkValidateError maps DingTalk validation errors to machine-readable reason codes.
func mapDingTalkValidateError(err error) string {
	switch {
	case errors.Is(err, authconfig.ErrDingTalkV1AppTypeMismatch):
		return "dingtalk_apptype_mismatch"
	case errors.Is(err, authconfig.ErrDingTalkV4InvalidAppKind):
		return "dingtalk_app_kind_invalid"
	default:
		return "dingtalk_corp_config_invalid"
	}
}

// ensureDingTalkSyncAttributes creates missing user attributes or updates their display names
// after settings commit. Existing types, options, and required flags remain unchanged.
// It applies to enabled synchronization fields under internal_only.
// Failures are logged and do not undo the committed settings.
func (h *Handler) ensureDingTalkSyncAttributes(ctx context.Context, settings *composite.Snapshot) {
	if h.userAttributeService == nil || settings == nil {
		return
	}
	if settings.DingTalkConnectCorpRestrictionPolicy != "internal_only" {
		return
	}
	if settings.DingTalkConnectSyncDisplayName {
		h.ensureUserAttributeDefinition(ctx, settings.DingTalkConnectSyncDisplayNameAttrKey, settings.DingTalkConnectSyncDisplayNameAttrName, "钉钉 internal_only 登录时同步的钉钉姓名", identity.AttributeTypeText)
	}
	if settings.DingTalkConnectSyncCorpEmail {
		h.ensureUserAttributeDefinition(ctx, settings.DingTalkConnectSyncCorpEmailAttrKey, settings.DingTalkConnectSyncCorpEmailAttrName, "钉钉 internal_only 登录时同步的企业邮箱", identity.AttributeTypeEmail)
	}
	if settings.DingTalkConnectSyncDept {
		h.ensureUserAttributeDefinition(ctx, settings.DingTalkConnectSyncDeptAttrKey, settings.DingTalkConnectSyncDeptAttrName, "钉钉 internal_only 登录时同步的完整部门路径（如：公司/研发部）", identity.AttributeTypeText)
	}
}

func (h *Handler) ensureUserAttributeDefinition(ctx context.Context, key, name, description string, attrType identity.UserAttributeType) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	existing, err := h.userAttributeService.GetDefinitionByKey(ctx, key)
	if err == nil && existing != nil {
		if strings.TrimSpace(name) != "" && existing.Name != name {
			if _, err := h.userAttributeService.UpdateDefinition(ctx, existing.ID, identity.UpdateAttributeDefinitionInput{
				Name: &name,
			}); err != nil {
				slog.Warn("dingtalk: update user attribute definition name failed", "key", key, "err", err.Error())
				return
			}
			slog.Info("dingtalk: updated user attribute definition name", "key", key, "name", name)
		}
		return
	}
	if _, err := h.userAttributeService.CreateDefinition(ctx, identity.CreateAttributeDefinitionInput{
		Key:         key,
		Name:        name,
		Description: description,
		Type:        attrType,
		Enabled:     true,
	}); err != nil {
		slog.Warn("dingtalk: ensure user attribute definition failed", "key", key, "err", err.Error())
		return
	}
	slog.Info("dingtalk: created user attribute definition", "key", key, "name", name, "type", attrType)
}

// rejectRemovedPlatformQuotaFields rejects removed platform quota fields and identifies the field in the response.
func rejectRemovedPlatformQuotaFields(c *gin.Context, fields map[string]json.RawMessage) bool {
	for name := range fields {
		if name == "default_platform_quotas" || strings.HasPrefix(name, "auth_source_default_") && strings.HasSuffix(name, "_platform_quotas") {
			httpx.ErrorWithDetails(c, http.StatusBadRequest, "User platform quotas have been removed", "REMOVED_SETTING_FIELD", map[string]string{"field": name})
			return true
		}
	}
	return false
}

// rejectRemovedUngroupedKeySchedulingField rejects the removed switch and requires API keys to have a group.
func rejectRemovedUngroupedKeySchedulingField(c *gin.Context, fields map[string]json.RawMessage) bool {
	const field = "allow_ungrouped_key_scheduling"
	if _, present := fields[field]; !present {
		return false
	}
	httpx.ErrorWithDetails(c, http.StatusBadRequest, "API keys require an explicit group", "REMOVED_SETTING_FIELD", map[string]string{"field": field})
	return true
}
