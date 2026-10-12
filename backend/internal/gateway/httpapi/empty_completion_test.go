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

// Empty Chat and Responses constructors retain different explanations while
// sharing silent-refusal classification and upstream correlation. This fixture
// checks the constructed failure and the single appended Ops event, including
// selected provider identity and the defaults used when no provider is supplied.

// TestEmptyCompletionKeepsOneObservationAndFailure constructs both failures with
// selected or absent providers. Each must return the asserted status, trimmed
// ID, and silent-refusal body, and append one event with the corresponding
// explanation and provider facts.
func TestEmptyCompletionKeepsOneObservationAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string                                                                            // response shape scenario
		create  func(*gin.Context, *UpstreamErrorProvider, string) *forward.UpstreamFailoverError // matching failure constructor
		message string                                                                            // expected Ops explanation
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
