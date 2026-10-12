package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// subscriptionGroupFixture supplies a group with a business name and translated display copy.
type subscriptionGroupFixture struct {
	routing.GroupRepository
	group *routing.Group
}

func (r subscriptionGroupFixture) GetByIDLite(context.Context, int64) (*routing.Group, error) {
	return r.group, nil
}

// TestSubscriptionGroupLanguage checks translations, stale-translation fallback, and legacy group names.
func TestSubscriptionGroupLanguage(t *testing.T) {
	en := "en"
	group := &routing.Group{ID: 1, Name: "business-name", Localization: routing.GroupLocalization{
		SourceLocale: &en, Source: routing.GroupCopy{DisplayName: "English group"},
		Revision: 1, SourceRevision: 1,
		Translations: map[string]locale.Translation[routing.GroupCopy]{"zh-Hans": {Value: routing.GroupCopy{DisplayName: "中文分组"}, SourceRevision: 1}},
	}}
	service := billing.NewSubscriptionService(billingGroups{Repository: subscriptionGroupFixture{group: group}}, nil, nil)
	for _, test := range []struct {
		language       string
		want           string
		sourceRevision int64
	}{
		{"en", "English group", 1},
		{"zh-Hans", "中文分组", 1},
		{"zh-Hans", "English group", 2},
		{"zh-Hans", "business-name", 0},
	} {
		group.Localization.SourceRevision = test.sourceRevision
		if test.sourceRevision == 0 {
			group.Localization = routing.GroupLocalization{}
		}
		items := []billing.UserSubscription{{Plan: &billing.SubscriptionPlan{ID: 1, Name: "Plan", GroupIDs: []int64{1}}}}
		ctx := locale.WithLanguage(context.Background(), test.language)
		service.EnrichSubscriptionPlanGroups(ctx, items)
		response := httpapi.UserSubscriptionFromService(&items[0], test.language)
		require.Equal(t, test.want, response.Plan.ApplicableGroups[0].Name)
		require.Equal(t, "business-name", group.Name)
	}
}
