package locale

import (
	"context"
	_ "embed"
	"encoding/json"
	"strings"

	"golang.org/x/text/language"
)

// locale.go normalizes supported language codes and negotiates request
// language from the embedded catalog.
// Supported locale codes and aliases come from the same embedded manifest used
// by the frontend. Normalization rejects malformed identifiers before comparing
// canonical codes, compatible regional suffixes, and explicit aliases. Request
// negotiation then uses accepted language weights, a supplied fallback, and the
// catalog default to choose a supported code.
//
// Context values carry both language selection and whether queries present
// localized user-facing names. An empty stored language deliberately requests
// source-content image lookup and differs from an absent context value.
// Definitions decodes the embedded manifest anew for independent editing, while
// matching reads the process catalog; returned candidate lists copy their outer
// slice.

var (
	// manifestJSON embeds the catalog shared with the frontend and supplies the
	// source decoded by startup initialization and independent Definitions calls.
	//
	//go:embed manifest.json
	manifestJSON []byte

	// catalog holds startup-parsed definitions used by normalization, negotiation,
	// and content fallback selection throughout the lifetime of the running process.
	catalog = loadCatalog()
)

// Definition describes one supported locale and its catalog-defined
// presentation and selection data.
type Definition struct {
	Code      string   `json:"code"`      // canonical supported locale code
	Name      string   `json:"name"`      // displayed language name
	Direction string   `json:"direction"` // text direction label
	Aliases   []string `json:"aliases"`   // equivalent input locale codes
	Fallbacks []string `json:"fallbacks"` // ordered compatible locale codes
	Catalog   string   `json:"catalog"`   // frontend locale resource directory
}

// Catalog holds the shipped default locale and the ordered supported language
// definitions.
type Catalog struct {
	Default string       `json:"default"` // default supported locale code
	Locales []Definition `json:"locales"` // ordered supported language definitions
}

// contextKey identifies the language string stored for request-level content
// selection.
type contextKey struct{}

// presentationKey identifies whether a query should resolve names for
// user-facing presentation.
type presentationKey struct{}

// loadCatalog decodes the embedded manifest into the process language catalog.
// Malformed JSON panics during initialization, making invalid embedded data a
// startup failure rather than a negotiated request result.
func loadCatalog() Catalog {
	var result Catalog
	if err := json.Unmarshal(manifestJSON, &result); err != nil {
		panic(err)
	}
	return result
}

// Default returns the default locale code stored in the process catalog.
func Default() string { return catalog.Default }

// Definitions decodes the embedded manifest into a new slice and nested slice
// storage. The embedded JSON was parsed at initialization; this accessor
// discards a repeated decode error and returns the resulting Locales value.
func Definitions() []Definition {
	var result Catalog
	// Startup already decoded this embedded document successfully. A repeated
	// decode supplies fresh nested storage while this accessor discards its error.
	_ = json.Unmarshal(manifestJSON, &result)
	return result.Locales
}

// Normalize trims whitespace, converts underscores to hyphens, and returns a
// supported canonical locale code. Malformed or unknown values return an empty
// string; canonical prefix matching accepts regional suffixes before explicit
// alias checks.
func Normalize(raw string) string {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "_", "-")
	// Reject long identifiers before scanning segments for accepted locale syntax.
	if len(raw) > 35 || strings.ContainsAny(raw, ",; \t\r\n") {
		return ""
	}
	for segment := range strings.SplitSeq(raw, "-") {
		if segment == "" {
			return ""
		}
		for _, char := range segment {
			valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
			if !valid {
				return ""
			}
		}
	}
	for _, item := range catalog.Locales {
		if strings.EqualFold(raw, item.Code) || strings.HasPrefix(strings.ToLower(raw), strings.ToLower(item.Code)+"-") {
			return item.Code
		}
		for _, alias := range item.Aliases {
			if strings.EqualFold(raw, alias) {
				return item.Code
			}
		}
	}
	return ""
}

// Negotiate selects a supported language from a direct identifier or weighted
// Accept-Language header. A directly normalized value wins; otherwise the
// language parser orders accepted tags and removes zero-weight entries before
// the first supported tag is selected.
//
// Malformed headers or headers with no supported tags use the normalized
// fallback. An unsupported fallback uses Default. This distinction lets callers
// pass a site preference while maintaining a catalog-supported result for
// request headers containing unsupported languages.
func Negotiate(header, fallback string) string {
	if direct := Normalize(header); direct != "" {
		return direct
	}
	tags, _, err := language.ParseAcceptLanguage(header)
	if err == nil {
		for _, tag := range tags {
			if value := Normalize(tag.String()); value != "" {
				return value
			}
		}
	}
	// Parsing failure or unsupported requested tags leave the caller's configured
	// preference in control; an invalid preference resolves to the shipped default.
	if value := Normalize(fallback); value != "" {
		return value
	}
	return Default()
}

// Candidates returns a normalized locale followed by its catalog-configured
// fallback languages. Unknown codes return nil, and the returned slice is newly
// allocated so callers can change its entries independently.
func Candidates(code string) []string {
	code = Normalize(code)
	for _, item := range catalog.Locales {
		if code == item.Code {
			return append([]string{code}, item.Fallbacks...)
		}
	}
	return nil
}

// WithLanguage returns a child context storing code after negotiation against
// Default. The parent must be nonnil, and the stored value lets display callers
// share a canonical language within one request.
func WithLanguage(ctx context.Context, code string) context.Context {
	return context.WithValue(ctx, contextKey{}, Negotiate(code, Default()))
}

// FromContext returns the stored language string, or Default for nil or missing
// language values. A deliberately stored empty string is returned unchanged for
// callers requesting source-content behavior.
func FromContext(ctx context.Context) string {
	if ctx == nil {
		return Default()
	}
	if value, ok := ctx.Value(contextKey{}).(string); ok {
		return value
	}
	return Default()
}

// Direction returns the catalog direction for a normalized supported language.
// Unknown input uses ltr, while a supported entry's configured Direction is
// returned as stored in the catalog.
func Direction(code string) string {
	for _, item := range catalog.Locales {
		if item.Code == Normalize(code) {
			return item.Direction
		}
	}
	return "ltr"
}

// Explicit returns the stored language and whether a nonempty string was
// present. The context must be nonnil; source-image callers can distinguish
// absent or deliberately empty values from a selected locale.
func Explicit(ctx context.Context) (string, bool) {
	value, ok := ctx.Value(contextKey{}).(string)
	return value, ok && value != ""
}

// WithoutLanguage returns a child context storing an empty language string.
// Source-content image lookup uses this value to select the shared directory,
// and callers supply a nonnil parent context.
func WithoutLanguage(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey{}, "")
}

// WithUserPresentation stores whether downstream queries should resolve
// user-facing localized names. It returns a child of the supplied nonnil
// context, leaving existing values and cancellation available to query
// consumers.
func WithUserPresentation(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, presentationKey{}, enabled)
}

// UserPresentation reports whether the context carries a true user-presentation
// flag. Nil, missing, or differently typed values return false, so ordinary
// queries keep their untranslated storage-facing names.
func UserPresentation(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value, _ := ctx.Value(presentationKey{}).(bool)
	return value
}
