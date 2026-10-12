package site

import (
	"encoding/json"
	"maps"
	"net/url"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

const (
	SettingKeySiteTexts     = "site_texts"
	SettingKeyDefaultLocale = "default_locale"
)

var siteTextKeys = []string{"site_name", "site_title", "site_subtitle", "contact_info", "doc_url", "home_content", "purchase_subscription_url", "footer_text"}

// LocalizedTexts stores site text by field so each field can resolve translations independently.
type LocalizedTexts = locale.TextUpdates

// ParseLocalizedTexts builds editable content from saved translations and fills
// missing fields from their existing source text.
func ParseLocalizedTexts(values map[string]string) LocalizedTexts {
	result := ConfiguredSiteTexts(values)
	for _, key := range siteTextKeys {
		if _, exists := result[key]; !exists {
			value := values[key]
			if key == "site_name" && value == "" {
				value = "TokenRouter"
			}
			result[key] = locale.Update[string]{Content: locale.Original(value)}
		}
	}
	return result
}

// PrepareLocalizedTexts validates field names and content revisions while retaining omitted fields.
func PrepareLocalizedTexts(values map[string]string, input LocalizedTexts) (LocalizedTexts, error) {
	previous := ParseLocalizedTexts(values)
	result := ConfiguredSiteTexts(values)
	for key, update := range input {
		current, known := previous[key]
		if !known {
			return nil, apperror.BadRequest("UNKNOWN_LOCALIZED_FIELD", "Unknown localized field.")
		}
		if reflect.DeepEqual(current.Content, update.Content) && len(update.ReviewedLocales) == 0 {
			continue
		}
		next, err := locale.Prepare(current.Content, update, func(value string) error {
			if key == "doc_url" || key == "purchase_subscription_url" || key == "home_content" && !strings.HasPrefix(strings.TrimSpace(value), "<") {
				parsed, err := url.Parse(strings.TrimSpace(value))
				if err != nil || parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" {
					return apperror.BadRequest("INVALID_LINK_URL", "Link URL is invalid.")
				}
			}
			limit := 2000
			if key == "home_content" {
				limit = 1 << 20
			}
			if utf8.RuneCountInString(value) > limit {
				return apperror.BadRequest("LOCALIZED_TEXT_TOO_LONG", "Text exceeds the allowed length.").WithMetadata(map[string]string{"field": key})
			}
			if key == "site_name" && strings.TrimSpace(value) == "" {
				return apperror.BadRequest("SITE_NAME_REQUIRED", "Site name is required.")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		result[key] = locale.Update[string]{Content: next}
	}
	return result, nil
}

// ResolveSiteTexts returns a copy of the settings with configured site text resolved for code.
func ResolveSiteTexts(values map[string]string, code string) map[string]string {
	result := make(map[string]string, len(values))
	maps.Copy(result, values)
	for key, content := range ConfiguredSiteTexts(values) {
		result[key], _ = content.Resolve(code)
	}
	return result
}

// PrepareLegalDocuments validates each document revision, treating its title and body as one content block.
func PrepareLegalDocuments(raw string, input []LoginAgreementDocument) ([]LoginAgreementDocument, error) {
	current := map[string]LoginAgreementDocument{}
	for _, doc := range ParseLoginAgreementDocuments(raw) {
		current[doc.ID] = doc
	}
	result := append([]LoginAgreementDocument(nil), input...)
	for i := range result {
		doc := &result[i]
		if doc.Localization == nil {
			if previous, exists := current[doc.ID]; exists && previous.Localization != nil {
				doc.Localization = previous.Localization
				doc.Title, doc.ContentMD = previous.Localization.Source.Title, previous.Localization.Source.ContentMD
			}
			continue
		}
		previous := current[doc.ID]
		original := locale.Original(LoginAgreementCopy{Title: previous.Title, ContentMD: previous.ContentMD})
		if previous.Localization != nil {
			original = previous.Localization.Content
		}
		if reflect.DeepEqual(original, doc.Localization.Content) && len(doc.Localization.ReviewedLocales) == 0 {
			doc.Title, doc.ContentMD = original.Source.Title, original.Source.ContentMD
			continue
		}
		next, err := locale.Prepare(original, *doc.Localization, func(copy LoginAgreementCopy) error {
			if strings.TrimSpace(copy.Title) == "" {
				return apperror.BadRequest("LEGAL_TITLE_REQUIRED", "Document title is required.")
			}
			if len(copy.ContentMD) > 1<<20 {
				return apperror.BadRequest("LEGAL_CONTENT_TOO_LONG", "Document exceeds the allowed length.")
			}
			if strings.TrimSpace(doc.Localization.Source.ContentMD) != "" && strings.TrimSpace(copy.ContentMD) == "" {
				return apperror.BadRequest("LEGAL_CONTENT_REQUIRED", "Document content is required.")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		doc.Localization = &locale.Update[LoginAgreementCopy]{Content: next}
		doc.Title, doc.ContentMD = next.Source.Title, next.Source.ContentMD
	}
	return result, nil
}

// ConfiguredSiteTexts returns saved content blocks, distinguishing absent fields
// from fields an administrator saved as empty.
func ConfiguredSiteTexts(values map[string]string) LocalizedTexts {
	result := LocalizedTexts{}
	_ = json.Unmarshal([]byte(values[SettingKeySiteTexts]), &result)
	if result == nil {
		return LocalizedTexts{}
	}
	return result
}
