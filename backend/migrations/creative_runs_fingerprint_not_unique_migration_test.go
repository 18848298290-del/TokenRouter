package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreativeRunsFingerprintNotUniqueMigration checks removal of fingerprint uniqueness.
// Idempotency keys govern retries; submitting identical content again must remain possible.
func TestCreativeRunsFingerprintNotUniqueMigration(t *testing.T) {
	content, err := FS.ReadFile("255_creative_runs_fingerprint_not_unique.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	require.Contains(t, sql, "alter table creative_runs drop constraint if exists creative_runs_request_fingerprint_key")
}
