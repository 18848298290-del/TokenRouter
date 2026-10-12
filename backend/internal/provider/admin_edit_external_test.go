package provider_test

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	billingcore "github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type providerRepoStubForBulkUpdate struct {
	providercore.AdminStore
	bulkUpdateErr     error
	bulkUpdateIDs     []int64
	lastBulkUpdate    providercore.ProviderBulkUpdate
	bindGroupErrByID  map[int64]error
	bindGroupsCalls   []int64
	getByIDsProviders []*providercore.Record
	getByIDsErr       error
	getByIDsCalled    bool
	getByIDsIDs       []int64
	getByIDProviders  map[int64]*providercore.Record
	getByIDErrByID    map[int64]error
	getByIDCalled     []int64
	listByGroupData   map[int64][]providercore.Record
	listByGroupErr    map[int64]error
	listData          []providercore.Record
	listResult        *pagination.PaginationResult
	listErr           error
	listCalled        bool
	lastListParams    pagination.PaginationParams
	lastListFilters   struct {
		platform     string
		providerType string
		status       string
		search       string
		groupID      int64
		privacyMode  string
	}
}

// bulkGroupsFixture provides group-existence checks for bulk binding tests.
type bulkGroupsFixture struct {
	routing.GroupRepository
	group *routing.Group
}

type updateProviderCredsRepoStub struct {
	providercore.AdminStore
	provider    *providercore.Record
	updateCalls int
}

type deprecatedProviderExtraRepoStub struct {
	providercore.AdminStore
	provider            *providercore.Record
	updateExtraCalls    int
	lastExtraUpdates    map[string]any
	bulkUpdateCalls     int
	lastBulkExtraUpdate map[string]any
}

type ollamaManagedExtraUpdateRepo struct {
	providercore.AdminStore
	provider *providercore.Record
	updated  *providercore.Record
}

type updateProviderOveragesRepoStub struct {
	providercore.AdminStore
	provider    *providercore.Record
	updateCalls int
}

func (s *providerRepoStubForBulkUpdate) BulkUpdate(_ context.Context, ids []int64, updates providercore.ProviderBulkUpdate) (int64, error) {
	s.bulkUpdateIDs = append([]int64{}, ids...)
	s.lastBulkUpdate = updates
	if s.bulkUpdateErr != nil {
		return 0, s.bulkUpdateErr
	}
	return int64(len(ids)), nil
}

func TestAdminServiceBulkUpdateProvidersNormalizesLegacyOpenAIConfiguration(t *testing.T) {
	repo := &providerRepoStubForBulkUpdate{
		getByIDsProviders: []*providercore.Record{{
			ID:       1,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
		}},
	}
	svc := newProviderEditorForTest(repo)
	input := &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{1},
		Credentials: map[string]any{
			providercore.LegacyOpenAICapabilitiesCredentialKey: []any{"chat_completions"},
		},
		Extra: map[string]any{
			providercore.LegacyOpenAIResponsesModeExtraKey: "auto",
			"openai_responses_supported":                   false,
		},
	}

	_, err := svc.BulkUpdateProviders(context.Background(), input)

	require.NoError(t, err)
	require.Equal(t, []string{"text_generation"}, repo.lastBulkUpdate.Credentials[providercore.OpenAIWorkloadCapabilitiesCredentialKey])
	require.NotContains(t, repo.lastBulkUpdate.Credentials, providercore.LegacyOpenAICapabilitiesCredentialKey)
	require.Equal(t, "preserve_client_protocol", repo.lastBulkUpdate.Extra["openai_text_route_mode"])
	require.NotContains(t, repo.lastBulkUpdate.Extra, "openai_responses_probe_status")
	require.NotContains(t, repo.lastBulkUpdate.Extra, providercore.LegacyOpenAIResponsesModeExtraKey)
	require.NotContains(t, repo.lastBulkUpdate.Extra, "openai_responses_supported")
}

func TestAdminServiceBulkUpdateProvidersNormalizesOpenAIWorkloadAndTextRoute(t *testing.T) {
	repo := &providerRepoStubForBulkUpdate{getByIDsProviders: []*providercore.Record{
		{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey},
	}}
	svc := newProviderEditorForTest(repo)

	result, err := svc.BulkUpdateProviders(context.Background(), &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{1},
		Credentials: map[string]any{
			providercore.OpenAIWorkloadCapabilitiesCredentialKey: []any{"text_generation", "embeddings"},
		},
		Extra: map[string]any{providercore.ExtraKeyTextRouteMode: "force_responses"},
	})

	require.NoError(t, err)
	require.Equal(t, 1, result.Success)
	require.Equal(t, []string{"text_generation", "embeddings"}, repo.lastBulkUpdate.Credentials[providercore.OpenAIWorkloadCapabilitiesCredentialKey])
	require.Equal(t, "force_responses", repo.lastBulkUpdate.Extra[providercore.ExtraKeyTextRouteMode])
}

func TestAdminServiceBulkUpdateProvidersNormalizesOpenAIContinuationCapability(t *testing.T) {
	repo := &providerRepoStubForBulkUpdate{getByIDsProviders: []*providercore.Record{
		{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey},
	}}
	svc := newProviderEditorForTest(repo)

	result, err := svc.BulkUpdateProviders(context.Background(), &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{1},
		Extra: map[string]any{
			providercore.ExtraKeyResponsesContinuationSupported: true,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, result.Success)
	require.Equal(t, true, repo.lastBulkUpdate.Extra[providercore.ExtraKeyResponsesContinuationSupported])
}

func TestAdminServiceBulkUpdateProvidersRejectsContinuationForNonOpenAIAPIKey(t *testing.T) {
	repo := &providerRepoStubForBulkUpdate{getByIDsProviders: []*providercore.Record{
		{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth},
	}}
	svc := newProviderEditorForTest(repo)

	result, err := svc.BulkUpdateProviders(context.Background(), &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{1},
		Extra: map[string]any{
			providercore.ExtraKeyResponsesContinuationSupported: true,
		},
	})

	require.Nil(t, result)
	var appErr *apperror.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, "OPENAI_CONFIGURATION_TARGET_INVALID", appErr.Reason)
	require.Empty(t, repo.bulkUpdateIDs)
}

func TestAdminServiceBulkUpdateProvidersRejectsInvalidOpenAITargetBeforeWrite(t *testing.T) {
	repo := &providerRepoStubForBulkUpdate{getByIDsProviders: []*providercore.Record{
		{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth},
	}}
	svc := newProviderEditorForTest(repo)

	result, err := svc.BulkUpdateProviders(context.Background(), &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{1},
		Extra:       map[string]any{providercore.ExtraKeyTextRouteMode: "force_responses"},
	})

	require.Nil(t, result)
	var appErr *apperror.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, "OPENAI_CONFIGURATION_TARGET_INVALID", appErr.Reason)
	require.Empty(t, repo.bulkUpdateIDs)
}

func TestAdminServiceBulkUpdateProvidersRejectsForcedTextRouteWithoutWorkload(t *testing.T) {
	repo := &providerRepoStubForBulkUpdate{getByIDsProviders: []*providercore.Record{
		{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey},
	}}
	svc := newProviderEditorForTest(repo)

	result, err := svc.BulkUpdateProviders(context.Background(), &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{1},
		Credentials: map[string]any{
			providercore.OpenAIWorkloadCapabilitiesCredentialKey: []any{"embeddings"},
		},
		Extra: map[string]any{providercore.ExtraKeyTextRouteMode: "force_responses"},
	})

	require.Nil(t, result)
	var appErr *apperror.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, "OPENAI_TEXT_ROUTE_MODE_INVALID", appErr.Reason)
	require.Empty(t, repo.bulkUpdateIDs)
}

func TestAdminServiceBulkUpdateProvidersRejectsInvalidCNProviderCombination(t *testing.T) {
	repo := &providerRepoStubForBulkUpdate{
		getByIDsProviders: []*providercore.Record{{
			ID:       9,
			Platform: capability.PlatformDeepseek,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key":       "sk-test",
				"provider_mode": providercore.ProviderModePayG,
			},
		}},
	}
	svc := newProviderEditorForTest(repo)
	_, err := svc.BulkUpdateProviders(context.Background(), &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{9},
		Credentials: map[string]any{"provider_mode": providercore.ProviderModeCoding},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "DeepSeek does not support coding")
	require.Empty(t, repo.bulkUpdateIDs)
}

func (s *providerRepoStubForBulkUpdate) BindGroups(_ context.Context, providerID int64, _ []int64) error {
	s.bindGroupsCalls = append(s.bindGroupsCalls, providerID)
	if err, ok := s.bindGroupErrByID[providerID]; ok {
		return err
	}
	return nil
}

func (s *providerRepoStubForBulkUpdate) GetByIDs(_ context.Context, ids []int64) ([]*providercore.Record, error) {
	s.getByIDsCalled = true
	s.getByIDsIDs = append([]int64{}, ids...)
	if s.getByIDsErr != nil {
		return nil, s.getByIDsErr
	}
	out := make([]*providercore.Record, len(s.getByIDsProviders))
	for i, v := range s.getByIDsProviders {
		out[i] = providercore.CloneRecord(v)
	}
	return out, nil
}

func (s *providerRepoStubForBulkUpdate) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	s.getByIDCalled = append(s.getByIDCalled, id)
	if err, ok := s.getByIDErrByID[id]; ok {
		return nil, err
	}
	if provider, ok := s.getByIDProviders[id]; ok {
		return providercore.CloneRecord(provider), nil
	}
	return nil, errors.New("provider not found")
}

func (s *providerRepoStubForBulkUpdate) ListByGroup(_ context.Context, groupID int64) ([]providercore.Record, error) {
	if err, ok := s.listByGroupErr[groupID]; ok {
		return nil, err
	}
	if rows, ok := s.listByGroupData[groupID]; ok {
		return rows, nil
	}
	return nil, nil
}

func (s *providerRepoStubForBulkUpdate) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]providercore.Record, error) {
	return nil, nil
}

func (s *providerRepoStubForBulkUpdate) ListWithFilters(_ context.Context, params pagination.PaginationParams, platform, providerType, status, search string, groupID int64, privacyMode string) ([]providercore.Record, *pagination.PaginationResult, error) {
	s.listCalled = true
	s.lastListParams = params
	s.lastListFilters.platform = platform
	s.lastListFilters.providerType = providerType
	s.lastListFilters.status = status
	s.lastListFilters.search = search
	s.lastListFilters.groupID = groupID
	s.lastListFilters.privacyMode = privacyMode
	if s.listErr != nil {
		return nil, nil, s.listErr
	}
	if s.listResult != nil {
		return s.listData, s.listResult, nil
	}
	return s.listData, &pagination.PaginationResult{Total: int64(len(s.listData))}, nil
}

// TestAdminService_BulkUpdateProviders_AllSuccessIDs checks the success and failure ID lists.
func TestAdminService_BulkUpdateProviders_AllSuccessIDs(t *testing.T) {
	repo := &providerRepoStubForBulkUpdate{}
	svc := newProviderEditorForTest(repo)

	schedulable := true
	input := &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{1, 2, 3},
		Schedulable: &schedulable,
	}

	result, err := svc.BulkUpdateProviders(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, 3, result.Success)
	require.Equal(t, 0, result.Failed)
	require.ElementsMatch(t, []int64{1, 2, 3}, result.SuccessIDs)
	require.Empty(t, result.FailedIDs)
	require.Len(t, result.Results, 3)
}

// TestAdminService_BulkUpdateProviders_PartialFailureIDs checks ID lists after a partial failure.
func TestAdminService_BulkUpdateProviders_PartialFailureIDs(t *testing.T) {
	repo := &providerRepoStubForBulkUpdate{
		bindGroupErrByID: map[int64]error{
			2: errors.New("bind failed"),
		},
	}
	svc := newProviderEditorForTest(repo, shadowGroupsFixture{&bulkGroupsFixture{group: &routing.Group{ID: 10, Name: "g10"}}})

	groupIDs := []int64{10}
	schedulable := false
	input := &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{1, 2, 3},
		GroupIDs:    &groupIDs,
		Schedulable: &schedulable,
	}

	result, err := svc.BulkUpdateProviders(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, 2, result.Success)
	require.Equal(t, 1, result.Failed)
	require.ElementsMatch(t, []int64{1, 3}, result.SuccessIDs)
	require.ElementsMatch(t, []int64{2}, result.FailedIDs)
	require.Len(t, result.Results, 3)
}

func TestAdminService_BulkUpdateProviders_NilGroupRepoReturnsError(t *testing.T) {
	repo := &providerRepoStubForBulkUpdate{}
	svc := newProviderEditorForTest(repo)

	groupIDs := []int64{10}
	input := &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{1},
		GroupIDs:    &groupIDs,
	}

	result, err := svc.BulkUpdateProviders(context.Background(), input)
	require.Nil(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "group repository not configured")
}

func TestAdminServiceBulkUpdateProvidersRejectsGeminiThirdPartyWithoutCustomBaseURL(t *testing.T) {
	repo := &providerRepoStubForBulkUpdate{
		getByIDsProviders: []*providercore.Record{
			{
				ID:       1,
				Platform: capability.PlatformGemini,
				Type:     capability.ProviderTypeAPIKey,
				Credentials: map[string]any{
					"base_url": "https://generativelanguage.googleapis.com",
				},
			},
		},
	}
	svc := newProviderEditorForTest(repo)

	result, err := svc.BulkUpdateProviders(context.Background(), &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{1},
		Credentials: map[string]any{
			providercore.GeminiProviderTypeCredentialKey: providercore.GeminiProviderTypeThirdParty,
		},
	})

	require.Nil(t, result)
	require.ErrorContains(t, err, "GEMINI_THIRD_PARTY_BASE_URL_REQUIRED")
	require.Empty(t, repo.bulkUpdateIDs)
}

// TestAdminServiceBulkUpdateAllowsMixedProviderPlatforms checks membership in a group containing another platform.
func TestAdminServiceBulkUpdateAllowsMixedProviderPlatforms(t *testing.T) {
	repo := &providerRepoStubForBulkUpdate{
		getByIDsProviders: []*providercore.Record{
			{ID: 1, Platform: capability.PlatformAntigravity},
		},
		// Group 10 already contains an Anthropic provider.
		listByGroupData: map[int64][]providercore.Record{
			10: {{ID: 99, Platform: capability.PlatformAnthropic}},
		},
	}
	svc := newProviderEditorForTest(repo, shadowGroupsFixture{&bulkGroupsFixture{group: &routing.Group{ID: 10, Name: "target-group"}}})

	groupIDs := []int64{10}
	input := &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{1},
		GroupIDs:    &groupIDs,
	}

	result, err := svc.BulkUpdateProviders(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.Success)
	require.Len(t, repo.bindGroupsCalls, 1)
}

func TestAdminServiceBulkUpdateProviders_ResolvesIDsFromFilters(t *testing.T) {
	repo := &providerRepoStubForBulkUpdate{
		listData: []providercore.Record{
			{ID: 7},
			{ID: 11},
		},
		listResult: &pagination.PaginationResult{Total: 2},
	}
	svc := newProviderEditorForTest(repo)

	schedulable := true
	input := &providercore.BulkUpdateProvidersInput{
		Schedulable: &schedulable,
	}

	filtersField := reflect.ValueOf(input).Elem().FieldByName("Filters")
	require.True(t, filtersField.IsValid(), "BulkUpdateProvidersInput should expose Filters for filter-target bulk update")
	require.Equal(t, reflect.Pointer, filtersField.Kind(), "BulkUpdateProvidersInput.Filters should be a pointer field")

	filtersValue := reflect.New(filtersField.Type().Elem())
	filtersValue.Elem().FieldByName("Platform").SetString(capability.PlatformOpenAI)
	filtersValue.Elem().FieldByName("Type").SetString(capability.ProviderTypeOAuth)
	filtersValue.Elem().FieldByName("Status").SetString(billingcore.StatusActive)
	filtersValue.Elem().FieldByName("Group").SetString("12")
	filtersValue.Elem().FieldByName("PrivacyMode").SetString(openai.PrivacyModeCFBlocked)
	filtersValue.Elem().FieldByName("Search").SetString("bulk-target")
	filtersField.Set(filtersValue)

	result, err := svc.BulkUpdateProviders(context.Background(), input)
	require.NoError(t, err)
	require.True(t, repo.listCalled, "expected filter-target bulk update to resolve matching IDs via provider list filters")
	require.Equal(t, capability.PlatformOpenAI, repo.lastListFilters.platform)
	require.Equal(t, capability.ProviderTypeOAuth, repo.lastListFilters.providerType)
	require.Equal(t, billingcore.StatusActive, repo.lastListFilters.status)
	require.Equal(t, "bulk-target", repo.lastListFilters.search)
	require.Equal(t, int64(12), repo.lastListFilters.groupID)
	require.Equal(t, openai.PrivacyModeCFBlocked, repo.lastListFilters.privacyMode)
	require.Equal(t, []int64{7, 11}, repo.bulkUpdateIDs)
	require.Equal(t, 2, result.Success)
	require.Equal(t, 0, result.Failed)
	require.Equal(t, []int64{7, 11}, result.SuccessIDs)
}

func (s *bulkGroupsFixture) GetByID(context.Context, int64) (*routing.Group, error) {
	return routing.CloneGroup(s.group), nil
}

// Validate the shared connection-mode field before any provider write.
func TestBulkResponsesWSConnectionModeValidation(t *testing.T) {
	for _, value := range []any{"typo", "", nil, false, 1} {
		repo := &providerRepoStubForBulkUpdate{}
		svc := newProviderEditorForTest(repo)
		_, err := svc.BulkUpdateProviders(context.Background(), &providercore.BulkUpdateProvidersInput{
			ProviderIDs: []int64{1, 2}, Extra: map[string]any{providercore.ResponsesWSConnectionModeKey: value},
		})
		require.Error(t, err, "mode=%#v", value)
		require.Empty(t, repo.bulkUpdateIDs)
	}
	for _, value := range []string{providercore.ResponsesWSPooled, providercore.ResponsesWSPerSession} {
		repo := &providerRepoStubForBulkUpdate{}
		svc := newProviderEditorForTest(repo)
		result, err := svc.BulkUpdateProviders(context.Background(), &providercore.BulkUpdateProvidersInput{
			ProviderIDs: []int64{1, 2}, Extra: map[string]any{providercore.ResponsesWSConnectionModeKey: value},
		})
		require.NoError(t, err)
		require.Equal(t, 2, result.Success)
		require.Equal(t, value, repo.lastBulkUpdate.Extra[providercore.ResponsesWSConnectionModeKey])
		require.Empty(t, repo.lastBulkUpdate.Credentials)
		require.Empty(t, repo.lastBulkUpdate.ProtocolUpdates)
	}
}

// TestCNProviderBulkProtocolValidationBeforeWrite rejects invalid platform/protocol combinations before writing.
func TestCNProviderBulkProtocolValidationBeforeWrite(t *testing.T) {
	t.Parallel()
	repo := &providerServiceTestRepo{providers: map[int64]*providercore.Record{
		1: {ID: 1, Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey, Credentials: cnProviderTestCredentials(capability.PlatformKimi, providercore.ProviderModePayG, providercore.APIProtocolAdaptive)},
		2: {ID: 2, Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey, Credentials: cnProviderTestCredentials(capability.PlatformZhipu, providercore.ProviderModePayG, providercore.APIProtocolAdaptive)},
	}}
	svc := newProviderEditorForTest(repo)
	_, err := svc.BulkUpdateProviders(context.Background(), &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{1, 2}, Credentials: map[string]any{"api_protocol": providercore.APIProtocolResponses},
	})
	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
	require.Equal(t, "CN_PROVIDER_PROTOCOL_INVALID", apperror.Reason(err))
	require.Empty(t, repo.bulkUpdates)
	for _, provider := range repo.providers {
		require.Equal(t, providercore.APIProtocolAdaptive, provider.Credentials["api_protocol"])
	}
}

func (r *updateProviderCredsRepoStub) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	return providercore.CloneRecord(r.provider), nil
}

func (r *updateProviderCredsRepoStub) Update(ctx context.Context, provider *providercore.Record) error {
	r.updateCalls++
	r.provider = providercore.CloneRecord(provider)
	return nil
}

func TestUpdateProvider_PreservesSensitiveCredsWhenIncomingOmits(t *testing.T) {
	providerID := int64(202)
	repo := &updateProviderCredsRepoStub{
		provider: &providercore.Record{
			ID:       providerID,
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeOAuth,
			Status:   billingcore.StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-existing",
				"access_token":  "at-existing",
				"id_token":      "id-existing",
				"base_url":      "https://old.example.com",
			},
		},
	}
	svc := newProviderEditorForTest(repo)

	// Changing base_url through the UI omits redacted token fields.
	updated, err := svc.UpdateProvider(context.Background(), providerID, &providercore.UpdateProviderInput{
		Credentials: map[string]any{
			"base_url": "https://new.example.com",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, repo.updateCalls)

	// Omitted secrets retain their stored values.
	require.Equal(t, "rt-existing", repo.provider.Credentials["refresh_token"])
	require.Equal(t, "at-existing", repo.provider.Credentials["access_token"])
	require.Equal(t, "id-existing", repo.provider.Credentials["id_token"])
	// Supplied non-secret fields replace their stored values.
	require.Equal(t, "https://new.example.com", repo.provider.Credentials["base_url"])
}

func TestUpdateProvider_ExplicitNewTokenOverwrites(t *testing.T) {
	providerID := int64(203)
	repo := &updateProviderCredsRepoStub{
		provider: &providercore.Record{
			ID:       providerID,
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeOAuth,
			Status:   billingcore.StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-old",
				"api_key":       "sk-old",
			},
		},
	}
	svc := newProviderEditorForTest(repo)

	updated, err := svc.UpdateProvider(context.Background(), providerID, &providercore.UpdateProviderInput{
		Credentials: map[string]any{
			"refresh_token": "rt-new",
			// An omitted api_key retains its stored value.
		},
	})
	require.NoError(t, err)
	require.NotNil(t, updated)

	require.Equal(t, "rt-new", repo.provider.Credentials["refresh_token"])
	require.Equal(t, "sk-old", repo.provider.Credentials["api_key"])
}

func TestUpdateProvider_EmptyCredentialsSkipsUpdate(t *testing.T) {
	providerID := int64(204)
	repo := &updateProviderCredsRepoStub{
		provider: &providercore.Record{
			ID:       providerID,
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeOAuth,
			Status:   billingcore.StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-existing",
			},
		},
	}
	svc := newProviderEditorForTest(repo)

	_, err := svc.UpdateProvider(context.Background(), providerID, &providercore.UpdateProviderInput{
		Credentials: map[string]any{}, // An empty map skips credential validation.
		Name:        "renamed",
	})
	require.NoError(t, err)

	require.Equal(t, "rt-existing", repo.provider.Credentials["refresh_token"], "空 credentials 不应触碰已有 token")
	require.Equal(t, "renamed", repo.provider.Name)
}

func (r *deprecatedProviderExtraRepoStub) GetByID(_ context.Context, _ int64) (*providercore.Record, error) {
	return providercore.CloneRecord(r.provider), nil
}

func (r *deprecatedProviderExtraRepoStub) Update(_ context.Context, provider *providercore.Record) error {
	r.provider = providercore.CloneRecord(provider)
	return nil
}

func (r *deprecatedProviderExtraRepoStub) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.updateExtraCalls++
	r.lastExtraUpdates = maps.Clone(updates)
	return nil
}

func (r *deprecatedProviderExtraRepoStub) BulkUpdate(_ context.Context, _ []int64, updates providercore.ProviderBulkUpdate) (int64, error) {
	r.bulkUpdateCalls++
	r.lastBulkExtraUpdate = maps.Clone(updates.Extra)
	return 1, nil
}

func TestAdminServiceUpdateProviderDiscardsDeprecatedLongContextBillingExtra(t *testing.T) {
	repo := &deprecatedProviderExtraRepoStub{provider: &providercore.Record{
		ID:       1,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Extra: map[string]any{
			"openai_long_context_billing_enabled": false,
			"old":                                 true,
			"quota_used":                          float64(5),
		},
	}}
	svc := newProviderEditorForTest(repo)

	provider, err := svc.UpdateProvider(context.Background(), 1, &providercore.UpdateProviderInput{Extra: map[string]any{
		"openai_long_context_billing_enabled": []bool{true},
		"privacy_mode":                        "blocked",
		"quota_limit":                         float64(25),
	}})

	require.NoError(t, err)
	require.NotContains(t, provider.Extra, "openai_long_context_billing_enabled")
	require.Equal(t, "blocked", provider.Extra["privacy_mode"])
	require.Equal(t, float64(25), provider.Extra["quota_limit"])
	require.Equal(t, float64(5), provider.Extra["quota_used"])
	require.NotContains(t, provider.Extra, "old")
}

func TestAdminServiceUpdateProviderDeprecatedOnlyPreservesExistingExtra(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "布尔旧值", value: false},
		{name: "非法类型", value: map[string]any{"malformed": true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &deprecatedProviderExtraRepoStub{provider: &providercore.Record{
				ID:       1,
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeAPIKey,
				Extra: map[string]any{
					"openai_long_context_billing_enabled": true,
					"privacy_mode":                        "limited",
					"quota_limit":                         float64(100),
					"quota_daily_limit":                   float64(20),
					"custom":                              "preserved",
				},
			}}
			svc := newProviderEditorForTest(repo)

			provider, err := svc.UpdateProvider(context.Background(), 1, &providercore.UpdateProviderInput{Extra: map[string]any{
				"openai_long_context_billing_enabled": tt.value,
			}})

			require.NoError(t, err)
			require.NotContains(t, provider.Extra, "openai_long_context_billing_enabled")
			require.Equal(t, "limited", provider.Extra["privacy_mode"])
			require.Equal(t, float64(100), provider.Extra["quota_limit"])
			require.Equal(t, float64(20), provider.Extra["quota_daily_limit"])
			require.Equal(t, "preserved", provider.Extra["custom"])
		})
	}
}

func TestAdminServiceUpdateProviderExplicitEmptyExtraStillClearsConfig(t *testing.T) {
	repo := &deprecatedProviderExtraRepoStub{provider: &providercore.Record{
		ID:       1,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Extra: map[string]any{
			"privacy_mode": "limited",
			"quota_limit":  float64(100),
			"quota_used":   float64(7),
		},
	}}
	svc := newProviderEditorForTest(repo)

	provider, err := svc.UpdateProvider(context.Background(), 1, &providercore.UpdateProviderInput{Extra: map[string]any{}})

	require.NoError(t, err)
	require.NotNil(t, provider.Extra)
	require.NotContains(t, provider.Extra, "privacy_mode")
	require.NotContains(t, provider.Extra, "quota_limit")
	require.Equal(t, float64(7), provider.Extra["quota_used"])
}

func TestAdminServiceUpdateProviderExtraIgnoresDeprecatedLongContextBillingExtra(t *testing.T) {
	repo := &deprecatedProviderExtraRepoStub{}
	svc := newProviderEditorForTest(repo)

	err := svc.UpdateProviderExtra(context.Background(), 1, map[string]any{
		"openai_long_context_billing_enabled": 1,
	})

	require.NoError(t, err)
	require.Zero(t, repo.updateExtraCalls)

	err = svc.UpdateProviderExtra(context.Background(), 1, map[string]any{
		"openai_long_context_billing_enabled": "true",
		"preserved":                           true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateExtraCalls)
	require.Equal(t, map[string]any{"preserved": true}, repo.lastExtraUpdates)
}

func TestAdminServiceBulkUpdateProvidersIgnoresDeprecatedLongContextBillingExtra(t *testing.T) {
	repo := &deprecatedProviderExtraRepoStub{}
	svc := newProviderEditorForTest(repo)

	result, err := svc.BulkUpdateProviders(context.Background(), &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{1},
		Extra: map[string]any{
			"openai_long_context_billing_enabled": map[string]any{"invalid": true},
			"preserved":                           true,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, result.Success)
	require.Equal(t, 1, repo.bulkUpdateCalls)
	require.Equal(t, map[string]any{"preserved": true}, repo.lastBulkExtraUpdate)
}

func TestUpdateProviderDiscardsDeprecatedBillingProbeExtra(t *testing.T) {
	providerID := int64(110)
	repo := &providerServiceTestRepo{providers: map[int64]*providercore.Record{
		providerID: {
			ID:       providerID,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Status:   billingcore.StatusActive,
			Extra: map[string]any{
				"upstream_billing_probe_enabled": true,
				"upstream_billing_probe":         map[string]any{"status": "ok"},
			},
		},
	}}

	updated, err := newProviderEditorForTest(repo).UpdateProvider(context.Background(), providerID, &providercore.UpdateProviderInput{
		Extra: map[string]any{
			"upstream_billing_probe_enabled": false,
			"upstream_billing_probe":         map[string]any{"status": "forged"},
			"custom":                         "value",
		},
	})

	require.NoError(t, err)
	require.NotContains(t, updated.Extra, "upstream_billing_probe_enabled")
	require.NotContains(t, updated.Extra, "upstream_billing_probe")
	require.Equal(t, "value", updated.Extra["custom"])
}

func TestBulkUpdateProvidersDiscardsDeprecatedBillingProbeExtra(t *testing.T) {
	repo := &providerServiceTestRepo{}
	result, err := newProviderEditorForTest(repo).BulkUpdateProviders(context.Background(), &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{1},
		Extra: map[string]any{
			"upstream_billing_probe_enabled": true,
			"upstream_billing_probe":         map[string]any{"status": "ok"},
			"custom":                         "value",
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, result.Success)
	require.Len(t, repo.bulkUpdates, 1)
	require.NotContains(t, repo.bulkUpdates[0].Extra, "upstream_billing_probe_enabled")
	require.NotContains(t, repo.bulkUpdates[0].Extra, "upstream_billing_probe")
	require.Equal(t, "value", repo.bulkUpdates[0].Extra["custom"])
}

func TestUpdateProviderPreservesGrokBillingSnapshotForUnrelatedEdit(t *testing.T) {
	providerID := int64(112)
	billing := &grok.BillingSummary{
		StatusCode:       http.StatusForbidden,
		WeeklyStatusCode: http.StatusForbidden,
	}
	repo := &providerServiceTestRepo{providers: map[int64]*providercore.Record{
		providerID: {
			ID:       providerID,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeOAuth,
			Status:   billingcore.StatusActive,
			Extra:    map[string]any{providercore.GrokUsageBillingExtraKey: billing},
		},
	}}

	updated, err := newProviderEditorForTest(repo).UpdateProvider(context.Background(), providerID, &providercore.UpdateProviderInput{
		Extra: map[string]any{"custom": "value"},
	})

	require.NoError(t, err)
	require.Equal(t, billing, updated.Extra[providercore.GrokUsageBillingExtraKey])
	require.Equal(t, "value", updated.Extra["custom"])
	eligible, reason := providercore.GrokMediaGenerationEligibility(updated, provideradapter.GrokTierRules())
	require.False(t, eligible)
	require.Equal(t, "billing_forbidden", reason)
}

func TestAdminUpdatePreservesOllamaManagedExtra(t *testing.T) {
	provider := ollamaUsageProvider(61)
	provider.Extra = map[string]any{
		providercore.OllamaCloudUsageSessionExtraKey:     "local-ciphertext",
		providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
		providercore.OllamaCloudUsageSnapshotExtraKey:    map[string]any{"status": providercore.OllamaCloudUsageStatusOK},
	}
	repo := &ollamaManagedExtraUpdateRepo{provider: provider}
	svc := newProviderEditorForTest(repo)
	requestedExtra := map[string]any{
		"note": "preserved",
		providercore.OllamaCloudUsageSessionExtraKey:     "forged-ciphertext",
		providercore.OllamaCloudUsageAutoRefreshExtraKey: nil,
		providercore.OllamaCloudUsageSnapshotExtraKey:    nil,
	}

	_, err := svc.UpdateProvider(context.Background(), provider.ID, &providercore.UpdateProviderInput{Extra: requestedExtra})
	require.NoError(t, err)
	require.Equal(t, "preserved", repo.updated.Extra["note"])
	require.Equal(t, "local-ciphertext", repo.updated.Extra[providercore.OllamaCloudUsageSessionExtraKey])
	require.Equal(t, true, repo.updated.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey])
	require.Equal(t, provider.Extra[providercore.OllamaCloudUsageSnapshotExtraKey], repo.updated.Extra[providercore.OllamaCloudUsageSnapshotExtraKey])

	require.Contains(t, requestedExtra, providercore.OllamaCloudUsageSessionExtraKey)
}

func (r *ollamaManagedExtraUpdateRepo) GetByID(_ context.Context, _ int64) (*providercore.Record, error) {
	return providercore.CloneRecord(r.provider), nil
}

func (r *ollamaManagedExtraUpdateRepo) Update(_ context.Context, provider *providercore.Record) error {
	r.updated = providercore.CloneRecord(provider)
	return nil
}

func TestUpdateProviderLegacyPatchOverridesEchoedNewShape(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	provider := &providercore.Record{
		Name:     "openai-provider",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "secret",
			providercore.OpenAIWorkloadCapabilitiesCredentialKey: []any{"embeddings"},
		},
		Extra: map[string]any{
			providercore.ExtraKeyTextRouteMode:                  "preserve_client_protocol",
			"openai_responses_probe_status":                     "supported",
			providercore.ExtraKeyResponsesContinuationSupported: true,
		},
	}
	require.NoError(t, repo.Create(ctx, provider))
	svc := newProviderEditorForTest(repo)

	updated, err := svc.UpdateProvider(ctx, provider.ID, &providercore.UpdateProviderInput{
		Credentials: map[string]any{
			providercore.LegacyOpenAICapabilitiesCredentialKey: []any{"chat_completions"},
		},
		Extra: map[string]any{
			providercore.ExtraKeyTextRouteMode:             "force_responses",
			providercore.LegacyOpenAIResponsesModeExtraKey: "force_chat_completions",
			"openai_responses_probe_status":                "supported",
			"openai_responses_supported":                   false,
		},
	})

	require.NoError(t, err)
	require.Contains(t, updated.UpstreamProtocols(), protocol.ProtocolOpenAIChatCompletions)
	require.NotContains(t, updated.UpstreamProtocols(), protocol.ProtocolOpenAIResponses)
	require.NotContains(t, updated.Credentials, providercore.LegacyOpenAICapabilitiesCredentialKey)
	require.NotContains(t, updated.Extra, providercore.ExtraKeyTextRouteMode)
	require.NotContains(t, updated.Extra, "openai_responses_probe_status")
	require.Equal(t, true, updated.Extra[providercore.ExtraKeyResponsesContinuationSupported])
	require.NotContains(t, updated.Extra, providercore.LegacyOpenAIResponsesModeExtraKey)
	require.NotContains(t, updated.Extra, "openai_responses_supported")
}

// TestUpdateProviderDeprecatedProbeOnlyPreservesConfiguration protects saved routing and both compaction settings from deprecated-only updates.
func TestUpdateProviderDeprecatedProbeOnlyPreservesConfiguration(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	provider := &providercore.Record{
		Name: "manual", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "test"},
		Extra:       map[string]any{"openai_text_route_mode": "force_responses", "openai_compact_mode": "force_off", providercore.OpenAINativeCompactionV2ModeExtraKey: "force_on", "keep": true},
	}
	require.NoError(t, repo.Create(ctx, provider))
	svc := newProviderEditorForTest(repo)
	updated, err := svc.UpdateProvider(ctx, provider.ID, &providercore.UpdateProviderInput{Extra: map[string]any{"openai_responses_supported": false}})
	require.NoError(t, err)
	require.Contains(t, updated.UpstreamProtocols(), protocol.ProtocolOpenAIResponses)
	require.NotContains(t, updated.UpstreamProtocols(), protocol.ProtocolOpenAIChatCompletions)
	require.Equal(t, "force_off", updated.Extra["openai_compact_mode"])
	require.Equal(t, "force_on", updated.Extra[providercore.OpenAINativeCompactionV2ModeExtraKey])
	require.Equal(t, true, updated.Extra["keep"])
	require.NotContains(t, updated.Extra, "openai_responses_supported")
}

func (r *updateProviderOveragesRepoStub) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	value := providercore.CloneRecord(r.provider)
	if value != nil {
		value.LoadLocation = time.LoadLocation
	}
	return value, nil
}

func (r *updateProviderOveragesRepoStub) Update(ctx context.Context, provider *providercore.Record) error {
	r.updateCalls++
	r.provider = providercore.CloneRecord(provider)
	return nil
}

func TestUpdateProvider_DisableOveragesClearsAICreditsKey(t *testing.T) {
	providerID := int64(101)
	repo := &updateProviderOveragesRepoStub{
		provider: &providercore.Record{
			ID:       providerID,
			Platform: capability.PlatformAntigravity,
			Type:     capability.ProviderTypeOAuth,
			Status:   billingcore.StatusActive,
			Extra: map[string]any{
				"allow_overages":   true,
				"mixed_scheduling": true,
				"model_rate_limits": map[string]any{
					"claude-sonnet-4-5": map[string]any{
						"rate_limited_at":     "2026-03-15T00:00:00Z",
						"rate_limit_reset_at": "2099-03-15T00:00:00Z",
					},
					providercore.CreditsExhaustedKey: map[string]any{
						"rate_limited_at":     "2026-03-15T00:00:00Z",
						"rate_limit_reset_at": time.Now().Add(5 * time.Hour).UTC().Format(time.RFC3339),
					},
				},
			},
		},
	}

	svc := newProviderEditorForTest(repo)
	updated, err := svc.UpdateProvider(context.Background(), providerID, &providercore.UpdateProviderInput{
		Extra: map[string]any{
			"mixed_scheduling": true,
			"model_rate_limits": map[string]any{
				"claude-sonnet-4-5": map[string]any{
					"rate_limited_at":     "2026-03-15T00:00:00Z",
					"rate_limit_reset_at": "2099-03-15T00:00:00Z",
				},
				providercore.CreditsExhaustedKey: map[string]any{
					"rate_limited_at":     "2026-03-15T00:00:00Z",
					"rate_limit_reset_at": time.Now().Add(5 * time.Hour).UTC().Format(time.RFC3339),
				},
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, repo.updateCalls)
	require.False(t, updated.IsOveragesEnabled())

	// Disabling overages removes the AI Credits rate limit.
	rawLimits, ok := repo.provider.Extra["model_rate_limits"].(map[string]any)
	if ok {
		_, exists := rawLimits[providercore.CreditsExhaustedKey]
		require.False(t, exists, "关闭 overages 时应清除 AICredits 限流 key")
	}
	// Ordinary model rate limits remain.
	require.True(t, ok)
	_, exists := rawLimits["claude-sonnet-4-5"]
	require.True(t, exists, "普通模型限流应保留")
}

func TestUpdateProvider_EnableOveragesClearsModelRateLimitsBeforePersist(t *testing.T) {
	providerID := int64(102)
	repo := &updateProviderOveragesRepoStub{
		provider: &providercore.Record{
			ID:       providerID,
			Platform: capability.PlatformAntigravity,
			Type:     capability.ProviderTypeOAuth,
			Status:   billingcore.StatusActive,
			Extra: map[string]any{
				"mixed_scheduling": true,
				"model_rate_limits": map[string]any{
					"claude-sonnet-4-5": map[string]any{
						"rate_limited_at":     "2026-03-15T00:00:00Z",
						"rate_limit_reset_at": "2099-03-15T00:00:00Z",
					},
				},
			},
		},
	}

	svc := newProviderEditorForTest(repo)
	updated, err := svc.UpdateProvider(context.Background(), providerID, &providercore.UpdateProviderInput{
		Extra: map[string]any{
			"mixed_scheduling": true,
			"allow_overages":   true,
		},
	})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, repo.updateCalls)
	require.True(t, updated.IsOveragesEnabled())

	_, exists := repo.provider.Extra["model_rate_limits"]
	require.False(t, exists, "开启 overages 时应在持久化前清掉旧模型限流")
}

func TestUpdateProvider_EmptyExtraPayloadCanClearQuotaLimits(t *testing.T) {
	providerID := int64(103)
	repo := &updateProviderOveragesRepoStub{
		provider: &providercore.Record{
			ID:       providerID,
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeAPIKey,
			Status:   billingcore.StatusActive,
			Extra: map[string]any{
				"quota_limit":        100.0,
				"quota_daily_limit":  10.0,
				"quota_weekly_limit": 40.0,
			},
		},
	}

	svc := newProviderEditorForTest(repo)
	updated, err := svc.UpdateProvider(context.Background(), providerID, &providercore.UpdateProviderInput{
		// An empty object clears configurable Extra keys, such as quota limits.
		Extra: map[string]any{},
	})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, repo.updateCalls)
	require.NotNil(t, repo.provider.Extra)
	require.NotContains(t, repo.provider.Extra, "quota_limit")
	require.NotContains(t, repo.provider.Extra, "quota_daily_limit")
	require.NotContains(t, repo.provider.Extra, "quota_weekly_limit")
	require.Len(t, repo.provider.Extra, 0)
}

func TestUpdateProvider_FixedWeeklyResetClearsLegacyRollingUsage(t *testing.T) {
	now := time.Now().UTC()
	daysSinceMonday := (int(now.Weekday()) + 6) % 7
	currentWeekStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -daysSinceMonday)
	legacyRollingStart := currentWeekStart.Add(-24 * time.Hour)
	providerID := int64(104)
	repo := &updateProviderOveragesRepoStub{
		provider: &providercore.Record{
			ID:       providerID,
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeAPIKey,
			Status:   billingcore.StatusActive,
			Extra: map[string]any{
				"quota_weekly_limit": 40.0,
				"quota_weekly_used":  12.5,
				"quota_weekly_start": legacyRollingStart.Format(time.RFC3339),
			},
		},
	}

	svc := newProviderEditorForTest(repo)
	updated, err := svc.UpdateProvider(context.Background(), providerID, &providercore.UpdateProviderInput{
		Extra: map[string]any{
			"quota_weekly_limit":      40.0,
			"quota_weekly_reset_mode": "fixed",
			"quota_weekly_reset_day":  float64(1),
			"quota_weekly_reset_hour": float64(0),
			"quota_reset_timezone":    "UTC",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, repo.updateCalls)
	require.InDelta(t, 0.0, updated.GetQuotaWeeklyUsed(), 1e-9)
	require.Equal(t, currentWeekStart.Format(time.RFC3339), updated.Extra["quota_weekly_start"])
	require.False(t, updated.IsWeeklyQuotaPeriodExpired())
}

func TestUpdateProvider_ShadowAllowsModelMappingAndGroupUpdate(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	groupRepo := &sparkShadowValidatingGroupRepoStub{existing: map[int64]bool{7: true}}
	svc := newProviderEditorForTest(repo, shadowGroupsFixture{groupRepo})
	parentID := int64(1)
	parent := &providercore.Record{
		ID:       parentID,
		Name:     "p",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   billingcore.StatusActive,
		Credentials: map[string]any{
			"access_token":       "parent-token",
			"chatgpt_account_id": "org-parent",
		},
	}
	require.NoError(t, repo.Create(ctx, parent))
	shadow := &providercore.Record{
		Name:             "s",
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		Status:           billingcore.StatusActive,
		ParentProviderID: &parentID,
		QuotaDimension:   providercore.QuotaDimensionSpark,
		Credentials:      map[string]any{},
	}
	require.NoError(t, repo.Create(ctx, shadow))

	groupIDs := []int64{7}
	updated, err := svc.UpdateProvider(ctx, shadow.ID, &providercore.UpdateProviderInput{
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gpt-5.3-codex-spark": "gpt-5.3-codex-spark",
			},
		},
		GroupIDs: &groupIDs,
	})

	require.NoError(t, err)
	require.Equal(t, []int64{7}, repo.groupsOf[shadow.ID])
	require.Equal(t, map[string]any{"gpt-5.3-codex-spark": "gpt-5.3-codex-spark"}, updated.Credentials["model_mapping"])
	require.Empty(t, updated.GetOpenAIAccessToken(), "影子提供商不可持有母提供商 access_token")
}

// TestBulkUpdateProviders_RejectsProxyChangeOnShadow checks that a batch containing a shadow
// cannot bypass the parent's ownership of its proxy.
func TestBulkUpdateProviders_RejectsProxyChangeOnShadow(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)
	parentProxy := int64(7)
	parent := &providercore.Record{
		Name: "p", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Status: billingcore.StatusActive, ProxyID: &parentProxy,
		Credentials: map[string]any{"chatgpt_account_id": "o"},
	}
	require.NoError(t, repo.Create(ctx, parent))
	shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "s"})
	require.NoError(t, err)

	newProxy := int64(42)
	_, err = svc.BulkUpdateProviders(ctx, &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{shadow.ID},
		ProxyID:     &newProxy,
	})
	require.Error(t, err, "批量给影子改 proxy 必须被拒")
	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err), "应 400")
	require.NotNil(t, repo.providers[shadow.ID].ProxyID)
	require.Equal(t, parentProxy, *repo.providers[shadow.ID].ProxyID, "影子 proxy 必须保持继承母提供商")
}

// TestUpdateProvider_RejectsTypeChangeOnShadow protects the shadow's OAuth type.
func TestUpdateProvider_RejectsTypeChangeOnShadow(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)
	parent := &providercore.Record{
		Name: "type-parent", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Status: billingcore.StatusActive, Credentials: map[string]any{"chatgpt_account_id": "org-t"},
	}
	require.NoError(t, repo.Create(ctx, parent))
	shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "type-shadow"})
	require.NoError(t, err)

	_, err = svc.UpdateProvider(ctx, shadow.ID, &providercore.UpdateProviderInput{Type: capability.ProviderTypeAPIKey})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err), "改影子 type 应 400")
	require.Equal(t, capability.ProviderTypeOAuth, repo.providers[shadow.ID].Type, "影子 type 必须保持 oauth")

	_, err = svc.UpdateProvider(ctx, shadow.ID, &providercore.UpdateProviderInput{Type: capability.ProviderTypeOAuth})
	require.NoError(t, err, "传入相同 type 应允许")
}

// TestBulkUpdateProviders_RejectsCredentialWriteToShadow checks that a batch containing a shadow
// cannot bypass credential ownership checks on the single-provider update path.
func TestBulkUpdateProviders_RejectsCredentialWriteToShadow(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)
	parent := &providercore.Record{
		Name: "bulk-parent", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Status: billingcore.StatusActive, Credentials: map[string]any{"chatgpt_account_id": "org-b", "access_token": "t"},
	}
	require.NoError(t, repo.Create(ctx, parent))
	shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "bulk-shadow"})
	require.NoError(t, err)

	_, err = svc.BulkUpdateProviders(ctx, &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{shadow.ID},
		Credentials: map[string]any{"access_token": "leaked"},
	})
	require.Error(t, err, "批量给影子写凭据必须被拒")
	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err), "应 400")

	require.Empty(t, repo.providers[shadow.ID].GetOpenAIAccessToken(), "影子 access_token 必须保持为空 —— 批量写入未生效")
}

// TestUpdateProvider_PropagatesProxyToShadow checks that parent proxy changes reach Spark shadows.
func TestUpdateProvider_PropagatesProxyToShadow(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)

	oldProxy := int64(7)
	parent := &providercore.Record{
		Name:        "proxy-parent",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billingcore.StatusActive,
		ProxyID:     &oldProxy,
		Credentials: map[string]any{"chatgpt_account_id": "org-proxy"},
	}
	require.NoError(t, repo.Create(ctx, parent))

	shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "proxy-shadow"})
	require.NoError(t, err)
	shadowID := shadow.ID

	newProxy := int64(42)
	_, err = svc.UpdateProvider(ctx, parent.ID, &providercore.UpdateProviderInput{ProxyID: &newProxy})
	require.NoError(t, err)

	storedShadow, ok := repo.providers[shadowID]
	require.True(t, ok)
	require.NotNil(t, storedShadow.ProxyID)
	require.Equal(t, newProxy, *storedShadow.ProxyID)
}

// TestUpdateProvider_RejectsCredentialWriteToShadow checks that shadows reject authentication credentials.
// The shared update path rejects access_token and refresh_token writes to shadows.
// Those token fields must remain empty, but Credentials may still contain
// model_mapping created by CreateShadow, so the entire map need not be empty.
func TestUpdateProvider_RejectsCredentialWriteToShadow(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)

	parent := &providercore.Record{
		Name:        "cred-parent",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billingcore.StatusActive,
		Credentials: map[string]any{"access_token": "parent-secret", "refresh_token": "parent-rt"},
	}
	require.NoError(t, repo.Create(ctx, parent))

	shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "cred-shadow"})
	require.NoError(t, err)
	require.Empty(t, shadow.GetOpenAIAccessToken(), "前提:影子创建后不持有 access_token")
	require.Empty(t, shadow.GetOpenAIRefreshToken(), "前提:影子创建后不持有 refresh_token")

	_, err = svc.UpdateProvider(ctx, shadow.ID, &providercore.UpdateProviderInput{
		Credentials: map[string]any{"access_token": "leaked", "refresh_token": "leaked-rt"},
	})
	require.Error(t, err, "对影子写入凭据必须被拒绝")

	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err), "应映射为 400 而非 500")

	storedShadow, ok := repo.providers[shadow.ID]
	require.True(t, ok)
	require.Empty(t, storedShadow.GetOpenAIAccessToken(), "影子 access_token 必须保持为空 —— 凭据未被写入")
	require.Empty(t, storedShadow.GetOpenAIRefreshToken(), "影子 refresh_token 必须保持为空 —— 凭据未被写入")

	newPriority := 5
	_, err = svc.UpdateProvider(ctx, shadow.ID, &providercore.UpdateProviderInput{Priority: &newPriority})
	require.NoError(t, err, "影子的非凭据字段更新应正常")
}

// TestBulkUpdateProviders_PropagatesProxyToShadow checks propagation to each parent's Spark shadows.
func TestBulkUpdateProviders_PropagatesProxyToShadow(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)

	oldProxy := int64(7)
	parent := &providercore.Record{
		Name:        "bulk-parent",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billingcore.StatusActive,
		ProxyID:     &oldProxy,
		Credentials: map[string]any{"chatgpt_account_id": "org-bulk"},
	}
	require.NoError(t, repo.Create(ctx, parent))

	shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "bulk-shadow"})
	require.NoError(t, err)
	shadowID := shadow.ID

	newProxy := int64(99)
	_, err = svc.BulkUpdateProviders(ctx, &providercore.BulkUpdateProvidersInput{
		ProviderIDs: []int64{parent.ID},
		ProxyID:     &newProxy,
	})
	require.NoError(t, err)

	storedShadow, ok := repo.providers[shadowID]
	require.True(t, ok)
	require.NotNil(t, storedShadow.ProxyID)
	require.Equal(t, newProxy, *storedShadow.ProxyID)
}

// TestUpdateProvider_RejectsParentTypeChangeWithShadow protects the OpenAI OAuth parent type
// required to resolve a scheduled shadow's shared credentials.
func TestUpdateProvider_RejectsParentTypeChangeWithShadow(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)
	parent := &providercore.Record{
		Name: "p", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Status: billingcore.StatusActive, Credentials: map[string]any{"chatgpt_account_id": "o"},
	}
	require.NoError(t, repo.Create(ctx, parent))
	_, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "s"})
	require.NoError(t, err)

	_, err = svc.UpdateProvider(ctx, parent.ID, &providercore.UpdateProviderInput{Type: capability.ProviderTypeAPIKey})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err), "母提供商有影子时改 type 出 oauth 应 400")
	require.Equal(t, capability.ProviderTypeOAuth, repo.providers[parent.ID].Type, "母提供商 type 必须保持 oauth")

	_, err = svc.UpdateProvider(ctx, parent.ID, &providercore.UpdateProviderInput{Type: capability.ProviderTypeOAuth})
	require.NoError(t, err, "传入相同 type(no-op)应允许")
}

// TestUpdateProvider_IgnoresProxyChangeOnShadow checks that ordinary edits
// leave the shadow's inherited proxy unchanged.
func TestUpdateProvider_IgnoresProxyChangeOnShadow(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)
	parentProxy := int64(7)
	parent := &providercore.Record{
		Name: "p", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Status: billingcore.StatusActive, ProxyID: &parentProxy,
		Credentials: map[string]any{"chatgpt_account_id": "o"},
	}
	require.NoError(t, repo.Create(ctx, parent))
	shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "s"})
	require.NoError(t, err)
	require.NotNil(t, repo.providers[shadow.ID].ProxyID)
	require.Equal(t, parentProxy, *repo.providers[shadow.ID].ProxyID, "前提:影子继承母 proxy=7")

	newProxy := int64(42)
	_, err = svc.UpdateProvider(ctx, shadow.ID, &providercore.UpdateProviderInput{ProxyID: &newProxy})
	require.NoError(t, err, "影子的非 proxy 字段更新仍应成功")
	require.NotNil(t, repo.providers[shadow.ID].ProxyID)
	require.Equal(t, parentProxy, *repo.providers[shadow.ID].ProxyID, "影子 proxy 不应被独立改动,恒继承母提供商")
}

func TestUpdateProvider_ShadowEmptyCredentialsClearsModelMapping(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)
	parentID := int64(1)
	shadow := &providercore.Record{
		Name:             "s",
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		Status:           billingcore.StatusActive,
		ParentProviderID: &parentID,
		QuotaDimension:   providercore.QuotaDimensionSpark,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gpt-5.3-codex-spark": "gpt-5.3-codex-spark",
			},
		},
	}
	require.NoError(t, repo.Create(ctx, shadow))

	updated, err := svc.UpdateProvider(ctx, shadow.ID, &providercore.UpdateProviderInput{
		Credentials: map[string]any{},
	})

	require.NoError(t, err)
	require.Empty(t, updated.Credentials)
	require.Empty(t, repo.providers[shadow.ID].Credentials)
}

func TestUpdateProvider_ShadowRejectsAuthCredentials(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)
	parentID := int64(1)
	shadow := &providercore.Record{
		Name:             "s",
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		Status:           billingcore.StatusActive,
		ParentProviderID: &parentID,
		QuotaDimension:   providercore.QuotaDimensionSpark,
		Credentials:      map[string]any{},
	}
	require.NoError(t, repo.Create(ctx, shadow))

	_, err := svc.UpdateProvider(ctx, shadow.ID, &providercore.UpdateProviderInput{
		Credentials: map[string]any{"access_token": "leak"},
	})

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
	require.Empty(t, repo.providers[shadow.ID].Credentials)
}
