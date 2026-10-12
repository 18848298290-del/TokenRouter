package dto

import (
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/site/content"
)

// LocalizedTexts is the admin editing format for site text.
type LocalizedTexts = locale.TextUpdates

type (
	LoginAgreementDocument = content.LoginAgreementDocument
	PublicSettings         struct {
		Locale                              string                       `json:"locale"`
		SiteTextOverrides                   []string                     `json:"site_text_overrides"`
		TextLanguages                       map[string]locale.Resolution `json:"text_languages"`
		DefaultLocale                       string                       `json:"default_locale"`
		SiteTitle                           string                       `json:"site_title"`
		RegistrationEnabled                 bool                         `json:"registration_enabled"`
		EmailVerifyEnabled                  bool                         `json:"email_verify_enabled"`
		ForceEmailOnThirdPartySignup        bool                         `json:"force_email_on_third_party_signup"`
		RegistrationEmailSuffixWhitelist    []string                     `json:"registration_email_suffix_whitelist"`
		RegistrationEmailDomainQuotaEnabled bool                         `json:"registration_email_domain_quota_enabled"`
		UserEmailChangeEnabled              bool                         `json:"user_email_change_enabled"` // allow users with an email identity to replace their primary email
		PromoCodeEnabled                    bool                         `json:"promo_code_enabled"`
		PasswordResetEnabled                bool                         `json:"password_reset_enabled"`
		InvitationCodeEnabled               bool                         `json:"invitation_code_enabled"`
		TotpEnabled                         bool                         `json:"totp_enabled"` // TOTP two-factor authentication
		PasskeyEnabled                      bool                         `json:"passkey_enabled"`
		LoginAgreementEnabled               bool                         `json:"login_agreement_enabled"`
		LoginAgreementMode                  string                       `json:"login_agreement_mode"`
		LoginAgreementUpdatedAt             string                       `json:"login_agreement_updated_at"`
		LoginAgreementRevision              string                       `json:"login_agreement_revision"`
		LoginAgreementDocuments             []LoginAgreementDocument     `json:"login_agreement_documents"`
		TurnstileEnabled                    bool                         `json:"turnstile_enabled"`
		TurnstileSiteKey                    string                       `json:"turnstile_site_key"`
		TencentCaptchaEnabled               bool                         `json:"tencent_captcha_enabled"`
		TencentCaptchaAppID                 string                       `json:"tencent_captcha_app_id"`
		TencentCaptchaRegion                string                       `json:"tencent_captcha_region"`
		AliyunCaptchaEnabled                bool                         `json:"aliyun_captcha_enabled"`
		AliyunCaptchaSceneID                string                       `json:"aliyun_captcha_scene_id"`
		AliyunCaptchaPrefix                 string                       `json:"aliyun_captcha_prefix"`
		AliyunCaptchaRegion                 string                       `json:"aliyun_captcha_region"`
		SiteName                            string                       `json:"site_name"`
		SiteLogo                            string                       `json:"site_logo"`
		SiteSubtitle                        string                       `json:"site_subtitle"`
		APIBaseURL                          string                       `json:"api_base_url"`
		ContactInfo                         string                       `json:"contact_info"`
		DocURL                              string                       `json:"doc_url"`
		HomeContent                         string                       `json:"home_content"`
		HideCcsImportButton                 bool                         `json:"hide_ccs_import_button"`
		PurchaseSubscriptionEnabled         bool                         `json:"purchase_subscription_enabled"`
		PurchaseSubscriptionURL             string                       `json:"purchase_subscription_url"`
		TableDefaultPageSize                int                          `json:"table_default_page_size"`
		TablePageSizeOptions                []int                        `json:"table_page_size_options"`
		UsageRankingLimit                   int                          `json:"usage_ranking_limit"`
		UsageRankingEnabled                 bool                         `json:"usage_ranking_enabled"`
		UsageRankingSortBy                  string                       `json:"usage_ranking_sort_by"`
		UsageRankingShowTotalTokens         bool                         `json:"usage_ranking_show_total_tokens"`
		UsageRankingShowRequests            bool                         `json:"usage_ranking_show_requests"`
		UsageRankingShowActualCost          bool                         `json:"usage_ranking_show_actual_cost"`
		CustomMenuItems                     []CustomMenuItem             `json:"custom_menu_items"`
		CustomEndpoints                     []CustomEndpoint             `json:"custom_endpoints"`
		FooterLinks                         []FooterLinkGroup            `json:"footer_links"`
		FooterText                          string                       `json:"footer_text"`
		HomeFeaturedModels                  []string                     `json:"home_featured_models"`
		DingTalkOAuthEnabled                bool                         `json:"dingtalk_oauth_enabled"`
		LinuxDoOAuthEnabled                 bool                         `json:"linuxdo_oauth_enabled"`
		WeChatOAuthEnabled                  bool                         `json:"wechat_oauth_enabled"`
		WeChatOAuthOpenEnabled              bool                         `json:"wechat_oauth_open_enabled"`
		WeChatOAuthMPEnabled                bool                         `json:"wechat_oauth_mp_enabled"`
		WeChatOAuthMobileEnabled            bool                         `json:"wechat_oauth_mobile_enabled"`
		OIDCOAuthEnabled                    bool                         `json:"oidc_oauth_enabled"`
		OIDCOAuthProviderName               string                       `json:"oidc_oauth_provider_name"`
		GitHubOAuthEnabled                  bool                         `json:"github_oauth_enabled"`
		GoogleOAuthEnabled                  bool                         `json:"google_oauth_enabled"`
		GoogleOneTapEnabled                 bool                         `json:"google_one_tap_enabled"`
		GoogleOAuthClientID                 string                       `json:"google_oauth_client_id"`
		BackendModeEnabled                  bool                         `json:"backend_mode_enabled"`
		PaymentEnabled                      bool                         `json:"payment_enabled"`
		TeamEnabled                         bool                         `json:"team_enabled"`
		TeamSelfServiceEnabled              bool                         `json:"team_self_service_enabled"`
		CreativeEnabled                     bool                         `json:"creative_enabled"`
		Version                             string                       `json:"version"`
		// Server timezone and current UTC offset used to label server-local times such as peak billing windows.
		ServerTimezone              string  `json:"server_timezone"`
		ServerUTCOffset             string  `json:"server_utc_offset"`
		BalanceUnitName             string  `json:"balance_unit_name"`
		BalanceUnitSymbol           string  `json:"balance_unit_symbol"`
		BalanceIconSVG              string  `json:"balance_icon_svg"`
		BalanceLowNotifyEnabled     bool    `json:"balance_low_notify_enabled"`
		ProviderQuotaNotifyEnabled  bool    `json:"provider_quota_notify_enabled"`
		RiskControlEnabled          bool    `json:"risk_control_enabled"` // risk center entry
		CyberSessionBlockEnabled    bool    `json:"cyber_session_block_enabled"`
		CyberSessionBlockTTLSeconds int     `json:"cyber_session_block_ttl_seconds"`
		AffiliateEnabled            bool    `json:"affiliate_enabled"` // affiliate rebate entry
		BalanceLowNotifyThreshold   float64 `json:"balance_low_notify_threshold"`
		BalanceLowNotifyRechargeURL string  `json:"balance_low_notify_recharge_url"`

		// Allow users to view their own failed requests on the usage page.
		AllowUserViewErrorRequests bool `json:"allow_user_view_error_requests"`
	}
)
