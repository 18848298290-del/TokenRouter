package site

import (
	"strings"

	settingvalues "github.com/TokenFlux/TokenRouter/internal/settings"
)

// AdminReadSettings contains site display and login agreement settings.
type AdminReadSettings struct {
	APIBaseURL                  string
	ContactInfo                 string
	CustomEndpoints             string
	CustomMenuItems             string
	DocURL                      string
	FooterLinks                 string
	FooterText                  string
	FrontendURL                 string
	HideCcsImportButton         bool
	HomeContent                 string
	HomeFeaturedModels          string
	LoginAgreementDocuments     []LoginAgreementDocument
	LoginAgreementEnabled       bool
	LoginAgreementMode          string
	LoginAgreementUpdatedAt     string
	PurchaseSubscriptionEnabled bool
	PurchaseSubscriptionURL     string
	SiteLogo                    string
	SiteTexts                   LocalizedTexts `json:"site_texts"`
	DefaultLocale               string         `json:"default_locale"`
	SiteName                    string
	SiteSubtitle                string
	TableDefaultPageSize        int
	TablePageSizeOptions        []int
}

// ReadAdminSettings parses site display and login agreement settings from stored values.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	loginAgreementDocuments := ParseLoginAgreementDocuments(settings[SettingKeyLoginAgreementDocuments])
	loginAgreementUpdatedAt := strings.TrimSpace(settings[SettingKeyLoginAgreementUpdatedAt])
	if loginAgreementUpdatedAt == "" {
		loginAgreementUpdatedAt = defaultLoginAgreementDate
	}
	result := &AdminReadSettings{}
	result.SiteTexts = ParseLocalizedTexts(settings)
	result.DefaultLocale = settings[SettingKeyDefaultLocale]
	if result.DefaultLocale == "" {
		result.DefaultLocale = "en"
	}
	result.FrontendURL = settings[SettingKeyFrontendURL]
	result.LoginAgreementEnabled = settings[SettingKeyLoginAgreementEnabled] == "true"
	result.LoginAgreementMode = NormalizeLoginAgreementMode(settings[SettingKeyLoginAgreementMode])
	result.LoginAgreementUpdatedAt = loginAgreementUpdatedAt
	result.LoginAgreementDocuments = loginAgreementDocuments
	result.SiteName = settingvalues.StringOrDefault(settings, SettingKeySiteName, "TokenRouter")
	result.SiteLogo = settings[SettingKeySiteLogo]
	result.SiteSubtitle = settingvalues.StringOrDefault(settings, SettingKeySiteSubtitle, "Subscription to API Conversion Platform")
	result.APIBaseURL = settings[SettingKeyAPIBaseURL]
	result.ContactInfo = settings[SettingKeyContactInfo]
	result.DocURL = settings[SettingKeyDocURL]
	result.HomeContent = settings[SettingKeyHomeContent]
	result.HideCcsImportButton = settings[SettingKeyHideCcsImportButton] == "true"
	result.PurchaseSubscriptionEnabled = settings[SettingKeyPurchaseSubscriptionEnabled] == "true"
	result.PurchaseSubscriptionURL = strings.TrimSpace(settings[SettingKeyPurchaseSubscriptionURL])
	result.CustomMenuItems = settings[SettingKeyCustomMenuItems]
	result.CustomEndpoints = settings[SettingKeyCustomEndpoints]
	result.FooterLinks = settings[SettingKeyFooterLinks]
	result.FooterText = settings[SettingKeyFooterText]
	result.HomeFeaturedModels = settings[SettingKeyHomeFeaturedModels]
	result.TableDefaultPageSize, result.TablePageSizeOptions = ParseTablePreferences(
		settings[SettingKeyTableDefaultPageSize],
		settings[SettingKeyTablePageSizeOptions],
	)
	return result
}
