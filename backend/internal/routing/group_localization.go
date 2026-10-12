package routing

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
)

type (
	GroupCopy         = accessview.GroupCopy
	GroupLocalization = accessview.GroupLocalization
)

// ValidateGroupCopy validates the user-facing display name and description.
func ValidateGroupCopy(copy GroupCopy) error {
	if strings.TrimSpace(copy.DisplayName) == "" || len([]rune(copy.DisplayName)) > 100 {
		return apperror.BadRequest("GROUP_DISPLAY_NAME_REQUIRED", "Group display name is required and must not exceed 100 characters.")
	}
	if len(copy.Description) > 65536 {
		return apperror.BadRequest("GROUP_DESCRIPTION_TOO_LONG", "Group description exceeds the allowed length.")
	}
	return nil
}

// GroupContent returns editable copy, using the group name and description at revision 1 when no copy is saved.
func GroupContent(group *Group) locale.Content[GroupCopy] {
	if group.Localization.Revision > 0 {
		return locale.Content[GroupCopy](group.Localization)
	}
	content := locale.Original(GroupCopy{DisplayName: group.Name, Description: group.Description})
	content.Revision, content.SourceRevision = 1, 1
	return content
}

// GroupDisplay resolves localized copy, using the group name and description when no copy is saved.
func GroupDisplay(group *Group, language string) (GroupCopy, locale.Resolution) {
	return GroupContent(group).Resolve(language)
}

// GroupSearchTexts returns searchable language variants of saved user-facing names and descriptions.
func GroupSearchTexts(group *Group) []string {
	if group.Localization.Revision == 0 {
		return nil
	}
	return locale.SearchTexts(locale.Content[GroupCopy](group.Localization), func(copy GroupCopy) []string { return []string{copy.DisplayName, copy.Description} })
}
