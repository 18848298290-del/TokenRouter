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
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	groupdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
)

// failingUpdateGroupService returns a configured error from administrative
// update requests.
type failingUpdateGroupService struct {
	*adminUpdateFixture       // embedded retained Key fixture
	err                 error // configured administrative failure result
}

// adminUpdateFixture retains Key records for administrative binding and
// window-reset responses.
type adminUpdateFixture struct {
	apiKeys []apikey.APIKey // retained administrative fixture records
}

// TestAdminAPIKeyHandler_UpdateGroup_InvalidID submits an unparseable route ID
// with valid JSON. It checks a bad-request response names the invalid Key
// identifier in the returned administrative response.
func TestAdminAPIKeyHandler_UpdateGroup_InvalidID(t *testing.T) {
	router := setupAPIKeyHandler(newAdminUpdateFixture())
	body := `{"group_id": 2}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/abc", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "Invalid API key ID")
}

// TestAdminAPIKeyHandler_UpdateGroup_InvalidJSON submits malformed JSON for a
// parseable Key route. It checks the handler returns a bad request identifying
// invalid input in the returned administrative response.
func TestAdminAPIKeyHandler_UpdateGroup_InvalidJSON(t *testing.T) {
	router := setupAPIKeyHandler(newAdminUpdateFixture())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(`{bad json`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "Invalid request")
}

// TestAdminAPIKeyHandler_UpdateGroup_KeyNotFound requests an ID absent from the
// retained fixture records. It checks the missing-Key application result
// becomes an HTTP not-found response.
func TestAdminAPIKeyHandler_UpdateGroup_KeyNotFound(t *testing.T) {
	router := setupAPIKeyHandler(newAdminUpdateFixture())
	body := `{"group_id": 2}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/999", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	// Missing keys map to HTTP 404.
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestAdminAPIKeyHandler_UpdateGroup_BindGroup submits a positive binding for
// the retained fixture Key. It checks a success envelope carries the Key
// identifier and the supplied target group identifier.
func TestAdminAPIKeyHandler_UpdateGroup_BindGroup(t *testing.T) {
	router := setupAPIKeyHandler(newAdminUpdateFixture())
	body := `{"group_id": 2}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Code int             `json:"code"` // response envelope status code
		Data json.RawMessage `json:"data"` // decoded operation response payload
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)

	var data struct {
		APIKey struct { // returned credential response object
			ID      int64  `json:"id"`       // returned credential record identifier
			GroupID *int64 `json:"group_id"` // returned credential group binding
		} `json:"api_key"`
		AutoGrantedGroupAccess bool `json:"auto_granted_group_access"` // reported exclusive grant occurrence
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	require.Equal(t, int64(10), data.APIKey.ID)
	require.NotNil(t, data.APIKey.GroupID)
	require.Equal(t, int64(2), *data.APIKey.GroupID)
}

// TestAdminAPIKeyHandler_UpdateGroup_Unbind supplies zero for a previously
// bound fixture Key. It checks the response maps the returned absent GroupID to
// a null binding after successful administration.
func TestAdminAPIKeyHandler_UpdateGroup_Unbind(t *testing.T) {
	svc := newAdminUpdateFixture()
	gid := int64(2)
	svc.apiKeys[0].GroupID = &gid
	router := setupAPIKeyHandler(svc)
	body := `{"group_id": 0}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data struct { // decoded operation response payload
			APIKey struct { // returned credential response object
				GroupID *int64 `json:"group_id"` // returned credential group binding
			} `json:"api_key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Nil(t, resp.Data.APIKey.GroupID)
}

// TestAdminAPIKeyHandler_ResetRateLimitUsage supplies active saved counters and
// starts, then requests a reset. It checks the successful response returns zero
// spending in all windows and absent saved window starts.
func TestAdminAPIKeyHandler_ResetRateLimitUsage(t *testing.T) {
	svc := newAdminUpdateFixture()
	now := time.Now()
	svc.apiKeys[0].Usage5h = 1.2
	svc.apiKeys[0].Usage1d = 3.4
	svc.apiKeys[0].Usage7d = 5.6
	svc.apiKeys[0].Window5hStart = &now
	svc.apiKeys[0].Window1dStart = &now
	svc.apiKeys[0].Window7dStart = &now
	router := setupAPIKeyHandler(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(`{"reset_rate_limit_usage":true}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data struct { // decoded operation response payload
			APIKey struct { // returned credential response object
				Usage5h       float64    `json:"usage_5h"`        // returned five-hour USD spending
				Usage1d       float64    `json:"usage_1d"`        // returned one-day USD spending
				Usage7d       float64    `json:"usage_7d"`        // returned seven-day USD spending
				Window5hStart *time.Time `json:"window_5h_start"` // returned five-hour window start
				Window1dStart *time.Time `json:"window_1d_start"` // returned one-day window start
				Window7dStart *time.Time `json:"window_7d_start"` // returned seven-day window start
			} `json:"api_key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Zero(t, resp.Data.APIKey.Usage5h)
	require.Zero(t, resp.Data.APIKey.Usage1d)
	require.Zero(t, resp.Data.APIKey.Usage7d)
	require.Nil(t, resp.Data.APIKey.Window5hStart)
	require.Nil(t, resp.Data.APIKey.Window1dStart)
	require.Nil(t, resp.Data.APIKey.Window7dStart)
}

// TestAdminAPIKeyHandler_UpdateGroup_ServiceError injects an internal failure
// from the administrative use case. It checks the shared application response
// returns an internal-server status for that supplied error.
func TestAdminAPIKeyHandler_UpdateGroup_ServiceError(t *testing.T) {
	svc := &failingUpdateGroupService{
		adminUpdateFixture: newAdminUpdateFixture(),
		err:                errors.New("internal failure"),
	}
	router := setupAPIKeyHandler(svc)
	body := `{"group_id": 2}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

// TestAdminAPIKeyHandler_UpdateGroup_EmptyBody_NoChange submits an empty JSON
// object for an existing fixture record. It checks a successful envelope
// contains that Key even though no binding or reset was selected.
func TestAdminAPIKeyHandler_UpdateGroup_EmptyBody_NoChange(t *testing.T) {
	router := setupAPIKeyHandler(newAdminUpdateFixture())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Code int      `json:"code"` // response envelope status code
		Data struct { // decoded operation response payload
			APIKey struct { // returned credential response object
				ID int64 `json:"id"` // returned credential record identifier
			} `json:"api_key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Equal(t, int64(10), resp.Data.APIKey.ID)
}

// TestAdminAPIKeyHandler_UpdateGroup_GroupNotActive supplies a GROUP_NOT_ACTIVE
// service failure for a requested target. It checks the HTTP adapter retains
// that reason and maps it to a bad-request status.
func TestAdminAPIKeyHandler_UpdateGroup_GroupNotActive(t *testing.T) {
	svc := &failingUpdateGroupService{
		adminUpdateFixture: newAdminUpdateFixture(),
		err:                apperror.BadRequest("GROUP_NOT_ACTIVE", "target group is not active"),
	}
	router := setupAPIKeyHandler(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(`{"group_id": 5}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "GROUP_NOT_ACTIVE")
}

// TestAdminAPIKeyHandler_UpdateGroup_NegativeGroupID supplies an
// INVALID_GROUP_ID service failure for negative binding input. It checks the
// handler maps the reason to bad request; the fixture supplies domain
// validation's result.
func TestAdminAPIKeyHandler_UpdateGroup_NegativeGroupID(t *testing.T) {
	svc := &failingUpdateGroupService{
		adminUpdateFixture: newAdminUpdateFixture(),
		err:                apperror.BadRequest("INVALID_GROUP_ID", "group_id must be non-negative"),
	}
	router := setupAPIKeyHandler(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(`{"group_id": -5}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "INVALID_GROUP_ID")
}

// setupAPIKeyHandler constructs a router with the administrative update route
// and group DTO conversion. It binds the supplied use case directly, omitting
// production middleware so tests isolate request decoding and response mapping.
func setupAPIKeyHandler(adminSvc KeyAdministration) *gin.Engine {
	router := gin.New()
	h := NewAdminAPIKeyHandler(adminSvc, func(g *routing.Group) *groupdto.Group { return groupdto.GroupFromRouting(apikey.RoutingGroup(g)) })
	router.PUT("/api/v1/admin/api-keys/:id", h.UpdateGroup)
	return router
}

// UpdateManagedFields returns the configured administrative failure for every
// supplied request.
func (f *failingUpdateGroupService) UpdateManagedFields(_ context.Context, _ int64, _ *int64, _ bool) (*apikey.AdminUpdateAPIKeyGroupIDResult, error) {
	return nil, f.err
}

// AdminResetAPIKeyRateLimitUsage clears counters and starts in the retained
// fixture record matching keyID. It returns a shallow copy after mutation, or
// ErrAPIKeyNotFound when the fixture contains no matching ID.
func (s *adminUpdateFixture) AdminResetAPIKeyRateLimitUsage(ctx context.Context, keyID int64) (*apikey.APIKey, error) {
	for i := range s.apiKeys {
		if s.apiKeys[i].ID == keyID {
			s.apiKeys[i].Usage5h = 0
			s.apiKeys[i].Usage1d = 0
			s.apiKeys[i].Usage7d = 0
			s.apiKeys[i].Window5hStart = nil
			s.apiKeys[i].Window1dStart = nil
			s.apiKeys[i].Window7dStart = nil
			k := s.apiKeys[i]
			return &k, nil
		}
	}
	return nil, apikey.ErrAPIKeyNotFound
}

// AdminUpdateAPIKeyGroupID returns a shallow record copy with the requested
// optional binding. Nil retains its group, zero clears it, and other values use
// copied ID storage; the retained fixture record itself is not replaced.
func (s *adminUpdateFixture) AdminUpdateAPIKeyGroupID(ctx context.Context, keyID int64, groupID *int64) (*apikey.AdminUpdateAPIKeyGroupIDResult, error) {
	for i := range s.apiKeys {
		if s.apiKeys[i].ID == keyID {
			k := s.apiKeys[i]
			if groupID != nil {
				if *groupID == 0 {
					k.GroupID = nil
				} else {
					gid := *groupID
					k.GroupID = &gid
				}
			}
			return &apikey.AdminUpdateAPIKeyGroupIDResult{APIKey: &k}, nil
		}
	}
	return nil, apikey.ErrAPIKeyNotFound
}

// UpdateManagedFields combines the fixture binding result with optional
// retained-record window reset. On reset, the reset copy replaces the earlier
// result Key, so this fixture does not model a persisted binding change and
// reset together.
func (s *adminUpdateFixture) UpdateManagedFields(ctx context.Context, id int64, gid *int64, reset bool) (*apikey.AdminUpdateAPIKeyGroupIDResult, error) {
	result, err := s.AdminUpdateAPIKeyGroupID(ctx, id, gid)
	if err != nil {
		return nil, err
	}
	if reset {
		key, err := s.AdminResetAPIKeyRateLimitUsage(ctx, id)
		if err != nil {
			return nil, err
		}
		result.APIKey = key
	}
	return result, nil
}

// newAdminUpdateFixture allocates one active Key record with current UTC
// timestamps for HTTP administration tests.
func newAdminUpdateFixture() *adminUpdateFixture {
	now := time.Now().UTC()
	return &adminUpdateFixture{apiKeys: []apikey.APIKey{{ID: 10, UserID: 1, Key: "sk-test", Name: "test", Status: apikey.StatusActive, CreatedAt: now, UpdatedAt: now}}}
}
