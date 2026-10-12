package moderation

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/notification"
)

// TestBuildContentModerationAccountDisabledEmailBody_ContainsBanDetails
// supplies a threshold-reaching observation and HTML-sensitive branding. It
// checks the disabled-state message, count, displayed category and score, and
// escaped site name in the generated body.
func TestBuildContentModerationAccountDisabledEmailBody_ContainsBanDetails(t *testing.T) {
	userID := int64(1001)
	cfg := defaultContentModerationConfig()
	cfg.BanThreshold = 10
	body := buildContentModerationAccountDisabledEmailBody("TokenRouter <Admin>", &ContentModerationLog{
		UserID:          &userID,
		UserEmail:       "user@example.com",
		GroupName:       "vip_2",
		HighestCategory: "sexual",
		HighestScore:    0.926,
		ViolationCount:  10,
	}, cfg)

	require.Contains(t, body, "账户已被自动禁用")
	require.Contains(t, body, "封禁详情")
	require.Contains(t, body, "账户当前处于封禁状态，所有 API 请求将被拒绝")
	require.Contains(t, body, "10 次（阈值 10）")
	require.Contains(t, body, "sexual / 0.926")
	require.Contains(t, body, "TokenRouter &lt;Admin&gt;")
}

// buildContentModerationAccountDisabledEmailBody converts moderation values for
// the notification HTML formatter. A nil log produces an empty body through
// that formatter; a present log requires configuration for its displayed
// threshold.
func buildContentModerationAccountDisabledEmailBody(site string, v *ContentModerationLog, c *ContentModerationConfig) string {
	return notification.BuildContentModerationAccountDisabledEmailBody(site, riskLog(v), riskPolicy(c))
}
