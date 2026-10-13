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

// Subscription display uses translated group text while retaining the stored
// business name. A shared group fixture changes localization revision across
// sequential cases, exposing valid translation, stale-translation fallback, and
// legacy-name behavior through the billing-to-HTTP adapter.

// subscriptionGroupFixture supplies shared group localization for subscription
// display scenarios.
type subscriptionGroupFixture struct {
	// GroupRepository supplies unused methods; GetByIDLite provides the group
	// data read by subscription enrichment and later exposed by the HTTP conversion.
	routing.GroupRepository
	group *routing.Group // shared localized group
}

// GetByIDLite returns the configured group pointer for subscription enrichment.
// The fixture ignores context and ID, sharing its localization with the
// sequential cases that update revision before each query.
func (r subscriptionGroupFixture) GetByIDLite(context.Context, int64) (*routing.Group, error) {
	return r.group, nil
}

// TestSubscriptionGroupLanguage enriches subscription plans under English and
// Chinese request language with changing revisions. It checks translated,
// stale, and legacy group display names while the stored business name remains
// unchanged.
func TestSubscriptionGroupLanguage(t *testing.T) {
	en := "en"
	group := &routing.Group{ID: 1, Name: "business-name", Localization: routing.GroupLocalization{
		SourceLocale: &en, Source: routing.GroupCopy{DisplayName: "English group"},
		Revision: 1, SourceRevision: 1,
		Translations: map[string]locale.Translation[routing.GroupCopy]{"zh-Hans": {Value: routing.GroupCopy{DisplayName: "中文分组"}, SourceRevision: 1}},
	}}
	service := billing.NewSubscriptionService(billingGroups{Repository: subscriptionGroupFixture{group: group}}, nil, nil)
	for _, test := range []struct {
		language       string // requested display language
		want           string // expected group label
		sourceRevision int64  // current source revision
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
