package app

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
)

// Gateway handlers retrieve authorization values from the Gin keys populated
// by middleware. A context populated with a key and subscription exercises the
// two reader functions and checks that each retains the identifier needed by
// its downstream consumer.

// TestAPIKeyAndSubscriptionFromContext populates a Gin context with both
// authorization values. Key and subscription readers must report the values
// present and return their respective identifiers.
func TestAPIKeyAndSubscriptionFromContext(t *testing.T) {
	c := &gin.Context{}

	key := &apikey.APIKey{ID: 1}
	c.Set(string(keyhttp.ContextKeyAPIKey), key)
	gotKey, ok := keyhttp.GetAPIKeyFromContext(c)
	require.True(t, ok)
	require.Equal(t, int64(1), gotKey.ID)

	sub := &billing.UserSubscription{ID: 2}
	c.Set(string(gatewayhttp.ContextKeySubscription), sub)
	gotSub, ok := gatewayhttp.SubscriptionFromContext(c)
	require.True(t, ok)
	require.Equal(t, int64(2), gotSub.ID)
}
