package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUserPlatformQuotasCNProvidersMigration inspects the platform quota constraint
// update. It requires dropping the previous constraint and the complete accepted
// platform list including Kimi, Zhipu, and DeepSeek, protecting the SQL used for
// their quota configuration.
func TestUserPlatformQuotasCNProvidersMigration(t *testing.T) {
	content, err := FS.ReadFile("248_allow_cn_user_platform_quotas.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check")
	require.Contains(t, sql,
		"CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'qoder', 'grok', 'kimi', 'zhipu', 'deepseek'))")
}
