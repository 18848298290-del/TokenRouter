package testkit

import (
	_ "embed"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymentpostgres "github.com/TokenFlux/TokenRouter/internal/payment/postgres"
)

const LegacyEncryptionKey = "0123456789abcdef0123456789abcdef"

// Fixed historical ciphertext tests legacy decryption independently of the current encryption implementation.
//
//go:embed testdata/legacy_webhook_configs.json
var legacyConfigsJSON []byte

func LegacyConfig(t *testing.T, name string) string {
	t.Helper()
	var fixtures map[string]string
	require.NoError(t, json.Unmarshal(legacyConfigsJSON, &fixtures))
	value, ok := fixtures[name]
	require.True(t, ok, "unknown legacy webhook fixture %q", name)
	return value
}

// LegacyLoadBalancer uses the fixed test key matching the embedded historical ciphertext.
func LegacyLoadBalancer(client *dbent.Client) payment.LoadBalancer {
	return payment.NewDefaultLoadBalancer(paymentpostgres.NewInstanceStore(client), []byte(LegacyEncryptionKey))
}
