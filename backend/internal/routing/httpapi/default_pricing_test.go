package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// default_pricing_test.go exercises manual catalogue refresh separately from reads.
// Counting callbacks establish the HTTP behavior, while operation schema checks
// keep the MCP description consistent with a refresh that accepts no configuration body.

// TestDefaultPricingManualUpdateIsSeparateFromQuery serves catalog reads and
// explicit refreshes with counting callbacks. It checks that GET reads
// without updating, POST updates without reading, and updater failure or
// absence returns 502 or 503.
func TestDefaultPricingManualUpdateIsSeparateFromQuery(t *testing.T) {
	updates, reads := 0, 0
	var updateErr error
	catalog := &routing.PricingCatalog{
		Snapshot: func() routing.DefaultPricingSnapshot {
			reads++
			return routing.DefaultPricingSnapshot{}
		},
		Update: func() error {
			updates++
			return updateErr
		},
	}
	router := gin.New()
	RegisterPricingRoutes(router.Group("/api/v1/admin"), NewPricingHandler(nil, catalog))
	query := httptest.NewRecorder()
	router.ServeHTTP(query, httptest.NewRequest(http.MethodGet, "/api/v1/admin/pricing/defaults", nil))
	require.Equal(t, http.StatusOK, query.Code)
	require.Equal(t, 1, reads)
	require.Zero(t, updates, "普通刷新只能读取已加载目录")
	update := httptest.NewRecorder()
	router.ServeHTTP(update, httptest.NewRequest(http.MethodPost, "/api/v1/admin/pricing/defaults/update", nil))
	require.Equal(t, http.StatusOK, update.Code)
	require.True(t, gjson.Get(update.Body.String(), "data.updated").Bool())
	require.Equal(t, 1, updates)
	require.Equal(t, 1, reads)
	updateErr = errors.New("remote unavailable")
	failed := httptest.NewRecorder()
	router.ServeHTTP(failed, httptest.NewRequest(http.MethodPost, "/api/v1/admin/pricing/defaults/update", nil))
	require.Equal(t, http.StatusBadGateway, failed.Code)
	catalog.Update = nil
	unavailable := httptest.NewRecorder()
	router.ServeHTTP(unavailable, httptest.NewRequest(http.MethodPost, "/api/v1/admin/pricing/defaults/update", nil))
	require.Equal(t, http.StatusServiceUnavailable, unavailable.Code)
}

// TestDefaultPricingFiltersPaginationAndZero pages a filtered snapshot
// containing a declared free price and another platform. It checks sorted
// pagination, preserved zero JSON value, and case-insensitive model search.
func TestDefaultPricingFiltersPaginationAndZero(t *testing.T) {
	zero := 0.0
	catalog := &routing.PricingCatalog{Snapshot: func() routing.DefaultPricingSnapshot {
		return routing.DefaultPricingSnapshot{Prices: []pricing.DefaultModelPrice{
			{Model: "z", Platform: "openai", BillingMode: "token", PriceStatus: "priced", Prices: []pricing.DefaultPriceValue{{Key: "input", Value: &zero, Unit: "USD/MTok"}}},
			{Model: "a", Platform: "openai", BillingMode: "token", PriceStatus: "unpriced"},
			{Model: "a-image", Platform: "grok", BillingMode: "image", PriceStatus: "priced"},
		}}
	}}
	router := gin.New()
	RegisterPricingRoutes(router.Group("/api/v1/admin"), NewPricingHandler(nil, catalog))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/pricing/defaults?platform=openai&billing_mode=token&page=2&page_size=1", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, int64(2), gjson.Get(response.Body.String(), "data.total").Int())
	require.Equal(t, "z", gjson.Get(response.Body.String(), "data.items.0.model").String())
	require.True(t, gjson.Get(response.Body.String(), "data.items.0.prices.0.value").Exists())
	require.Equal(t, 0.0, gjson.Get(response.Body.String(), "data.items.0.prices.0.value").Float())
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/pricing/defaults?search=A-IMAGE", nil))
	require.Equal(t, int64(1), gjson.Get(response.Body.String(), "data.total").Int())
}

// TestDefaultPricingRefreshCatalogueRejectsGroupUpdates checks the operation's actual bodyless HTTP behavior.
// A refresh remains callable without arguments, while unrelated group configuration cannot be silently accepted by MCP.
func TestDefaultPricingRefreshCatalogueRejectsGroupUpdates(t *testing.T) {
	op := ManagementOperations["pricing.defaults.update.create"]
	require.NoError(t, op.Validate(map[string]any{}))
	require.Error(t, op.Validate(map[string]any{"body": map[string]any{"rate_multiplier": float64(2)}}))
}
