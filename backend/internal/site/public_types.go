package site

import (
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/site/content"
)

type (
	LoginAgreementCopy     = content.LoginAgreementCopy
	LoginAgreementDocument = content.LoginAgreementDocument
	PublicSettings         struct {
		Locale                              string                       `json:"locale"`
		SiteTextOverrides                   []string                     `json:"site_text_overrides"`
		TextLanguages                       map[string]locale.Resolution `json:"text_languages"`
		DefaultLocale                       string                       `json:"default_locale"`
		SiteTitle                           string                       `json:"site_title"`
		RegistrationEnabled                 bool
		EmailVerifyEnabled                  bool
		ForceEmailOnThirdPartySignup        bool
		RegistrationEmailSuffixWhitelist    []string
		RegistrationEmailDomainQuotaEnabled bool
		UserEmailChangeEnabled              bool // Allows users with an email address to replace their primary email.
		PromoCodeEnabled                    bool
		PasswordResetEnabled                bool
		InvitationCodeEnabled               bool
		TotpEnabled                         bool // TOTP two-factor authentication.
		PasskeyEnabled                      bool // Controlled by deployment-level WebAuthn configuration.
		LoginAgreementEnabled               bool
		LoginAgreementMode                  string
		LoginAgreementUpdatedAt             string
		LoginAgreementRevision              string
		LoginAgreementDocuments             []LoginAgreementDocument
		TurnstileEnabled                    bool
		TurnstileSiteKey                    string
		TencentCaptchaEnabled               bool
		TencentCaptchaAppID                 string
		TencentCaptchaRegion                string
		AliyunCaptchaEnabled                bool
		AliyunCaptchaSceneID                string
		AliyunCaptchaPrefix                 string
		AliyunCaptchaRegion                 string
		SiteName                            string
		SiteLogo                            string
		SiteSubtitle                        string
		APIBaseURL                          string
		ContactInfo                         string
		DocURL                              string
		HomeContent                         string
		HideCcsImportButton                 bool

		PurchaseSubscriptionEnabled bool
		PurchaseSubscriptionURL     string
		TableDefaultPageSize        int
		TablePageSizeOptions        []int
		UsageRankingLimit           int
		UsageRankingEnabled         bool
		UsageRankingSortBy          string
		UsageRankingShowTotalTokens bool
		UsageRankingShowRequests    bool
		UsageRankingShowActualCost  bool
		CustomMenuItems             string // JSON array of custom menu items
		CustomEndpoints             string // JSON array of custom endpoints
		FooterLinks                 string // JSON array of footer link groups
		FooterText                  string // Extra footer text (ICP number etc.)
		HomeFeaturedModels          string // JSON array of model IDs featured on the home page

		LinuxDoOAuthEnabled      bool
		DingTalkOAuthEnabled     bool
		WeChatOAuthEnabled       bool
		WeChatOAuthOpenEnabled   bool
		WeChatOAuthMPEnabled     bool
		WeChatOAuthMobileEnabled bool
		BackendModeEnabled       bool
		PaymentEnabled           bool
		TeamEnabled              bool
		TeamSelfServiceEnabled   bool
		// CreativeEnabled controls the frontend creative studio entry and route guard.
		CreativeEnabled       bool
		OIDCOAuthEnabled      bool
		OIDCOAuthProviderName string
		GitHubOAuthEnabled    bool
		GoogleOAuthEnabled    bool
		GoogleOneTapEnabled   bool
		GoogleOAuthClientID   string
		Version               string
		BalanceUnitName       string
		BalanceUnitSymbol     string
		BalanceIconSVG        string

		BalanceLowNotifyEnabled    bool
		ProviderQuotaNotifyEnabled bool
		// RiskControlEnabled controls the frontend risk control entry.
		RiskControlEnabled          bool
		AffiliateEnabled            bool
		BalanceLowNotifyThreshold   float64
		BalanceLowNotifyRechargeURL string

		// Allows users to view their own failed requests on the usage page.
		AllowUserViewErrorRequests bool `json:"allow_user_view_error_requests"`
	}
)
