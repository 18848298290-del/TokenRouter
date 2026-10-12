package site

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Site updates share one persistence batch with other settings participants.
// Preparation interprets only submitted fields and returns data, leaving the
// registry and update session to decide when storage becomes visible. Localized
// content additionally records the exact old stored value, or expected absence,
// so the eventual conditional write can reject a concurrent editor save.
//
// Site text groups mirror source copy into legacy keys used by existing readers.
// Legal and navigation localization preparation is selected when the submitted
// field or its current value contains the localization marker. After common
// normalization, unsubmitted flat keys are removed, then source mirrors are
// added from the prepared group. The participant performs no storage calls.

// SettingsParticipant declares the site-owned fields and prepares their stored string values.
// Empty input returns no changes. The registry supplies the participant's old
// values, and the callback records conditional-write expectations for prepared
// localization rather than applying changes during this phase.
//
// Source text mirrors may add owned legacy keys alongside a submitted grouped
// edit. Legal and navigation fields are matched against current data before
// normalization; the result is then filtered to submitted flat keys so omitted
// fields do not acquire zero values from JSON decoding into AdminSettings.
func SettingsParticipant() settings.Participant {
	keys := []string{SettingKeySiteTexts, SettingKeyDefaultLocale, "site_title", SettingKeyAPIBaseURL, SettingKeyContactInfo, SettingKeyCustomEndpoints, SettingKeyCustomMenuItems, SettingKeyDocURL, SettingKeyFooterLinks, SettingKeyFooterText, SettingKeyFrontendURL, SettingKeyHideCcsImportButton, SettingKeyHomeContent, SettingKeyHomeFeaturedModels, SettingKeyLoginAgreementDocuments, SettingKeyLoginAgreementEnabled, SettingKeyLoginAgreementMode, SettingKeyLoginAgreementUpdatedAt, SettingKeyPurchaseSubscriptionEnabled, SettingKeyPurchaseSubscriptionURL, SettingKeySiteLogo, SettingKeySiteName, SettingKeySiteSubtitle, SettingKeyTableDefaultPageSize, SettingKeyTablePageSizeOptions}
	fields := make([]string, 0, len(keys))
	for _, key := range keys {
		// site_title belongs to the grouped source-text mirror, not a direct
		// flat editor field. Owning its storage key still permits prepared group updates to write it.
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
		// Localized preparation compares against this read snapshot. Capture its
		// raw stored value or absence so commit can detect another save before applying these edits.
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
		// A localization marker in either submitted or current legal JSON selects
		// revision-aware preparation; legacy document lists continue through the common normalization path below.
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
		// Prepare and compare each submitted navigation setting containing saved
		// or supplied localization, retaining omitted editor state against its matching stored entry IDs.
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
		// AdminSettings decoding supplies zero values for absent fields. Filter
		// those writes before returning the batch so an unrelated site edit cannot clear omitted settings.
		for key := range values {
			if _, ok := input[key]; !ok {
				delete(values, key)
			}
		}
		// Legacy display readers consume source keys directly. Include the source
		// mirrors in this same batch so saved localization and legacy source text advance together.
		if value.SiteTexts != nil {
			for key, content := range value.SiteTexts {
				values[key] = content.Source
			}
		}
		return settings.PreparedChange{Values: values, Expected: expected}, nil
	}}
}
