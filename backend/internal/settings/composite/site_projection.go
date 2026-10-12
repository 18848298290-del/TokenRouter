package composite

import "github.com/TokenFlux/TokenRouter/internal/site"

// SiteAdminSettings extracts site settings from the snapshot.
func (s *Snapshot) SiteAdminSettings() site.AdminSettings {
	return site.AdminSettings{
		APIBaseURL:                  s.APIBaseURL,
		ContactInfo:                 s.ContactInfo,
		CustomEndpoints:             s.CustomEndpoints,
		CustomMenuItems:             s.CustomMenuItems,
		DocURL:                      s.DocURL,
		FooterLinks:                 s.FooterLinks,
		FooterText:                  s.FooterText,
		FrontendURL:                 s.FrontendURL,
		HideCcsImportButton:         s.HideCcsImportButton,
		HomeContent:                 s.HomeContent,
		HomeFeaturedModels:          s.HomeFeaturedModels,
		LoginAgreementDocuments:     s.LoginAgreementDocuments,
		LoginAgreementEnabled:       s.LoginAgreementEnabled,
		LoginAgreementMode:          s.LoginAgreementMode,
		LoginAgreementUpdatedAt:     s.LoginAgreementUpdatedAt,
		PurchaseSubscriptionEnabled: s.PurchaseSubscriptionEnabled,
		PurchaseSubscriptionURL:     s.PurchaseSubscriptionURL,
		SiteLogo:                    s.SiteLogo,
		SiteTexts:                   s.SiteTexts,
		DefaultLocale:               s.DefaultLocale,
		SiteName:                    s.SiteName,
		SiteSubtitle:                s.SiteSubtitle,
		TableDefaultPageSize:        s.TableDefaultPageSize,
		TablePageSizeOptions:        s.TablePageSizeOptions,
	}
}

// ApplySiteAdminSettings copies site settings into the snapshot.
func (s *Snapshot) ApplySiteAdminSettings(value site.AdminSettings) {
	s.APIBaseURL = value.APIBaseURL
	s.ContactInfo = value.ContactInfo
	s.CustomEndpoints = value.CustomEndpoints
	s.CustomMenuItems = value.CustomMenuItems
	s.DocURL = value.DocURL
	s.FooterLinks = value.FooterLinks
	s.FooterText = value.FooterText
	s.FrontendURL = value.FrontendURL
	s.HideCcsImportButton = value.HideCcsImportButton
	s.HomeContent = value.HomeContent
	s.HomeFeaturedModels = value.HomeFeaturedModels
	s.LoginAgreementDocuments = value.LoginAgreementDocuments
	s.LoginAgreementEnabled = value.LoginAgreementEnabled
	s.LoginAgreementMode = value.LoginAgreementMode
	s.LoginAgreementUpdatedAt = value.LoginAgreementUpdatedAt
	s.PurchaseSubscriptionEnabled = value.PurchaseSubscriptionEnabled
	s.PurchaseSubscriptionURL = value.PurchaseSubscriptionURL
	s.SiteLogo = value.SiteLogo
	s.SiteTexts = value.SiteTexts
	s.DefaultLocale = value.DefaultLocale
	s.SiteName = value.SiteName
	s.SiteSubtitle = value.SiteSubtitle
	s.TableDefaultPageSize = value.TableDefaultPageSize
	s.TablePageSizeOptions = value.TablePageSizeOptions
}

// ApplySiteAdminReadSettings copies site display, subscription entry, and login agreement settings into the
// snapshot.
func (s *Snapshot) ApplySiteAdminReadSettings(value *site.AdminReadSettings) {
	s.APIBaseURL = value.APIBaseURL
	s.ContactInfo = value.ContactInfo
	s.CustomEndpoints = value.CustomEndpoints
	s.CustomMenuItems = value.CustomMenuItems
	s.DocURL = value.DocURL
	s.FooterLinks = value.FooterLinks
	s.FooterText = value.FooterText
	s.FrontendURL = value.FrontendURL
	s.HideCcsImportButton = value.HideCcsImportButton
	s.HomeContent = value.HomeContent
	s.HomeFeaturedModels = value.HomeFeaturedModels
	s.LoginAgreementDocuments = value.LoginAgreementDocuments
	s.LoginAgreementEnabled = value.LoginAgreementEnabled
	s.LoginAgreementMode = value.LoginAgreementMode
	s.LoginAgreementUpdatedAt = value.LoginAgreementUpdatedAt
	s.PurchaseSubscriptionEnabled = value.PurchaseSubscriptionEnabled
	s.PurchaseSubscriptionURL = value.PurchaseSubscriptionURL
	s.SiteLogo = value.SiteLogo
	s.SiteTexts = value.SiteTexts
	s.DefaultLocale = value.DefaultLocale
	s.SiteName = value.SiteName
	s.SiteSubtitle = value.SiteSubtitle
	s.TableDefaultPageSize = value.TableDefaultPageSize
	s.TablePageSizeOptions = value.TablePageSizeOptions
}
