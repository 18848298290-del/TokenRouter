package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	billingcore "github.com/TokenFlux/TokenRouter/internal/billing"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Public usage displays resolved funding source alongside request range
// information. These cases check injected-calendar DST expansion and explicit
// offsets, then inspect subscription and wallet response shapes, including
// unavailable selected grants and their exposed weekly-window anchor.

// TestPublicUsageDateRangeInjectedCalendar parses a fall-DST date using
// injected server calendars and an invalid user zone. It requires next local
// midnight and a twenty-five-hour New York range.
func TestPublicUsageDateRangeInjectedCalendar(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	for _, loc := range []*time.Location{newYork, time.UTC} {
		t.Run(loc.String(), func(t *testing.T) {
			h := NewPublicUsageHandler(nil, nil, nil, nil, PublicUsageContext{}, timezone.NewCalendar(loc))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/?start_date=2026-11-01&end_date=2026-11-01&timezone=invalid-zone", nil)
			start, end := h.parseUsageDateRange(c)
			require.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, loc), start)
			require.Equal(t, time.Date(2026, 11, 2, 0, 0, 0, 0, loc), end)
			if loc == newYork {
				require.Equal(t, 25*time.Hour, end.Sub(start))
			}
		})
	}
}

// TestPublicUsageDateRangeExplicitOffset supplies start and end timestamps
// with their own offsets across the spring shift. It requires those exact UTC
// instants rather than date expansion.
func TestPublicUsageDateRangeExplicitOffset(t *testing.T) {
	h := NewPublicUsageHandler(nil, nil, nil, nil, PublicUsageContext{}, timezone.NewCalendar(time.UTC))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/?start_date=2026-03-08T01:00:00-05:00&end_date=2026-03-08T03:00:00-04:00", nil)
	start, end := h.parseUsageDateRange(c)
	require.Equal(t, time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC), start.UTC())
	require.Equal(t, time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), end.UTC())
}

// TestUsageUnrestrictedIncludesWeeklyWindowStart supplies a selected
// subscription with a weekly anchor. It requires successful JSON and a
// decoded anchor equal to the supplied instant.
func TestUsageUnrestrictedIncludesWeeklyWindowStart(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)

	weeklyWindowStart := time.Date(2026, time.July, 13, 0, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	c.Set(string(gatewayhttp.ContextKeySubscription), &billingcore.UserSubscription{
		WeeklyWindowStart: &weeklyWindowStart,
	})

	handler := publicUsageContractHandler()
	handler.UsageUnrestricted(
		c,
		context.Background(),
		&apikey.APIKey{Group: &routing.Group{
			Name: "Weekly plan",
		}},
		authctx.AuthSubject{},
		nil,
		nil,
		nil,
		"USD",
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Subscription struct {
			WeeklyWindowStart *time.Time `json:"weekly_window_start"` // optional weekly spending anchor
		} `json:"subscription"` // the selected grant response body
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.NotNil(t, response.Subscription.WeeklyWindowStart)
	require.True(t, weeklyWindowStart.Equal(*response.Subscription.WeeklyWindowStart))
}

// TestUsageUnrestrictedPreferredSubscriptionDoesNotExposeBalance supplies an
// unavailable selected grant beside wallet funds. It requires subscription
// source and preferred ID while the wallet field remains absent.
func TestUsageUnrestrictedPreferredSubscriptionDoesNotExposeBalance(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	preferredID := int64(99)
	c.Set(string(gatewayhttp.ContextKeyAPIKeyBilling), &billingcore.APIKeyBillingContext{
		Mode: apikey.APIKeyBillingModeSubscription, Source: "subscription", Available: false,
	})

	publicUsageContractHandler().UsageUnrestricted(
		c,
		context.Background(),
		&apikey.APIKey{
			BillingMode:             apikey.APIKeyBillingModeSubscription,
			PreferredSubscriptionID: &preferredID,
			User:                    &identity.User{Balance: 123},
		},
		authctx.AuthSubject{},
		nil,
		nil,
		nil,
		"USD",
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	billing, ok := response["billing"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "subscription", billing["source"])
	require.Equal(t, false, billing["available"])
	require.Equal(t, float64(preferredID), billing["preferred_subscription_id"])
	_, hasBalance := response["balance"]
	require.False(t, hasBalance)
}

// TestUsageUnrestrictedBalanceModeDoesNotExposeSubscription supplies resolved
// wallet funding. It requires wallet source and the loaded balance while a
// subscription response field remains absent.
func TestUsageUnrestrictedBalanceModeDoesNotExposeSubscription(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	c.Set(string(gatewayhttp.ContextKeyAPIKeyBilling), &billingcore.APIKeyBillingContext{
		Mode: apikey.APIKeyBillingModeBalance, Source: "balance", Available: true,
	})

	publicUsageContractHandler().UsageUnrestricted(
		c,
		context.Background(),
		&apikey.APIKey{BillingMode: apikey.APIKeyBillingModeBalance, User: &identity.User{Balance: 12.5}},
		authctx.AuthSubject{},
		nil,
		nil,
		nil,
		"USD",
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	billing, ok := response["billing"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "balance", billing["source"])
	require.Equal(t, float64(12.5), response["balance"])
	_, hasSubscription := response["subscription"]
	require.False(t, hasSubscription)
}

// publicUsageContractHandler constructs the public response fixture with
// gateway funding callbacks. It binds billing and subscription readers plus
// the process calendar; these cases call funding formatting directly.
func publicUsageContractHandler() *PublicUsageHandler {
	return NewPublicUsageHandler(nil, nil, nil, nil, PublicUsageContext{
		Billing: func(c *gin.Context) (*billingcore.APIKeyBillingContext, bool) {
			return gatewayhttp.GetAPIKeyBillingContext(c)
		},
		Subscription: gatewayhttp.SubscriptionFromContext,
	}, timezone.NewCalendar(time.Local))
}
