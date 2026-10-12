package httpapi

import (
	"github.com/gin-gonic/gin"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/site"
	"github.com/TokenFlux/TokenRouter/internal/site/httpapi/dto"
)

type PublicHandler struct {
	settingService *site.PublicService
	version        string
}

func NewPublicHandler(s *site.PublicService, version string) *PublicHandler {
	return &PublicHandler{settingService: s, version: version}
}

func publicLoginAgreementDocumentsToDTO(items []site.LoginAgreementDocument) []dto.LoginAgreementDocument {
	result := make([]dto.LoginAgreementDocument, 0, len(items))
	for _, item := range items {
		result = append(result, dto.LoginAgreementDocument{
			ID:        item.ID,
			Title:     item.Title,
			ContentMD: item.ContentMD,
		})
	}
	return result
}

// GetPublicSettings returns public site settings.
// GET /api/v1/settings/public
func (h *PublicHandler) GetPublicSettings(c *gin.Context) {
	settings, err := h.settingService.GetPublicSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	zone, offset := h.settingService.ServerTimezone()
	response.Success(c, dto.PublicSettings{
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
		LoginAgreementDocuments:             publicLoginAgreementDocumentsToDTO(settings.LoginAgreementDocuments),
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
		CustomMenuItems:                     dto.ParseUserVisibleMenuItems(settings.CustomMenuItems),
		CustomEndpoints:                     dto.ParseCustomEndpoints(settings.CustomEndpoints),
		FooterLinks:                         dto.ParseFooterLinks(settings.FooterLinks),
		FooterText:                          settings.FooterText,
		HomeFeaturedModels:                  dto.ParseHomeFeaturedModels(settings.HomeFeaturedModels),
		DingTalkOAuthEnabled:                settings.DingTalkOAuthEnabled,
		LinuxDoOAuthEnabled:                 settings.LinuxDoOAuthEnabled,
		WeChatOAuthEnabled:                  settings.WeChatOAuthEnabled,
		WeChatOAuthOpenEnabled:              settings.WeChatOAuthOpenEnabled,
		WeChatOAuthMPEnabled:                settings.WeChatOAuthMPEnabled,
		WeChatOAuthMobileEnabled:            settings.WeChatOAuthMobileEnabled,
		OIDCOAuthEnabled:                    settings.OIDCOAuthEnabled,
		OIDCOAuthProviderName:               settings.OIDCOAuthProviderName,
		GitHubOAuthEnabled:                  settings.GitHubOAuthEnabled,
		GoogleOAuthEnabled:                  settings.GoogleOAuthEnabled,
		GoogleOneTapEnabled:                 settings.GoogleOneTapEnabled,
		GoogleOAuthClientID:                 settings.GoogleOAuthClientID,
		BackendModeEnabled:                  settings.BackendModeEnabled,
		PaymentEnabled:                      settings.PaymentEnabled,
		TeamEnabled:                         settings.TeamEnabled,
		TeamSelfServiceEnabled:              settings.TeamSelfServiceEnabled,
		CreativeEnabled:                     settings.CreativeEnabled,
		Version:                             h.version,
		ServerTimezone:                      zone,
		ServerUTCOffset:                     offset,
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
	})
}

// GetLegalDocument returns the public agreement in the current language when requested.
func (h *PublicHandler) GetLegalDocument(c *gin.Context) {
	doc, err := h.settingService.GetLegalDocument(c.Request.Context(), c.Param("id"))
	if err != nil {
		response.NotFound(c, "Document not found.")
		return
	}
	c.Header("Vary", "Accept-Language")
	response.Success(c, doc)
}
