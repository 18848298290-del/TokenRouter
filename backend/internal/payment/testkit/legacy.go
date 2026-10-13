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

// Historical payment instance ciphertext uses a fixed thirty-two-byte key and
// named stored configuration examples. The embedded fixture lets compatibility
// tests read those strings directly, while LegacyLoadBalancer supplies the
// matching raw key bytes to the production configuration reader.

// LegacyEncryptionKey supplies the thirty-two ASCII key bytes used by the
// embedded historical provider ciphertext fixtures.
const LegacyEncryptionKey = "0123456789abcdef0123456789abcdef"

// legacyConfigsJSON embeds fixed historical provider ciphertext strings for
// named compatibility scenarios. Fixtures remain independent of encryption
// performed during the current test run.
//
//go:embed testdata/legacy_webhook_configs.json
var legacyConfigsJSON []byte

// LegacyConfig decodes and returns the named historical ciphertext fixture for
// a test. JSON errors or unknown names fail through require assertions
// attributed to the caller, and the stored string is returned as supplied.
func LegacyConfig(t *testing.T, name string) string {
	t.Helper()
	var fixtures map[string]string
	require.NoError(t, json.Unmarshal(legacyConfigsJSON, &fixtures))
	value, ok := fixtures[name]
	require.True(t, ok, "unknown legacy webhook fixture %q", name)
	return value
}

// LegacyLoadBalancer builds the production selector with an Ent instance
// reader and LegacyEncryptionKey as raw key bytes. Stored encrypted instances
// can then be read using the fixture's matching deployment key.
func LegacyLoadBalancer(client *dbent.Client) payment.LoadBalancer {
	return payment.NewDefaultLoadBalancer(paymentpostgres.NewInstanceStore(client), []byte(LegacyEncryptionKey))
}
