package site

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// SettingsParticipant prepares site settings updates and expected values for
// checking concurrent localization edits during the settings write.
func SettingsParticipant() settings.Participant {
	keys := []string{SettingKeySiteTexts, SettingKeyDefaultLocale, "site_title", SettingKeyAPIBaseURL, SettingKeyContactInfo, SettingKeyCustomEndpoints, SettingKeyCustomMenuItems, SettingKeyDocURL, SettingKeyFooterLinks, SettingKeyFooterText, SettingKeyFrontendURL, SettingKeyHideCcsImportButton, SettingKeyHomeContent, SettingKeyHomeFeaturedModels, SettingKeyLoginAgreementDocuments, SettingKeyLoginAgreementEnabled, SettingKeyLoginAgreementMode, SettingKeyLoginAgreementUpdatedAt, SettingKeyPurchaseSubscriptionEnabled, SettingKeyPurchaseSubscriptionURL, SettingKeySiteLogo, SettingKeySiteName, SettingKeySiteSubtitle, SettingKeyTableDefaultPageSize, SettingKeyTablePageSizeOptions}
	fields := make([]string, 0, len(keys))
	for _, key := range keys {
		if key != "site_title" {
			fields = append(fields, key)
		}
	}
	return settings.Participant{Module: "site", Fields: fields, Keys: keys, Prepare: func(_ context.Context, input settings.Fields, current map[string]string) (settings.PreparedChange, error) {
		if len(input) == 0 {
			return settings.PreparedChange{}, nil
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		var value AdminSettings
		if err = json.Unmarshal(raw, &value); err != nil {
			return settings.PreparedChange{}, err
		}
		expected := map[string]*string{}
		if _, exists := input[SettingKeySiteTexts]; exists {
			prepared, err := PrepareLocalizedTexts(current, value.SiteTexts)
			if err != nil {
				return settings.PreparedChange{}, err
			}
			value.SiteTexts = prepared
			if raw, exists := current[SettingKeySiteTexts]; exists {
				expected[SettingKeySiteTexts] = &raw
			} else {
				expected[SettingKeySiteTexts] = nil
			}
		}
		if _, exists := input[SettingKeyLoginAgreementDocuments]; exists && (strings.Contains(string(input[SettingKeyLoginAgreementDocuments]), `"localization"`) || strings.Contains(current[SettingKeyLoginAgreementDocuments], `"localization"`)) {
			docs, err := PrepareLegalDocuments(current[SettingKeyLoginAgreementDocuments], value.LoginAgreementDocuments)
			if err != nil {
				return settings.PreparedChange{}, err
			}
			value.LoginAgreementDocuments = docs
			if raw, exists := current[SettingKeyLoginAgreementDocuments]; exists {
				expected[SettingKeyLoginAgreementDocuments] = &raw
			} else {
				expected[SettingKeyLoginAgreementDocuments] = nil
			}
		}
		for key, target := range map[string]*string{SettingKeyCustomMenuItems: &value.CustomMenuItems, SettingKeyCustomEndpoints: &value.CustomEndpoints, SettingKeyFooterLinks: &value.FooterLinks} {
			if _, sent := input[key]; !sent || !strings.Contains(*target, `"localization"`) && !strings.Contains(current[key], `"localization"`) {
				continue
			}
			prepared, err := PrepareNavigation(key, *target, current[key])
			if err != nil {
				return settings.PreparedChange{}, err
			}
			*target = prepared
			if raw, exists := current[key]; exists {
				expected[key] = &raw
			} else {
				expected[key] = nil
			}
		}
		if _, exists := input[SettingKeyDefaultLocale]; exists {
			value.DefaultLocale = locale.Normalize(value.DefaultLocale)
			if value.DefaultLocale == "" {
				return settings.PreparedChange{}, apperror.BadRequest("INVALID_LOCALE", "Language is not supported.")
			}
		}
		values, err := PrepareAdminSettings(&value)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		for key := range values {
			if _, ok := input[key]; !ok {
				delete(values, key)
			}
		}
		if value.SiteTexts != nil {
			for key, content := range value.SiteTexts {
				values[key] = content.Source
			}
		}
		return settings.PreparedChange{Values: values, Expected: expected}, nil
	}}
}
