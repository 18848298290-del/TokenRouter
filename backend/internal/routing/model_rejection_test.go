package routing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Rejection suggestions read provider rules lazily so ineligible candidates
// need no catalogue or support work. The probe records callback order and
// compares absent suggestions with an explicitly configured list filtered to
// empty, including blank-request short-circuiting before provider access.

// rejectionRulesProbe supplies eligibility and model results while recording
// rule reads.
type rejectionRulesProbe struct {
	// eligible supplies IsSchedulable and mixed supplies IsMixedSchedulingEnabled.
	// The call log identifies which callback was read so tests can distinguish
	// current eligibility from legacy policy access.
	//
	// eligible: provider scheduling eligibility. mixed: legacy mixed scheduling.
	eligible, mixed bool
	// configured supplies the candidate slice returned by the probe. Tests keep
	// its storage unchanged while suggestion filtering records its lazy reads.
	configured []string // shared configured candidate names
	allowed    bool     // configured model support result
	// calls receives every callback name in invocation order. Each probe requires
	// a nonnil pointer so the test can inspect shared evidence after rejection
	// resolution.
	calls *[]string
}

// IsSchedulable records the eligibility query and returns its configured flag.
func (r rejectionRulesProbe) IsSchedulable() bool {
	*r.calls = append(*r.calls, "eligible")
	return r.eligible
}

// IsMixedSchedulingEnabled records the legacy mixed flag query and returns its
// fixture value.
func (r rejectionRulesProbe) IsMixedSchedulingEnabled() bool {
	*r.calls = append(*r.calls, "mixed")
	return r.mixed
}

// GetConfiguredRequestModels records a configured-model query and returns its
// shared fixture slice. Tests keep the slice stable while using call order to
// detect eager suggestion reads.
func (r rejectionRulesProbe) GetConfiguredRequestModels() []string {
	*r.calls = append(*r.calls, "configured")
	return r.configured
}

// IsModelSupported records a support check and returns its configured result.
func (r rejectionRulesProbe) IsModelSupported(string) bool {
	*r.calls = append(*r.calls, "supports")
	return r.allowed
}

// TestModelRejectionPreservesLazyReadAndEmptyShape supplies ineligible,
// filtered, and blank-request cases to rejection helpers. Recorded calls must
// stay lazy, and configured candidates filtered to empty must retain an
// allocated result.
func TestModelRejectionPreservesLazyReadAndEmptyShape(t *testing.T) {
	t.Run("不读取不合资格提供商", func(t *testing.T) {
		var calls []string
		sources := []ModelRejectionSource{{Platform: "qoder", Rules: rejectionRulesProbe{calls: &calls}, Defaults: func(string) ([]string, error) { t.Fatal("不应读取默认目录"); return nil, nil }}}
		require.Nil(t, AvailableModelsForRejection(sources, "qoder"))
		require.Equal(t, []string{"eligible"}, calls)
	})
	t.Run("显式模型过滤为空仍为非nil", func(t *testing.T) {
		var calls []string
		sources := []ModelRejectionSource{{Platform: "qoder", Rules: rejectionRulesProbe{eligible: true, configured: []string{"blocked"}, calls: &calls}, Defaults: func(string) ([]string, error) { t.Fatal("不应读取默认目录"); return nil, nil }}}
		result := AvailableModelsForRejection(sources, "qoder")
		require.NotNil(t, result)
		require.Empty(t, result)
		require.Equal(t, []string{"eligible", "configured", "supports"}, calls)
	})
	t.Run("空请求不读提供商规则", func(t *testing.T) {
		var calls []string
		require.NoError(t, NewGroupModelRejection("qoder", " ", []ModelRejectionSource{{Platform: "qoder", Rules: rejectionRulesProbe{calls: &calls}}}))
		require.Empty(t, calls)
	})
}
