package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// marketplaceHTTPGroups supplies one group with provider membership for
// market browsing tests.
type marketplaceHTTPGroups struct{}

// marketplaceHTTPCapacity records capacity reads initiated by marketplace
// HTTP requests.
type marketplaceHTTPCapacity struct {
	calls int // observed capacity reader calls
}

// marketplaceHTTPModels supplies a visible requestable model for capacity
// parameter tests.
type marketplaceHTTPModels struct{}

// ListActive returns one fixture group with an active provider association.
func (marketplaceHTTPGroups) ListActive(context.Context) ([]routing.Group, error) {
	return []routing.Group{{ID: 1, ActiveProviderCount: 1}}, nil
}

// GetGroupCapacityByIDs counts a capacity read and returns the fixture
// group's zero summary. The requested IDs are ignored so the test isolates
// whether the HTTP capacity option initiates a read.
func (s *marketplaceHTTPCapacity) GetGroupCapacityByIDs(context.Context, []int64) (map[int64]routing.GroupCapacitySummary, error) {
	s.calls++
	return map[int64]routing.GroupCapacitySummary{1: {}}, nil
}

// TestMarketplaceCapacityQuery serves default, enabled, disabled, and
// malformed capacity options. It checks response status, reader call count,
// capacity JSON presence, and continued model visibility for successful
// queries.
func TestMarketplaceCapacityQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		query  string // URL capacity query suffix
		status int    // expected HTTP response status
		calls  int    // expected capacity reader calls
	}{
		{"", 200, 1},
		{"?include_capacity=true", 200, 1},
		{"?include_capacity=false", 200, 0},
		{"?include_capacity=invalid", 400, 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			capacity := &marketplaceHTTPCapacity{}
			core := routing.NewMarketplace(marketplaceHTTPGroups{}, nil, marketplaceHTTPModels{}, routing.RequestableResolver{}, nil, capacity, nil, routing.MarketplaceOptions{})
			router := gin.New()
			RegisterPublicMarketplaceRoutes(router.Group("/api/v1"), NewMarketplaceHandler(core, nil))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/marketplace/models"+tc.query, nil))
			require.Equal(t, tc.status, response.Code)
			require.Equal(t, tc.calls, capacity.calls)
			if tc.calls > 0 {
				require.Contains(t, response.Body.String(), `"capacity":`)
			} else {
				require.NotContains(t, response.Body.String(), `"capacity":`)
			}
			if tc.status == 200 {
				require.Contains(t, response.Body.String(), `"id":"model"`)
			}
		})
	}
}

// ResolveRequestableModels returns one fixture model for every group catalog
// query.
func (marketplaceHTTPModels) ResolveRequestableModels(context.Context, *int64, string) routing.RequestableModelsResult {
	return routing.RequestableModelsResult{Models: []routing.RequestableModel{{ID: "model"}}}
}

// Prefetch declines prefetched provider data for the HTTP fixture. The false
// result makes Marketplace resolve the visible group through its single-group
// resolver, which returns the model used by these assertions.
func (marketplaceHTTPModels) Prefetch(context.Context) ([]routing.CatalogueProvider, bool, error) {
	return nil, false, nil
}
