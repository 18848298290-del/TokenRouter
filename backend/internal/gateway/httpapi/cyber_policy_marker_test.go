package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// TestCyberPolicyMarkerFirstEventAndTurnReset checks that the first evidence remains until reset allows the next turn
// to record a marker.
func TestCyberPolicyMarkerFirstEventAndTurnReset(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	mark := moderationflow.Mark{Message: " first ", Body: " body ", UpstreamStatus: 403, UpstreamInTok: 3}
	MarkOpsCyberPolicy(c, mark)
	mark.Message = "mutated"
	MarkOpsCyberPolicy(c, moderationflow.Mark{Message: "second", UpstreamInTok: 9})
	stored := GetOpsCyberPolicy(c)
	require.Equal(t, "cyber_policy", stored.Code)
	require.Equal(t, "first", stored.Message)
	require.Equal(t, "body", stored.Body)
	require.Equal(t, 3, stored.UpstreamInTok)
	ClearOpsCyberPolicy(c)
	require.Nil(t, GetOpsCyberPolicy(c))
	usage := &openai.ForwardUsage{InputTokens: 5, OutputTokens: 2}
	payload := []byte(`{"error":{"code":"cyber_policy","message":"blocked"}}`)
	require.True(t, MarkOpenAICyberPolicyEvent(c, payload, http.StatusForbidden, usage))
	usage.InputTokens = 99
	require.Equal(t, 5, GetOpsCyberPolicy(c).UpstreamInTok)
	require.Equal(t, 2, GetOpsCyberPolicy(c).UpstreamOutTok)
	require.False(t, MarkOpenAICyberPolicyEvent(c, []byte(`{"error":{"code":"other"}}`), 500, nil))
	require.Equal(t, 5, GetOpsCyberPolicy(c).UpstreamInTok)
	// A nil HTTP context stores nothing; true still means the upstream event was recognized.
	require.True(t, MarkOpenAICyberPolicyEvent(nil, payload, 403, nil))
	require.Nil(t, GetOpsCyberPolicy(nil))
	ClearOpsCyberPolicy(nil)
}
