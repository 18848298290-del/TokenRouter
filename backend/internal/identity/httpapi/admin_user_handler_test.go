package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keydto "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
)

// adminUserFixture supplies user operations and key listings for handler tests.
type adminUserFixture struct {
	UserAdministration
	users                []identity.User
	apiKeys              []apikey.APIKey
	getUserErr           error
	boundAuthIdentityFor int64
	boundAuthIdentity    *identity.AdminBindAuthIdentityInput
	lastListUsers        struct {
		page, pageSize, calls int
		filters               identity.UserListFilters
		sortBy, sortOrder     string
	}
}

// adminUserStub supplies user lists and records query and update inputs.
type adminUserStub struct {
	UserAdministration
	users         []identity.User
	lastListUsers struct {
		page, pageSize, calls int
		filters               identity.UserListFilters
		sortBy, sortOrder     string
	}
}

type batchLimitsAdminServiceStub struct {
	*adminUserStub
	calls []batchLimitsAdminServiceCall
}

type batchLimitsAdminServiceCall struct {
	userIDs     []int64
	concurrency *int
	rpmLimit    *int
}

type getByIDAdminStub struct {
	UserAdministration
}

// listUsersFilterStub records the filters supplied to ListUsers.
type listUsersFilterStub struct {
	UserAdministration
	captured identity.UserListFilters
}

func TestTruncateSearchByRune(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxRunes int
		wantLen  int // Expected rune count.
	}{
		{
			name:     "纯中文超长",
			input:    string(make([]rune, 150)),
			maxRunes: 100,
			wantLen:  100,
		},
		{
			name:     "纯 ASCII 超长",
			input:    string(make([]byte, 150)),
			maxRunes: 100,
			wantLen:  100,
		},
		{
			name:     "空字符串",
			input:    "",
			maxRunes: 100,
			wantLen:  0,
		},
		{
			name:     "恰好 100 个字符",
			input:    string(make([]rune, 100)),
			maxRunes: 100,
			wantLen:  100,
		},
		{
			name:     "不足 100 字符不截断",
			input:    "hello世界",
			maxRunes: 100,
			wantLen:  7,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := captureTruncatedAdminSearch(t, tc.input, tc.maxRunes)
			require.Equal(t, tc.wantLen, len([]rune(result)))
		})
	}
}

func TestTruncateSearchByRune_PreservesMultibyte(t *testing.T) {
	// Truncating 101 Chinese characters to 100 runes must retain valid UTF-8.
	input := ""
	for range 101 {
		input += "中"
	}
	result := captureTruncatedAdminSearch(t, input, 100)

	require.Equal(t, 100, len([]rune(result)))
	// Each character in this fixture occupies three bytes.
	require.Equal(t, 300, len(result))
}

func TestTruncateSearchByRune_MixedASCIIAndMultibyte(t *testing.T) {
	// 50 ASCII characters plus 51 Chinese characters produce 101 runes.
	input := ""
	for range 50 {
		input += "a"
	}
	for range 51 {
		input += "中"
	}
	result := captureTruncatedAdminSearch(t, input, 100)

	runes := []rune(result)
	require.Equal(t, 100, len(runes))
	// The first 50 retained runes are ASCII and the remaining 50 are Chinese.
	require.Equal(t, 'a', runes[0])
	require.Equal(t, 'a', runes[49])
	require.Equal(t, '中', runes[50])
	require.Equal(t, '中', runes[99])
}

func TestUserHandlerEndpoints(t *testing.T) {
	router, _ := setupAdminRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/users?page=1&page_size=20", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/users/1", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	bindBody := map[string]any{
		"provider_type":    "wechat",
		"provider_key":     "wechat-main",
		"provider_subject": "union-123",
		"metadata":         map[string]any{"source": "admin-repair"},
		"channel": map[string]any{
			"channel":         "open",
			"channel_app_id":  "wx-open",
			"channel_subject": "openid-123",
		},
	}
	body, _ := json.Marshal(bindBody)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/1/auth-identities", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	createBody := map[string]any{"email": "new@example.com", "password": "pass123", "balance": 1, "concurrency": 2}
	body, _ = json.Marshal(createBody)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	updateBody := map[string]any{"email": "updated@example.com"}
	body, _ = json.Marshal(updateBody)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/users/1", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/1/balance", bytes.NewBufferString(`{"balance":1,"operation":"add"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/users/1/api-keys", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/users/1/usage?period=today", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestUserHandlerBindAuthIdentityMapsRequest(t *testing.T) {
	router, adminSvc := setupAdminRouter()

	body, err := json.Marshal(map[string]any{
		"provider_type":    "oidc",
		"provider_key":     "https://issuer.example",
		"provider_subject": "subject-123",
		"issuer":           "https://issuer.example",
		"metadata":         map[string]any{"report_id": 12},
	})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/9/auth-identities", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(9), adminSvc.boundAuthIdentityFor)
	require.NotNil(t, adminSvc.boundAuthIdentity)
	require.Equal(t, "oidc", adminSvc.boundAuthIdentity.ProviderType)
	require.Equal(t, "https://issuer.example", adminSvc.boundAuthIdentity.ProviderKey)
	require.Equal(t, "subject-123", adminSvc.boundAuthIdentity.ProviderSubject)
	require.Nil(t, adminSvc.boundAuthIdentity.Channel)
	require.Equal(t, float64(12), adminSvc.boundAuthIdentity.Metadata["report_id"])
}

func TestUserHandlerListIncludesActivityFieldsAndSortParams(t *testing.T) {
	lastLoginAt := time.Date(2026, 4, 20, 8, 0, 0, 0, time.UTC)
	lastActiveAt := lastLoginAt.Add(30 * time.Minute)
	lastUsedAt := lastLoginAt.Add(90 * time.Minute)

	adminSvc := newAdminUserStub()
	adminSvc.users = []identity.User{
		{
			ID:           7,
			Email:        "activity@example.com",
			Username:     "activity-user",
			Role:         identity.RoleUser,
			Status:       identity.StatusActive,
			LastActiveAt: &lastActiveAt,
			LastUsedAt:   &lastUsedAt,
			CreatedAt:    lastLoginAt.Add(-24 * time.Hour),
			UpdatedAt:    lastLoginAt,
		},
	}
	handler := newAdminUserTestHandler(adminSvc)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/users?sort_by=last_used_at&sort_order=asc&search=activity",
		nil,
	)

	handler.List(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "last_used_at", adminSvc.lastListUsers.sortBy)
	require.Equal(t, "asc", adminSvc.lastListUsers.sortOrder)
	require.Equal(t, "activity", adminSvc.lastListUsers.filters.Search)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				LastActiveAt *time.Time `json:"last_active_at"`
				LastUsedAt   *time.Time `json:"last_used_at"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Len(t, resp.Data.Items, 1)
	require.WithinDuration(t, lastActiveAt, *resp.Data.Items[0].LastActiveAt, time.Second)
	require.WithinDuration(t, lastUsedAt, *resp.Data.Items[0].LastUsedAt, time.Second)
}

func TestUserHandlerGetByIDIncludesActivityFields(t *testing.T) {
	lastLoginAt := time.Date(2026, 4, 20, 8, 0, 0, 0, time.UTC)
	lastActiveAt := lastLoginAt.Add(30 * time.Minute)
	lastUsedAt := lastLoginAt.Add(90 * time.Minute)

	adminSvc := newAdminUserStub()
	adminSvc.users = []identity.User{
		{
			ID:           8,
			Email:        "detail@example.com",
			Username:     "detail-user",
			Role:         identity.RoleUser,
			Status:       identity.StatusActive,
			LastActiveAt: &lastActiveAt,
			LastUsedAt:   &lastUsedAt,
			CreatedAt:    lastLoginAt.Add(-24 * time.Hour),
			UpdatedAt:    lastLoginAt,
		},
	}
	handler := newAdminUserTestHandler(adminSvc)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Params = gin.Params{{Key: "id", Value: "8"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/users/8", nil)

	handler.GetByID(c)

	require.Equal(t, http.StatusOK, recorder.Code)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			LastActiveAt *time.Time `json:"last_active_at"`
			LastUsedAt   *time.Time `json:"last_used_at"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.WithinDuration(t, lastActiveAt, *resp.Data.LastActiveAt, time.Second)
	require.WithinDuration(t, lastUsedAt, *resp.Data.LastUsedAt, time.Second)
}

func TestUserHandlerBatchUpdateLimitsAcceptsPartialAndZeroValues(t *testing.T) {
	tests := []struct {
		name                string
		body                string
		expectedConcurrency *int
		expectedRPMLimit    *int
	}{
		{name: "concurrency only", body: `{"user_ids":[1,2],"concurrency":10}`, expectedConcurrency: pointerTo(10)},
		{name: "both limits", body: `{"user_ids":[1,2],"concurrency":8,"rpm_limit":60}`, expectedConcurrency: pointerTo(8), expectedRPMLimit: pointerTo(60)},
		{name: "explicit zero", body: `{"user_ids":[1,2],"concurrency":0,"rpm_limit":0}`, expectedConcurrency: pointerTo(0), expectedRPMLimit: pointerTo(0)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serviceStub := &batchLimitsAdminServiceStub{adminUserStub: newAdminUserStub()}
			recorder := postBatchLimits(t, setupBatchLimitsRouter(serviceStub), []byte(test.body))

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Len(t, serviceStub.calls, 1)
			require.Equal(t, []int64{1, 2}, serviceStub.calls[0].userIDs)
			require.Equal(t, test.expectedConcurrency, serviceStub.calls[0].concurrency)
			require.Equal(t, test.expectedRPMLimit, serviceStub.calls[0].rpmLimit)

			var response struct {
				Data struct {
					Affected int `json:"affected"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, 2, response.Data.Affected)
		})
	}
}

func TestUserHandlerBatchUpdateLimitsRejectsInvalidRequests(t *testing.T) {
	tooManyIDs := make([]int64, 501)
	for index := range tooManyIDs {
		tooManyIDs[index] = int64(index + 1)
	}
	tooManyBody, err := json.Marshal(map[string]any{"user_ids": tooManyIDs, "rpm_limit": 10})
	require.NoError(t, err)

	tests := []struct {
		name string
		body []byte
	}{
		{name: "no limits", body: []byte(`{"user_ids":[1]}`)},
		{name: "invalid json", body: []byte(`{"user_ids":`)},
		{name: "missing user ids", body: []byte(`{"rpm_limit":10}`)},
		{name: "more than 500 ids", body: tooManyBody},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serviceStub := &batchLimitsAdminServiceStub{adminUserStub: newAdminUserStub()}
			recorder := postBatchLimits(t, setupBatchLimitsRouter(serviceStub), test.body)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Empty(t, serviceStub.calls)
		})
	}
}

func TestUserHandlerBatchUpdateLimitsAllUsesEveryListedUser(t *testing.T) {
	base := newAdminUserStub()
	base.users = []identity.User{{ID: 11}, {ID: 12}, {ID: 13}}
	serviceStub := &batchLimitsAdminServiceStub{adminUserStub: base}
	recorder := postBatchLimits(
		t,
		setupBatchLimitsRouter(serviceStub),
		[]byte(`{"all":true,"user_ids":[999],"rpm_limit":0}`),
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, serviceStub.calls, 1)
	require.Equal(t, []int64{11, 12, 13}, serviceStub.calls[0].userIDs)
	require.Equal(t, 1, base.lastListUsers.calls)
}

func TestAdminUserGetByID_IncludeDeleted(t *testing.T) {
	svc := &getByIDAdminStub{UserAdministration: newAdminUserStub()}
	router := setupGetByIDRouter(svc)

	t.Run("normal path returns 404 for deleted user", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/admin/users/7", nil)
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("include_deleted=true returns 200", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/admin/users/7?include_deleted=true", nil)
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
	})
}

func TestAdminUserList_ParsesAPIKeyGroupID(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  int64
	}{
		{"valid id", "?api_key_group_id=42", 42},
		{"missing", "", 0},
		{"zero ignored", "?api_key_group_id=0", 0},
		{"negative ignored", "?api_key_group_id=-3", 0},
		{"non-numeric ignored", "?api_key_group_id=abc", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &listUsersFilterStub{UserAdministration: newAdminUserStub()}
			r := gin.New()
			h := newAdminUserTestHandler(stub)
			r.GET("/admin/users", h.List)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/admin/users"+tc.query, nil)
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, tc.want, stub.captured.APIKeyGroupID)
		})
	}
}

func TestUpdateUserPromoteToAdminRequiresStepUp(t *testing.T) {
	router, _ := setupRoleStepUpRouter(t)

	rec := doJSON(t, router, http.MethodPut, "/api/v1/admin/users/1", map[string]any{"role": "admin"})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestUpdateUserExplicitAdminRoleAlwaysRequiresStepUp(t *testing.T) {
	router, _ := setupRoleStepUpRouter(t)

	rec := doJSON(t, router, http.MethodPut, "/api/v1/admin/users/2", map[string]any{"role": "admin"})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestUpdateUserWithoutRoleSkipsStepUp(t *testing.T) {
	router, _ := setupRoleStepUpRouter(t)

	rec := doJSON(t, router, http.MethodPut, "/api/v1/admin/users/2", map[string]any{"email": "admin-updated@example.com"})
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdateUserRegularRoleSkipsStepUp(t *testing.T) {
	router, _ := setupRoleStepUpRouter(t)

	rec := doJSON(t, router, http.MethodPut, "/api/v1/admin/users/1", map[string]any{"role": "user", "email": "u@example.com"})
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestCreateAdminUserRequiresStepUp(t *testing.T) {
	router, _ := setupRoleStepUpRouter(t)

	rec := doJSON(t, router, http.MethodPost, "/api/v1/admin/users", map[string]any{
		"email": "new-admin@example.com", "password": "pass123", "role": "admin",
	})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestCreateRegularUserSkipsStepUp(t *testing.T) {
	router, _ := setupRoleStepUpRouter(t)

	rec := doJSON(t, router, http.MethodPost, "/api/v1/admin/users", map[string]any{
		"email": "new-user@example.com", "password": "pass123", "role": "user",
	})
	require.Equal(t, http.StatusOK, rec.Code)
}

// captureTruncatedAdminSearch records the search value passed through the administrator route.
func captureTruncatedAdminSearch(t *testing.T, search string, maxRunes int) string {
	t.Helper()
	require.Equal(t, 100, maxRunes)
	router, source := setupAdminRouter()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/users?search="+url.QueryEscape(search), nil))
	require.Equal(t, http.StatusOK, response.Code)
	return source.lastListUsers.filters.Search
}

func (s *adminUserFixture) ListUsers(ctx context.Context, page, pageSize int, filters identity.UserListFilters, sortBy, sortOrder string) ([]identity.User, int64, error) {
	s.lastListUsers.page = page
	s.lastListUsers.pageSize = pageSize
	s.lastListUsers.filters = filters
	s.lastListUsers.sortBy = sortBy
	s.lastListUsers.sortOrder = sortOrder
	s.lastListUsers.calls++
	return s.users, int64(len(s.users)), nil
}

func (s *adminUserFixture) GetUser(ctx context.Context, id int64) (*identity.User, error) {
	if s.getUserErr != nil {
		return nil, s.getUserErr
	}
	for i := range s.users {
		if s.users[i].ID == id {
			return &s.users[i], nil
		}
	}
	user := identity.User{ID: id, Email: "user@example.com", Status: billing.StatusActive}
	return &user, nil
}

func (s *adminUserFixture) GetUserIncludeDeleted(ctx context.Context, id int64) (*identity.User, error) {
	return s.GetUser(ctx, id)
}

func (s *adminUserFixture) CreateUser(ctx context.Context, input *identity.CreateUserInput) (*identity.User, error) {
	user := identity.User{ID: 100, Email: input.Email, Status: billing.StatusActive}
	return &user, nil
}

func (s *adminUserFixture) UpdateUser(ctx context.Context, id int64, input *identity.UpdateUserInput) (*identity.User, error) {
	user := identity.User{ID: id, Email: "updated@example.com", Status: billing.StatusActive}
	return &user, nil
}

func (s *adminUserFixture) DeleteUser(ctx context.Context, id int64) error {
	return nil
}

func (s *adminUserFixture) UpdateUserBalance(ctx context.Context, userID int64, balance float64, operation string, notes string) (*identity.User, error) {
	user := identity.User{ID: userID, Balance: balance, Status: billing.StatusActive}
	return &user, nil
}

func (s *adminUserFixture) GetUserAPIKeys(ctx context.Context, userID int64, page, pageSize int, sortBy, sortOrder string) ([]apikey.APIKey, int64, error) {
	return s.apiKeys, int64(len(s.apiKeys)), nil
}

func (s *adminUserFixture) GetUserUsageStats(ctx context.Context, userID int64, period string) (any, error) {
	return map[string]any{"user_id": userID}, nil
}

func (s *adminUserFixture) GetUserRPMStatus(ctx context.Context, userID int64) (*identity.UserRPMStatus, error) {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &identity.UserRPMStatus{
		UserRPMUsed:  0,
		UserRPMLimit: user.RPMLimit,
	}, nil
}

func (s *adminUserFixture) BindUserAuthIdentity(ctx context.Context, userID int64, input identity.AdminBindAuthIdentityInput) (*identity.AdminBoundAuthIdentity, error) {
	s.boundAuthIdentityFor = userID
	copied := input
	if input.Metadata != nil {
		copied.Metadata = map[string]any{}
		maps.Copy(copied.Metadata, input.Metadata)
	}
	if input.Channel != nil {
		channel := *input.Channel
		if input.Channel.Metadata != nil {
			channel.Metadata = map[string]any{}
			maps.Copy(channel.Metadata, input.Channel.Metadata)
		}
		copied.Channel = &channel
	}
	s.boundAuthIdentity = &copied

	now := time.Now().UTC()
	result := &identity.AdminBoundAuthIdentity{
		UserID:          userID,
		ProviderType:    input.ProviderType,
		ProviderKey:     input.ProviderKey,
		ProviderSubject: input.ProviderSubject,
		VerifiedAt:      &now,
		Issuer:          input.Issuer,
		Metadata:        input.Metadata,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if input.Channel != nil {
		result.Channel = &identity.AdminBoundAuthIdentityChannel{
			Channel:        input.Channel.Channel,
			ChannelAppID:   input.Channel.ChannelAppID,
			ChannelSubject: input.Channel.ChannelSubject,
			Metadata:       input.Channel.Metadata,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
	}
	return result, nil
}

func newAdminUserFixture() *adminUserFixture {
	now := time.Now().UTC()
	return &adminUserFixture{users: []identity.User{{ID: 1, Email: "user@example.com", Role: identity.RoleUser, Status: identity.StatusActive, CreatedAt: now, UpdatedAt: now}}, apiKeys: []apikey.APIKey{{ID: 10, UserID: 1, Key: "sk-test", Name: "test", Status: apikey.StatusActive, CreatedAt: now, UpdatedAt: now}}}
}

func setupAdminRouter() (*gin.Engine, *adminUserFixture) {
	router := gin.New()
	adminSvc := newAdminUserFixture()

	listKeys := func(ctx context.Context, id int64, page, size int, sortBy, order string) ([]keydto.APIKey[routingdto.Group], int64, error) {
		keys, total, err := adminSvc.GetUserAPIKeys(ctx, id, page, size, sortBy, order)
		if err != nil {
			return nil, 0, err
		}
		out := make([]keydto.APIKey[routingdto.Group], 0, len(keys))
		for i := range keys {
			out = append(out, *keydto.APIKeyFromKey(apikey.CopyAPIKey(&keys[i]), func(g *routing.Group) *routingdto.Group { return routingdto.GroupFromRouting(apikey.RoutingGroup(g)) }))
		}
		return out, total, nil
	}
	userHandler := NewAdminUserHandler(adminSvc, listKeys, nil, func(c *gin.Context) bool { return EnforceStepUp(c, nil, nil, nil) })

	router.GET("/api/v1/admin/users", userHandler.List)
	router.GET("/api/v1/admin/users/:id", userHandler.GetByID)
	router.POST("/api/v1/admin/users/:id/auth-identities", userHandler.BindAuthIdentity)
	router.POST("/api/v1/admin/users", userHandler.Create)
	router.PUT("/api/v1/admin/users/:id", userHandler.Update)
	router.DELETE("/api/v1/admin/users/:id", userHandler.Delete)
	router.POST("/api/v1/admin/users/:id/balance", userHandler.UpdateBalance)
	router.GET("/api/v1/admin/users/:id/api-keys", userHandler.GetUserAPIKeys)
	router.GET("/api/v1/admin/users/:id/usage", userHandler.GetUserUsage)

	return router, adminSvc
}

func newAdminUserStub() *adminUserStub {
	now := time.Now().UTC()
	return &adminUserStub{users: []identity.User{{ID: 1, Email: "user@example.com", Role: identity.RoleUser, Status: identity.StatusActive, CreatedAt: now, UpdatedAt: now}}}
}

func (s *adminUserStub) ListUsers(_ context.Context, page, size int, filters identity.UserListFilters, sortBy, order string) ([]identity.User, int64, error) {
	s.lastListUsers.page, s.lastListUsers.pageSize = page, size
	s.lastListUsers.filters, s.lastListUsers.sortBy, s.lastListUsers.sortOrder = filters, sortBy, order
	s.lastListUsers.calls++
	return s.users, int64(len(s.users)), nil
}

func (s *adminUserStub) GetUser(_ context.Context, id int64) (*identity.User, error) {
	for i := range s.users {
		if s.users[i].ID == id {
			return &s.users[i], nil
		}
	}
	return &identity.User{ID: id, Email: "user@example.com", Status: identity.StatusActive}, nil
}

func (s *adminUserStub) GetUserIncludeDeleted(ctx context.Context, id int64) (*identity.User, error) {
	return s.GetUser(ctx, id)
}

func (s *adminUserStub) CreateUser(_ context.Context, input *identity.CreateUserInput) (*identity.User, error) {
	return &identity.User{ID: 100, Email: input.Email, Status: identity.StatusActive}, nil
}

func (s *adminUserStub) UpdateUser(_ context.Context, id int64, _ *identity.UpdateUserInput) (*identity.User, error) {
	return &identity.User{ID: id, Email: "updated@example.com", Status: identity.StatusActive}, nil
}

// newAdminUserTestHandler enables step-up checks that reject requests without an authenticated principal.
func newAdminUserTestHandler(users UserAdministration) *AdminUserHandler[struct{}] {
	return NewAdminUserHandler[struct{}](users, nil, nil, func(c *gin.Context) bool {
		return EnforceStepUp(c, nil, nil, nil)
	})
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (s *batchLimitsAdminServiceStub) BatchUpdateLimits(_ context.Context, userIDs []int64, concurrency, rpmLimit *int) (int, error) {
	s.calls = append(s.calls, batchLimitsAdminServiceCall{
		userIDs:     append([]int64(nil), userIDs...),
		concurrency: cloneIntPointer(concurrency),
		rpmLimit:    cloneIntPointer(rpmLimit),
	})
	return len(userIDs), nil
}

func setupBatchLimitsRouter(serviceStub UserAdministration) *gin.Engine {
	router := gin.New()
	handler := newAdminUserTestHandler(serviceStub)
	router.POST("/api/v1/admin/users/batch-limits", handler.BatchUpdateLimits)
	return router
}

func postBatchLimits(t *testing.T, router *gin.Engine, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/users/batch-limits",
		bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

func pointerTo(value int) *int {
	return &value
}

func (s *getByIDAdminStub) GetUser(_ context.Context, _ int64) (*identity.User, error) {
	return nil, identity.ErrUserNotFound
}

func (s *getByIDAdminStub) GetUserIncludeDeleted(_ context.Context, id int64) (*identity.User, error) {
	return &identity.User{ID: id, Email: "del@test.com"}, nil
}

func setupGetByIDRouter(svc UserAdministration) *gin.Engine {
	r := gin.New()
	h := newAdminUserTestHandler(svc)
	r.GET("/admin/users/:id", h.GetByID)
	return r
}

func (s *listUsersFilterStub) ListUsers(_ context.Context, _, _ int, filters identity.UserListFilters, _, _ string) ([]identity.User, int64, error) {
	s.captured = filters
	return []identity.User{}, 0, nil
}

// setupRoleStepUpRouter registers user creation and update routes.
// Missing authentication context produces 401 during step-up; accepted operations return 200.
func setupRoleStepUpRouter(t *testing.T) (*gin.Engine, *adminUserStub) {
	t.Helper()
	router := gin.New()
	adminSvc := newAdminUserStub()
	// An existing administrator distinguishes an explicit admin role from an omitted role.
	adminSvc.users = append(adminSvc.users, identity.User{
		ID:     2,
		Email:  "admin@example.com",
		Role:   identity.RoleAdmin,
		Status: identity.StatusActive,
	})

	h := newAdminUserTestHandler(adminSvc)
	router.POST("/api/v1/admin/users", h.Create)
	router.PUT("/api/v1/admin/users/:id", h.Update)
	return router, adminSvc
}

func doJSON(t *testing.T, router *gin.Engine, method, path string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}
