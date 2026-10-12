package site

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

const (
	defaultLoginAgreementMode = "modal"
	defaultLoginAgreementDate = "2026-03-31"

	SettingKeyAPIBaseURL                          = "api_base_url"
	SettingKeyProviderQuotaNotifyEnabled          = "provider_quota_notify_enabled"
	SettingKeyAffiliateEnabled                    = "affiliate_enabled"
	SettingKeyAliyunCaptchaEnabled                = "aliyun_captcha_enabled"
	SettingKeyAliyunCaptchaPrefix                 = "aliyun_captcha_prefix"
	SettingKeyAliyunCaptchaSceneID                = "aliyun_captcha_scene_id"
	SettingKeyAllowUserViewErrorRequests          = "allow_user_view_error_requests"
	SettingKeyBackendModeEnabled                  = "backend_mode_enabled"
	SettingKeyBalanceIconSVG                      = "balance_icon_svg"
	SettingKeyBalanceLowNotifyEnabled             = "balance_low_notify_enabled"
	SettingKeyBalanceLowNotifyRechargeURL         = "balance_low_notify_recharge_url"
	SettingKeyBalanceLowNotifyThreshold           = "balance_low_notify_threshold"
	SettingKeyBalanceUnitName                     = "balance_unit_name"
	SettingKeyBalanceUnitSymbol                   = "balance_unit_symbol"
	SettingKeyContactInfo                         = "contact_info"
	SettingKeyCreativeEnabled                     = "creative_enabled"
	SettingKeyCustomEndpoints                     = "custom_endpoints"
	SettingKeyCustomMenuItems                     = "custom_menu_items"
	SettingKeyDocURL                              = "doc_url"
	SettingKeyEmailVerifyEnabled                  = "email_verify_enabled"
	SettingKeyFooterLinks                         = "footer_links"
	SettingKeyFooterText                          = "footer_text"
	SettingKeyForceEmailOnThirdPartySignup        = "force_email_on_third_party_signup"
	SettingKeyHideCcsImportButton                 = "hide_ccs_import_button"
	SettingKeyHomeContent                         = "home_content"
	SettingKeyHomeFeaturedModels                  = "home_featured_models"
	SettingKeyInvitationCodeEnabled               = "invitation_code_enabled"
	SettingKeyLoginAgreementDocuments             = "login_agreement_documents"
	SettingKeyLoginAgreementEnabled               = "login_agreement_enabled"
	SettingKeyLoginAgreementMode                  = "login_agreement_mode"
	SettingKeyLoginAgreementUpdatedAt             = "login_agreement_updated_at"
	SettingKeyPasswordResetEnabled                = "password_reset_enabled"
	SettingKeyPromoCodeEnabled                    = "promo_code_enabled"
	SettingKeyPurchaseSubscriptionEnabled         = "purchase_subscription_enabled"
	SettingKeyPurchaseSubscriptionURL             = "purchase_subscription_url"
	SettingKeyRegistrationEmailDomainQuotaEnabled = "registration_email_domain_quota_enabled"
	SettingKeyRegistrationEnabled                 = "registration_enabled"
	SettingKeyRiskControlEnabled                  = "risk_control_enabled"
	SettingKeySiteLogo                            = "site_logo"
	SettingKeySiteName                            = "site_name"
	SettingKeySiteSubtitle                        = "site_subtitle"
	SettingKeyTableDefaultPageSize                = "table_default_page_size"
	SettingKeyTablePageSizeOptions                = "table_page_size_options"
	SettingKeyTencentCaptchaAppID                 = "tencent_captcha_app_id"
	SettingKeyTencentCaptchaEnabled               = "tencent_captcha_enabled"
	SettingKeyTotpEnabled                         = "totp_enabled"
	SettingKeyTurnstileEnabled                    = "turnstile_enabled"
	SettingKeyTurnstileSiteKey                    = "turnstile_site_key"
	SettingKeyUserEmailChangeEnabled              = "user_email_change_enabled"
	SettingPaymentEnabled                         = "payment_enabled"
)

type PublicSource interface {
	LoadSitePublicInputs(context.Context) (PublicInputs, error)
	PublicVersion() string
}
type PublicInputs struct {
	Values map[string]string
	Auth   PublicAuth
	Usage  PublicUsage
}
type PublicAuth struct {
	LinuxDo, DingTalk, OIDC, WeChat, WeChatOpen, WeChatMP, WeChatMobile, GitHub, Google, GoogleOneTap, Team, TeamSelfService, Passkey bool
	OIDCName, GoogleClientID, TencentRegion, AliyunRegion                                                                             string
	RegistrationSuffixes                                                                                                              []string
}
type PublicUsage struct {
	Limit                                         int
	Enabled                                       bool
	SortBy                                        string
	ShowTotalTokens, ShowRequests, ShowActualCost bool
}
type PublicService struct {
	source       PublicSource
	calendar     timezone.Calendar
	timezoneName string
}

func NewPublicService(source PublicSource, calendar timezone.Calendar, timezoneName string) *PublicService {
	if timezoneName == "" {
		timezoneName = calendar.Location().String()
	}
	return &PublicService{source: source, calendar: calendar, timezoneName: timezoneName}
}

// ServerTimezone returns the configured timezone and current UTC offset for API and HTML responses.
func (s *PublicService) ServerTimezone() (string, string) {
	return s.timezoneName, s.calendar.UTCOffset(s.calendar.Now())
}

func (s *PublicService) GetPublicSettings(ctx context.Context) (*PublicSettings, error) {
	input, err := s.source.LoadSitePublicInputs(ctx)
	if err != nil {
		return nil, err
	}
	settings := ResolveSiteTexts(input.Values, locale.FromContext(ctx))
	ResolveNavigation(settings, locale.FromContext(ctx))

	// Password reset requires email verification to be enabled.
	emailVerifyEnabled := settings[SettingKeyEmailVerifyEnabled] == "true"
	passwordResetEnabled := emailVerifyEnabled && settings[SettingKeyPasswordResetEnabled] == "true"
	registrationEmailSuffixWhitelist := input.Auth.RegistrationSuffixes
	tableDefaultPageSize, tablePageSizeOptions := ParseTablePreferences(
		settings[SettingKeyTableDefaultPageSize],
		settings[SettingKeyTablePageSizeOptions],
	)
	usageRanking := input.Usage
	loginAgreementDocuments := ParseLoginAgreementDocuments(settings[SettingKeyLoginAgreementDocuments])
	agreementDate := strings.TrimSpace(settings[SettingKeyLoginAgreementUpdatedAt])
	if agreementDate == "" {
		agreementDate = defaultLoginAgreementDate
	}
	agreementRevision := BuildLoginAgreementRevision(agreementDate, loginAgreementDocuments)
	for i := range loginAgreementDocuments {
		doc := &loginAgreementDocuments[i]
		if doc.Localization != nil {
			copy, actual := doc.Localization.Resolve(locale.FromContext(ctx))
			doc.Resolution = &actual
			doc.Title, doc.ContentMD = copy.Title, copy.ContentMD
			doc.Localization = nil
		} else if settings[SettingKeyLoginAgreementDocuments] == "" && locale.FromContext(ctx) == "en" {
			doc.Title = map[string]string{"terms": "Terms of Service", "usage-policy": "Usage Policy", "supported-regions": "Supported Countries and Regions", "service-specific-terms": "Service-specific Terms"}[doc.ID]
		}
	}
	loginAgreementUpdatedAt := strings.TrimSpace(settings[SettingKeyLoginAgreementUpdatedAt])
	if loginAgreementUpdatedAt == "" {
		loginAgreementUpdatedAt = defaultLoginAgreementDate
	}

	var balanceLowNotifyThreshold float64
	if v, err := strconv.ParseFloat(settings[SettingKeyBalanceLowNotifyThreshold], 64); err == nil && v >= 0 {
		balanceLowNotifyThreshold = v
	}
	balanceUnitName := locale.ResolveSettingText(settings, SettingKeyBalanceUnitName, "USD", locale.FromContext(ctx))
	if balanceUnitName == "" {
		balanceUnitName = "USD"
	}
	balanceUnitSymbol := strings.TrimSpace(settings[SettingKeyBalanceUnitSymbol])
	if balanceUnitSymbol == "" {
		balanceUnitSymbol = "$"
	}

	overrides := []string{}
	textLanguages := map[string]locale.Resolution{}
	for key, content := range ConfiguredSiteTexts(input.Values) {
		overrides = append(overrides, key)
		_, textLanguages[key] = content.Resolve(locale.FromContext(ctx))
	}
	sort.Strings(overrides)
	return &PublicSettings{
		Locale:                              locale.FromContext(ctx),
		SiteTextOverrides:                   overrides,
		TextLanguages:                       textLanguages,
		DefaultLocale:                       locale.Negotiate(settings[SettingKeyDefaultLocale], locale.Default()),
		SiteTitle:                           settings["site_title"],
		RegistrationEnabled:                 settings[SettingKeyRegistrationEnabled] == "true",
		EmailVerifyEnabled:                  emailVerifyEnabled,
		ForceEmailOnThirdPartySignup:        settings[SettingKeyForceEmailOnThirdPartySignup] == "true",
		RegistrationEmailSuffixWhitelist:    registrationEmailSuffixWhitelist,
		RegistrationEmailDomainQuotaEnabled: settings[SettingKeyRegistrationEmailDomainQuotaEnabled] == "true",
		UserEmailChangeEnabled:              settings[SettingKeyUserEmailChangeEnabled] == "true",
		PromoCodeEnabled:                    settings[SettingKeyPromoCodeEnabled] != "false", // Enabled by default.
		PasswordResetEnabled:                passwordResetEnabled,
		InvitationCodeEnabled:               settings[SettingKeyInvitationCodeEnabled] == "true",
		// app combines the database team setting with the deployment switch.
		TeamEnabled:                 input.Auth.Team,
		TeamSelfServiceEnabled:      input.Auth.TeamSelfService,
		CreativeEnabled:             settings[SettingKeyCreativeEnabled] != "false",
		AffiliateEnabled:            settings[SettingKeyAffiliateEnabled] == "true",
		TotpEnabled:                 settings[SettingKeyTotpEnabled] == "true",
		PasskeyEnabled:              input.Auth.Passkey,
		LoginAgreementEnabled:       settings[SettingKeyLoginAgreementEnabled] == "true" && len(loginAgreementDocuments) > 0,
		LoginAgreementMode:          NormalizeLoginAgreementMode(settings[SettingKeyLoginAgreementMode]),
		LoginAgreementUpdatedAt:     loginAgreementUpdatedAt,
		LoginAgreementRevision:      agreementRevision,
		LoginAgreementDocuments:     loginAgreementDocuments,
		TurnstileEnabled:            settings[SettingKeyTurnstileEnabled] == "true",
		TurnstileSiteKey:            settings[SettingKeyTurnstileSiteKey],
		TencentCaptchaEnabled:       settings[SettingKeyTencentCaptchaEnabled] == "true",
		TencentCaptchaAppID:         settings[SettingKeyTencentCaptchaAppID],
		TencentCaptchaRegion:        input.Auth.TencentRegion,
		AliyunCaptchaEnabled:        settings[SettingKeyAliyunCaptchaEnabled] == "true",
		AliyunCaptchaSceneID:        settings[SettingKeyAliyunCaptchaSceneID],
		AliyunCaptchaPrefix:         settings[SettingKeyAliyunCaptchaPrefix],
		AliyunCaptchaRegion:         input.Auth.AliyunRegion,
		SiteName:                    s.getStringOrDefault(settings, SettingKeySiteName, "TokenRouter"),
		SiteLogo:                    settings[SettingKeySiteLogo],
		SiteSubtitle:                settings[SettingKeySiteSubtitle],
		APIBaseURL:                  settings[SettingKeyAPIBaseURL],
		ContactInfo:                 settings[SettingKeyContactInfo],
		DocURL:                      settings[SettingKeyDocURL],
		HomeContent:                 settings[SettingKeyHomeContent],
		HideCcsImportButton:         settings[SettingKeyHideCcsImportButton] == "true",
		PurchaseSubscriptionEnabled: settings[SettingKeyPurchaseSubscriptionEnabled] == "true",
		PurchaseSubscriptionURL:     strings.TrimSpace(settings[SettingKeyPurchaseSubscriptionURL]),
		TableDefaultPageSize:        tableDefaultPageSize,
		TablePageSizeOptions:        tablePageSizeOptions,
		UsageRankingLimit:           usageRanking.Limit,
		UsageRankingEnabled:         usageRanking.Enabled,
		UsageRankingSortBy:          usageRanking.SortBy,
		UsageRankingShowTotalTokens: usageRanking.ShowTotalTokens,
		UsageRankingShowRequests:    usageRanking.ShowRequests,
		UsageRankingShowActualCost:  usageRanking.ShowActualCost,
		CustomMenuItems:             settings[SettingKeyCustomMenuItems],
		CustomEndpoints:             settings[SettingKeyCustomEndpoints],
		FooterLinks:                 settings[SettingKeyFooterLinks],
		FooterText:                  settings[SettingKeyFooterText],
		HomeFeaturedModels:          settings[SettingKeyHomeFeaturedModels],
		LinuxDoOAuthEnabled:         input.Auth.LinuxDo,
		DingTalkOAuthEnabled:        input.Auth.DingTalk,
		WeChatOAuthEnabled:          input.Auth.WeChat,
		WeChatOAuthOpenEnabled:      input.Auth.WeChatOpen,
		WeChatOAuthMPEnabled:        input.Auth.WeChatMP,
		WeChatOAuthMobileEnabled:    input.Auth.WeChatMobile,
		BackendModeEnabled:          settings[SettingKeyBackendModeEnabled] == "true",
		PaymentEnabled:              settings[SettingPaymentEnabled] == "true",
		OIDCOAuthEnabled:            input.Auth.OIDC,
		OIDCOAuthProviderName:       locale.ResolveSettingText(settings, "oidc_connect_provider_name", input.Auth.OIDCName, locale.FromContext(ctx)),
		GitHubOAuthEnabled:          input.Auth.GitHub,
		GoogleOAuthEnabled:          input.Auth.Google,
		GoogleOneTapEnabled:         input.Auth.GoogleOneTap,
		GoogleOAuthClientID:         input.Auth.GoogleClientID,
		BalanceUnitName:             balanceUnitName,
		BalanceUnitSymbol:           balanceUnitSymbol,
		BalanceIconSVG:              strings.TrimSpace(settings[SettingKeyBalanceIconSVG]),
		BalanceLowNotifyEnabled:     settings[SettingKeyBalanceLowNotifyEnabled] == "true",
		ProviderQuotaNotifyEnabled:  settings[SettingKeyProviderQuotaNotifyEnabled] == "true",
		RiskControlEnabled:          settings[SettingKeyRiskControlEnabled] == "true",
		BalanceLowNotifyThreshold:   balanceLowNotifyThreshold,
		BalanceLowNotifyRechargeURL: locale.ResolveSettingText(settings, SettingKeyBalanceLowNotifyRechargeURL, "", locale.FromContext(ctx)),
		AllowUserViewErrorRequests:  settings[SettingKeyAllowUserViewErrorRequests] == "true",
	}, nil
}

// getStringOrDefault returns defaultValue when the setting is absent or empty.
func (s *PublicService) getStringOrDefault(settings map[string]string, key, defaultValue string) string {
	if value, ok := settings[key]; ok && value != "" {
		return value
	}
	return defaultValue
}

func NormalizeLoginAgreementMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "checkbox":
		return "checkbox"
	default:
		return defaultLoginAgreementMode
	}
}

func DefaultLoginAgreementDocuments() []LoginAgreementDocument {
	return []LoginAgreementDocument{
		{
			ID:        "terms",
			Title:     "服务条款",
			ContentMD: "",
		},
		{
			ID:        "usage-policy",
			Title:     "使用政策",
			ContentMD: "",
		},
		{
			ID:        "supported-regions",
			Title:     "支持的国家和地区",
			ContentMD: "",
		},
		{
			ID:        "service-specific-terms",
			Title:     "服务特定条款",
			ContentMD: "",
		},
	}
}

func NormalizeLoginAgreementDocumentID(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	lastSeparator := false
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			_, _ = b.WriteRune(r)
			lastSeparator = false
			continue
		}
		if r == '-' || r == '_' || r == ' ' || r == '.' || r == '/' {
			if !lastSeparator && b.Len() > 0 {
				if r == '_' {
					_, _ = b.WriteRune('_')
				} else {
					_, _ = b.WriteRune('-')
				}
				lastSeparator = true
			}
		}
	}
	return strings.Trim(b.String(), "-_")
}

func NormalizeLoginAgreementDocuments(docs []LoginAgreementDocument) []LoginAgreementDocument {
	normalized := make([]LoginAgreementDocument, 0, len(docs))
	seen := make(map[string]int, len(docs))
	for i, doc := range docs {
		title := strings.TrimSpace(doc.Title)
		content := strings.TrimSpace(doc.ContentMD)
		if title == "" && content == "" {
			continue
		}
		id := NormalizeLoginAgreementDocumentID(doc.ID)
		if id == "" {
			sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%s", i, title, content)))
			id = hex.EncodeToString(sum[:])[:12]
		}
		baseID := id
		for suffix := 2; seen[id] > 0; suffix++ {
			id = fmt.Sprintf("%s-%d", baseID, suffix)
		}
		seen[id]++
		normalized = append(normalized, LoginAgreementDocument{
			ID:           id,
			Localization: doc.Localization,
			Title:        title,
			ContentMD:    content,
		})
	}
	return normalized
}

func ParseLoginAgreementDocuments(raw string) []LoginAgreementDocument {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultLoginAgreementDocuments()
	}
	var docs []LoginAgreementDocument
	if err := json.Unmarshal([]byte(raw), &docs); err != nil {
		return DefaultLoginAgreementDocuments()
	}
	docs = NormalizeLoginAgreementDocuments(docs)
	if len(docs) == 0 {
		return DefaultLoginAgreementDocuments()
	}
	return docs
}

func MarshalLoginAgreementDocuments(docs []LoginAgreementDocument) (string, error) {
	normalized := NormalizeLoginAgreementDocuments(docs)
	if len(normalized) == 0 {
		normalized = DefaultLoginAgreementDocuments()
	}
	b, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("marshal login agreement documents: %w", err)
	}
	return string(b), nil
}

func BuildLoginAgreementRevision(updatedAt string, docs []LoginAgreementDocument) string {
	normalized := NormalizeLoginAgreementDocuments(docs)
	for i := range normalized {
		normalized[i].Localization = nil
		normalized[i].Resolution = nil
	}
	payload, err := json.Marshal(struct {
		UpdatedAt string                   `json:"updated_at"`
		Documents []LoginAgreementDocument `json:"documents"`
	}{
		UpdatedAt: strings.TrimSpace(updatedAt),
		Documents: normalized,
	})
	if err != nil {
		payload = []byte(strings.TrimSpace(updatedAt))
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])[:16]
}

// GetLegalDocument returns a public legal document resolved for the request language.
func (s *PublicService) GetLegalDocument(ctx context.Context, id string) (*LoginAgreementDocument, error) {
	settings, err := s.GetPublicSettings(ctx)
	if err != nil {
		return nil, err
	}
	for _, doc := range settings.LoginAgreementDocuments {
		if doc.ID == id {
			return &doc, nil
		}
	}
	return nil, ErrPageNotFound
}

// GetPublicSettingsForInjection returns public settings for HTML injection,
// with legal document bodies omitted.
// It implements web.PublicSettingsProvider.
func (s *PublicService) GetPublicSettingsForInjection(ctx context.Context) (any, error) {
	settings, err := s.GetPublicSettings(ctx)
	if err != nil {
		return nil, err
	}

	for i := range settings.LoginAgreementDocuments {
		settings.LoginAgreementDocuments[i].ContentMD = ""
	}

	// JSON tags match the settings object consumed by the frontend.
	return &struct {
		RegistrationEnabled                 bool                         `json:"registration_enabled"`
		EmailVerifyEnabled                  bool                         `json:"email_verify_enabled"`
		ForceEmailOnThirdPartySignup        bool                         `json:"force_email_on_third_party_signup"`
		RegistrationEmailSuffixWhitelist    []string                     `json:"registration_email_suffix_whitelist"`
		RegistrationEmailDomainQuotaEnabled bool                         `json:"registration_email_domain_quota_enabled"`
		UserEmailChangeEnabled              bool                         `json:"user_email_change_enabled"` // Allows users with an email address to replace their primary email.
		PromoCodeEnabled                    bool                         `json:"promo_code_enabled"`
		PasswordResetEnabled                bool                         `json:"password_reset_enabled"`
		InvitationCodeEnabled               bool                         `json:"invitation_code_enabled"`
		TotpEnabled                         bool                         `json:"totp_enabled"`
		PasskeyEnabled                      bool                         `json:"passkey_enabled"`
		LoginAgreementEnabled               bool                         `json:"login_agreement_enabled"`
		LoginAgreementMode                  string                       `json:"login_agreement_mode"`
		LoginAgreementUpdatedAt             string                       `json:"login_agreement_updated_at"`
		LoginAgreementRevision              string                       `json:"login_agreement_revision"`
		LoginAgreementDocuments             []LoginAgreementDocument     `json:"login_agreement_documents"`
		TurnstileEnabled                    bool                         `json:"turnstile_enabled"`
		TurnstileSiteKey                    string                       `json:"turnstile_site_key,omitempty"`
		TencentCaptchaEnabled               bool                         `json:"tencent_captcha_enabled"`
		TencentCaptchaAppID                 string                       `json:"tencent_captcha_app_id,omitempty"`
		TencentCaptchaRegion                string                       `json:"tencent_captcha_region,omitempty"`
		AliyunCaptchaEnabled                bool                         `json:"aliyun_captcha_enabled"`
		AliyunCaptchaSceneID                string                       `json:"aliyun_captcha_scene_id,omitempty"`
		AliyunCaptchaPrefix                 string                       `json:"aliyun_captcha_prefix,omitempty"`
		AliyunCaptchaRegion                 string                       `json:"aliyun_captcha_region,omitempty"`
		Locale                              string                       `json:"locale"`
		SiteTextOverrides                   []string                     `json:"site_text_overrides"`
		TextLanguages                       map[string]locale.Resolution `json:"text_languages"`
		DefaultLocale                       string                       `json:"default_locale"`
		SiteTitle                           string                       `json:"site_title"`
		SiteName                            string                       `json:"site_name"`
		SiteLogo                            string                       `json:"site_logo,omitempty"`
		SiteSubtitle                        string                       `json:"site_subtitle,omitempty"`
		APIBaseURL                          string                       `json:"api_base_url,omitempty"`
		ContactInfo                         string                       `json:"contact_info,omitempty"`
		DocURL                              string                       `json:"doc_url,omitempty"`
		HomeContent                         string                       `json:"home_content,omitempty"`
		HideCcsImportButton                 bool                         `json:"hide_ccs_import_button"`
		PurchaseSubscriptionEnabled         bool                         `json:"purchase_subscription_enabled"`
		PurchaseSubscriptionURL             string                       `json:"purchase_subscription_url,omitempty"`
		TableDefaultPageSize                int                          `json:"table_default_page_size"`
		TablePageSizeOptions                []int                        `json:"table_page_size_options"`
		UsageRankingLimit                   int                          `json:"usage_ranking_limit"`
		UsageRankingEnabled                 bool                         `json:"usage_ranking_enabled"`
		UsageRankingSortBy                  string                       `json:"usage_ranking_sort_by"`
		UsageRankingShowTotalTokens         bool                         `json:"usage_ranking_show_total_tokens"`
		UsageRankingShowRequests            bool                         `json:"usage_ranking_show_requests"`
		UsageRankingShowActualCost          bool                         `json:"usage_ranking_show_actual_cost"`
		CustomMenuItems                     json.RawMessage              `json:"custom_menu_items"`
		CustomEndpoints                     json.RawMessage              `json:"custom_endpoints"`
		FooterLinks                         json.RawMessage              `json:"footer_links"`
		FooterText                          string                       `json:"footer_text,omitempty"`
		HomeFeaturedModels                  json.RawMessage              `json:"home_featured_models"`
		LinuxDoOAuthEnabled                 bool                         `json:"linuxdo_oauth_enabled"`
		DingTalkOAuthEnabled                bool                         `json:"dingtalk_oauth_enabled"`
		WeChatOAuthEnabled                  bool                         `json:"wechat_oauth_enabled"`
		WeChatOAuthOpenEnabled              bool                         `json:"wechat_oauth_open_enabled"`
		WeChatOAuthMPEnabled                bool                         `json:"wechat_oauth_mp_enabled"`
		WeChatOAuthMobileEnabled            bool                         `json:"wechat_oauth_mobile_enabled"`
		BackendModeEnabled                  bool                         `json:"backend_mode_enabled"`
		PaymentEnabled                      bool                         `json:"payment_enabled"`
		TeamEnabled                         bool                         `json:"team_enabled"`
		TeamSelfServiceEnabled              bool                         `json:"team_self_service_enabled"`
		CreativeEnabled                     bool                         `json:"creative_enabled"`
		OIDCOAuthEnabled                    bool                         `json:"oidc_oauth_enabled"`
		OIDCOAuthProviderName               string                       `json:"oidc_oauth_provider_name"`
		GitHubOAuthEnabled                  bool                         `json:"github_oauth_enabled"`
		GoogleOAuthEnabled                  bool                         `json:"google_oauth_enabled"`
		GoogleOneTapEnabled                 bool                         `json:"google_one_tap_enabled"`
		GoogleOAuthClientID                 string                       `json:"google_oauth_client_id"`
		Version                             string                       `json:"version,omitempty"`
		// Server timezone and current UTC offset for displaying server-local times, such as peak billing windows.
		ServerTimezone              string  `json:"server_timezone"`
		ServerUTCOffset             string  `json:"server_utc_offset"`
		BalanceUnitName             string  `json:"balance_unit_name"`
		BalanceUnitSymbol           string  `json:"balance_unit_symbol"`
		BalanceIconSVG              string  `json:"balance_icon_svg"`
		BalanceLowNotifyEnabled     bool    `json:"balance_low_notify_enabled"`
		ProviderQuotaNotifyEnabled  bool    `json:"provider_quota_notify_enabled"`
		RiskControlEnabled          bool    `json:"risk_control_enabled"`
		AffiliateEnabled            bool    `json:"affiliate_enabled"`
		BalanceLowNotifyThreshold   float64 `json:"balance_low_notify_threshold"`
		BalanceLowNotifyRechargeURL string  `json:"balance_low_notify_recharge_url"`
		AllowUserViewErrorRequests  bool    `json:"allow_user_view_error_requests"`
	}{
		RegistrationEnabled:                 settings.RegistrationEnabled,
		EmailVerifyEnabled:                  settings.EmailVerifyEnabled,
		ForceEmailOnThirdPartySignup:        settings.ForceEmailOnThirdPartySignup,
		RegistrationEmailSuffixWhitelist:    settings.RegistrationEmailSuffixWhitelist,
		RegistrationEmailDomainQuotaEnabled: settings.RegistrationEmailDomainQuotaEnabled,
		UserEmailChangeEnabled:              settings.UserEmailChangeEnabled,
		PromoCodeEnabled:                    settings.PromoCodeEnabled,
		PasswordResetEnabled:                settings.PasswordResetEnabled,
		InvitationCodeEnabled:               settings.InvitationCodeEnabled,
		TotpEnabled:                         settings.TotpEnabled,
		PasskeyEnabled:                      settings.PasskeyEnabled,
		LoginAgreementEnabled:               settings.LoginAgreementEnabled,
		LoginAgreementMode:                  settings.LoginAgreementMode,
		LoginAgreementUpdatedAt:             settings.LoginAgreementUpdatedAt,
		LoginAgreementRevision:              settings.LoginAgreementRevision,
		LoginAgreementDocuments:             settings.LoginAgreementDocuments,
		TurnstileEnabled:                    settings.TurnstileEnabled,
		TurnstileSiteKey:                    settings.TurnstileSiteKey,
		TencentCaptchaEnabled:               settings.TencentCaptchaEnabled,
		TencentCaptchaAppID:                 settings.TencentCaptchaAppID,
		TencentCaptchaRegion:                settings.TencentCaptchaRegion,
		AliyunCaptchaEnabled:                settings.AliyunCaptchaEnabled,
		AliyunCaptchaSceneID:                settings.AliyunCaptchaSceneID,
		AliyunCaptchaPrefix:                 settings.AliyunCaptchaPrefix,
		AliyunCaptchaRegion:                 settings.AliyunCaptchaRegion,
		Locale:                              settings.Locale,
		SiteTextOverrides:                   settings.SiteTextOverrides,
		TextLanguages:                       settings.TextLanguages,
		DefaultLocale:                       settings.DefaultLocale,
		SiteTitle:                           settings.SiteTitle,
		SiteName:                            settings.SiteName,
		SiteLogo:                            settings.SiteLogo,
		SiteSubtitle:                        settings.SiteSubtitle,
		APIBaseURL:                          settings.APIBaseURL,
		ContactInfo:                         settings.ContactInfo,
		DocURL:                              settings.DocURL,
		HomeContent:                         settings.HomeContent,
		HideCcsImportButton:                 settings.HideCcsImportButton,
		PurchaseSubscriptionEnabled:         settings.PurchaseSubscriptionEnabled,
		PurchaseSubscriptionURL:             settings.PurchaseSubscriptionURL,
		TableDefaultPageSize:                settings.TableDefaultPageSize,
		TablePageSizeOptions:                settings.TablePageSizeOptions,
		UsageRankingLimit:                   settings.UsageRankingLimit,
		UsageRankingEnabled:                 settings.UsageRankingEnabled,
		UsageRankingSortBy:                  settings.UsageRankingSortBy,
		UsageRankingShowTotalTokens:         settings.UsageRankingShowTotalTokens,
		UsageRankingShowRequests:            settings.UsageRankingShowRequests,
		UsageRankingShowActualCost:          settings.UsageRankingShowActualCost,
		CustomMenuItems:                     FilterUserVisibleMenuItems(settings.CustomMenuItems),
		CustomEndpoints:                     SafeRawJSONArray(settings.CustomEndpoints),
		FooterLinks:                         SafeRawJSONArray(settings.FooterLinks),
		FooterText:                          settings.FooterText,
		HomeFeaturedModels:                  SafeRawJSONArray(settings.HomeFeaturedModels),
		LinuxDoOAuthEnabled:                 settings.LinuxDoOAuthEnabled,
		DingTalkOAuthEnabled:                settings.DingTalkOAuthEnabled,
		WeChatOAuthEnabled:                  settings.WeChatOAuthEnabled,
		WeChatOAuthOpenEnabled:              settings.WeChatOAuthOpenEnabled,
		WeChatOAuthMPEnabled:                settings.WeChatOAuthMPEnabled,
		WeChatOAuthMobileEnabled:            settings.WeChatOAuthMobileEnabled,
		BackendModeEnabled:                  settings.BackendModeEnabled,
		PaymentEnabled:                      settings.PaymentEnabled,
		TeamEnabled:                         settings.TeamEnabled,
		TeamSelfServiceEnabled:              settings.TeamSelfServiceEnabled,
		CreativeEnabled:                     settings.CreativeEnabled,
		OIDCOAuthEnabled:                    settings.OIDCOAuthEnabled,
		OIDCOAuthProviderName:               settings.OIDCOAuthProviderName,
		GitHubOAuthEnabled:                  settings.GitHubOAuthEnabled,
		GoogleOAuthEnabled:                  settings.GoogleOAuthEnabled,
		GoogleOneTapEnabled:                 settings.GoogleOneTapEnabled,
		GoogleOAuthClientID:                 settings.GoogleOAuthClientID,
		Version:                             s.source.PublicVersion(),
		ServerTimezone:                      s.timezoneName,
		ServerUTCOffset:                     s.calendar.UTCOffset(s.calendar.Now()),
		BalanceUnitName:                     settings.BalanceUnitName,
		BalanceUnitSymbol:                   settings.BalanceUnitSymbol,
		BalanceIconSVG:                      settings.BalanceIconSVG,
		BalanceLowNotifyEnabled:             settings.BalanceLowNotifyEnabled,
		ProviderQuotaNotifyEnabled:          settings.ProviderQuotaNotifyEnabled,
		RiskControlEnabled:                  settings.RiskControlEnabled,
		AffiliateEnabled:                    settings.AffiliateEnabled,
		BalanceLowNotifyThreshold:           settings.BalanceLowNotifyThreshold,
		BalanceLowNotifyRechargeURL:         settings.BalanceLowNotifyRechargeURL,
		AllowUserViewErrorRequests:          settings.AllowUserViewErrorRequests,
	}, nil
}

// FilterUserVisibleMenuItems removes menu entries with admin visibility from a JSON array.
func FilterUserVisibleMenuItems(raw string) json.RawMessage {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return json.RawMessage("[]")
	}
	var items []struct {
		Visibility string `json:"visibility"`
	}
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return json.RawMessage("[]")
	}

	// Keep each retained item as raw JSON so fields beyond visibility survive filtering.
	var fullItems []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fullItems); err != nil {
		return json.RawMessage("[]")
	}

	var filtered []json.RawMessage
	for i, item := range items {
		if item.Visibility != "admin" {
			filtered = append(filtered, fullItems[i])
		}
	}
	if len(filtered) == 0 {
		return json.RawMessage("[]")
	}
	result, err := json.Marshal(filtered)
	if err != nil {
		return json.RawMessage("[]")
	}
	return result
}

// SafeRawJSONArray returns valid JSON as a RawMessage, or [] for empty or invalid
// input. It does not check whether valid JSON has an array shape.
func SafeRawJSONArray(raw string) json.RawMessage {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return json.RawMessage("[]")
	}
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw)
	}
	return json.RawMessage("[]")
}

// GetFrameSrcOrigins collects unique HTTP(S) origins across supported languages
// from home content, enabled subscription links, and menu URLs for CSP frame-src.
func (s *PublicService) GetFrameSrcOrigins(ctx context.Context) ([]string, error) {
	seen := make(map[string]struct{})
	var origins []string

	addOrigin := func(rawURL string) {
		if origin := ExtractOriginFromURL(rawURL); origin != "" {
			if _, ok := seen[origin]; !ok {
				seen[origin] = struct{}{}
				origins = append(origins, origin)
			}
		}
	}

	for _, definition := range locale.Definitions() {
		settings, err := s.GetPublicSettings(locale.WithLanguage(ctx, definition.Code))
		if err != nil {
			return nil, err
		}
		// Home content may be an iframe URL.
		addOrigin(settings.HomeContent)

		// Disabled subscription links do not need a frame-src origin.
		if settings.PurchaseSubscriptionEnabled {
			addOrigin(settings.PurchaseSubscriptionURL)
		}

		// Admin menu entries also need their iframe origins allowed by CSP.
		for _, item := range ParseCustomMenuItemURLs(settings.CustomMenuItems) {
			addOrigin(item)
		}
	}

	return origins, nil
}

// ExtractOriginFromURL returns the scheme and host of an HTTP(S) URL, or an empty string.
func ExtractOriginFromURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// ParseCustomMenuItemURLs extracts nonempty URLs from a JSON array of menu items.
func ParseCustomMenuItemURLs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var items []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	urls := make([]string, 0, len(items))
	for _, item := range items {
		if item.URL != "" {
			urls = append(urls, item.URL)
		}
	}
	return urls
}

func NormalizeTablePreferences(defaultPageSize int, options []int) (int, []int) {
	const minPageSize = 5
	const maxPageSize = 1000
	const fallbackPageSize = 20

	seen := make(map[int]struct{}, len(options))
	normalizedOptions := make([]int, 0, len(options))
	for _, option := range options {
		if option < minPageSize || option > maxPageSize {
			continue
		}
		if _, ok := seen[option]; ok {
			continue
		}
		seen[option] = struct{}{}
		normalizedOptions = append(normalizedOptions, option)
	}
	sort.Ints(normalizedOptions)

	if defaultPageSize < minPageSize || defaultPageSize > maxPageSize {
		defaultPageSize = fallbackPageSize
	}

	if len(normalizedOptions) == 0 {
		normalizedOptions = []int{10, 20, 50}
	}

	return defaultPageSize, normalizedOptions
}

func ParseTablePreferences(defaultPageSizeRaw, optionsRaw string) (int, []int) {
	defaultPageSize := 20
	if v, err := strconv.Atoi(strings.TrimSpace(defaultPageSizeRaw)); err == nil {
		defaultPageSize = v
	}

	var options []int
	if strings.TrimSpace(optionsRaw) != "" {
		_ = json.Unmarshal([]byte(optionsRaw), &options)
	}

	return NormalizeTablePreferences(defaultPageSize, options)
}

func PublicValueKeys() []string {
	return []string{"balance_unit_name_localized", "oidc_connect_provider_name_localized", "balance_low_notify_recharge_url_localized", SettingKeySiteTexts, SettingKeyDefaultLocale, SettingKeyAPIBaseURL, SettingKeyProviderQuotaNotifyEnabled, SettingKeyAffiliateEnabled, SettingKeyAliyunCaptchaEnabled, SettingKeyAliyunCaptchaPrefix, SettingKeyAliyunCaptchaSceneID, SettingKeyAllowUserViewErrorRequests, SettingKeyBackendModeEnabled, SettingKeyBalanceIconSVG, SettingKeyBalanceLowNotifyEnabled, SettingKeyBalanceLowNotifyRechargeURL, SettingKeyBalanceLowNotifyThreshold, SettingKeyBalanceUnitName, SettingKeyBalanceUnitSymbol, SettingKeyContactInfo, SettingKeyCreativeEnabled, SettingKeyCustomEndpoints, SettingKeyCustomMenuItems, SettingKeyDocURL, SettingKeyEmailVerifyEnabled, SettingKeyFooterLinks, SettingKeyFooterText, SettingKeyForceEmailOnThirdPartySignup, SettingKeyHideCcsImportButton, SettingKeyHomeContent, SettingKeyHomeFeaturedModels, SettingKeyInvitationCodeEnabled, SettingKeyLoginAgreementDocuments, SettingKeyLoginAgreementEnabled, SettingKeyLoginAgreementMode, SettingKeyLoginAgreementUpdatedAt, SettingKeyPasswordResetEnabled, SettingKeyPromoCodeEnabled, SettingKeyPurchaseSubscriptionEnabled, SettingKeyPurchaseSubscriptionURL, SettingKeyRegistrationEmailDomainQuotaEnabled, SettingKeyRegistrationEnabled, SettingKeyRiskControlEnabled, SettingKeySiteLogo, SettingKeySiteName, SettingKeySiteSubtitle, SettingKeyTableDefaultPageSize, SettingKeyTablePageSizeOptions, SettingKeyTencentCaptchaAppID, SettingKeyTencentCaptchaEnabled, SettingKeyTotpEnabled, SettingKeyTurnstileEnabled, SettingKeyTurnstileSiteKey, SettingKeyUserEmailChangeEnabled, SettingPaymentEnabled}
}
