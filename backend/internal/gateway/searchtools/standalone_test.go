package searchtools

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/search/contract"
)

// standaloneStub supplies numbered providers and records
// acquisition/execution/release ordering with configurable failures.
type standaloneStub struct {
	selected       int      // current numbered provider fixture index
	events         []string // observed selection and resource event order
	failures       int      // number of initial execution failures
	selectionError bool     // force initial candidate selection error
	acquireError   bool     // force provider admission error
}

// Select returns a numbered candidate and checks the previous provider's
// exclusion on later invocations. A configured selection error returns
// unavailable; otherwise a missing expected exclusion panics to expose
// incorrect loop state.
func (s *standaloneStub) Select(_ context.Context, _ string, excluded map[int64]struct{}) (Selection, bool, error) {
	s.selected++
	s.events = append(s.events, "select")
	if s.selectionError {
		return Selection{}, false, errors.New("selection failed")
	}
	if s.selected > 1 {
		if _, ok := excluded[int64(s.selected-1)]; !ok {
			panic("previous provider not excluded")
		}
	}
	return Selection{ProviderID: int64(s.selected)}, true, nil
}

// Acquire returns a recording release callback or a configured admission
// error. Tests provide a request lease that owns successful releases, allowing
// event order and repeated cleanup calls to be asserted.
func (s *standaloneStub) Acquire(context.Context, Selection) (func(), bool, error) {
	if s.acquireError {
		return nil, false, errors.New("slot failed")
	}
	s.events = append(s.events, "acquire")
	return func() { s.events = append(s.events, "release") }, true, nil
}

// Execute records forwarding and fails while the numbered provider is within
// the configured failure count. Later candidates return a nonnil response
// fixture and vendor label for successful lease-ownership assertions.
func (s *standaloneStub) Execute(context.Context, int64, StandaloneRequest, string, int) (*contract.SearchResponse, string, error) {
	s.events = append(s.events, "execute")
	if s.selected <= s.failures {
		return nil, "", errors.New("upstream failed")
	}
	return &contract.SearchResponse{}, "grok-native", nil
}

// CanSwitch permits every supplied fixture error to try another provider.
func (s *standaloneStub) CanSwitch(error) bool { return true }

// TestStandaloneLeaseSpansResponseAndReleasesBeforeSwitch fails one provider
// and succeeds on the next. The preceding release must occur before
// reselection, and repeated parent cleanup must release the retained
// successful resource once.
func TestStandaloneLeaseSpansResponseAndReleasesBeforeSwitch(t *testing.T) {
	ports := &standaloneStub{failures: 1}
	lease := scheduler.NewLease(context.Background(), scheduler.ReleaseOnCompletion)
	result, err := RunStandalone(context.Background(), StandaloneRequest{Query: "q"}, "model", 5, ports, lease)
	require.NoError(t, err)
	require.Equal(t, int64(2), result.ProviderID)
	require.Equal(t, []string{"select", "acquire", "execute", "release", "select", "acquire", "execute"}, ports.events)
	// Repeated parent cleanup must retire the successful attempt only once,
	// after response handling has retained that provider resource across the loop return.
	lease.Release()
	lease.Release()
	require.Equal(t, "release", ports.events[len(ports.events)-1])
	require.Len(t, ports.events, 8)
}

// TestStandaloneFourAttemptsAndFirstFailureKinds supplies four execution
// failures and separate initial selection/admission failures. The loop must
// stop at four selected candidates and retain the initial stages for those
// early error cases.
func TestStandaloneFourAttemptsAndFirstFailureKinds(t *testing.T) {
	t.Run("four attempts", func(t *testing.T) {
		p := &standaloneStub{failures: 4}
		lease := scheduler.NewLease(context.Background(), scheduler.ReleaseOnCompletion)
		defer lease.Release()
		_, err := RunStandalone(context.Background(), StandaloneRequest{}, "model", 5, p, lease)
		require.Error(t, err)
		require.Equal(t, 4, p.selected)
	})
	for _, stage := range []string{"selection", "concurrency"} {
		t.Run(stage, func(t *testing.T) {
			p := &standaloneStub{selectionError: stage == "selection", acquireError: stage == "concurrency"}
			lease := scheduler.NewLease(context.Background(), scheduler.ReleaseOnCompletion)
			defer lease.Release()
			_, err := RunStandalone(context.Background(), StandaloneRequest{}, "model", 5, p, lease)
			var failure *StandaloneFailure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, stage, failure.Stage)
			require.Equal(t, 1, p.selected)
		})
	}
}
