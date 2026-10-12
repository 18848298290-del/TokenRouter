package dto

import (
	"encoding/json"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/site/content"
)

// Public navigation retains stable entry IDs while selecting display text for
// menus, endpoints, and footer links. The response mapper excludes admin-only
// menu entries, and featured model IDs remain a plain JSON string list.

// FooterLinkGroup contains one column of localized home page footer links.
type FooterLinkGroup = content.FooterLinkGroup

// FooterLink contains one localized home page footer label and destination.
type FooterLink = content.FooterLink

// CustomEndpoint represents an admin-configured API endpoint for quick copy.
type CustomEndpoint = content.CustomEndpoint

// CustomMenuItem represents a configured menu entry with identity and visibility.
type CustomMenuItem = content.CustomMenuItem

// ParseHomeFeaturedModels parses the JSON list of home page model IDs.
// Blank, empty-array, or malformed input returns an empty slice. A successful
// JSON null returns nil, so callers can retain that distinction when encoding.
func ParseHomeFeaturedModels(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return []string{}
	}
	var models []string
	// A malformed model list leaves the home-page feature list empty; partial
	// decoder output is discarded so the response contains a usable string collection.
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		return []string{}
	}
	return models
}

// ParseFooterLinks returns footer groups with identities supplied by the content parser.
// Missing group and link IDs are synthesized from their positions and source
// values; stored IDs remain available for later localization matching.
func ParseFooterLinks(raw string) []FooterLinkGroup {
	return content.ParseFooterGroups(raw)
}

// ParseCustomEndpoints returns copyable endpoints using the content parser's identity rules.
// Entries lacking IDs receive deterministic IDs from their position and endpoint
// URL, while entries with stored IDs keep them for later content updates.
func ParseCustomEndpoints(raw string) []CustomEndpoint {
	return content.ParseEndpoints(raw)
}

// ParseCustomMenuItems returns configured menus using the content parser's identity rules.
// Stored IDs survive parsing, and missing IDs are derived from entry positions
// and labels so legacy settings can be edited through the localization model.
func ParseCustomMenuItems(raw string) []CustomMenuItem {
	return content.ParseMenus(raw)
}

// ParseUserVisibleMenuItems returns menus whose visibility differs from literal admin.
// It retains entry order and every other visibility value, including empty
// values. This exclusion matches site.Pages.ImagePath for public page images;
// Markdown reads separately consider the authenticated caller's administrator role.
func ParseUserVisibleMenuItems(raw string) []CustomMenuItem {
	items := ParseCustomMenuItems(raw)
	filtered := make([]CustomMenuItem, 0, len(items))
	for _, item := range items {
		if item.Visibility != "admin" {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
