package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// TestPricingRoutesRejectPolicyFieldsAndRemoveOldEndpoints sends retired
// policy fields to strict price-config binding. It checks each request fails
// before service use and confirms removed channel and default-model-list
// paths return 404.
func TestPricingRoutesRejectPolicyFieldsAndRemoveOldEndpoints(t *testing.T) {
	router := gin.New()
	// Keep the service absent so accepting a retired field would reach an
	// unusable dependency instead of returning the required binding error.
	handler := NewPricingHandler(nil, &routing.PricingCatalog{})
	RegisterPricingRoutes(router.Group("/api/v1/admin"), handler)
	for _, field := range []string{"model_mapping", "restrict_models", "features_config", "features", "apply_pricing_to_provider_stats"} {
		response := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/pricing/configs", strings.NewReader(`{"name":"price","`+field+`":null}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, req)
		require.Equal(t, http.StatusBadRequest, response.Code, field)
	}
	for _, path := range []string{"/channels", "/channels/model-pricing", "/channels/pricing/sync-models", "/pricing/defaults/models"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin"+path, nil))
		require.Equal(t, http.StatusNotFound, response.Code, path)
	}
}
