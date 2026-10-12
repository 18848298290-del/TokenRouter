package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// Provider list observations distinguish response redaction and entity tags
// from optional scheduler-score queries. management_list.go uses one item shape
// for either lite input, so the lite-named tests protect tag behavior and
// controlled payload budgets while other scenarios inspect candidate
// populations, hydrated groups and platform-specific sticky weights.

// TestProviderHandlerListLiteUsesCompactDTOAndETag requests a lite-tagged
// provider list containing credentials and loaded groups. Secret values are
// redacted, selection fields remain visible, a matching tag returns 304 and a
// request omitting lite still includes relationships.
func TestProviderHandlerListLiteUsesCompactDTOAndETag(t *testing.T) {
	router, adminSvc := setupProviderListRouter()
	now := time.Now().UTC()
	groupID := int64(77)
	adminSvc.providers = []provider.Record{{
		ID: 501, Name: "compact-provider", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Credentials: map[string]any{"email": "compact@example.com", "access_token": strings.Repeat("x", 4096)},
		Extra:       map[string]any{"privacy_mode": "training_off"}, Status: billing.StatusActive,
		Schedulable: true, Concurrency: 4, GroupIDs: []int64{groupID},
		Groups:         []*accessview.GroupConfig{{ID: groupID, Name: "codex"}},
		ProviderGroups: []provider.GroupMembership{{ProviderID: 501, GroupID: groupID, Group: &accessview.GroupConfig{Hydrated: true, Status: billing.StatusActive, AllowedProtocols: capability.DefaultGroupClientProtocols(""), ID: groupID, Name: "codex"}}},
		CreatedAt:      now, UpdatedAt: now,
	}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/providers?page=1&page_size=20&lite=1", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, rec.Header().Get("ETag"))

	var litePayload struct {
		Data struct { // decoded paginated response object
			Items []map[string]any `json:"items"` // returned provider page items
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &litePayload))
	require.Len(t, litePayload.Data.Items, 1)
	liteItem := litePayload.Data.Items[0]
	require.Equal(t, float64(501), liteItem["id"])
	require.Equal(t, []any{float64(groupID)}, liteItem["group_ids"])
	require.Equal(t, true, liteItem["schedulable"])
	credentials, ok := liteItem["credentials"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "compact@example.com", credentials["email"])
	require.NotContains(t, credentials, "access_token")
	credentialsStatus, ok := liteItem["credentials_status"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, credentialsStatus["has_access_token"])

	// A matching ETag for the lite response yields 304 on a repeated request.
	rec304 := httptest.NewRecorder()
	req304 := httptest.NewRequest(http.MethodGet, "/api/v1/admin/providers?page=1&page_size=20&lite=1", nil)
	req304.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	router.ServeHTTP(rec304, req304)
	require.Equal(t, http.StatusNotModified, rec304.Code)

	// Lite participates in tag construction, while the handler returns the same
	// DTO shape. This request checks loaded relationships when the flag is absent.
	recFull := httptest.NewRecorder()
	reqFull := httptest.NewRequest(http.MethodGet, "/api/v1/admin/providers?page=1&page_size=20", nil)
	router.ServeHTTP(recFull, reqFull)
	require.Equal(t, http.StatusOK, recFull.Code)
	var fullPayload struct {
		Data struct { // decoded paginated response object
			Items []map[string]any `json:"items"` // returned provider page items
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recFull.Body.Bytes(), &fullPayload))
	require.Contains(t, fullPayload.Data.Items[0], "groups")
	require.Contains(t, fullPayload.Data.Items[0], "provider_groups")
}

// TestProviderHandlerListLiteStaysBelowResponseBudget serializes twenty
// controlled provider records with loaded group descriptions under lite input.
// This fixture payload stays below its raw and gzip byte budgets, without
// asserting a different compact DTO format.
func TestProviderHandlerListLiteStaysBelowResponseBudget(t *testing.T) {
	router, adminSvc := setupProviderListRouter()
	now := time.Now().UTC()
	providers := make([]provider.Record, 20)
	for i := range providers {
		id := int64(600 + i)
		groupID := int64(800 + i)
		providers[i] = provider.Record{
			ID: id, Name: "provider-" + strconv.Itoa(i), Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true,
			Concurrency: 4, GroupIDs: []int64{groupID},
			Groups:         []*accessview.GroupConfig{{ID: groupID, Name: "group-" + strconv.Itoa(i), Description: strings.Repeat("description ", 20)}},
			ProviderGroups: []provider.GroupMembership{{ProviderID: id, GroupID: groupID, Group: &accessview.GroupConfig{Hydrated: true, Status: billing.StatusActive, AllowedProtocols: capability.DefaultGroupClientProtocols(""), ID: groupID, Name: "group-" + strconv.Itoa(i), Description: strings.Repeat("description ", 20)}}},
			CreatedAt:      now, UpdatedAt: now,
		}
	}
	adminSvc.providers = providers

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/providers?page=1&page_size=20&lite=1", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	// The raw-response budget applies to these twenty complete fixture DTOs.
	require.Less(t, rec.Body.Len(), 80*1024)

	var compressed bytes.Buffer
	// Compression has buffered framing bytes. Close the writer before measuring
	// its complete fixture payload against the compressed response budget.
	zw := gzip.NewWriter(&compressed)
	_, err := zw.Write(rec.Body.Bytes())
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.Less(t, compressed.Len(), 15*1024)
}

// TestProviderHandlerListIncludesCreatedAt requests created-at sorting against
// the list fixture. The sort selector reaches administration, and the returned
// creation timestamp parses as UTC text with a zero timezone offset.
func TestProviderHandlerListIncludesCreatedAt(t *testing.T) {
	router, adminSvc := setupProviderListRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/providers?page=1&page_size=20&sort_by=created_at&sort_order=desc", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "created_at", adminSvc.lastListProviders.sortBy)

	var payload struct {
		Data struct { // decoded paginated response object
			Items []struct { // returned provider page items
				ID        int64  `json:"id"`         // returned TokenRouter provider identifier
				CreatedAt string `json:"created_at"` // returned provider creation timestamp
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)

	createdAt := payload.Data.Items[0].CreatedAt
	require.NotEmpty(t, createdAt)
	require.True(t, strings.HasSuffix(createdAt, "Z"), "created_at 应序列化为 UTC")
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	require.NoError(t, err)
	_, offset := parsed.Zone()
	require.Equal(t, 0, offset)
}

// TestProviderHandlerListReturnsSchedulerScoresPerGroup lists two OpenAI group
// peers with different priorities and requested scoring. Each response includes
// its group identity and the higher-priority fixture receives the greater base
// score.
func TestProviderHandlerListReturnsSchedulerScoresPerGroup(t *testing.T) {
	router, adminSvc := setupProviderListRouter()
	now := time.Now().UTC()
	groupID := int64(41)
	adminSvc.providers = []provider.Record{
		{
			ID:          101,
			Name:        "provider-high-priority",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 10,
			Priority:    1,
			ProviderGroups: []provider.GroupMembership{
				{ProviderID: 101, GroupID: groupID, Group: &accessview.GroupConfig{Hydrated: true, Status: billing.StatusActive, AllowedProtocols: capability.DefaultGroupClientProtocols(""), ID: groupID, Name: "openai", SchedulerType: routing.GroupSchedulerTypeAdvanced}},
			},
			GroupIDs:  []int64{groupID},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:          102,
			Name:        "provider-low-priority",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 10,
			Priority:    100000,
			ProviderGroups: []provider.GroupMembership{
				{ProviderID: 102, GroupID: groupID, Group: &accessview.GroupConfig{Hydrated: true, Status: billing.StatusActive, AllowedProtocols: capability.DefaultGroupClientProtocols(""), ID: groupID, Name: "openai", SchedulerType: routing.GroupSchedulerTypeAdvanced}},
			},
			GroupIDs:  []int64{groupID},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/providers?page=1&page_size=20&platform=openai&include_scheduler_score=1", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct { // decoded paginated response object
			Items []struct { // returned provider page items
				ID             int64    `json:"id"` // returned TokenRouter provider identifier
				SchedulerScore struct { // overall filter-pool score observation
					BaseScore float64 `json:"base_score"` // returned scheduler base score
				} `json:"scheduler_score"`
				SchedulerScores []struct { // returned group score observations
					GroupID   *int64  `json:"group_id"`   // returned routing group identifier
					GroupName string  `json:"group_name"` // returned routing group name
					BaseScore float64 `json:"base_score"` // returned scheduler base score
				} `json:"scheduler_scores"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 2)

	var high, low *struct {
		ID             int64    `json:"id"` // returned TokenRouter provider identifier
		SchedulerScore struct { // overall filter-pool score observation
			BaseScore float64 `json:"base_score"` // returned scheduler base score
		} `json:"scheduler_score"`
		SchedulerScores []struct { // returned group score observations
			GroupID   *int64  `json:"group_id"`   // returned routing group identifier
			GroupName string  `json:"group_name"` // returned routing group name
			BaseScore float64 `json:"base_score"` // returned scheduler base score
		} `json:"scheduler_scores"`
	}
	for i := range payload.Data.Items {
		item := &payload.Data.Items[i]
		switch item.ID {
		case 101:
			high = item
		case 102:
			low = item
		}
	}
	require.NotNil(t, high)
	require.NotNil(t, low)
	require.Len(t, high.SchedulerScores, 1)
	require.Len(t, low.SchedulerScores, 1)
	require.Equal(t, groupID, *high.SchedulerScores[0].GroupID)
	require.Equal(t, "openai", high.SchedulerScores[0].GroupName)
	require.Greater(t, high.SchedulerScores[0].BaseScore, low.SchedulerScores[0].BaseScore)
}

// TestProviderHandlerListNonOpenAIStickyScoreExcludesPreviousResponse scores a
// Gemini provider with separate previous-response and session weights. Its
// group sticky score equals base plus session weight, protecting the
// platform-specific exclusion of previous-response affinity.
func TestProviderHandlerListNonOpenAIStickyScoreExcludesPreviousResponse(t *testing.T) {
	router, adminSvc := setupProviderListRouter()
	now := time.Now().UTC()
	groupID := int64(45)
	stickyWeighted := true
	previousWeight := 11.0
	sessionWeight := 7.0
	group := &accessview.GroupConfig{
		Hydrated: true, Status: billing.StatusActive, AllowedProtocols: capability.DefaultGroupClientProtocols(""),
		ID:   groupID,
		Name: "gemini",

		SchedulerType: routing.GroupSchedulerTypeAdvanced,
		AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{
			StickyWeightedEnabled:  &stickyWeighted,
			WeightPreviousResponse: &previousWeight,
			WeightSessionSticky:    &sessionWeight,
		},
	}
	adminSvc.providers = []provider.Record{
		{
			ID: 105, Name: "gemini-provider", Platform: capability.PlatformGemini,
			Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true,
			Concurrency: 10, Priority: 1,
			ProviderGroups: []provider.GroupMembership{{ProviderID: 105, GroupID: groupID, Group: group}},
			GroupIDs:       []int64{groupID}, CreatedAt: now, UpdatedAt: now,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/providers?page=1&page_size=20&platform=gemini&include_scheduler_score=1", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct { // decoded paginated response object
			Items []struct { // returned provider page items
				SchedulerScores []struct { // returned group score observations
					BaseScore   float64 `json:"base_score"`   // returned scheduler base score
					StickyScore float64 `json:"sticky_score"` // returned scheduler sticky score
				} `json:"scheduler_scores"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	require.Len(t, payload.Data.Items[0].SchedulerScores, 1)
	score := payload.Data.Items[0].SchedulerScores[0]
	// Allow floating-point addition rounding in the selected sticky-score sum.
	require.InDelta(t, score.BaseScore+sessionWeight, score.StickyScore, 0.000001)
}

// TestProviderHandlerListReusesHydratedGroupsWithoutRepositoryLookups lists two
// providers with shared loaded groups and repeated group IDs. Group reads
// remain absent, pool lookups occur once per distinct group and each response
// contains two group score observations.
func TestProviderHandlerListReusesHydratedGroupsWithoutRepositoryLookups(t *testing.T) {
	router, adminSvc := setupProviderListRouter()
	now := time.Now().UTC()
	firstGroup := &accessview.GroupConfig{
		Hydrated: true, Status: billing.StatusActive, AllowedProtocols: capability.DefaultGroupClientProtocols(""),
		ID: 51, Name: "openai-primary",
		SchedulerType: routing.GroupSchedulerTypeAdvanced,
	}
	secondGroup := &accessview.GroupConfig{
		Hydrated: true, Status: billing.StatusActive, AllowedProtocols: capability.DefaultGroupClientProtocols(""),
		ID: 52, Name: "openai-secondary",
		SchedulerType: routing.GroupSchedulerTypeAdvanced,
	}
	providerGroups := func(providerID int64) []provider.GroupMembership {
		return []provider.GroupMembership{
			{ProviderID: providerID, GroupID: firstGroup.ID, Group: firstGroup},
			{ProviderID: providerID, GroupID: secondGroup.ID, Group: secondGroup},
		}
	}
	adminSvc.providers = []provider.Record{
		{
			ID: 111, Name: "first", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Status: billing.StatusActive, Schedulable: true, Concurrency: 10, Priority: 1,
			ProviderGroups: providerGroups(111), GroupIDs: []int64{51, 52, 51}, CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: 112, Name: "second", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Status: billing.StatusActive, Schedulable: true, Concurrency: 10, Priority: 2,
			ProviderGroups: providerGroups(112), GroupIDs: []int64{52, 51, 52}, CreatedAt: now, UpdatedAt: now,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/providers?page=1&page_size=20&platform=openai&include_scheduler_score=1", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Zero(t, adminSvc.getGroupCalls)
	require.Equal(t, []int64{firstGroup.ID, secondGroup.ID}, adminSvc.openAISchedulerScorePoolGroupIDs)
	require.Equal(t, 2, adminSvc.openAISchedulerScorePoolCalls)

	var payload struct {
		Data struct { // decoded paginated response object
			Items []struct { // returned provider page items
				SchedulerScores []struct { // returned group score observations
					GroupID *int64 `json:"group_id"` // returned routing group identifier
				} `json:"scheduler_scores"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 2)
	for _, item := range payload.Data.Items {
		require.Len(t, item.SchedulerScores, 2)
		groupIDs := []int64{*item.SchedulerScores[0].GroupID, *item.SchedulerScores[1].GroupID}
		require.ElementsMatch(t, []int64{firstGroup.ID, secondGroup.ID}, groupIDs)
	}
}

// TestProviderHandlerListSkipsSchedulerScoresByDefault lists an OpenAI provider
// without the include-score query flag. Both score candidate query counters
// remain zero and optional score fields stay absent from the response.
func TestProviderHandlerListSkipsSchedulerScoresByDefault(t *testing.T) {
	router, adminSvc := setupProviderListRouter()
	now := time.Now().UTC()
	adminSvc.providers = []provider.Record{
		{
			ID:          110,
			Name:        "openai-provider",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 10,
			Priority:    1,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/providers?page=1&page_size=20&platform=openai", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Zero(t, adminSvc.schedulerScoreFilterCalls)
	require.Zero(t, adminSvc.openAISchedulerScorePoolCalls)

	var payload struct {
		Data struct { // decoded paginated response object
			Items []map[string]any `json:"items"` // returned provider page items
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	require.NotContains(t, payload.Data.Items[0], "scheduler_score")
	require.NotContains(t, payload.Data.Items[0], "scheduler_scores")
}

// TestProviderHandlerListKeepsSchedulerScoreScopedToFilter returns one visible
// provider while supplying a hidden peer in score populations. The response's
// overall and group-specific base scores agree for the visible provider and
// retain the selected group identity.
func TestProviderHandlerListKeepsSchedulerScoreScopedToFilter(t *testing.T) {
	router, adminSvc := setupProviderListRouter()
	now := time.Now().UTC()
	groupID := int64(42)
	visibleProvider := provider.Record{
		ID:          201,
		Name:        "visible-low-priority",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 10,
		Priority:    100000,
		ProviderGroups: []provider.GroupMembership{
			{ProviderID: 201, GroupID: groupID, Group: &accessview.GroupConfig{Hydrated: true, Status: billing.StatusActive, AllowedProtocols: capability.DefaultGroupClientProtocols(""), ID: groupID, Name: "openai", SchedulerType: routing.GroupSchedulerTypeAdvanced}},
		},
		GroupIDs:  []int64{groupID},
		CreatedAt: now,
		UpdatedAt: now,
	}
	hiddenGroupPeer := provider.Record{
		ID:          202,
		Name:        "hidden-high-priority",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 10,
		Priority:    1,
		ProviderGroups: []provider.GroupMembership{
			{ProviderID: 202, GroupID: groupID, Group: &accessview.GroupConfig{Hydrated: true, Status: billing.StatusActive, AllowedProtocols: capability.DefaultGroupClientProtocols(""), ID: groupID, Name: "openai", SchedulerType: routing.GroupSchedulerTypeAdvanced}},
		},
		GroupIDs:  []int64{groupID},
		CreatedAt: now,
		UpdatedAt: now,
	}
	adminSvc.providers = []provider.Record{visibleProvider}
	adminSvc.providerSchedulerScoreFilterProviders = []provider.Record{visibleProvider, hiddenGroupPeer}
	adminSvc.openAISchedulerScorePoolProviders = []provider.Record{visibleProvider, hiddenGroupPeer}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/providers?page=1&page_size=1&platform=openai&include_scheduler_score=1", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct { // decoded paginated response object
			Items []struct { // returned provider page items
				ID             int64    `json:"id"` // returned TokenRouter provider identifier
				SchedulerScore struct { // overall filter-pool score observation
					BaseScore float64 `json:"base_score"` // returned scheduler base score
				} `json:"scheduler_score"`
				SchedulerScores []struct { // returned group score observations
					GroupID   *int64  `json:"group_id"`   // returned routing group identifier
					BaseScore float64 `json:"base_score"` // returned scheduler base score
				} `json:"scheduler_scores"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	item := payload.Data.Items[0]
	require.Equal(t, int64(201), item.ID)
	require.Len(t, item.SchedulerScores, 1)
	require.Equal(t, groupID, *item.SchedulerScores[0].GroupID)
	require.Equal(t, item.SchedulerScores[0].BaseScore, item.SchedulerScore.BaseScore)
}

// TestProviderHandlerListSchedulerScoreIgnoresPagination returns one page item
// while including an additional unpaged scoring candidate. The visible base
// score reflects that wider candidate population and no group-specific scores
// are emitted.
func TestProviderHandlerListSchedulerScoreIgnoresPagination(t *testing.T) {
	router, adminSvc := setupProviderListRouter()
	now := time.Now().UTC()
	visibleProvider := provider.Record{
		ID:          301,
		Name:        "visible-low-priority",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 10,
		Priority:    100000,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	hiddenFilterPeer := provider.Record{
		ID:          302,
		Name:        "hidden-high-priority",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 10,
		Priority:    1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	adminSvc.providers = []provider.Record{visibleProvider}
	adminSvc.providerSchedulerScoreFilterProviders = []provider.Record{visibleProvider, hiddenFilterPeer}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/providers?page=1&page_size=1&platform=openai&include_scheduler_score=1", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct { // decoded paginated response object
			Items []struct { // returned provider page items
				ID             int64    `json:"id"` // returned TokenRouter provider identifier
				SchedulerScore struct { // overall filter-pool score observation
					BaseScore float64 `json:"base_score"` // returned scheduler base score
				} `json:"scheduler_score"`
				SchedulerScores []struct { // returned group score observations
					GroupID   *int64  `json:"group_id"`   // returned routing group identifier
					BaseScore float64 `json:"base_score"` // returned scheduler base score
				} `json:"scheduler_scores"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	require.Equal(t, int64(301), payload.Data.Items[0].ID)
	require.Less(t, payload.Data.Items[0].SchedulerScore.BaseScore, 3.75)
	require.Empty(t, payload.Data.Items[0].SchedulerScores)
}

// setupProviderListRouter returns a narrow list router and its initialized
// provider fixture. The caller can replace configured rows before requests,
// while production ManagementList and RuntimePresenter supply observation and
// encoding behavior.
func setupProviderListRouter() (*gin.Engine, *managementListFixture) {
	router := gin.New()
	adminSvc := newManagementListFixture()
	handler := newManagementListFixtureHandler(adminSvc)
	router.GET("/api/v1/admin/providers", handler.List)
	return router, adminSvc
}

// newManagementListFixtureHandler connects production listing, scoring and
// presentation with fixed effective score defaults. Hydrated advanced groups
// apply their overrides, while runtime status uses the empty observation
// options for these controlled tests.
func newManagementListFixtureHandler(source *managementListFixture) *ManagementHandler {
	runtime := provider.NewRuntimeStatusReader(provider.RuntimeStatusOptions{})
	scores := provider.NewSchedulerScoreView(source, provideradapter.SchedulerScoreOptions(nil, nil, func(_ context.Context, group *accessview.GroupConfig) policy.EffectiveSettings {
		var overrides policy.GroupAdvancedSchedulerOverrides
		if group != nil && group.SchedulerType == "advanced" {
			overrides = group.AdvancedSchedulerOverrides
		}
		return policy.ResolveEffective(7, policy.ScoreWeights{Priority: 1, Load: 1, Queue: 0.7, ErrorRate: 0.8, TTFT: 0.5, Previous: 5, SessionSticky: 3}, policy.RuntimeSettings{}, overrides)
	}))
	presenter := NewRuntimePresenter(runtime, source, nil)
	return NewManagementHandler(source, ManagementOptions{List: provider.NewManagementList(source, runtime, scores, nil), RuntimePresenter: presenter})
}
