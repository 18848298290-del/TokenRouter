package site

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/site/content"
)

// These content types give admin and user views a shared serialization format.
type (
	NavigationCopy  = content.NavigationCopy
	CustomMenuItem  = content.CustomMenuItem
	EndpointCopy    = content.EndpointCopy
	CustomEndpoint  = content.CustomEndpoint
	FooterLinkGroup = content.FooterLinkGroup
	FooterLink      = content.FooterLink
)

// ParseMenus parses menu items with stable IDs and display text.
func ParseMenus(raw string) []CustomMenuItem { return content.ParseMenus(raw) }

// ParseEndpoints parses the names and descriptions of copyable endpoints.
func ParseEndpoints(raw string) []CustomEndpoint { return content.ParseEndpoints(raw) }

// ParseFooterGroups parses footer groups and their links.
func ParseFooterGroups(raw string) []FooterLinkGroup { return content.ParseFooterGroups(raw) }

// validateNavigationCopy requires a label and accepts relative, HTTP, HTTPS,
// mailto, and md: links.
func validateNavigationCopy(copy NavigationCopy) error {
	if strings.TrimSpace(copy.Label) == "" {
		return apperror.BadRequest("NAVIGATION_LABEL_REQUIRED", "Link label is required.")
	}
	value := strings.TrimSpace(copy.URL)
	if strings.HasPrefix(value, "md:") {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "" && parsed.Scheme != "https" && parsed.Scheme != "http" && parsed.Scheme != "mailto" {
		return apperror.BadRequest("INVALID_LINK_URL", "Link URL is invalid.")
	}
	return nil
}

// prepareCopy retains previous content when input is omitted; supplied updates
// pass through the shared localization revision checks.
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

// PrepareNavigation matches nested translations by stable ID and validates their
// revisions. The settings write checks for concurrent changes in the database.
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
				item.Localization = nil
				continue
			}
			previous := old[item.ID]
			copy, err := prepareCopy(previous.Localization, NavigationCopy{Label: previous.Label, URL: previous.URL}, item.Localization, func(copy NavigationCopy) error {
				if err := validateNavigationCopy(copy); err != nil {
					return err
				}
				if strings.HasPrefix(copy.URL, "md:") && copy.URL != item.URL {
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

// ResolveNavigation mutates values with resolved display text and removes
// localization data used by the admin editor.
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
