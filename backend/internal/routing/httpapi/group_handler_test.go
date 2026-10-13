package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keydto "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	idempotencytest "github.com/TokenFlux/TokenRouter/internal/idempotency/testkit"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	groupdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// groupAdminFixture supplies group records and observes group-detail reads
// for HTTP tests.
type groupAdminFixture struct {
	// GroupAdministration provides promoted methods that this fixture does not
	// implement; the embedded interface remains nil, so those methods cannot be
	// called.
	GroupAdministration // unused embedded administration methods
	// groups is returned directly by fixture list methods; readers share its
	// backing storage and keep it unchanged during these endpoint tests.
	groups        []routing.Group // shared fixture group records
	getGroupCalls int             // observed group-detail read count
}

// duplicateGroupAdminServiceStub records duplication identity and exposes a
// recoverable fixture copy.
type duplicateGroupAdminServiceStub struct {
	// GroupAdministration provides promoted methods that this fixture does not
	// implement; the embedded interface remains nil, so those methods cannot be
	// called.
	GroupAdministration // unused embedded administration methods
	// group retains the supplied fixture pointer. DuplicateGroup and successful
	// RecoverDuplicateGroup calls return that same pointer, so fixture edits
	// remain visible to later calls.
	group        *routing.Group // returned fixture duplicate pointer
	calls        int            // observed duplication execution count
	recoverCalls int            // observed duplication recovery count
	// groupID, actorScope, and operationKey record the latest DuplicateGroup
	// invocation. Tests compare this identity with the route, authenticated
	// administrator, and supplied request key.
	groupID      int64  // last duplication source ID
	actorScope   string // last duplication actor scope
	operationKey string // last duplication operation key
	// recoverScope and recoverKey record the latest recovery identity. Recovery
	// assertions compare these values with the same administrator and request
	// key used for the original duplication.
	recoverScope string // last recovery actor scope
	recoverKey   string // last recovery operation key
	// created becomes true when DuplicateGroup executes; RecoverDuplicateGroup
	// returns the fixture copy only after that state records creation.
	created bool // fixture copy availability flag
}

// failOnceMarkSucceededRepo injects one idempotency completion failure after
// the duplication callback.
type failOnceMarkSucceededRepo struct {
	*idempotencytest.MemoryStore // underlying idempotency fixture store
	// failNext is consumed before returning the injected success-marker error,
	// so later MarkSucceeded calls delegate to the embedded memory store.
	failNext bool // pending completion failure injection
}

// TestDuplicateGroupHandlerReturnsAdminDTOWithoutOperationMetadata duplicates
// a fixture group containing an internal duplication identifier. It checks
// copied product and provider fields appear in the successful administrator
// response, while duplicate_operation_id and its stored value are omitted.
func TestDuplicateGroupHandlerReturnsAdminDTOWithoutOperationMetadata(t *testing.T) {
	svc := &duplicateGroupAdminServiceStub{group: duplicateGroupHandlerFixture()}
	router := setupDuplicateGroupRouter(t, svc)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/42/duplicate", nil)

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, svc.calls)
	require.Contains(t, recorder.Body.String(), `"name":"primary (Copy)"`)
	require.Contains(t, recorder.Body.String(), `"status":"inactive"`)
	require.Contains(t, recorder.Body.String(), `"provider_count":3`)
	require.NotContains(t, recorder.Body.String(), "duplicate_operation_id")
	require.NotContains(t, recorder.Body.String(), "internal-operation-must-not-leak")
}

// TestDuplicateGroupHandlerRejectsInvalidID sends malformed, zero, and
// negative source group IDs to duplication. Each request returns a bad
// request before the fixture's duplication method is invoked.
func TestDuplicateGroupHandlerRejectsInvalidID(t *testing.T) {
	for _, id := range []string{"not-a-number", "0", "-1"} {
		t.Run(id, func(t *testing.T) {
			svc := &duplicateGroupAdminServiceStub{}
			router := setupDuplicateGroupRouter(t, svc)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/"+id+"/duplicate", nil)

			router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Zero(t, svc.calls)
		})
	}
}

// TestDuplicateGroupHandlerReplaysSameIdempotencyKey sends the same
// authenticated duplication request twice through a memory coordinator. It
// checks one service invocation, propagated source/actor/key identity, and
// the replay header on the second response.
func TestDuplicateGroupHandlerReplaysSameIdempotencyKey(t *testing.T) {
	svc := &duplicateGroupAdminServiceStub{group: duplicateGroupHandlerFixture()}
	coordinator := idempotency.NewIdempotencyCoordinator(idempotencytest.NewMemoryStore(), idempotency.DefaultIdempotencyConfig())
	router := setupDuplicateGroupRouter(t, svc, coordinator)

	call := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/42/duplicate", nil)
		request.Header.Set("Idempotency-Key", "duplicate-group-42")
		router.ServeHTTP(recorder, request)
		return recorder
	}

	first := call()
	second := call()

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, 1, svc.calls)
	require.Equal(t, int64(42), svc.groupID)
	require.Equal(t, "admin:77", svc.actorScope)
	require.Equal(t, "duplicate-group-42", svc.operationKey)
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
}

// TestDuplicateGroupHandlerRecoversAfterMarkSucceededFailure fails the first
// completion marker after the copy is created. Two requests return recovered
// success with the same actor/key, while the duplication side effect runs
// once and recovery runs for each response.
func TestDuplicateGroupHandlerRecoversAfterMarkSucceededFailure(t *testing.T) {
	svc := &duplicateGroupAdminServiceStub{group: duplicateGroupHandlerFixture()}
	repo := &failOnceMarkSucceededRepo{MemoryStore: idempotencytest.NewMemoryStore(), failNext: true}
	coordinator := idempotency.NewIdempotencyCoordinator(repo, idempotency.DefaultIdempotencyConfig())
	router := setupDuplicateGroupRouter(t, svc, coordinator)

	call := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/42/duplicate", nil)
		request.Header.Set("Idempotency-Key", "duplicate-group-42-recovery")
		router.ServeHTTP(recorder, request)
		return recorder
	}

	first := call()
	second := call()

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, "true", first.Header().Get("X-Idempotency-Recovered"))
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Recovered"))
	require.Equal(t, 1, svc.calls, "ambiguous retries must not repeat the create side effect")
	require.Equal(t, 2, svc.recoverCalls)
	require.Equal(t, "admin:77", svc.recoverScope)
	require.Equal(t, "duplicate-group-42-recovery", svc.recoverKey)
}

// TestGroupHandlerEndpoints serves registered group list, detail, creation,
// update, deletion, model-choice, and key queries through fixture services.
// It checks successful responses, visible new-group model suggestions, and a
// 404 for the removed group stats path.
func TestGroupHandlerEndpoints(t *testing.T) {
	router, _ := setupGroupAdminContractRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/groups", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/groups/all", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/groups/2", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/groups/0/models-list-candidates", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "gpt-5.5")

	// The create and update payloads below contain fixed strings. Their JSON
	// shapes need no unsupported-value recovery, so setup uses Marshal's bytes
	// directly while exercising the HTTP routes.
	body, _ := json.Marshal(map[string]any{"name": "new"})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body, _ = json.Marshal(map[string]any{"name": "update"})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/groups/2", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/groups/2", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/groups/2/stats", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/groups/2/api-keys", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestGroupRequestsDecodeOpenAIFast decodes true creation, false update, and
// omitted update Fast inputs. It checks the update pointer keeps a supplied
// false distinct from omission.
func TestGroupRequestsDecodeOpenAIFast(t *testing.T) {
	var createReq CreateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"fast","force_openai_fast":true}`), &createReq))
	require.True(t, createReq.ForceOpenAIFast)

	var updateReq UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"force_openai_fast":false}`), &updateReq))
	require.NotNil(t, updateReq.ForceOpenAIFast)
	require.False(t, *updateReq.ForceOpenAIFast)

	var omitted UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{}`), &omitted))
	require.Nil(t, omitted.ForceOpenAIFast)
}

// TestGroupManagementRejectsRetiredFields binds creation and update requests
// containing retired platform or default-group fields. It checks strict
// rejection and accepts unified protocol fields with an explicit empty
// conversion-target array.
func TestGroupManagementRejectsRetiredFields(t *testing.T) {
	for _, body := range []string{`{"name":"g","platform":"openai"}`, `{"name":"g","is_default":false}`} {
		require.Error(t, bindGroupPlatformJSON(t, &CreateGroupRequest{}, body))
		require.Error(t, bindGroupPlatformJSON(t, &UpdateGroupRequest{}, body))
	}
	var req CreateGroupRequest
	require.NoError(t, bindGroupPlatformJSON(t, &req, `{"name":"mixed","allowed_protocols":["anthropic_messages","openai_responses"],"protocol_fallbacks":{"anthropic_messages":[]}}`))
	require.Equal(t, "mixed", req.Name)
	require.NotNil(t, req.ProtocolFallbacks["anthropic_messages"])
}

// TestGroupManagementRejectsRetiredMessagesMapping binds an obsolete
// Messages-specific mapping in both group request shapes. It checks rejection
// and verifies that the supported group routing policy retains the requested
// model mapping.
func TestGroupManagementRejectsRetiredMessagesMapping(t *testing.T) {
	body := `{"messages_dispatch_model_config":{"exact_model_mappings":{"claude-sonnet-4-6":"target"}}}`
	require.Error(t, bindGroupPlatformJSON(t, &CreateGroupRequest{}, body))
	require.Error(t, bindGroupPlatformJSON(t, &UpdateGroupRequest{}, body))
	var req UpdateGroupRequest
	require.NoError(t, bindGroupPlatformJSON(t, &req, `{"routing_policy":{"enabled":true,"model_mapping":{"claude-sonnet-4-6":"target"}}}`))
	require.Equal(t, "target", req.RoutingPolicy.ModelMapping["claude-sonnet-4-6"])
}

// TestUpdateGroupRequestReasoningEffortMappingsTriState decodes omitted,
// empty, populated, and model-scoped mapping inputs. It checks pointer
// presence, list contents, and model condition fields needed to distinguish
// retention, clearing, and replacement.
func TestUpdateGroupRequestReasoningEffortMappingsTriState(t *testing.T) {
	t.Run("omitted means unchanged", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{}`), &req))
		require.Nil(t, req.ReasoningEffortMappings)
	})

	t.Run("empty array means clear", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{"reasoning_effort_mappings":[]}`), &req))
		require.NotNil(t, req.ReasoningEffortMappings)
		require.Empty(t, *req.ReasoningEffortMappings)
	})

	t.Run("non empty array means replace", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{"reasoning_effort_mappings":[{"from":"max","to":"xhigh"}]}`), &req))
		require.NotNil(t, req.ReasoningEffortMappings)
		require.Len(t, *req.ReasoningEffortMappings, 1)
		require.Equal(t, "max", (*req.ReasoningEffortMappings)[0].From)
		require.Equal(t, "xhigh", (*req.ReasoningEffortMappings)[0].To)
	})

	t.Run("accepts model scoped mappings", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{"reasoning_effort_mappings":[{"from":"max","to":"low","match_type":"prefix","model":"gpt"}]}`), &req))
		require.NotNil(t, req.ReasoningEffortMappings)
		require.Len(t, *req.ReasoningEffortMappings, 1)
		require.Equal(t, "max", (*req.ReasoningEffortMappings)[0].From)
		require.Equal(t, "low", (*req.ReasoningEffortMappings)[0].To)
		require.Equal(t, "prefix", (*req.ReasoningEffortMappings)[0].MatchType)
		require.Equal(t, "gpt", (*req.ReasoningEffortMappings)[0].Model)
	})
}

// TestGroupRequestReasoningEffortOverLimitAction decodes a deny action in an
// update request. It checks the optional action pointer exists and retains
// the supplied value for group administration.
func TestGroupRequestReasoningEffortOverLimitAction(t *testing.T) {
	var req UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"max_reasoning_effort_over_limit":"deny"}`), &req))
	require.NotNil(t, req.MaxReasoningEffortOverLimit)
	require.Equal(t, "deny", *req.MaxReasoningEffortOverLimit)
}

// TestUpdateGroupRequestAdvancedSchedulerOverridesTriState decodes omission,
// an empty object, and populated overrides with false and zero. It checks
// presence, cleared fields, and retained explicit scheduler values before
// administration interprets the patch.
func TestUpdateGroupRequestAdvancedSchedulerOverridesTriState(t *testing.T) {
	t.Run("omitted means unchanged", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{}`), &req))
		require.Nil(t, req.AdvancedSchedulerOverrides)
	})

	t.Run("empty object means clear all overrides", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{"advanced_scheduler_overrides":{}}`), &req))
		require.NotNil(t, req.AdvancedSchedulerOverrides)
		require.Zero(t, *req.AdvancedSchedulerOverrides)
	})

	t.Run("explicit false and zero are retained", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{
			"advanced_scheduler_overrides":{
				"sticky_weighted_enabled":false,
				"lb_top_k":3,
				"weight_queue":0
			}
		}`), &req))
		require.NotNil(t, req.AdvancedSchedulerOverrides)
		require.NotNil(t, req.AdvancedSchedulerOverrides.StickyWeightedEnabled)
		require.False(t, *req.AdvancedSchedulerOverrides.StickyWeightedEnabled)
		require.Equal(t, 3, *req.AdvancedSchedulerOverrides.LBTopK)
		require.Equal(t, 0.0, *req.AdvancedSchedulerOverrides.WeightQueue)
	})
}

// setupGroupAdminContractRouter registers group routes against fixed group
// and key readers. It returns the router and shared group fixture so endpoint
// tests can serve requests and inspect observed detail reads.
func setupGroupAdminContractRouter() (*gin.Engine, *groupAdminFixture) {
	now := time.Now().UTC()
	source := &groupAdminFixture{groups: []routing.Group{{ID: 2, Name: "group", Status: billing.StatusActive, CreatedAt: now, UpdatedAt: now}}}
	key := &apikey.APIKey{ID: 10, UserID: 1, Key: "sk-test", Name: "test", Status: billing.StatusActive, CreatedAt: now, UpdatedAt: now}
	handler := NewGroupHandler(source, GroupResources{Keys: func(context.Context, int64, int, int) ([]keydto.APIKey[groupdto.Group], int64, error) {
		return []keydto.APIKey[groupdto.Group]{*keydto.APIKeyFromKey(key, func(g *routing.Group) *groupdto.Group { return groupdto.GroupFromRouting(apikey.RoutingGroup(g)) })}, 1, nil
	}})
	router := gin.New()
	RegisterGroupRoutes(router.Group("/api/v1/admin"), handler)
	return router, source
}

// ListGroups returns the fixture's shared groups and their total count. It
// ignores filters and pagination, allowing endpoint registration tests to
// exercise HTTP mapping with one fixed list.
func (s *groupAdminFixture) ListGroups(ctx context.Context, page, pageSize int, platform, status, search string, isExclusive *bool, sortBy, sortOrder string) ([]routing.Group, int64, error) {
	return s.groups, int64(len(s.groups)), nil
}

// GetAllGroups returns the fixture's shared records for active-group
// selection. It applies no status filtering, so this fixture tests endpoint
// forwarding and serialization rather than repository eligibility.
func (s *groupAdminFixture) GetAllGroups(ctx context.Context) ([]routing.Group, error) {
	return s.groups, nil
}

// GetAllGroupsByPlatform returns the fixture's shared group list for the
// supplied query. The platform argument is ignored; the helper applies no
// platform filtering to the retained records.
func (s *groupAdminFixture) GetAllGroupsByPlatform(ctx context.Context, platform string) ([]routing.Group, error) {
	return s.groups, nil
}

// GetAllGroupsIncludingInactive returns all retained fixture records through
// the administrative query. It shares the same storage as ordinary fixture
// list reads and performs no independent status selection.
func (s *groupAdminFixture) GetAllGroupsIncludingInactive(ctx context.Context) ([]routing.Group, error) {
	return s.groups, nil
}

// GetGroup counts a detail read and creates an active fixture group with the
// requested ID.
func (s *groupAdminFixture) GetGroup(ctx context.Context, id int64) (*routing.Group, error) {
	s.getGroupCalls++
	group := routing.Group{ID: id, Name: "group", Status: billing.StatusActive}
	return &group, nil
}

// GetGroupModelsListCandidates supplies two suggestions when the source ID is
// zero. Other IDs receive one Claude model, and platform is ignored so tests
// isolate the handler's new-group query path.
func (s *groupAdminFixture) GetGroupModelsListCandidates(ctx context.Context, id int64, platform string) ([]string, error) {
	if id == 0 {
		return []string{"gpt-5.5", "gpt-5.4"}, nil
	}
	return []string{"claude-sonnet-4-6"}, nil
}

// CreateGroup returns an active fixture row using the submitted business
// name.
func (s *groupAdminFixture) CreateGroup(ctx context.Context, input *routing.CreateGroupInput) (*routing.Group, error) {
	group := routing.Group{ID: 200, Name: input.Name, Status: billing.StatusActive}
	return &group, nil
}

// DuplicateGroup returns a disabled fixture copy for group endpoint tests.
func (s *groupAdminFixture) DuplicateGroup(ctx context.Context, id int64, actorScope, operationKey string) (*routing.Group, error) {
	group := routing.Group{ID: 201, Name: "group (Copy)", Status: "inactive"}
	return &group, nil
}

// RecoverDuplicateGroup returns no committed fixture copy for the endpoint
// stub. The nil result leaves recovery unsuccessful, while dedicated
// duplication tests use duplicateGroupAdminServiceStub to provide a
// recoverable row.
func (s *groupAdminFixture) RecoverDuplicateGroup(ctx context.Context, id int64, actorScope, operationKey string) (*routing.Group, error) {
	return nil, nil
}

// UpdateGroup returns an active fixture row with the submitted name and
// requested ID.
func (s *groupAdminFixture) UpdateGroup(ctx context.Context, id int64, input *routing.UpdateGroupInput) (*routing.Group, error) {
	group := routing.Group{ID: id, Name: input.Name, Status: billing.StatusActive}
	return &group, nil
}

// DeleteGroup acknowledges fixture deletion so HTTP tests can inspect the
// response.
func (s *groupAdminFixture) DeleteGroup(ctx context.Context, id int64) error {
	return nil
}

// DuplicateGroup records the supplied source, actor, and request key, marks
// the fixture created, and returns its group pointer. Repeated direct calls
// increment execution count so retry tests can detect an extra create
// invocation.
func (s *duplicateGroupAdminServiceStub) DuplicateGroup(_ context.Context, groupID int64, actorScope, operationKey string) (*routing.Group, error) {
	s.calls++
	s.groupID = groupID
	s.actorScope = actorScope
	s.operationKey = operationKey
	s.created = true
	return s.group, nil
}

// RecoverDuplicateGroup records recovery actor/key and increments its
// observation count. It returns nil before creation and the shared fixture
// group afterward, modeling the committed-copy lookup used after idempotency
// failure.
func (s *duplicateGroupAdminServiceStub) RecoverDuplicateGroup(_ context.Context, _ int64, actorScope, operationKey string) (*routing.Group, error) {
	s.recoverCalls++
	s.recoverScope = actorScope
	s.recoverKey = operationKey
	if !s.created {
		return nil, nil
	}
	return s.group, nil
}

// setupDuplicateGroupRouter registers duplication with administrator 77 in
// the request context. When supplied, the first coordinator is bound before
// requests; omission exercises the executor's direct-callback fixture path.
func setupDuplicateGroupRouter(t *testing.T, svc GroupAdministration, coordinators ...*idempotency.IdempotencyCoordinator) *gin.Engine {
	t.Helper()

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 77})
		c.Next()
	})
	handler := NewGroupHandler(svc)
	if len(coordinators) > 0 {
		handler.BindIdempotency(coordinators[0])
	}
	router.POST("/api/v1/admin/groups/:id/duplicate", handler.Duplicate)
	return router
}

// duplicateGroupHandlerFixture creates a disabled copied group with provider
// counts and internal operation metadata for response omission assertions.
func duplicateGroupHandlerFixture() *routing.Group {
	return &routing.Group{
		ID:   43,
		Name: "primary (Copy)",

		Status:               "inactive",
		RateMultiplier:       1,
		ProviderCount:        3,
		ActiveProviderCount:  2,
		DuplicateOperationID: "internal-operation-must-not-leak",
		ModelRouting:         map[string][]int64{"claude-*": {7}},
	}
}

// MarkSucceeded returns one injected completion-storage error before
// delegating later writes to MemoryStore. The failNext flag is consumed on
// that first call, allowing the test to distinguish an executed copy from
// missing completion state.
func (r *failOnceMarkSucceededRepo) MarkSucceeded(ctx context.Context, id int64, responseStatus int, responseBody string, expiresAt time.Time) error {
	// Fail completion persistence after the duplication callback has produced
	// its copy, creating the ambiguous state that HTTP recovery is expected to
	// resolve.
	if r.failNext {
		r.failNext = false
		return errors.New("mark succeeded failed")
	}
	return r.MemoryStore.MarkSucceeded(ctx, id, responseStatus, responseBody, expiresAt)
}

// bindGroupPlatformJSON binds body into the supplied target using strict HTTP
// JSON decoding. It creates a JSON request and returns the binding error so
// tests can inspect rejected fields without serving a complete route.
func bindGroupPlatformJSON(t *testing.T, target any, body string) error {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return httpx.BindJSONStrict(c, target)
}
