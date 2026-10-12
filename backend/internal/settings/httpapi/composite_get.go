package httpapi

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"

	billinghttp "github.com/TokenFlux/TokenRouter/internal/billing/httpapi"
	gatewaydto "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	settingsdto "github.com/TokenFlux/TokenRouter/internal/settings/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/site"
	sitedto "github.com/TokenFlux/TokenRouter/internal/site/httpapi/dto"
)

// Administrator settings GET loads system fields and authentication grants before
// adding deployment-aware monitoring, optional payment configuration, and a
// separately stored tier policy. Required system/grant read failures stop response
// construction. Payment failures can still contribute an available result, while
// a nil payment result becomes empty configuration for the response mapper.
//
// The response converts selected collections and then flattens its JSON fields
// with authentication-source defaults. Conversion helpers allocate outer lists
// but retain nested references where assigned directly. Optional policy reads
// are logged on failure, and missing rule tiers normalize to the shared all value
// when the inverse DTO helper prepares an administrator request.

// GetSettings returns parsed administrator settings with grants, effective
// monitoring, payment data, and optional tier policy. System and grant read errors
// stop the response; payment read errors are discarded while a returned config
// can still be used, or nil becomes an empty config.
//
// Monitoring is intersected with the installed service's effective availability,
// and this GET fills provider thresholds from the Snapshot. A fast-policy read
// error is logged and its optional field remains absent. The final flattener
// appends authentication-source fields and follows its JSON-conversion error rule
// before the success response is emitted.
func (h *Handler) GetSettings(c *gin.Context) {
	settings, err := h.settingService.GetAllSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	authSourceDefaults, err := h.settingService.GetAuthSourceDefaultSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Combine the saved runtime switch with effective service availability so
	// GET reflects deployment-disabled monitoring before this flag reaches the administrator.
	opsEnabled := h.opsService != nil && h.opsService.IsMonitoringEnabled(c.Request.Context())
	defaultSubscriptions := make([]billinghttp.DefaultSubscriptionSetting, 0, len(settings.DefaultSubscriptions))
	for _, sub := range settings.DefaultSubscriptions {
		defaultSubscriptions = append(defaultSubscriptions, billinghttp.DefaultSubscriptionSetting{
			PlanID: sub.PlanID,
		})
	}

	// GET continues after payment-read failure, using an available result or
	// an empty config; required system and grant reads above still stop on failure.
	var paymentCfg *payment.PaymentConfig
	if h.paymentConfigService != nil {
		paymentCfg, _ = h.paymentConfigService.GetPaymentConfig(c.Request.Context())
	}
	if paymentCfg == nil {
		paymentCfg = &payment.PaymentConfig{}
	}

	payload := buildSystemSettingsResponse(settings, paymentCfg, defaultSubscriptions, h.settingService.IsTotpEncryptionKeyConfigured())
	payload.OpsMonitoringEnabled = opsEnabled && settings.OpsMonitoringEnabled
	payload.ProviderSchedulingThresholds = settings.ProviderSchedulingThresholds

	// Read the separately stored tier policy after the broad response exists;
	// its failure is logged and leaves this optional field absent from a successful GET.
	if fastPolicy, err := h.settingService.GetOpenAIFastPolicySettings(c.Request.Context()); err != nil {
		slog.Error("openai_fast_policy_settings_get_failed", "error", err)
	} else if fastPolicy != nil {
		payload.OpenAIFastPolicySettings = openaiFastPolicySettingsToDTO(fastPolicy)
	}

	response.Success(c, systemSettingsResponseData(payload, authSourceDefaults))
}

// openaiFastPolicySettingsToDTO converts the tier-policy value for an HTTP
// response, returning nil for nil input. It allocates the Rules slice, while
// nested UserIDs and ModelWhitelist slices stay shared with their source rule values.
func openaiFastPolicySettingsToDTO(s *tierpolicy.OpenAIFastPolicySettings) *gatewaydto.OpenAIFastPolicySettings {
	if s == nil {
		return nil
	}
	rules := make([]gatewaydto.OpenAIFastPolicyRule, len(s.Rules))
	for i, r := range s.Rules {
		rules[i] = gatewaydto.OpenAIFastPolicyRule(r)
	}
	return &gatewaydto.OpenAIFastPolicySettings{Rules: rules}
}

// OpenaiFastPolicySettingsFromDTO converts the HTTP policy and normalizes rule
// tiers before gateway preparation. Nil input returns nil; empty or whitespace
// tiers become all, and other tier text is trimmed and lowercased. The shared
// tierpolicy.Prepare validates nonempty choices.
//
// The outer Rules slice is newly allocated, while nested UserIDs and ModelWhitelist
// slices retain the DTO's storage. Tier normalization changes the new scalar rule
// values, and later tierpolicy.Prepare clones those nested collections before its
// own normalization and validation.
func OpenaiFastPolicySettingsFromDTO(s *gatewaydto.OpenAIFastPolicySettings) *tierpolicy.OpenAIFastPolicySettings {
	if s == nil {
		return nil
	}
	rules := make([]tierpolicy.OpenAIFastPolicyRule, len(s.Rules))
	for i, r := range s.Rules {
		rules[i] = tierpolicy.OpenAIFastPolicyRule(r)
		tier := strings.ToLower(strings.TrimSpace(rules[i].ServiceTier))
		// Normalize an omitted matching tier to all before preparation so rules
		// use the same match-any value when later encoded by the gateway policy module.
		if tier == "" {
			tier = tierpolicy.OpenAIFastTierAny
		}
		rules[i].ServiceTier = tier
	}
	return &tierpolicy.OpenAIFastPolicySettings{Rules: rules}
}

// loginAgreementDocumentsToDTO allocates an outer legal-document slice for the
// response. An empty input yields an empty nonnil list, while each record keeps
// its nested Localization pointer and other referenced data from the source.
func loginAgreementDocumentsToDTO(items []site.LoginAgreementDocument) []sitedto.LoginAgreementDocument {
	result := make([]sitedto.LoginAgreementDocument, 0, len(items))
	result = append(result, items...)
	return result
}

// loginAgreementDocumentsToService builds legal records from administrator DTO
// values, omitting entries whose chosen title and body are both empty. It trims
// plain title/body and ID, but a supplied Localization selects its source text
// without that extra trim.
//
// Localization pointers remain shared with the DTO, and the new outer list
// contains records accepted for later site preparation. That preparer owns content
// validation and revisions; this conversion selects source text so the plain fields
// can agree with the editor's localized document content.
func loginAgreementDocumentsToService(items []sitedto.LoginAgreementDocument) []site.LoginAgreementDocument {
	result := make([]site.LoginAgreementDocument, 0, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		content := strings.TrimSpace(item.ContentMD)
		// Editor source text takes precedence over the separately submitted plain
		// fields so legal preparation uses the content that carries localization revisions.
		if item.Localization != nil {
			title = item.Localization.Source.Title
			content = item.Localization.Source.ContentMD
		}
		if title == "" && content == "" {
			continue
		}
		result = append(result, site.LoginAgreementDocument{
			ID:           strings.TrimSpace(item.ID),
			Localization: item.Localization,
			Title:        title,
			ContentMD:    content,
		})
	}
	return result
}

// systemSettingsResponseData flattens the combined response JSON and appends
// authentication-source grant fields. A nil fee map becomes empty, and nil grant
// settings become zero-valued defaults for all appended provider fields.
//
// Response marshal and unmarshal errors are discarded, so failed conversion can
// leave an empty or partial system map before the grant fields are appended.
// Callers therefore receive the available flattened map rather than a separate
// conversion error. Numeric JSON values use encoding/json's generic map conversion
// when the initial response encoding succeeds.
func systemSettingsResponseData(settings settingsdto.SystemSettings, authSourceDefaults *identity.AuthSourceDefaultSettings) map[string]any {
	if settings.PaymentMethodFees == nil {
		settings.PaymentMethodFees = payment.MethodFeeSettings{}
	}

	data := make(map[string]any)
	// Flatten successfully encoded response fields, retaining any decoded map
	// after an ignored conversion failure; appended auth fields below remain available regardless.
	raw, err := json.Marshal(settings)
	if err == nil {
		_ = json.Unmarshal(raw, &data)
	}
	if authSourceDefaults == nil {
		authSourceDefaults = &identity.AuthSourceDefaultSettings{}
	}

	data["auth_source_default_email_balance"] = authSourceDefaults.Email.Balance
	data["auth_source_default_email_concurrency"] = authSourceDefaults.Email.Concurrency
	data["auth_source_default_email_subscriptions"] = authSourceDefaults.Email.Subscriptions
	data["auth_source_default_email_grant_on_signup"] = authSourceDefaults.Email.GrantOnSignup
	data["auth_source_default_email_grant_on_first_bind"] = authSourceDefaults.Email.GrantOnFirstBind
	data["auth_source_default_linuxdo_balance"] = authSourceDefaults.LinuxDo.Balance
	data["auth_source_default_linuxdo_concurrency"] = authSourceDefaults.LinuxDo.Concurrency
	data["auth_source_default_linuxdo_subscriptions"] = authSourceDefaults.LinuxDo.Subscriptions
	data["auth_source_default_linuxdo_grant_on_signup"] = authSourceDefaults.LinuxDo.GrantOnSignup
	data["auth_source_default_linuxdo_grant_on_first_bind"] = authSourceDefaults.LinuxDo.GrantOnFirstBind
	data["auth_source_default_dingtalk_balance"] = authSourceDefaults.DingTalk.Balance
	data["auth_source_default_dingtalk_concurrency"] = authSourceDefaults.DingTalk.Concurrency
	data["auth_source_default_dingtalk_subscriptions"] = authSourceDefaults.DingTalk.Subscriptions
	data["auth_source_default_dingtalk_grant_on_signup"] = authSourceDefaults.DingTalk.GrantOnSignup
	data["auth_source_default_dingtalk_grant_on_first_bind"] = authSourceDefaults.DingTalk.GrantOnFirstBind
	data["auth_source_default_oidc_balance"] = authSourceDefaults.OIDC.Balance
	data["auth_source_default_oidc_concurrency"] = authSourceDefaults.OIDC.Concurrency
	data["auth_source_default_oidc_subscriptions"] = authSourceDefaults.OIDC.Subscriptions
	data["auth_source_default_oidc_grant_on_signup"] = authSourceDefaults.OIDC.GrantOnSignup
	data["auth_source_default_oidc_grant_on_first_bind"] = authSourceDefaults.OIDC.GrantOnFirstBind
	data["auth_source_default_wechat_balance"] = authSourceDefaults.WeChat.Balance
	data["auth_source_default_wechat_concurrency"] = authSourceDefaults.WeChat.Concurrency
	data["auth_source_default_wechat_subscriptions"] = authSourceDefaults.WeChat.Subscriptions
	data["auth_source_default_wechat_grant_on_signup"] = authSourceDefaults.WeChat.GrantOnSignup
	data["auth_source_default_wechat_grant_on_first_bind"] = authSourceDefaults.WeChat.GrantOnFirstBind
	data["auth_source_default_github_balance"] = authSourceDefaults.GitHub.Balance
	data["auth_source_default_github_concurrency"] = authSourceDefaults.GitHub.Concurrency
	data["auth_source_default_github_subscriptions"] = authSourceDefaults.GitHub.Subscriptions
	data["auth_source_default_github_grant_on_signup"] = authSourceDefaults.GitHub.GrantOnSignup
	data["auth_source_default_github_grant_on_first_bind"] = authSourceDefaults.GitHub.GrantOnFirstBind
	data["auth_source_default_google_balance"] = authSourceDefaults.Google.Balance
	data["auth_source_default_google_concurrency"] = authSourceDefaults.Google.Concurrency
	data["auth_source_default_google_subscriptions"] = authSourceDefaults.Google.Subscriptions
	data["auth_source_default_google_grant_on_signup"] = authSourceDefaults.Google.GrantOnSignup
	data["auth_source_default_google_grant_on_first_bind"] = authSourceDefaults.Google.GrantOnFirstBind
	data["force_email_on_third_party_signup"] = authSourceDefaults.ForceEmailOnThirdPartySignup

	return data
}
