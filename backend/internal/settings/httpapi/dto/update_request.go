package dto

import (
	"encoding/json"

	billinghttp "github.com/TokenFlux/TokenRouter/internal/billing/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	gatewaydto "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
	identitydto "github.com/TokenFlux/TokenRouter/internal/identity/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	sitedto "github.com/TokenFlux/TokenRouter/internal/site/httpapi/dto"
)

// UpdateSettingsRequest represents the flat combined settings payload. Pointer fields distinguish omitted
// values from supplied zero values.
type UpdateSettingsRequest struct {
	ResponsesWS json.RawMessage `json:"responses_ws,omitempty"`
	// Registration settings.
	RegistrationEnabled                 bool                             `json:"registration_enabled"`
	EmailVerifyEnabled                  bool                             `json:"email_verify_enabled"`
	RegistrationEmailSuffixWhitelist    []string                         `json:"registration_email_suffix_whitelist"`
	RegistrationEmailNormalization      bool                             `json:"registration_email_normalization"`
	RegistrationEmailDomainQuotaEnabled *bool                            `json:"registration_email_domain_quota_enabled"` // omission retains the current value
	UserEmailChangeEnabled              *bool                            `json:"user_email_change_enabled"`               // omission retains the current value
	PromoCodeEnabled                    bool                             `json:"promo_code_enabled"`
	PasswordResetEnabled                bool                             `json:"password_reset_enabled"`
	FrontendURL                         string                           `json:"frontend_url"`
	InvitationCodeEnabled               bool                             `json:"invitation_code_enabled"`
	TotpEnabled                         bool                             `json:"totp_enabled"`             // TOTP two-factor authentication
	SessionBindingEnabled               *bool                            `json:"session_binding_enabled"`  // session IP/User-Agent binding; omission retains the current value
	StepUpEnabled                       *bool                            `json:"step_up_enabled"`          // step-up 2FA; omission retains the current value
	AuditLogRetentionDays               int                              `json:"audit_log_retention_days"` // retention in days
	LoginAgreementEnabled               bool                             `json:"login_agreement_enabled"`
	LoginAgreementMode                  string                           `json:"login_agreement_mode"`
	LoginAgreementUpdatedAt             string                           `json:"login_agreement_updated_at"`
	LoginAgreementDocuments             []sitedto.LoginAgreementDocument `json:"login_agreement_documents"`

	// SMTP settings.
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPUsername string `json:"smtp_username"`
	SMTPPassword string `json:"smtp_password"`
	SMTPFrom     string `json:"smtp_from_email"`
	SMTPFromName string `json:"smtp_from_name"`
	SMTPUseTLS   bool   `json:"smtp_use_tls"`

	// Cloudflare Turnstile settings.
	TurnstileEnabled   bool   `json:"turnstile_enabled"`
	TurnstileSiteKey   string `json:"turnstile_site_key"`
	TurnstileSecretKey string `json:"turnstile_secret_key"`

	// Tencent Captcha settings.
	TencentCaptchaEnabled        bool   `json:"tencent_captcha_enabled"`
	TencentCaptchaAppID          string `json:"tencent_captcha_app_id"`
	TencentCaptchaAppSecretKey   string `json:"tencent_captcha_app_secret_key"`
	TencentCaptchaCloudSecretID  string `json:"tencent_captcha_cloud_secret_id"`
	TencentCaptchaCloudSecretKey string `json:"tencent_captcha_cloud_secret_key"`
	TencentCaptchaRegion         string `json:"tencent_captcha_region"`

	// Aliyun Captcha 2.0 settings.
	AliyunCaptchaEnabled         bool   `json:"aliyun_captcha_enabled"`
	AliyunCaptchaAccessKeyID     string `json:"aliyun_captcha_access_key_id"`
	AliyunCaptchaAccessKeySecret string `json:"aliyun_captcha_access_key_secret"`
	AliyunCaptchaSceneID         string `json:"aliyun_captcha_scene_id"`
	AliyunCaptchaPrefix          string `json:"aliyun_captcha_prefix"`
	AliyunCaptchaRegion          string `json:"aliyun_captcha_region"`

	// API key IP access control.
	APIKeyACLTrustForwardedIP *bool     `json:"api_key_acl_trust_forwarded_ip"`
	ForwardedClientIPHeaders  *[]string `json:"forwarded_client_ip_headers"`

	// LinuxDo Connect OAuth login.
	LinuxDoConnectEnabled      bool   `json:"linuxdo_connect_enabled"`
	LinuxDoConnectClientID     string `json:"linuxdo_connect_client_id"`
	LinuxDoConnectClientSecret string `json:"linuxdo_connect_client_secret"`
	LinuxDoConnectRedirectURL  string `json:"linuxdo_connect_redirect_url"`

	// DingTalk Connect OAuth login.
	DingTalkConnectEnabled                 bool   `json:"dingtalk_connect_enabled"`
	DingTalkConnectClientID                string `json:"dingtalk_connect_client_id"`
	DingTalkConnectClientSecret            string `json:"dingtalk_connect_client_secret"`
	DingTalkConnectRedirectURL             string `json:"dingtalk_connect_redirect_url"`
	DingTalkConnectCorpRestrictionPolicy   string `json:"dingtalk_connect_corp_restriction_policy"`
	DingTalkConnectInternalCorpID          string `json:"dingtalk_connect_internal_corp_id"`
	DingTalkConnectBypassRegistration      bool   `json:"dingtalk_connect_bypass_registration"`
	DingTalkConnectSyncCorpEmail           bool   `json:"dingtalk_connect_sync_corp_email"`
	DingTalkConnectSyncDisplayName         bool   `json:"dingtalk_connect_sync_display_name"`
	DingTalkConnectSyncDept                bool   `json:"dingtalk_connect_sync_dept"`
	DingTalkConnectSyncCorpEmailAttrKey    string `json:"dingtalk_connect_sync_corp_email_attr_key"`
	DingTalkConnectSyncDisplayNameAttrKey  string `json:"dingtalk_connect_sync_display_name_attr_key"`
	DingTalkConnectSyncDeptAttrKey         string `json:"dingtalk_connect_sync_dept_attr_key"`
	DingTalkConnectSyncCorpEmailAttrName   string `json:"dingtalk_connect_sync_corp_email_attr_name"`
	DingTalkConnectSyncDisplayNameAttrName string `json:"dingtalk_connect_sync_display_name_attr_name"`
	DingTalkConnectSyncDeptAttrName        string `json:"dingtalk_connect_sync_dept_attr_name"`

	// WeChat Connect OAuth login.
	WeChatConnectEnabled             bool   `json:"wechat_connect_enabled"`
	WeChatConnectAppID               string `json:"wechat_connect_app_id"`
	WeChatConnectAppSecret           string `json:"wechat_connect_app_secret"`
	WeChatConnectOpenAppID           string `json:"wechat_connect_open_app_id"`
	WeChatConnectOpenAppSecret       string `json:"wechat_connect_open_app_secret"`
	WeChatConnectMPAppID             string `json:"wechat_connect_mp_app_id"`
	WeChatConnectMPAppSecret         string `json:"wechat_connect_mp_app_secret"`
	WeChatConnectMobileAppID         string `json:"wechat_connect_mobile_app_id"`
	WeChatConnectMobileAppSecret     string `json:"wechat_connect_mobile_app_secret"`
	WeChatConnectOpenEnabled         bool   `json:"wechat_connect_open_enabled"`
	WeChatConnectMPEnabled           bool   `json:"wechat_connect_mp_enabled"`
	WeChatConnectMobileEnabled       bool   `json:"wechat_connect_mobile_enabled"`
	WeChatConnectMode                string `json:"wechat_connect_mode"`
	WeChatConnectScopes              string `json:"wechat_connect_scopes"`
	WeChatConnectRedirectURL         string `json:"wechat_connect_redirect_url"`
	WeChatConnectFrontendRedirectURL string `json:"wechat_connect_frontend_redirect_url"`

	// Generic OIDC login.
	OIDCConnectEnabled              bool   `json:"oidc_connect_enabled"`
	OIDCConnectProviderName         string `json:"oidc_connect_provider_name"`
	OIDCConnectClientID             string `json:"oidc_connect_client_id"`
	OIDCConnectClientSecret         string `json:"oidc_connect_client_secret"`
	OIDCConnectIssuerURL            string `json:"oidc_connect_issuer_url"`
	OIDCConnectDiscoveryURL         string `json:"oidc_connect_discovery_url"`
	OIDCConnectAuthorizeURL         string `json:"oidc_connect_authorize_url"`
	OIDCConnectTokenURL             string `json:"oidc_connect_token_url"`
	OIDCConnectUserInfoURL          string `json:"oidc_connect_userinfo_url"`
	OIDCConnectJWKSURL              string `json:"oidc_connect_jwks_url"`
	OIDCConnectScopes               string `json:"oidc_connect_scopes"`
	OIDCConnectRedirectURL          string `json:"oidc_connect_redirect_url"`
	OIDCConnectFrontendRedirectURL  string `json:"oidc_connect_frontend_redirect_url"`
	OIDCConnectTokenAuthMethod      string `json:"oidc_connect_token_auth_method"`
	OIDCConnectUsePKCE              *bool  `json:"oidc_connect_use_pkce"`
	OIDCConnectValidateIDToken      *bool  `json:"oidc_connect_validate_id_token"`
	OIDCConnectAllowedSigningAlgs   string `json:"oidc_connect_allowed_signing_algs"`
	OIDCConnectClockSkewSeconds     int    `json:"oidc_connect_clock_skew_seconds"`
	OIDCConnectRequireEmailVerified bool   `json:"oidc_connect_require_email_verified"`
	OIDCConnectUserInfoEmailPath    string `json:"oidc_connect_userinfo_email_path"`
	OIDCConnectUserInfoIDPath       string `json:"oidc_connect_userinfo_id_path"`
	OIDCConnectUserInfoUsernamePath string `json:"oidc_connect_userinfo_username_path"`

	// GitHub and Google email login.
	GitHubOAuthEnabled             bool   `json:"github_oauth_enabled"`
	GitHubOAuthClientID            string `json:"github_oauth_client_id"`
	GitHubOAuthClientSecret        string `json:"github_oauth_client_secret"`
	GitHubOAuthRedirectURL         string `json:"github_oauth_redirect_url"`
	GitHubOAuthFrontendRedirectURL string `json:"github_oauth_frontend_redirect_url"`
	GoogleOAuthEnabled             bool   `json:"google_oauth_enabled"`
	GoogleOneTapEnabled            bool   `json:"google_one_tap_enabled"`
	GoogleOAuthClientID            string `json:"google_oauth_client_id"`
	GoogleOAuthClientSecret        string `json:"google_oauth_client_secret"`
	GoogleOAuthRedirectURL         string `json:"google_oauth_redirect_url"`
	GoogleOAuthFrontendRedirectURL string `json:"google_oauth_frontend_redirect_url"`

	// Site branding and display settings.
	LocalizedSettings           locale.TextUpdates               `json:"localized_settings"`
	SiteTexts                   sitedto.LocalizedTexts           `json:"site_texts"`
	DefaultLocale               string                           `json:"default_locale"`
	SiteName                    string                           `json:"site_name"`
	SiteLogo                    string                           `json:"site_logo"`
	SiteSubtitle                string                           `json:"site_subtitle"`
	APIBaseURL                  string                           `json:"api_base_url"`
	ContactInfo                 string                           `json:"contact_info"`
	DocURL                      string                           `json:"doc_url"`
	HomeContent                 string                           `json:"home_content"`
	HideCcsImportButton         bool                             `json:"hide_ccs_import_button"`
	PurchaseSubscriptionEnabled *bool                            `json:"purchase_subscription_enabled"`
	PurchaseSubscriptionURL     *string                          `json:"purchase_subscription_url"`
	TableDefaultPageSize        int                              `json:"table_default_page_size"`
	TablePageSizeOptions        []int                            `json:"table_page_size_options"`
	UsageRankingLimit           int                              `json:"usage_ranking_limit"`
	UsageRankingEnabled         *bool                            `json:"usage_ranking_enabled"`
	UsageRankingSortBy          *string                          `json:"usage_ranking_sort_by"`
	UsageRankingShowTotalTokens *bool                            `json:"usage_ranking_show_total_tokens"`
	UsageRankingShowRequests    *bool                            `json:"usage_ranking_show_requests"`
	UsageRankingShowActualCost  *bool                            `json:"usage_ranking_show_actual_cost"`
	CustomMenuItems             *[]sitedto.CustomMenuItem        `json:"custom_menu_items"`
	CustomEndpoints             *[]sitedto.CustomEndpoint        `json:"custom_endpoints"`
	FooterLinks                 *[]sitedto.FooterLinkGroup       `json:"footer_links"`
	FooterText                  *string                          `json:"footer_text"`
	HomeFeaturedModels          *[]string                        `json:"home_featured_models"`
	CreativeModelSettings       *[]creative.CreativeModelSetting `json:"creative_model_settings"`
	CreativeWorkerCount         *int                             `json:"creative_worker_count"`

	// Default user settings and signup grants.
	DefaultConcurrency                        int                                       `json:"default_concurrency"`
	DefaultBalance                            float64                                   `json:"default_balance"`
	AffiliateEnabled                          bool                                      `json:"affiliate_enabled"`
	AffiliateRebateRate                       float64                                   `json:"affiliate_rebate_rate"`
	AffiliateRebateFreezeHours                int                                       `json:"affiliate_rebate_freeze_hours"`
	AffiliateRebateDurationDays               int                                       `json:"affiliate_rebate_duration_days"`
	AffiliateRebatePerInviteeCap              float64                                   `json:"affiliate_rebate_per_invitee_cap"`
	AdminRechargeRebateEnabled                *bool                                     `json:"affiliate_admin_recharge_enabled"`
	DefaultUserRPMLimit                       int                                       `json:"default_user_rpm_limit"`
	DefaultUserAPIKeyLimit                    *int                                      `json:"default_user_api_key_limit"`
	DefaultSubscriptions                      []billinghttp.DefaultSubscriptionSetting  `json:"default_subscriptions"`
	BalanceUnitName                           string                                    `json:"balance_unit_name"`
	BalanceUnitSymbol                         string                                    `json:"balance_unit_symbol"`
	BalanceIconSVG                            string                                    `json:"balance_icon_svg"`
	ReasoningPointRMBUnitPrice                *float64                                  `json:"reasoning_point_rmb_unit_price"`
	USDExchangeRate                           *float64                                  `json:"usd_exchange_rate"`
	MarketplaceAvailabilityWindowDays         *int                                      `json:"marketplace_availability_window_days"`
	MarketplaceAvailabilityBucketMinutes      *int                                      `json:"marketplace_availability_bucket_minutes"`
	AuthSourceDefaultEmailBalance             *float64                                  `json:"auth_source_default_email_balance"`
	AuthSourceDefaultEmailConcurrency         *int                                      `json:"auth_source_default_email_concurrency"`
	AuthSourceDefaultEmailSubscriptions       *[]billinghttp.DefaultSubscriptionSetting `json:"auth_source_default_email_subscriptions"`
	AuthSourceDefaultEmailGrantOnSignup       *bool                                     `json:"auth_source_default_email_grant_on_signup"`
	AuthSourceDefaultEmailGrantOnFirstBind    *bool                                     `json:"auth_source_default_email_grant_on_first_bind"`
	AuthSourceDefaultLinuxDoBalance           *float64                                  `json:"auth_source_default_linuxdo_balance"`
	AuthSourceDefaultLinuxDoConcurrency       *int                                      `json:"auth_source_default_linuxdo_concurrency"`
	AuthSourceDefaultLinuxDoSubscriptions     *[]billinghttp.DefaultSubscriptionSetting `json:"auth_source_default_linuxdo_subscriptions"`
	AuthSourceDefaultLinuxDoGrantOnSignup     *bool                                     `json:"auth_source_default_linuxdo_grant_on_signup"`
	AuthSourceDefaultLinuxDoGrantOnFirstBind  *bool                                     `json:"auth_source_default_linuxdo_grant_on_first_bind"`
	AuthSourceDefaultOIDCBalance              *float64                                  `json:"auth_source_default_oidc_balance"`
	AuthSourceDefaultOIDCConcurrency          *int                                      `json:"auth_source_default_oidc_concurrency"`
	AuthSourceDefaultOIDCSubscriptions        *[]billinghttp.DefaultSubscriptionSetting `json:"auth_source_default_oidc_subscriptions"`
	AuthSourceDefaultOIDCGrantOnSignup        *bool                                     `json:"auth_source_default_oidc_grant_on_signup"`
	AuthSourceDefaultOIDCGrantOnFirstBind     *bool                                     `json:"auth_source_default_oidc_grant_on_first_bind"`
	AuthSourceDefaultWeChatBalance            *float64                                  `json:"auth_source_default_wechat_balance"`
	AuthSourceDefaultWeChatConcurrency        *int                                      `json:"auth_source_default_wechat_concurrency"`
	AuthSourceDefaultWeChatSubscriptions      *[]billinghttp.DefaultSubscriptionSetting `json:"auth_source_default_wechat_subscriptions"`
	AuthSourceDefaultWeChatGrantOnSignup      *bool                                     `json:"auth_source_default_wechat_grant_on_signup"`
	AuthSourceDefaultWeChatGrantOnFirstBind   *bool                                     `json:"auth_source_default_wechat_grant_on_first_bind"`
	AuthSourceDefaultGitHubBalance            *float64                                  `json:"auth_source_default_github_balance"`
	AuthSourceDefaultGitHubConcurrency        *int                                      `json:"auth_source_default_github_concurrency"`
	AuthSourceDefaultGitHubSubscriptions      *[]billinghttp.DefaultSubscriptionSetting `json:"auth_source_default_github_subscriptions"`
	AuthSourceDefaultGitHubGrantOnSignup      *bool                                     `json:"auth_source_default_github_grant_on_signup"`
	AuthSourceDefaultGitHubGrantOnFirstBind   *bool                                     `json:"auth_source_default_github_grant_on_first_bind"`
	AuthSourceDefaultGoogleBalance            *float64                                  `json:"auth_source_default_google_balance"`
	AuthSourceDefaultGoogleConcurrency        *int                                      `json:"auth_source_default_google_concurrency"`
	AuthSourceDefaultGoogleSubscriptions      *[]billinghttp.DefaultSubscriptionSetting `json:"auth_source_default_google_subscriptions"`
	AuthSourceDefaultGoogleGrantOnSignup      *bool                                     `json:"auth_source_default_google_grant_on_signup"`
	AuthSourceDefaultGoogleGrantOnFirstBind   *bool                                     `json:"auth_source_default_google_grant_on_first_bind"`
	AuthSourceDefaultDingTalkBalance          *float64                                  `json:"auth_source_default_dingtalk_balance"`
	AuthSourceDefaultDingTalkConcurrency      *int                                      `json:"auth_source_default_dingtalk_concurrency"`
	AuthSourceDefaultDingTalkSubscriptions    *[]billinghttp.DefaultSubscriptionSetting `json:"auth_source_default_dingtalk_subscriptions"`
	AuthSourceDefaultDingTalkGrantOnSignup    *bool                                     `json:"auth_source_default_dingtalk_grant_on_signup"`
	AuthSourceDefaultDingTalkGrantOnFirstBind *bool                                     `json:"auth_source_default_dingtalk_grant_on_first_bind"`
	ForceEmailOnThirdPartySignup              *bool                                     `json:"force_email_on_third_party_signup"`

	// Model fallback configuration
	EnableModelFallback      bool   `json:"enable_model_fallback"`
	FallbackModelAnthropic   string `json:"fallback_model_anthropic"`
	FallbackModelOpenAI      string `json:"fallback_model_openai"`
	FallbackModelGemini      string `json:"fallback_model_gemini"`
	FallbackModelAntigravity string `json:"fallback_model_antigravity"`

	// Omitted Grok defaults retain their saved values.
	GrokDefaultTextModel   *string `json:"grok_default_text_model"`
	GrokDefaultBaseURLMode *string `json:"grok_default_base_url_mode"`

	// Identity patch configuration (Claude -> Gemini)
	EnableIdentityPatch bool   `json:"enable_identity_patch"`
	IdentityPatchPrompt string `json:"identity_patch_prompt"`

	// Operations monitoring.
	OpsMonitoringEnabled         *bool `json:"ops_monitoring_enabled"`
	OpsRealtimeMonitoringEnabled *bool `json:"ops_realtime_monitoring_enabled"`
	OpsMetricsIntervalSeconds    *int  `json:"ops_metrics_interval_seconds"`

	MinClaudeCodeVersion string `json:"min_claude_code_version"`
	MaxClaudeCodeVersion string `json:"max_claude_code_version"`

	// Backend Mode
	BackendModeEnabled bool `json:"backend_mode_enabled"`

	// Gateway forwarding behavior.
	OpenAITTFTMode                         *string                                   `json:"openai_ttft_mode"`
	EnableFingerprintUnification           *bool                                     `json:"enable_fingerprint_unification"`
	EnableMetadataPassthrough              *bool                                     `json:"enable_metadata_passthrough"`
	EnableCCHSigning                       *bool                                     `json:"enable_cch_signing"`
	EnableClaudeOAuthSystemPromptInjection *bool                                     `json:"enable_claude_oauth_system_prompt_injection"`
	ClaudeOAuthSystemPrompt                *string                                   `json:"claude_oauth_system_prompt"`
	ClaudeOAuthSystemPromptBlocks          *string                                   `json:"claude_oauth_system_prompt_blocks"`
	EnableAnthropicCacheTTL1hInjection     *bool                                     `json:"enable_anthropic_cache_ttl_1h_injection"`
	RewriteMessageCacheControl             *bool                                     `json:"rewrite_message_cache_control"`
	EnableClientDatelineNormalization      *bool                                     `json:"enable_client_dateline_normalization"`
	AntigravityUserAgentVersion            *string                                   `json:"antigravity_user_agent_version"`
	OpenAICodexUserAgent                   *string                                   `json:"openai_codex_user_agent"`
	OpenAIAllowClaudeCodeCodexPlugin       *bool                                     `json:"openai_allow_claude_code_codex_plugin"`
	UserPromptReplacementConfig            *promptpolicy.UserPromptReplacementConfig `json:"user_prompt_replacement_config"`

	// Payment visible method routing
	PaymentVisibleMethodAlipaySource  *string `json:"payment_visible_method_alipay_source"`
	PaymentVisibleMethodWxpaySource   *string `json:"payment_visible_method_wxpay_source"`
	PaymentVisibleMethodAlipayEnabled *bool   `json:"payment_visible_method_alipay_enabled"`
	PaymentVisibleMethodWxpayEnabled  *bool   `json:"payment_visible_method_wxpay_enabled"`

	// Shared advanced scheduler parameters.
	AdvancedSchedulerStickyWeightedEnabled       *bool   `json:"advanced_scheduler_sticky_weighted_enabled"`
	AdvancedSchedulerSubscriptionPriorityEnabled *bool   `json:"advanced_scheduler_subscription_priority_enabled"`
	AdvancedSchedulerEWMAErrorRateAlpha          *string `json:"advanced_scheduler_ewma_error_rate_alpha"`
	AdvancedSchedulerEWMATTFTAlpha               *string `json:"advanced_scheduler_ewma_ttft_alpha"`
	AdvancedSchedulerStickyEscapeEnabled         *bool   `json:"advanced_scheduler_sticky_escape_enabled"`
	AdvancedSchedulerStickyEscapeTTFTMs          *string `json:"advanced_scheduler_sticky_escape_ttft_ms"`
	AdvancedSchedulerStickyEscapeErrorRate       *string `json:"advanced_scheduler_sticky_escape_error_rate"`
	AdvancedSchedulerLBTopK                      *string `json:"advanced_scheduler_lb_top_k"`
	AdvancedSchedulerWeightPriority              *string `json:"advanced_scheduler_weight_priority"`
	AdvancedSchedulerWeightLoad                  *string `json:"advanced_scheduler_weight_load"`
	AdvancedSchedulerWeightQueue                 *string `json:"advanced_scheduler_weight_queue"`
	AdvancedSchedulerWeightErrorRate             *string `json:"advanced_scheduler_weight_error_rate"`
	AdvancedSchedulerWeightTTFT                  *string `json:"advanced_scheduler_weight_ttft"`
	AdvancedSchedulerWeightReset                 *string `json:"advanced_scheduler_weight_reset"`
	AdvancedSchedulerWeightQuotaHeadroom         *string `json:"advanced_scheduler_weight_quota_headroom"`
	AdvancedSchedulerWeightPreviousResponse      *string `json:"advanced_scheduler_weight_previous_response"`
	AdvancedSchedulerWeightSessionSticky         *string `json:"advanced_scheduler_weight_session_sticky"`
	// Global OpenAI quota auto-pause thresholds; nil retains saved values, while a supplied zero disables the
	// threshold.
	OpenAIQuotaAutoPauseSettings *provider.QuotaAutoPauseSettings `json:"openai_provider_quota_auto_pause"`

	// Low-balance notifications.
	BalanceLowNotifyEnabled         *bool                           `json:"balance_low_notify_enabled"`
	BalanceLowNotifyThreshold       *float64                        `json:"balance_low_notify_threshold"`
	BalanceLowNotifyRechargeURL     *string                         `json:"balance_low_notify_recharge_url"`
	SubscriptionExpiryNotifyEnabled *bool                           `json:"subscription_expiry_notify_enabled"`
	ProviderQuotaNotifyEnabled      *bool                           `json:"provider_quota_notify_enabled"`
	ProviderQuotaNotifyEmails       *[]identitydto.NotifyEmailEntry `json:"provider_quota_notify_emails"`

	// Payment configuration; omitted fields retain saved values.
	PaymentEnabled                   *bool                     `json:"payment_enabled"`
	PaymentMinAmount                 *float64                  `json:"payment_min_amount"`
	PaymentMaxAmount                 *float64                  `json:"payment_max_amount"`
	PaymentDailyLimit                *float64                  `json:"payment_daily_limit"`
	PaymentOrderTimeoutMin           *int                      `json:"payment_order_timeout_minutes"`
	PaymentMaxPendingOrders          *int                      `json:"payment_max_pending_orders"`
	PaymentEnabledTypes              []string                  `json:"payment_enabled_types"`
	PaymentBalanceDisabled           *bool                     `json:"payment_balance_disabled"`
	PaymentBalanceRechargeMultiplier *float64                  `json:"payment_balance_recharge_multiplier"`
	PaymentSubscriptionUSDToCNYRate  *float64                  `json:"payment_subscription_usd_to_cny_rate"`
	PaymentRechargeFeeRate           *float64                  `json:"payment_recharge_fee_rate"`
	PaymentMethodFees                payment.MethodFeeSettings `json:"payment_method_fees"`
	PaymentLoadBalanceStrat          *string                   `json:"payment_load_balance_strategy"`
	PaymentProductNamePrefix         *string                   `json:"payment_product_name_prefix"`
	PaymentProductNameSuffix         *string                   `json:"payment_product_name_suffix"`
	PaymentHelpImageURL              *string                   `json:"payment_help_image_url"`
	PaymentHelpText                  *string                   `json:"payment_help_text"`

	// Cancel rate limit
	PaymentCancelRateLimitEnabled *bool   `json:"payment_cancel_rate_limit_enabled"`
	PaymentCancelRateLimitMax     *int    `json:"payment_cancel_rate_limit_max"`
	PaymentCancelRateLimitWindow  *int    `json:"payment_cancel_rate_limit_window"`
	PaymentCancelRateLimitUnit    *string `json:"payment_cancel_rate_limit_unit"`
	PaymentCancelRateLimitMode    *string `json:"payment_cancel_rate_limit_window_mode"`

	// Force QR-code payment for Alipay on mobile devices.
	PaymentAlipayForceQRCode *bool `json:"payment_alipay_force_qrcode"`
	// Use Alipay face-to-face precreation and a deep link to open the mobile app.
	PaymentAlipayMobilePrecreateDeepLink *bool `json:"payment_alipay_mobile_precreate_deep_link"`

	// Risk center access.
	RiskControlEnabled *bool `json:"risk_control_enabled"`
	// Team and Creative Studio access.
	TeamEnabled     *bool `json:"team_enabled"`
	CreativeEnabled *bool `json:"creative_enabled"`

	// Cyber session blocking and TTL.
	CyberSessionBlockEnabled    *bool `json:"cyber_session_block_enabled"`
	CyberSessionBlockTTLSeconds *int  `json:"cyber_session_block_ttl_seconds"`

	// OpenAI Fast/Flex policy; updated when supplied.
	OpenAIFastPolicySettings *gatewaydto.OpenAIFastPolicySettings `json:"openai_fast_policy_settings,omitempty"`

	// Provider auto-pause thresholds by platform; nil leaves the map unchanged, while a non-nil map replaces it.
	ProviderSchedulingThresholds map[string]int `json:"provider_scheduling_thresholds"`

	AllowUserViewErrorRequests *bool `json:"allow_user_view_error_requests"`
}
