package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// TestEmptyCompletionKeepsOneObservationAndFailure checks distinct Chat and Responses messages with stable failure
// codes and request IDs.
func TestEmptyCompletionKeepsOneObservationAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		create  func(*gin.Context, *UpstreamErrorProvider, string) *forward.UpstreamFailoverError
		message string
	}{
		{"chat", NewOpenAISilentRefusalFailoverError, openAISilentRefusalUpstreamMessage},
		{"responses", NewOpenAIResponsesEmptyCompletedFailoverError, openAIResponsesEmptyCompletedMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, selected := range []*UpstreamErrorProvider{nil, {ID: 7, Name: "fixture", Platform: "openai"}} {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				failure := tc.create(c, selected, " request-7 ")
				require.Equal(t, http.StatusBadGateway, failure.StatusCode)
				require.Equal(t, "request-7", http.Header(failure.ResponseHeaders).Get("X-Request-Id"))
				require.True(t, forward.IsOpenAISilentRefusalErrorBody(failure.ResponseBody))
				value, present := c.Get(OpsUpstreamErrorsKey)
				require.True(t, present)
				events, valid := value.([]*ops.OpsUpstreamErrorEvent)
				require.True(t, valid)
				require.Len(t, events, 1)
				require.Equal(t, tc.message, events[0].Message)
				require.Equal(t, "request-7", events[0].UpstreamRequestID)
				require.Equal(t, "openai", events[0].Platform)
				if selected == nil {
					require.Zero(t, events[0].ProviderID)
				} else {
					require.Equal(t, selected.ID, events[0].ProviderID)
					require.Equal(t, selected.Name, events[0].ProviderName)
				}
			}
		})
	}
}
