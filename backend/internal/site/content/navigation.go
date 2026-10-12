package content

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// Stored navigation entries may predate the editor's explicit IDs. Parsing
// retains saved IDs and derives missing ones from the entry kind, position,
// and a source value, giving later translation updates a way to match entries
// in the currently stored JSON. Moving or editing an unidentified legacy
// entry can therefore change its synthesized identity.
//
// Display copy is separate from page authorization and endpoint addresses.
// Menus and footer links translate label and URL together; endpoint copies
// translate name and description while sharing one endpoint URL. Footer group
// titles have their own localization state, and each nested link receives its
// own identity under the containing group.

// NavigationCopy contains localized menu or footer link text and destination.
type NavigationCopy struct {
	// Label and URL form one localized copy, so a translated destination stays
	// with its caption. CustomMenuItem separately retains the page authorization fields.
	Label string `json:"label"` // displayed navigation label
	URL   string `json:"url"`   // localized link destination
}

// CustomMenuItem has a stable ID; page authorization uses PageSlug and Visibility.
type CustomMenuItem struct {
	// Resolution describes the selected Label and URL on public reads; the
	// settings preparation path clears this read metadata before persisting edits.
	Resolution *locale.Resolution `json:"localization_resolution,omitempty"`
	// ID identifies the editable entry; ParseMenus derives it from index and
	// Label when legacy JSON omits an identifier for that menu item.
	ID      string `json:"id"`
	Label   string `json:"label"`    // displayed menu label
	IconSVG string `json:"icon_svg"` // configured menu icon
	URL     string `json:"url"`      // displayed menu destination
	// PageSlug and Visibility govern Markdown access independently of localized
	// display copy. Pages prefers PageSlug, deriving a slug from URL only when
	// PageSlug is empty and URL has the legacy md: prefix.
	PageSlug   string `json:"page_slug,omitempty"` // authorized Markdown slug
	Visibility string `json:"visibility"`          // menu access class
	SortOrder  int    `json:"sort_order"`          // configured display order
	// Localization carries label and destination edits; admin-only entries have
	// this state cleared by PrepareNavigation and keep their source display copy.
	Localization *locale.Update[NavigationCopy] `json:"localization,omitempty"`
}

// EndpointCopy contains the name and description of a copyable API endpoint.
type EndpointCopy struct {
	Name        string `json:"name"`        // displayed endpoint name
	Description string `json:"description"` // displayed endpoint explanation
}

// CustomEndpoint identifies a copyable endpoint with one address shared across languages.
type CustomEndpoint struct {
	// Resolution describes the chosen Name and Description for public reads;
	// PrepareNavigation removes this read metadata before producing stored endpoint JSON.
	Resolution *locale.Resolution `json:"localization_resolution,omitempty"`
	// ID matches endpoint translation updates; ParseEndpoints synthesizes missing
	// values from the entry position and Endpoint in legacy stored JSON.
	ID          string `json:"id"`
	Name        string `json:"name"`        // displayed endpoint name
	Endpoint    string `json:"endpoint"`    // shared API address
	Description string `json:"description"` // displayed endpoint explanation
	// Localization updates Name and Description together; the Endpoint address
	// remains the shared value used by copy actions for every language.
	Localization *locale.Update[EndpointCopy] `json:"localization,omitempty"`
}

// FooterLinkGroup contains one column of footer links with a separately translated title.
type FooterLinkGroup struct {
	// Resolution describes the selected Title in the public footer response;
	// nested links carry their own selected-language metadata and localized copy.
	Resolution *locale.Resolution `json:"localization_resolution,omitempty"`
	// ID matches this group across edits; ParseFooterGroups derives missing IDs
	// from group position and Title before assigning IDs to nested links.
	ID    string       `json:"id"`
	Title string       `json:"title"` // displayed column title
	Links []FooterLink `json:"links"` // ordered footer entries
	// Localization translates the group Title as its own content value; each
	// nested link independently translates its caption and destination when resolved.
	Localization *locale.Update[string] `json:"localization,omitempty"`
}

// FooterLink identifies localized labels and destinations by a stable ID.
type FooterLink struct {
	// Resolution describes the selected Label and URL; ResolveNavigation clears
	// previous link resolution metadata before selecting a copy for each public read.
	Resolution *locale.Resolution `json:"localization_resolution,omitempty"`
	// ID identifies a nested editable link; ParseFooterGroups derives missing
	// IDs from the containing group ID, link position, and source URL.
	ID    string `json:"id"`
	Label string `json:"label"` // displayed footer caption
	URL   string `json:"url"`   // localized footer destination
	// Localization updates the caption and destination as one content value;
	// public resolution selects that copy and removes the editor state afterward.
	Localization *locale.Update[NavigationCopy] `json:"localization,omitempty"`
}

// legacyItemID returns a deterministic identifier from kind, position, and source value.
// It encodes the first eight SHA-256 bytes as hexadecimal. Callers retain stored
// IDs, using this identifier for legacy entries that have none.
func legacyItemID(kind string, index int, value string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", kind, index, value)))
	// Eight digest bytes produce sixteen printable identifier characters.
	return hex.EncodeToString(sum[:8])
}

// ParseMenus returns decoded menu entries with an identifier for each legacy item.
// Decoder errors are discarded, and any entries left in the decoded slice are
// processed. Saved IDs remain authoritative when present.
//
// Missing IDs use the menu kind, entry index, and Label as their input. That
// gives unchanged legacy JSON repeatable editor identifiers; changing order or
// labels before an ID is saved can change which identifier is produced for
// the entry and therefore which previous translation it matches.
func ParseMenus(raw string) []CustomMenuItem {
	items := []CustomMenuItem{}
	// Stored navigation is interpreted as available display configuration; JSON
	// errors retain whatever entries decoded so identity preparation can handle that result.
	_ = json.Unmarshal([]byte(raw), &items)
	for i := range items {
		if items[i].ID == "" {
			items[i].ID = legacyItemID("menu", i, items[i].Label)
		}
	}
	return items
}

// ParseEndpoints returns decoded endpoints with identifiers for entries lacking stored IDs.
// Decoder errors are discarded, and the remaining decoded slice supplies the
// result. Entries that already have IDs keep them.
//
// Legacy endpoint identities use the endpoint kind, position, and Endpoint
// address. Name and Description can then be localized as one copy independently
// of that address. Editing an address or moving an entry before saving its ID
// can change the synthesized identity used by later translation matching.
func ParseEndpoints(raw string) []CustomEndpoint {
	items := []CustomEndpoint{}
	// Decoding errors leave any populated entries available to the caller;
	// the content preparation path performs its own duplicate-ID and translation checks afterward.
	_ = json.Unmarshal([]byte(raw), &items)
	for i := range items {
		if items[i].ID == "" {
			items[i].ID = legacyItemID("endpoint", i, items[i].Endpoint)
		}
	}
	return items
}

// ParseFooterGroups returns decoded footer columns and nested links with editable identities.
// Decoder errors are discarded, and saved group and link IDs remain unchanged.
// The remaining entries receive identifiers derived from their source values.
//
// A group's missing ID uses its position and Title. A nested link's missing
// ID uses that resulting group ID, its position, and URL, so groups participate
// in link identity before translation updates are matched. Changes to legacy
// ordering or source values can change IDs until those IDs are saved.
func ParseFooterGroups(raw string) []FooterLinkGroup {
	items := []FooterLinkGroup{}
	// The display parser retains entries populated before a JSON type error;
	// callers receive those entries and apply their own editing or visibility rules.
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
