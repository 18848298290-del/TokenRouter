package media

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// embeddingPortsStub scripts embedding attempts and records observable
// callback order.
type embeddingPortsStub struct {
	// outcomes is indexed by selected minus one and retains result pointers.
	// Tests supply sufficient entries and execute forwarding and assertions
	// serially.
	outcomes []EmbeddingOutcome // shared embedding outcome script
	// selected determines candidate IDs and script position; acquired,
	// released, reported, completed and switches count later callbacks. Serial
	// assertions relate their totals to the events sequence for admission and
	// recovery.
	selected, acquired, released, reported, completed, switches int // selection sequence number, admitted callback count, release callback invocations, scheduling feedback calls, successful completion calls and adapter transition calls
	// events records forward, release, report and complete under serial
	// execution. Tests compare the entire sequence to protect slot release
	// before feedback and completion.
	events []string // ordered fixture callback event labels
	// bodies stores a separate byte copy per ForwardEmbedding call. Retry
	// assertions can compare each payload independently of the original
	// request storage or later attempts.
	bodies [][]byte // owned forwarded request byte snapshots
	// exclusions stores separately copied maps before each selection result.
	// The fixture records rejected IDs for assertions but returns increasing
	// candidate IDs rather than filtering them itself.
	exclusions []map[int64]struct{} // owned selection exclusion snapshots
	// gone and denyAcquire independently select early-exit paths: ClientGone
	// reports gone, and AcquireEmbedding rejects when denyAcquire is true.
	// They do not cancel a context or operate a real wait queue.
	gone, denyAcquire bool // adapter reports departure and fixture rejects admission
	// selectErr ends SelectEmbedding after its call count and copied
	// exclusions are recorded. Nil permits returning the next increasing
	// fixture provider ID.
	selectErr error // optional injected selection error
}

// SelectEmbedding copies exclusions and advances the candidate and script
// index. It returns selectErr when injected; otherwise the selection count
// supplies the provider ID without enforcing the exclusion set.
func (p *embeddingPortsStub) SelectEmbedding(_ context.Context, excluded map[int64]struct{}) (provider.ProviderSnapshot, bool, error) {
	p.selected++
	copySet := make(map[int64]struct{}, len(excluded))
	for id := range excluded {
		copySet[id] = struct{}{}
	}
	p.exclusions = append(p.exclusions, copySet)
	if p.selectErr != nil {
		return provider.ProviderSnapshot{}, false, p.selectErr
	}
	return provider.ProviderSnapshot{ID: int64(p.selected)}, true, nil
}

// AcquireEmbedding returns false when denyAcquire is set, before recording
// admission. Otherwise its release callback records both a count and
// release event, allowing serial ordering assertions without a real
// provider slot.
func (p *embeddingPortsStub) AcquireEmbedding(context.Context, provider.ProviderSnapshot) (func(), bool) {
	if p.denyAcquire {
		return nil, false
	}
	p.acquired++
	return func() { p.released++; p.events = append(p.events, "release") }, true
}

// ForwardEmbedding copies the supplied body, records a forward event and
// returns the selected script entry. Tests provide enough outcomes, while
// copied request bytes remain independent for retry payload assertions.
func (p *embeddingPortsStub) ForwardEmbedding(_ context.Context, _ provider.ProviderSnapshot, body []byte) EmbeddingOutcome {
	p.bodies = append(p.bodies, append([]byte(nil), body...))
	p.events = append(p.events, "forward")
	return p.outcomes[p.selected-1]
}

// ReportEmbedding increments feedback observations and appends the
// report-order event.
func (p *embeddingPortsStub) ReportEmbedding(context.Context, provider.ProviderSnapshot, *EmbeddingResult, bool, error) {
	p.reported++
	p.events = append(p.events, "report")
}

// CompleteEmbedding records successful completion count and its
// callback-order event.
func (p *embeddingPortsStub) CompleteEmbedding(context.Context, provider.ProviderSnapshot, *EmbeddingResult) {
	p.completed++
	p.events = append(p.events, "complete")
}

// SwitchEmbedding increments the fixture count of provider transition
// bookkeeping callbacks.
func (p *embeddingPortsStub) SwitchEmbedding(provider.ProviderSnapshot) { p.switches++ }

// ObserveEmbedding accepts loop events without adding fixture state or
// effects.
func (p *embeddingPortsStub) ObserveEmbedding(EmbeddingEvent) {}

// ClientGone returns the fixture's injected client-departure flag to the
// loop.
func (p *embeddingPortsStub) ClientGone() bool { return p.gone }

// TestEmbeddingsRetryReleasesBeforeFeedbackAndCompletesOnce switches after
// one classified failure and forwards the same body again. It checks the
// copied exclusion, release-before-feedback event order and one completion
// on subsequent success.
func TestEmbeddingsRetryReleasesBeforeFeedbackAndCompletesOnce(t *testing.T) {
	failure := errors.New("switch")
	ports := &embeddingPortsStub{outcomes: []EmbeddingOutcome{{Err: failure, Failover: true}, {Result: &EmbeddingResult{RequestID: "ok"}}}}
	body := []byte(`{"model":"alias","input":["a","b"]}`)
	require.Nil(t, RunEmbeddings(context.Background(), body, 1, ports))
	require.Equal(t, 2, ports.selected)
	require.Equal(t, 2, ports.released)
	require.Equal(t, 1, ports.completed)
	require.Equal(t, 1, ports.switches)
	require.Equal(t, []string{"forward", "release", "report", "forward", "release", "report", "complete"}, ports.events)
	require.Contains(t, ports.exclusions[1], int64(1))
	require.Equal(t, body, ports.bodies[0])
	require.Equal(t, body, ports.bodies[1])
}

// TestEmbeddingsOutputClosesRetryWindow injects a failover error with
// changed output. It checks the exhausted stage after slot release and
// confirms that report, switch and completion callbacks are not invoked.
func TestEmbeddingsOutputClosesRetryWindow(t *testing.T) {
	ports := &embeddingPortsStub{outcomes: []EmbeddingOutcome{{Err: errors.New("partial"), Failover: true, OutputChanged: true}}}
	failure := RunEmbeddings(context.Background(), nil, 3, ports)
	require.NotNil(t, failure)
	require.Equal(t, "exhausted", failure.Stage)
	require.True(t, failure.Outcome.OutputChanged)
	require.Equal(t, 1, ports.released)
	require.Zero(t, ports.reported)
	require.Zero(t, ports.completed)
	require.Zero(t, ports.switches)
}

// TestEmbeddingsWaitCancellationAndExhaustion injects wait rejection,
// adapter-reported client departure and repeated failover errors. The cases
// distinguish early handling from transition exhaustion and check release
// and completion counts for each path.
func TestEmbeddingsWaitCancellationAndExhaustion(t *testing.T) {
	t.Run("wait", func(t *testing.T) {
		ports := &embeddingPortsStub{denyAcquire: true}
		require.Nil(t, RunEmbeddings(context.Background(), nil, 1, ports))
		require.Zero(t, ports.acquired)
		require.Empty(t, ports.bodies)
	})
	t.Run("canceled after failure", func(t *testing.T) {
		ports := &embeddingPortsStub{gone: true, outcomes: []EmbeddingOutcome{{Err: context.Canceled, Failover: true}}}
		require.Nil(t, RunEmbeddings(context.Background(), nil, 3, ports))
		require.Equal(t, 1, ports.released)
		require.Zero(t, ports.switches)
	})
	t.Run("limit", func(t *testing.T) {
		failure := EmbeddingOutcome{Err: errors.New("switch"), Failover: true}
		ports := &embeddingPortsStub{outcomes: []EmbeddingOutcome{failure, failure}}
		result := RunEmbeddings(context.Background(), nil, 1, ports)
		require.Equal(t, "exhausted", result.Stage)
		require.Equal(t, 2, ports.selected)
		require.Equal(t, 2, ports.released)
		require.Zero(t, ports.completed)
	})
}
