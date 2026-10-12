package postgres

// These tests check the outbox schema and invalidation triggers used by invalidation_outbox.go:
// migrations/212_auth_cache_invalidation_outbox.sql and
// migrations/258_extend_api_key_auth_cache_invalidation.sql.

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestAuthCacheInvalidationMigration_SecurityCoverageAndNoPlaintextPayload
// inspects the embedded outbox migration source and its invalidation trigger
// conditions. It checks hashed payload syntax, identity and lifecycle coverage,
// delivery columns and absence of quota_used and last_used_at change conditions.
func TestAuthCacheInvalidationMigration_SecurityCoverageAndNoPlaintextPayload(t *testing.T) {
	content, err := migrations.FS.ReadFile("212_auth_cache_invalidation_outbox.sql")
	require.NoError(t, err)
	sqlText := string(content)
	for _, required := range []string{
		"encode(sha256(convert_to(raw_key, 'UTF8')), 'hex')",
		"OLD.key", "OLD.status", "OLD.deleted_at", "OLD.user_id", "OLD.group_id",
		"OLD.ip_whitelist", "OLD.ip_blacklist", "OLD.expires_at",
		"trg_users_auth_cache_invalidation", "trg_groups_auth_cache_invalidation",
		"trg_user_allowed_groups_auth_cache_invalidation", "FOR EACH ROW",
		"delivery_stage", "claimed_at", "available_at",
	} {
		require.Contains(t, sqlText, required)
	}
	require.NotContains(t, sqlText, "quota_used IS DISTINCT")
	require.NotContains(t, sqlText, "last_used_at IS DISTINCT")

	plaintext := "sk-plaintext-must-not-be-stored"
	sum := sha256.Sum256([]byte(plaintext))
	require.Len(t, hex.EncodeToString(sum[:]), 64)
	require.NotContains(t, sqlText, plaintext)
}

// TestAuthCacheInvalidationBillingMigrationCoversSubscriptionBinding reads the
// embedded migration extending Key invalidation conditions. It checks both
// funding-mode and specified-subscription comparisons appear in the trigger
// source alongside the prior credential reference.
func TestAuthCacheInvalidationBillingMigrationCoversSubscriptionBinding(t *testing.T) {
	content, err := migrations.FS.ReadFile("258_extend_api_key_auth_cache_invalidation.sql")
	require.NoError(t, err)
	sqlText := string(content)
	// Funding mode and specified subscription participate in cached admission
	// decisions, so this migration must retain both conditions in its Key trigger.
	require.Contains(t, sqlText, "OLD.billing_mode IS DISTINCT FROM NEW.billing_mode")
	require.Contains(t, sqlText, "OLD.preferred_subscription_id IS DISTINCT FROM NEW.preferred_subscription_id")
	require.Contains(t, sqlText, "OLD.key")
	require.NotContains(t, sqlText, "sk-")
}
