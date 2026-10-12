package selection

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// TestSchedulerRuntimeBindingKeepsSharedState checks that all three selectors and diagnostics share feedback and settings.
func TestSchedulerRuntimeBindingKeepsSharedState(t *testing.T) {
	feedback := schedulercore.NewRuntimeStats(time.Now)
	settings := schedulercore.NewParameters(schedulercore.NewSettingsRuntime(schedulercore.Diagnostics{}), nil, schedulercore.DefaultParameters())
	shared := Shared{Feedback: feedback, Parameters: settings}
	messages := NewGeneric(GenericDependencies{Shared: shared}, DefaultOptions())
	openai := NewCompatible(CompatibleDependencies{Shared: shared}, DefaultOptions())
	gemini := NewGemini(GeminiDependencies{Shared: shared}, DefaultOptions())
	diagnostics := NewDiagnostics(nil, shared, messages, openai)
	require.Same(t, feedback, messages.advancedSchedulerStats())
	require.Same(t, feedback, openai.openaiProviderStats)
	require.Same(t, feedback, gemini.advancedSchedulerStats())
	require.Same(t, feedback, diagnostics.feedback)
	require.Same(t, settings, messages.schedulerParameters)
	require.Same(t, settings, openai.schedulerParameters)
	require.Same(t, settings, gemini.schedulerParameters)
	require.Same(t, settings, diagnostics.schedulerParameters)
}
