package dto

import (
	"encoding/json"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/site/content"
)

// FooterLinkGroup contains one column of home page footer links.
type FooterLinkGroup = content.FooterLinkGroup

// FooterLink contains one home page footer link.
type FooterLink = content.FooterLink

// CustomEndpoint represents an admin-configured API endpoint for quick copy.
type CustomEndpoint = content.CustomEndpoint

// CustomMenuItem represents a user-configured custom menu entry.
type CustomMenuItem = content.CustomMenuItem

// ParseHomeFeaturedModels parses the JSON list of home page model IDs.
// Empty or malformed input returns an empty slice; JSON null returns nil.
func ParseHomeFeaturedModels(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return []string{}
	}
	var models []string
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		return []string{}
	}
	return models
}

// ParseFooterLinks parses a JSON string into a slice of FooterLinkGroup.
// Missing IDs are assigned by content.ParseFooterGroups.
func ParseFooterLinks(raw string) []FooterLinkGroup {
	return content.ParseFooterGroups(raw)
}

// ParseCustomEndpoints parses a JSON string into a slice of CustomEndpoint.
// Missing IDs are assigned by content.ParseEndpoints.
func ParseCustomEndpoints(raw string) []CustomEndpoint {
	return content.ParseEndpoints(raw)
}

// ParseCustomMenuItems parses a JSON string into a slice of CustomMenuItem.
// Missing IDs are assigned by content.ParseMenus.
func ParseCustomMenuItems(raw string) []CustomMenuItem {
	return content.ParseMenus(raw)
}

// ParseUserVisibleMenuItems parses custom menu items and filters out admin-only entries.
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
