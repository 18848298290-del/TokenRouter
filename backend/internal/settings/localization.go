package settings

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// Short operational text keeps its localized content under a setting key derived
// from the source field's storage key. The administrator reader can combine those
// records with legacy source strings, while update preparation validates the
// submitted content and carries the old serialized record into conditional storage.
//
// The localized participant declares the localized keys as its owned values,
// so registry-driven preparation receives those records and uses source defaults
// when no localized record is present. Unchanged content with no review or deletion
// instructions skips writing. Changed content is prepared through locale revisions,
// then compared and saved with the complete settings batch to reject a stale update.

// LocalizedTextFields maps administrator text names to the source-setting keys
// used to derive each field's localized JSON storage key.
var LocalizedTextFields = map[string]string{
	"balance_unit_name":               "balance_unit_name",
	"balance_low_notify_recharge_url": "balance_low_notify_recharge_url",
	"oidc_connect_provider_name":      "oidc_connect_provider_name",
	"payment_help_text":               "PAYMENT_HELP_TEXT",
	"payment_help_image_url":          "PAYMENT_HELP_IMAGE_URL",
	"payment_product_name_prefix":     "PRODUCT_NAME_PREFIX",
	"payment_product_name_suffix":     "PRODUCT_NAME_SUFFIX",
	"smtp_from_name":                  "smtp_from_name",
}

// LocalizedTexts aliases the content and review instructions for administrator text updates.
type LocalizedTexts = locale.TextUpdates

// ReadLocalizedTexts builds editor content from supplied source and localized
// settings. Empty balance-unit text starts as USD; legacy content has an unknown
// source language, and localized JSON is decoded over that initial content.
//
// The decode error is discarded, so unreadable JSON can leave the source value
// or partially decoded content available to the editor. Callers choose the values
// map: composite reads provide all settings, while the registered localized
// participant receives its declared localized records and uses source defaults
// when those records are absent.
func ReadLocalizedTexts(values map[string]string) LocalizedTexts {
	result := LocalizedTexts{}
	for field, key := range LocalizedTextFields {
		value := values[key]
		if field == "balance_unit_name" && value == "" {
			value = "USD"
		}
		content := locale.Original(value)
		// Start from source content for editor display, then keep any decoded
		// localized fields even if decoding reports an error; strict update preparation
		// checks that saved record separately before revision comparison.
		_ = json.Unmarshal([]byte(values[locale.TextSettingKey(key)]), &content)
		result[field] = locale.Update[string]{Content: content}
	}
	return result
}

// LocalizationParticipant prepares changed short-text content and raw-value
// expectations for a conditional settings batch. It accepts the localized_settings
// field, rejects unknown text names, and uses locale.Prepare to validate revision,
// source language, source text, translations, and review/deletion instructions.
//
// Current JSON is decoded before comparison, and unchanged content with no
// reviewed or deleted locales contributes no write or expectation. Other updates
// retain the serialized current value, or a nil expectation when absent, and
// produce one normalized JSON record per changed field. Ordinary text allows
// 65,536 bytes; payment help images allow 4 MiB and a data:image/ form. URL fields
// otherwise allow scheme-less, HTTP, or HTTPS parses.
func LocalizationParticipant() Participant {
	keys := make([]string, 0, len(LocalizedTextFields))
	for _, key := range LocalizedTextFields {
		keys = append(keys, locale.TextSettingKey(key))
	}
	return Participant{Module: "localized-texts", Fields: []string{"localized_settings"}, Keys: keys, Prepare: func(_ context.Context, input Fields, current map[string]string) (PreparedChange, error) {
		var fields LocalizedTexts
		if raw, exists := input["localized_settings"]; exists {
			if err := json.Unmarshal(raw, &fields); err != nil {
				return PreparedChange{}, err
			}
		}
		change := PreparedChange{Values: map[string]string{}, Expected: map[string]*string{}}
		for field, update := range fields {
			key, known := LocalizedTextFields[field]
			if !known {
				return change, apperror.BadRequest("UNKNOWN_LOCALIZED_FIELD", "Unknown localized field.")
			}
			stored := locale.TextSettingKey(key)
			old := ReadLocalizedTexts(current)[field].Content
			if raw, exists := current[stored]; exists {
				if err := json.Unmarshal([]byte(raw), &old); err != nil {
					return change, err
				}
				// Save the raw record used for revision preparation so the SQL writer
				// can compare the same value while committing this text with sibling settings.
				change.Expected[stored] = &raw
			} else {
				change.Expected[stored] = nil
			}
			if reflect.DeepEqual(old, update.Content) && len(update.ReviewedLocales) == 0 && len(update.DeletedLocales) == 0 {
				// Skip the conditional write when content and review instructions are
				// unchanged, keeping a routine settings save from advancing this field's revision.
				delete(change.Expected, stored)
				continue
			}
			next, err := locale.Prepare(old, update, func(value string) error {
				// Bound source and translation storage by UTF-8 byte length.
				limit := 65536
				if field == "payment_help_image_url" {
					// Allow embedded image data up to four mebibytes.
					limit = 4 << 20
				}
				if len(value) > limit {
					return apperror.BadRequest("LOCALIZED_TEXT_TOO_LONG", "Text exceeds the allowed length.")
				}
				// Embedded image text uses the separate data:image/ form, so it bypasses
				// ordinary link-scheme validation after its image-specific byte budget is checked.
				if field == "payment_help_image_url" && strings.HasPrefix(value, "data:image/") {
					return nil
				}
				if strings.HasSuffix(field, "_url") && strings.TrimSpace(value) != "" {
					parsed, err := url.Parse(value)
					if err != nil || parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" {
						return apperror.BadRequest("INVALID_LINK_URL", "Link URL is invalid.")
					}
				}
				return nil
			})
			if err != nil {
				return change, err
			}
			body, err := json.Marshal(next)
			if err != nil {
				return change, err
			}
			change.Values[stored] = string(body)
		}
		return change, nil
	}}
}
