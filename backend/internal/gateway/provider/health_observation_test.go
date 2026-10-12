package provider

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// TestUpdateSessionWindow_EmptyHeadersSkipsOptionalDependencies checks that empty observations tolerate an absent health service.
func TestUpdateSessionWindow_EmptyHeadersSkipsOptionalDependencies(t *testing.T) {
	var service *provideradapter.UpstreamHealth

	for name, headers := range map[string]http.Header{"nil": nil, "empty": {}, "unrelated": {"Content-Type": {"application/json"}}} {
		t.Run(name, func(t *testing.T) {
			require.NotPanics(t, func() {
				ObserveExecutionSessionWindow(context.Background(), service, nil,

					headers)
			})
		})
	}
}

// TestHealthObservationKeepsRawAndEffectiveModel distinguishes an explicit empty model from no model and retains the
// effective cooldown model.
func TestHealthObservationKeepsRawAndEffectiveModel(t *testing.T) {
	ctx := requeststate.WithHealthModel(context.Background(), []string{"stored-model"})
	ctx = requeststate.WithOpenAIImagesEndpoint(requeststate.WithThinkingEnabled(ctx, false))
	for _, models := range [][]string{nil, {""}, {" mapped-model "}} {
		input := HealthObservationFromContext(ctx, http.StatusNotFound, nil, []byte("failure"), models)
		require.Equal(t, len(models) > 0, input.ModelProvided)
		require.Equal(t, requeststate.HealthModel(ctx, models), input.EffectiveModel)
		if len(models) > 0 {
			require.Equal(t, models[0], input.Model)
		}
		require.True(t, input.ImagesEndpoint)
		require.NotNil(t, input.Thinking)
		require.False(t, *input.Thinking)
	}
}
