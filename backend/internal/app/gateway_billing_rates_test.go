package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
)

// TestGatewayGroupRateCacheTTL checks the default and configured TTL.
func TestGatewayGroupRateCacheTTL(t *testing.T) {
	t.Run("resolve_user_group_rate_cache_ttl", func(t *testing.T) {
		require.Equal(t, billing.DefaultGroupRateCacheTTL, gatewayGroupRateCacheTTL(nil))
		cfg := &config.Config{Gateway: config.GatewayConfig{UserGroupRateCacheTTLSeconds: 45}}
		require.Equal(t, 45*time.Second, gatewayGroupRateCacheTTL(cfg))
	})
}
