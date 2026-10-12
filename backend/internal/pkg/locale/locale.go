package locale

import (
	"context"
	_ "embed"
	"encoding/json"
	"strings"

	"golang.org/x/text/language"
)

var (
	// manifestJSON is the language catalog shared with the frontend.
	//
	//go:embed manifest.json
	manifestJSON []byte

	// catalog is the parsed language catalog.
	catalog = loadCatalog()
)

// Definition describes a language, its aliases, text direction, and fallback languages.
type Definition struct {
	Code      string   `json:"code"`
	Name      string   `json:"name"`
	Direction string   `json:"direction"`
	Aliases   []string `json:"aliases"`
	Fallbacks []string `json:"fallbacks"`
	Catalog   string   `json:"catalog"`
}

// Catalog is the language catalog shipped with the application.
type Catalog struct {
	Default string       `json:"default"`
	Locales []Definition `json:"locales"`
}

type contextKey struct{}

type presentationKey struct{}

// loadCatalog parses the embedded catalog and panics on invalid JSON.
func loadCatalog() Catalog {
	var result Catalog
	if err := json.Unmarshal(manifestJSON, &result); err != nil {
		panic(err)
	}
	return result
}

// Default returns the catalog's default language.
func Default() string { return catalog.Default }

// Definitions returns a fresh copy of the embedded locale definitions, including nested slices.
func Definitions() []Definition {
	var result Catalog
	_ = json.Unmarshal(manifestJSON, &result)
	return result.Locales
}

// Normalize maps supported language aliases to canonical codes, returning empty for unknown input.
func Normalize(raw string) string {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "_", "-")
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

// Negotiate selects a supported language by request weight, then fallback, then the catalog default.
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
	if value := Normalize(fallback); value != "" {
		return value
	}
	return Default()
}

// Candidates returns the requested locale followed by its configured fallback languages.
func Candidates(code string) []string {
	code = Normalize(code)
	for _, item := range catalog.Locales {
		if code == item.Code {
			return append([]string{code}, item.Fallbacks...)
		}
	}
	return nil
}

// WithLanguage attaches a negotiated language to context.
func WithLanguage(ctx context.Context, code string) context.Context {
	return context.WithValue(ctx, contextKey{}, Negotiate(code, Default()))
}

// FromContext returns the stored language, or the catalog default when the key is absent.
func FromContext(ctx context.Context) string {
	if ctx == nil {
		return Default()
	}
	if value, ok := ctx.Value(contextKey{}).(string); ok {
		return value
	}
	return Default()
}

// Direction returns the catalog's text direction, defaulting to ltr for unknown languages.
func Direction(code string) string {
	for _, item := range catalog.Locales {
		if item.Code == Normalize(code) {
			return item.Direction
		}
	}
	return "ltr"
}

// Explicit returns the stored language and whether it is nonempty, for localized image selection.
func Explicit(ctx context.Context) (string, bool) {
	value, ok := ctx.Value(contextKey{}).(string)
	return value, ok && value != ""
}

// WithoutLanguage stores an empty language so source-content images use the shared directory.
func WithoutLanguage(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey{}, "")
}

// WithUserPresentation marks queries that should resolve user-facing localized names.
func WithUserPresentation(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, presentationKey{}, enabled)
}

// UserPresentation reports whether the query is for user-facing presentation.
func UserPresentation(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value, _ := ctx.Value(presentationKey{}).(bool)
	return value
}
