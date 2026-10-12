package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUserPlatformQuotasCNProvidersMigration checks that migration 248 adds kimi,
// zhipu, and deepseek to the user_platform_quotas.platform CHECK constraint.
// Rejecting these platforms would abort inserts that seed their default quotas
// and updates that configure their limits.
func TestUserPlatformQuotasCNProvidersMigration(t *testing.T) {
	content, err := FS.ReadFile("248_allow_cn_user_platform_quotas.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check")
	require.Contains(t, sql,
		"CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'qoder', 'grok', 'kimi', 'zhipu', 'deepseek'))")
}
