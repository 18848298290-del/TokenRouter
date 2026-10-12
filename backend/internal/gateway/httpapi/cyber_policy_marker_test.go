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

// Policy marker lookup exposes the current turn's first saved evidence and
// treats a typed nil reset as absence. This scenario checks normalized text,
// copied input counts, reset behavior, and the distinction between event
// recognition and whether an HTTP context can store that evidence.

// TestCyberPolicyMarkerFirstEventAndTurnReset records competing markers and then
// clears the turn. Saved text and input counts must remain from the first
// marker, fresh parsed usage must remain after caller mutation, and recognition
// must still return true for a nil context.
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
