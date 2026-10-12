package site

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/site/content"
)

// Navigation translations match stored entries by ID, so menu labels and footer
// captions can change without selecting another entry's localization state.
// Prepared writes clear response resolution metadata, reject repeated IDs, and
// reuse saved localization when an update omits it. Menu page access continues
// to use the shared visibility and page-slug fields.
//
// Admin-only menus keep source copy and discard localization edits. Other menus
// translate captions and destinations as one copy, with a validation rule that
// binds md: destinations to the submitted menu URL. Page authorization uses the
// saved PageSlug and Visibility fields. Endpoint addresses remain shared across
// languages. Public resolution writes selected copies back into the provided
// settings map and removes the editor's content state.

type (
	// NavigationCopy is the caption and destination stored as one localized value.
	NavigationCopy = content.NavigationCopy
	// CustomMenuItem is the shared menu representation with page-access and display fields.
	CustomMenuItem = content.CustomMenuItem
	// EndpointCopy is the localized name and description for a shared endpoint address.
	EndpointCopy = content.EndpointCopy
	// CustomEndpoint is the editable endpoint address and its localized display copy.
	CustomEndpoint = content.CustomEndpoint
	// FooterLinkGroup is an identified footer column with an independently localized title.
	FooterLinkGroup = content.FooterLinkGroup
	// FooterLink is an identified caption and destination within a footer column.
	FooterLink = content.FooterLink
)

// ParseMenus returns menu entries using the content parser's legacy identity rules.
// Stored IDs are retained; entries lacking IDs receive deterministic identifiers
// based on position and label before callers match their translation updates.
func ParseMenus(raw string) []CustomMenuItem { return content.ParseMenus(raw) }

// ParseEndpoints returns endpoints with identities supplied by the shared content parser.
// Saved IDs remain unchanged, and legacy entries receive identifiers based on
// their position and address for matching later localization edits.
func ParseEndpoints(raw string) []CustomEndpoint { return content.ParseEndpoints(raw) }

// ParseFooterGroups returns footer columns and links using the content identity rules.
// Legacy group IDs depend on title and position; legacy link IDs also depend
// on the containing group so later content updates can match nested entries.
func ParseFooterGroups(raw string) []FooterLinkGroup { return content.ParseFooterGroups(raw) }

// validateNavigationCopy checks a nonblank caption and an allowed destination scheme.
// It trims URL for validation and accepts blank, relative, HTTP, HTTPS, mailto,
// and md: values. An md: prefix passes directly here; PrepareNavigation
// separately compares raw URLs on supplied menu localization copies.
func validateNavigationCopy(copy NavigationCopy) error {
	if strings.TrimSpace(copy.Label) == "" {
		return apperror.BadRequest("NAVIGATION_LABEL_REQUIRED", "Link label is required.")
	}
	value := strings.TrimSpace(copy.URL)
	// The md: marker names an internal page route. Treat it as an allowed link
	// format here; slug format and menu visibility are checked when Pages reads
	// a requested page.
	if strings.HasPrefix(value, "md:") {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "" && parsed.Scheme != "https" && parsed.Scheme != "http" && parsed.Scheme != "mailto" {
		return apperror.BadRequest("INVALID_LINK_URL", "Link URL is invalid.")
	}
	return nil
}

// prepareCopy returns the saved localization object when input omits an update.
// A supplied update is prepared against previous content, or against an
// original content block initialized from source when no localization exists.
//
// The helper delegates revision checks and copy validation to locale.Prepare,
// then returns a wrapper containing the prepared content. It can return the
// previous pointer unchanged for omission, so callers should distinguish a
// retained object from a newly prepared update when deciding ownership.
func prepareCopy[T any](previous *locale.Update[T], source T, input *locale.Update[T], validate func(T) error) (*locale.Update[T], error) {
	if input == nil {
		return previous, nil
	}
	current := locale.Original(source)
	if previous != nil {
		current = previous.Content
	}
	next, err := locale.Prepare(current, *input, validate)
	if err != nil {
		return nil, err
	}
	return &locale.Update[T]{Content: next}, nil
}

// PrepareNavigation returns navigation JSON prepared against the previously stored value.
// It handles menu, endpoint, and footer settings; unrelated keys return raw
// unchanged. Preparation produces data for the settings writer and performs no
// persistence itself.
//
// Entries match prior localization by ID, and each setting rejects repeated
// IDs before returning prepared JSON. Footer IDs are checked across columns and
// nested links together. Missing localization edits reuse saved content; source
// fields are then synchronized with that content so an omitted editor block
// cannot silently replace its tracked source copy.
//
// When localization is supplied for a non-admin menu, source and submitted
// translations whose raw URL begins with literal md: must equal item.URL.
// General URL scheme validation uses trimmed input. Admin menus discard editor
// state before these checks.
func PrepareNavigation(key, raw, prior string) (string, error) {
	switch key {
	case SettingKeyCustomMenuItems:
		old := map[string]CustomMenuItem{}
		for _, item := range ParseMenus(prior) {
			old[item.ID] = item
		}
		items := ParseMenus(raw)
		seen := map[string]bool{}
		for i := range items {
			item := &items[i]
			item.Resolution = nil
			if seen[item.ID] {
				return "", apperror.BadRequest("DUPLICATE_CONTENT_ID", "Content ID is duplicated.")
			}
			seen[item.ID] = true
			if item.Visibility == "admin" {
				// Administrator menus use source copy on public resolution, so preparation
				// discards their submitted localization state before any translation validation or revision matching.
				item.Localization = nil
				continue
			}
			previous := old[item.ID]
			copy, err := prepareCopy(previous.Localization, NavigationCopy{Label: previous.Label, URL: previous.URL}, item.Localization, func(copy NavigationCopy) error {
				if err := validateNavigationCopy(copy); err != nil {
					return err
				}
				if strings.HasPrefix(copy.URL, "md:") && copy.URL != item.URL {
					// A raw md: copy names a Markdown route. Match the submitted menu URL
					// before overwriting source fields so the copy cannot attach a different
					// page destination to this entry's stable ID.
					return apperror.BadRequest("LOCALIZED_PAGE_SLUG", "Page translations use the same page slug.")
				}
				return nil
			})
			if err != nil {
				return "", err
			}
			item.Localization = copy
			if copy != nil {
				item.Label, item.URL = copy.Source.Label, copy.Source.URL
			}
		}
		body, err := json.Marshal(items)
		return string(body), err
	case SettingKeyCustomEndpoints:
		old := map[string]CustomEndpoint{}
		for _, item := range ParseEndpoints(prior) {
			old[item.ID] = item
		}
		items := ParseEndpoints(raw)
		seen := map[string]bool{}
		for i := range items {
			item := &items[i]
			item.Resolution = nil
			if seen[item.ID] {
				return "", apperror.BadRequest("DUPLICATE_CONTENT_ID", "Content ID is duplicated.")
			}
			seen[item.ID] = true
			previous := old[item.ID]
			copy, err := prepareCopy(previous.Localization, EndpointCopy{Name: previous.Name, Description: previous.Description}, item.Localization, func(copy EndpointCopy) error {
				if strings.TrimSpace(copy.Name) == "" {
					return apperror.BadRequest("ENDPOINT_NAME_REQUIRED", "Endpoint name is required.")
				}
				return nil
			})
			if err != nil {
				return "", err
			}
			item.Localization = copy
			if copy != nil {
				item.Name, item.Description = copy.Source.Name, copy.Source.Description
			}
		}
		body, err := json.Marshal(items)
		return string(body), err
	case SettingKeyFooterLinks:
		old := map[string]FooterLinkGroup{}
		for _, group := range ParseFooterGroups(prior) {
			old[group.ID] = group
		}
		items := ParseFooterGroups(raw)
		seen := map[string]bool{}
		for i := range items {
			group := &items[i]
			group.Resolution = nil
			if seen[group.ID] {
				return "", apperror.BadRequest("DUPLICATE_CONTENT_ID", "Content ID is duplicated.")
			}
			seen[group.ID] = true
			previous := old[group.ID]
			copy, err := prepareCopy(previous.Localization, previous.Title, group.Localization, func(string) error { return nil })
			if err != nil {
				return "", err
			}
			group.Localization = copy
			if copy != nil {
				group.Title = copy.Source
			}
			links := map[string]FooterLink{}
			for _, link := range previous.Links {
				links[link.ID] = link
			}
			for j := range group.Links {
				link := &group.Links[j]
				link.Resolution = nil
				if seen[link.ID] {
					return "", apperror.BadRequest("DUPLICATE_CONTENT_ID", "Content ID is duplicated.")
				}
				seen[link.ID] = true
				prior := links[link.ID]
				copy, err := prepareCopy(prior.Localization, NavigationCopy{Label: prior.Label, URL: prior.URL}, link.Localization, validateNavigationCopy)
				if err != nil {
					return "", err
				}
				link.Localization = copy
				if copy != nil {
					link.Label, link.URL = copy.Source.Label, copy.Source.URL
				}
			}
		}
		body, err := json.Marshal(items)
		return string(body), err
	}
	return raw, nil
}

// ResolveNavigation replaces navigation JSON in values with display copies for code.
// It mutates the supplied map, so callers pass settings they own for this
// response. Resolution retains IDs and page-access fields while removing
// localization content intended for the editor.
//
// Non-admin menu and footer copies resolve their caption and URL together;
// endpoint copies resolve name and description while their address stays shared.
// Group titles and individual links report their own selected languages.
// Admin-only menu entries keep source display text, and nested footer links
// clear any old resolution metadata before computing their current selection.
func ResolveNavigation(values map[string]string, code string) {
	menus := ParseMenus(values[SettingKeyCustomMenuItems])
	for i := range menus {
		item := &menus[i]
		if item.Localization != nil && item.Visibility != "admin" {
			copy, actual := item.Localization.Resolve(code)
			item.Resolution = &actual
			item.Label, item.URL = copy.Label, copy.URL
		}
		item.Localization = nil
	}
	// Menu, endpoint, and footer structures contain JSON-compatible content fields.
	// Resolution discards marshal errors and rewrites each setting with its serialized display result.
	data, _ := json.Marshal(menus)
	values[SettingKeyCustomMenuItems] = string(data)
	endpoints := ParseEndpoints(values[SettingKeyCustomEndpoints])
	for i := range endpoints {
		item := &endpoints[i]
		if item.Localization != nil {
			copy, actual := item.Localization.Resolve(code)
			item.Resolution = &actual
			item.Name, item.Description = copy.Name, copy.Description
		}
		item.Localization = nil
	}
	data, _ = json.Marshal(endpoints)
	values[SettingKeyCustomEndpoints] = string(data)
	groups := ParseFooterGroups(values[SettingKeyFooterLinks])
	for i := range groups {
		group := &groups[i]
		if group.Localization != nil {
			title, actual := group.Localization.Resolve(code)
			group.Title, group.Resolution = title, &actual
		}
		group.Localization = nil
		for j := range group.Links {
			link := &group.Links[j]
			link.Resolution = nil
			if link.Localization != nil {
				copy, actual := link.Localization.Resolve(code)
				link.Resolution = &actual
				link.Label, link.URL = copy.Label, copy.URL
			}
			link.Localization = nil
		}
	}
	data, _ = json.Marshal(groups)
	values[SettingKeyFooterLinks] = string(data)
}
