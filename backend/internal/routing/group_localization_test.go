package routing_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// TestGroupLocalizationWithoutCopy checks omitted creation copy and later editing of a zero-revision record.
func TestGroupLocalizationWithoutCopy(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	admin := newGroupAdminForTest(repo, nil, nil)
	group, err := admin.CreateGroup(context.Background(), &routing.CreateGroupInput{Name: "business", RateMultiplier: 1})
	require.NoError(t, err)
	require.Equal(t, "business", group.Localization.Source.DisplayName)
	require.NotNil(t, group.Localization.Translations)
	require.EqualValues(t, 1, group.Localization.Revision)

	for _, legacy := range []bool{false, true} {
		group.ID = 1
		if legacy {
			group.Localization = routing.GroupLocalization{}
		}
		repo.getByID = group
		content := routing.GroupContent(group)
		updated, err := admin.UpdateGroup(context.Background(), group.ID, &routing.UpdateGroupInput{
			Localization: &locale.Update[routing.GroupCopy]{Content: content},
		})
		require.NoError(t, err)
		require.Equal(t, "business", updated.Localization.Source.DisplayName)
		require.NotNil(t, updated.Localization.Translations)
		language := "en"
		content = routing.GroupContent(updated)
		content.SourceLocale = &language
		content.Source.DisplayName = "Display name"
		repo.getByID = updated
		updated, err = admin.UpdateGroup(context.Background(), group.ID, &routing.UpdateGroupInput{
			Localization: &locale.Update[routing.GroupCopy]{Content: content},
		})
		require.NoError(t, err)
		display, _ := routing.GroupDisplay(updated, "en")
		require.Equal(t, "Display name", display.DisplayName)
	}
}
