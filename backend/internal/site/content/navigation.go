package content

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// NavigationCopy contains localized menu or footer link text and destination.
type NavigationCopy struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// CustomMenuItem has a stable ID; page authorization uses PageSlug and Visibility.
type CustomMenuItem struct {
	Resolution   *locale.Resolution             `json:"localization_resolution,omitempty"`
	ID           string                         `json:"id"`
	Label        string                         `json:"label"`
	IconSVG      string                         `json:"icon_svg"`
	URL          string                         `json:"url"`
	PageSlug     string                         `json:"page_slug,omitempty"`
	Visibility   string                         `json:"visibility"`
	SortOrder    int                            `json:"sort_order"`
	Localization *locale.Update[NavigationCopy] `json:"localization,omitempty"`
}

// EndpointCopy contains the name and description of a copyable API endpoint.
type EndpointCopy struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// CustomEndpoint shares its endpoint URL across languages.
type CustomEndpoint struct {
	Resolution   *locale.Resolution           `json:"localization_resolution,omitempty"`
	ID           string                       `json:"id"`
	Name         string                       `json:"name"`
	Endpoint     string                       `json:"endpoint"`
	Description  string                       `json:"description"`
	Localization *locale.Update[EndpointCopy] `json:"localization,omitempty"`
}

// FooterLinkGroup contains one column of footer links with a separately translated title.
type FooterLinkGroup struct {
	Resolution   *locale.Resolution     `json:"localization_resolution,omitempty"`
	ID           string                 `json:"id"`
	Title        string                 `json:"title"`
	Links        []FooterLink           `json:"links"`
	Localization *locale.Update[string] `json:"localization,omitempty"`
}

// FooterLink identifies localized labels and destinations by a stable ID.
type FooterLink struct {
	Resolution   *locale.Resolution             `json:"localization_resolution,omitempty"`
	ID           string                         `json:"id"`
	Label        string                         `json:"label"`
	URL          string                         `json:"url"`
	Localization *locale.Update[NavigationCopy] `json:"localization,omitempty"`
}

// legacyItemID generates a deterministic editor ID for an entry without a stored ID.
func legacyItemID(kind string, index int, value string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", kind, index, value)))
	return hex.EncodeToString(sum[:8])
}

// ParseMenus keeps existing menu IDs and assigns deterministic IDs to entries that lack one.
func ParseMenus(raw string) []CustomMenuItem {
	items := []CustomMenuItem{}
	_ = json.Unmarshal([]byte(raw), &items)
	for i := range items {
		if items[i].ID == "" {
			items[i].ID = legacyItemID("menu", i, items[i].Label)
		}
	}
	return items
}

// ParseEndpoints parses endpoint entries and assigns missing IDs.
func ParseEndpoints(raw string) []CustomEndpoint {
	items := []CustomEndpoint{}
	_ = json.Unmarshal([]byte(raw), &items)
	for i := range items {
		if items[i].ID == "" {
			items[i].ID = legacyItemID("endpoint", i, items[i].Endpoint)
		}
	}
	return items
}

// ParseFooterGroups assigns missing IDs to footer groups and links.
func ParseFooterGroups(raw string) []FooterLinkGroup {
	items := []FooterLinkGroup{}
	_ = json.Unmarshal([]byte(raw), &items)
	for i := range items {
		if items[i].ID == "" {
			items[i].ID = legacyItemID("footer", i, items[i].Title)
		}
		for j := range items[i].Links {
			if items[i].Links[j].ID == "" {
				items[i].Links[j].ID = legacyItemID(items[i].ID, j, items[i].Links[j].URL)
			}
		}
	}
	return items
}
