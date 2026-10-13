package routing

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
)

// A group's localized display copy is stored separately from its administrative
// name and description. Records without a saved revision expose those legacy
// fields as revision-one source content, so translation editing and display
// resolution have a usable starting point.

type (
	// GroupCopy represents the user-facing group display name and description.
	GroupCopy = accessview.GroupCopy
	// GroupLocalization stores versioned source copy and its language
	// translations.
	GroupLocalization = accessview.GroupLocalization
)

// ValidateGroupCopy checks the display name and description used by
// localization edits. The display name requires nonblank text and at most 100
// runes; the description limit counts 65,536 bytes rather than characters.
func ValidateGroupCopy(copy GroupCopy) error {
	if strings.TrimSpace(copy.DisplayName) == "" || len([]rune(copy.DisplayName)) > 100 {
		return apperror.BadRequest("GROUP_DISPLAY_NAME_REQUIRED", "Group display name is required and must not exceed 100 characters.")
	}
	// The description ceiling bounds stored content by bytes, while the display
	// name's user-facing limit counts Unicode characters.
	if len(copy.Description) > 65536 {
		return apperror.BadRequest("GROUP_DESCRIPTION_TOO_LONG", "Group description exceeds the allowed length.")
	}
	return nil
}

// GroupContent returns the group's saved localization or revision-one content
// derived from its legacy fields. Supply a non-nil group; a positive revision
// retains the source-language pointer and translation map in the returned
// value. Callers copy those references before mutating their contents.
func GroupContent(group *Group) locale.Content[GroupCopy] {
	if group.Localization.Revision > 0 {
		return locale.Content[GroupCopy](group.Localization)
	}
	content := locale.Original(GroupCopy{DisplayName: group.Name, Description: group.Description})
	content.Revision, content.SourceRevision = 1, 1
	return content
}

// GroupDisplay resolves the group's display copy for the requested language
// through locale.Content.Resolve. Supply a non-nil group; an unsaved record
// uses its administrative name and description as the source content. Resolution
// can retain the group's source-language pointer, which callers keep read-only.
func GroupDisplay(group *Group, language string) (GroupCopy, locale.Resolution) {
	return GroupContent(group).Resolve(language)
}

// GroupSearchTexts returns sorted, distinct nonempty names and descriptions
// from the source and current translations. Supply a non-nil group; revision
// zero returns nil so callers use legacy source fields through their usual
// search inputs. Translation text participates only when its reviewed source
// revision matches the current source revision.
func GroupSearchTexts(group *Group) []string {
	if group.Localization.Revision == 0 {
		return nil
	}
	return locale.SearchTexts(locale.Content[GroupCopy](group.Localization), func(copy GroupCopy) []string { return []string{copy.DisplayName, copy.Description} })
}
