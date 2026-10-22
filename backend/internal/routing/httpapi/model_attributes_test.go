package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// attributeHTTPStore keeps one submitted profile for model-attribute HTTP
// fixture reads.
type attributeHTTPStore struct {
	// saved retains the exact submitted profile pointer; fixture reads share
	// that pointer after Save records it.
	saved *routing.ModelAttributeConfig // retained submitted profile pointer
}

// List returns an empty nonnil profile list for the HTTP fixture.
func (r *attributeHTTPStore) List(context.Context) ([]routing.ModelAttributeConfig, error) {
	return []routing.ModelAttributeConfig{}, nil
}

// Get returns the last submitted profile for any fixture ID. It shares the
// stored pointer and may return nil before Save, allowing HTTP assertions to
// inspect the submitted value.
func (r *attributeHTTPStore) Get(context.Context, int64) (*routing.ModelAttributeConfig, error) {
	return r.saved, nil
}

// ForGroup returns the retained profile for every fixture group lookup. It
// ignores the requested group ID and shares the same pointer stored by Save,
// including nil before any save.
func (r *attributeHTTPStore) ForGroup(context.Context, int64) (*routing.ModelAttributeConfig, error) {
	return r.saved, nil
}

// ForGroups maps each requested group ID to the fixture's retained profile
// pointer. Each result shares that pointer, and empty input produces an empty
// nonnil map for batch display queries.
func (r *attributeHTTPStore) ForGroups(_ context.Context, ids []int64) (map[int64]*routing.ModelAttributeConfig, error) {
	result := make(map[int64]*routing.ModelAttributeConfig, len(ids))
	for _, id := range ids {
		result[id] = r.saved
	}
	return result, nil
}

// Save retains the submitted profile pointer for later fixture reads. It does
// not assign a database ID or copy nested values, so HTTP assertions observe
// the service's submitted configuration directly.
func (r *attributeHTTPStore) Save(_ context.Context, c *routing.ModelAttributeConfig) error {
	r.saved = c
	return nil
}

// Delete acknowledges fixture removal while retaining the submitted profile
// for inspection.
func (r *attributeHTTPStore) Delete(context.Context, int64) error { return nil }

// TestModelAttributeHTTPReadWriteAndAuthorization registers attribute routes
// behind fixture administrator middleware. It checks authorization, catalog
// read/update separation, reset create IDs, retained false attributes, and
// rejection of a malformed update route ID.
func TestModelAttributeHTTPReadWriteAndAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &attributeHTTPStore{}
	updates := 0
	service := &routing.ModelAttributeService{Repo: store, Catalog: routing.ModelAttributeCatalog{
		Snapshot: func() routing.ModelAttributeSnapshot {
			return routing.ModelAttributeSnapshot{Version: "one", Items: []modelcatalog.Entry{{Model: "custom", Provider: "original", Source: "models.dev"}}}
		},
		Lookup: func(string) modelcatalog.Attributes { return modelcatalog.Attributes{} },
		Update: func() error { updates++; return nil },
	}}
	router := gin.New()
	admin := router.Group("/api/v1/admin", func(c *gin.Context) {
		if c.GetHeader("X-Test-Admin") != "yes" {
			c.AbortWithStatus(403)
		}
	})
	RegisterModelAttributeRoutes(admin, NewModelAttributeHandler(service, nil))
	call := func(method, path, body string, authorized bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if authorized {
			req.Header.Set("X-Test-Admin", "yes")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	require.Equal(t, 403, call("GET", "/api/v1/admin/model-attributes/defaults", "", false).Code)
	query := call("GET", "/api/v1/admin/model-attributes/defaults?search=custom", "", true)
	require.Equal(t, 200, query.Code)
	require.Contains(t, query.Body.String(), `"version":"one"`)
	require.Zero(t, updates)
	require.Equal(t, 200, call("POST", "/api/v1/admin/model-attributes/defaults/update", "", true).Code)
	require.Equal(t, 1, updates)
	created := call("POST", "/api/v1/admin/model-attributes/configs", `{"id":99,"name":"display","status":"active","group_ids":[1],"rules":[{"models":["custom"],"attributes":{"tool_call":false}}]}`, true)
	require.Equal(t, 200, created.Code)
	require.Zero(t, store.saved.ID)
	require.False(t, *store.saved.Rules[0].Attributes.ToolCall)
	require.Equal(t, 400, call("PUT", "/api/v1/admin/model-attributes/configs/no", "{}", true).Code)
}

// TestModelAttributeDefaultsUsePagedIndex queries true and false reasoning
// entries through an injected index. It checks total=2, the indexed version,
// and no bare b model in the page, and fails if Defaults requests a snapshot
// copy.
func TestModelAttributeDefaultsUsePagedIndex(t *testing.T) {
	catalog, err := modelcatalog.Parse([]byte(`{"providers":{"test":{"models":{"a":{"reasoning":true},"b":{"reasoning":false}}}}}`))
	require.NoError(t, err)
	index := modelcatalog.NewIndex(catalog)
	service := &routing.ModelAttributeService{Catalog: routing.ModelAttributeCatalog{
		Index: func() (modelcatalog.Index, routing.ModelAttributeSnapshot) {
			return index, routing.ModelAttributeSnapshot{Version: "indexed"}
		},
		Snapshot: func() routing.ModelAttributeSnapshot {
			t.Fatal("unexpected full directory copy")
			return routing.ModelAttributeSnapshot{}
		},
	}}
	handler := NewModelAttributeHandler(service, nil)
	engine := gin.New()
	engine.GET("/defaults", handler.Defaults)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/defaults?capability=reasoning&page_size=1", nil))
	require.Equal(t, 200, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"total":2`)
	require.Contains(t, recorder.Body.String(), `"version":"indexed"`)
	require.NotContains(t, recorder.Body.String(), `"model":"b"`)
}
