package media

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// generationStub scripts media outcomes and observes serial lifecycle
// callback order.
type generationStub struct {
	// selected indexes scripted outcomes; activated and acquired count earlier
	// phases, while released, completed, switched and keepaliveStopped count
	// later callbacks. Serial tests compare these totals with event order to
	// distinguish partial completion and recovery.
	selected, activated, acquired, released, completed, switched, keepaliveStopped int // selection sequence number, provider activation calls, admission callback calls, release callback invocations, completion callback calls, adapter switch calls and keepalive stop invocations
	// outcomes retains one entry per selection and its result pointers. Tests
	// provide sufficient entries for reached forwarding calls and keep the
	// script stable until assertions.
	outcomes []GenerationOutcome // shared ordered media outcome script
	// reports appends each ReportGeneration success flag in call order. The
	// slice belongs to the serial fixture and remains available to inspect
	// user-error versus provider-failure feedback.
	reports []bool // owned scheduling success-flag observations
	// terminal points to EndGeneration's copied argument after that callback
	// runs. Nested result pointers remain shared, and tests inspect the
	// terminal stage after loop completion.
	terminal *GenerationFailure // optional retained terminal failure value
	events   []string           // serial callback event sequence
	// rejected controls eligibility admission and gone reports adapter client
	// departure. The flags operate independently and do not cancel ctx or
	// exercise a real provider probe.
	rejected, gone bool // fixture rejects eligibility and adapter reports departure
}

// SelectGeneration returns provider one with retry limit one while
// advancing the script index. It ignores exclusions, so tests measure loop
// accounting and do not enforce routing eligibility through this fixture.
func (p *generationStub) SelectGeneration(context.Context, map[int64]struct{}) (GenerationSelection, bool, error) {
	p.selected++
	return GenerationSelection{Provider: provider.ProviderSnapshot{ID: 1}, RetryLimit: 1}, true, nil
}

// ActivateGeneration counts selected-provider activation before the fixture
// admission callback runs.
func (p *generationStub) ActivateGeneration(GenerationSelection) { p.activated++ }

// GenerationEligible returns eligibility opposite to the fixture rejected
// flag and a fixed reason. It performs no external probe, letting tests
// isolate rejection budgets from provider eligibility I/O.
func (p *generationStub) GenerationEligible(context.Context, GenerationSelection) (bool, string, error) {
	return !p.rejected, "denied", nil
}

// AcquireGeneration admits every reached call and returns a counting
// release callback. Release also appends its event so tests can compare
// slot cleanup with reporting, completion and keepalive shutdown.
func (p *generationStub) AcquireGeneration(context.Context, GenerationSelection) (func(), bool) {
	p.acquired++
	return func() { p.released++; p.events = append(p.events, "release") }, true
}

// StartGenerationKeepalive returns a stopper that records its count and
// event when called. The fixture starts no goroutine or writer, so tests
// observe loop lifecycle ordering rather than keepalive byte delivery.
func (p *generationStub) StartGenerationKeepalive() func() {
	return func() { p.keepaliveStopped++; p.events = append(p.events, "keepalive_stop") }
}

// ForwardGeneration returns the outcome indexed by the preceding selection
// count. Tests provide a sufficient stable script, and returned result
// pointers remain shared with the fixture for loop observation.
func (p *generationStub) ForwardGeneration(context.Context, GenerationSelection, []byte) GenerationOutcome {
	return p.outcomes[p.selected-1]
}

// ReportGeneration records the success flag and appends scheduling feedback
// order.
func (p *generationStub) ReportGeneration(_ context.Context, _ GenerationSelection, _ *GenerationResult, success bool, _ error) {
	p.reports = append(p.reports, success)
	p.events = append(p.events, "report")
}

// CompleteGeneration increments completion observations and appends the
// completion-order event.
func (p *generationStub) CompleteGeneration(context.Context, GenerationSelection, *GenerationResult) {
	p.completed++
	p.events = append(p.events, "complete")
}

// SwitchGeneration counts adapter provider-transition bookkeeping callbacks
// during fixture recovery.
func (p *generationStub) SwitchGeneration(GenerationSelection) { p.switched++ }

// StopGeneration429 returns false for every provider, status and count.
// Test recovery therefore stops through loop budgets or output/lookup
// decisions, independently of status-specific adapter termination policy.
func (p *generationStub) StopGeneration429(GenerationSelection, int, int) bool { return false }

// GenerationClientGone returns the fixture's adapter-departure flag for
// loop handling.
func (p *generationStub) GenerationClientGone() bool { return p.gone }

// ObserveGeneration accepts loop events without mutating the fixture
// observation state.
func (p *generationStub) ObserveGeneration(GenerationEvent) {}

// EndGeneration retains the terminal argument and appends its order event.
// It copies that value while sharing any nested outcome pointers, and
// serial tests inspect the saved stage afterward.
func (p *generationStub) EndGeneration(f GenerationFailure) {
	p.terminal = &f
	p.events = append(p.events, "end")
}

// TestImagesLoopPartialOutputCompletesAndStopsKeepalive returns two
// observed images with a tail error. It checks slot release, one completion
// and deferred keepalive shutdown in order, with no separate failure-report
// or terminal callback.
func TestImagesLoopPartialOutputCompletesAndStopsKeepalive(t *testing.T) {
	p := &generationStub{outcomes: []GenerationOutcome{{Result: &GenerationResult{ImageCount: 2}, Err: errors.New("tail failure")}}}
	RunImages(context.Background(), GenerationRequest{RoutingStarted: time.Now()}, p)
	require.Equal(t, 1, p.released)
	require.Equal(t, 1, p.completed)
	require.Empty(t, p.reports)
	require.Nil(t, p.terminal)
	require.Equal(t, []string{"release", "complete", "keepalive_stop"}, p.events)
}

// TestImagesLoopStopsSwitchingAfterOutput returns a classified failure
// after output changes. It checks failure feedback and exhausted handling
// after slot release, then keepalive cleanup, with no second candidate
// selected.
func TestImagesLoopStopsSwitchingAfterOutput(t *testing.T) {
	p := &generationStub{outcomes: []GenerationOutcome{{Err: errors.New("upstream"), Failure: &failover.FailureInfo{RetryNext: true}, OutputChanged: true}}}
	RunImages(context.Background(), GenerationRequest{MaxSwitches: 3, RoutingStarted: time.Now()}, p)
	require.Equal(t, 1, p.selected)
	require.Equal(t, []bool{false}, p.reports)
	require.Equal(t, "exhausted", p.terminal.Stage)
	require.Equal(t, []string{"release", "report", "end", "keepalive_stop"}, p.events)
}

// TestImagesLoopPreservesUserErrorAndSameProviderRetry first returns a user
// image error, then runs a same-provider recovery script. It checks healthy
// feedback without completion for the user error and two released attempts
// before one later completion.
func TestImagesLoopPreservesUserErrorAndSameProviderRetry(t *testing.T) {
	p := &generationStub{outcomes: []GenerationOutcome{{Err: errors.New("bad prompt"), ImageError: true}}}
	RunImages(context.Background(), GenerationRequest{Stream: true, RoutingStarted: time.Now()}, p)
	require.Equal(t, []bool{true}, p.reports)
	require.Zero(t, p.completed)
	require.Zero(t, p.switched)
	p = &generationStub{outcomes: []GenerationOutcome{{Err: errors.New("retry"), Failure: &failover.FailureInfo{RetryableOnSameProvider: true, RetryNext: true, SameProviderRetryDelay: time.Millisecond}}, {Result: &GenerationResult{ImageCount: 1}}}}
	RunImages(context.Background(), GenerationRequest{MaxSwitches: 1, RoutingStarted: time.Now()}, p)
	require.Equal(t, 2, p.released)
	require.Equal(t, 1, p.completed)
	require.Zero(t, p.switched)
}

// TestGrokLoopBoundLookupDoesNotSwitchOrProbeEligibility supplies a
// mismatched task provider before a matching lookup failure. It checks
// rejection before activation/admission, then exhausted lookup handling
// without scheduling feedback or provider switching.
func TestGrokLoopBoundLookupDoesNotSwitchOrProbeEligibility(t *testing.T) {
	p := &generationStub{}
	RunGrokMedia(context.Background(), GenerationRequest{VideoLookup: true, BoundProviderID: 8, RoutingStarted: time.Now()}, p)
	require.Equal(t, "bound_unavailable", p.terminal.Stage)
	require.Zero(t, p.activated)
	require.Zero(t, p.acquired)
	p = &generationStub{outcomes: []GenerationOutcome{{Err: errors.New("lookup"), Failure: &failover.FailureInfo{RetryNext: true}, ReportFailure: false}}}
	RunGrokMedia(context.Background(), GenerationRequest{VideoLookup: true, BoundProviderID: 1, RoutingStarted: time.Now()}, p)
	require.Equal(t, 1, p.released)
	require.Empty(t, p.reports)
	require.Zero(t, p.switched)
	require.Equal(t, "exhausted", p.terminal.Stage)
}

// TestGrokLoopEligibilityAndCancellationBudgets repeatedly rejects
// eligibility with one allowed transition, then reports client departure
// before selection. It checks two candidate considerations without
// admission and an early departure with no terminal callback.
func TestGrokLoopEligibilityAndCancellationBudgets(t *testing.T) {
	p := &generationStub{rejected: true}
	RunGrokMedia(context.Background(), GenerationRequest{Generation: true, MaxSwitches: 1, RoutingStarted: time.Now()}, p)
	require.Equal(t, 2, p.selected)
	require.Zero(t, p.acquired)
	require.Equal(t, "ineligible", p.terminal.Stage)
	p = &generationStub{gone: true}
	RunGrokMedia(context.Background(), GenerationRequest{Generation: true}, p)
	require.Zero(t, p.selected)
	require.Nil(t, p.terminal)
}

// TestGenerationSnapshotIsolatesMutableObservedFields copies timing, size
// and header observations before mutating the source. It checks independent
// retained values and a present empty response-header slice in the
// snapshot.
func TestGenerationSnapshotIsolatesMutableObservedFields(t *testing.T) {
	first := 7
	original := &GenerationResult{Timings: &timing.Performance{Stream: true, Values: map[string]int64{"upstream_started_ms": 0, "upstream_first_output_ms": int64(first)}}, ImageOutputSizes: []string{"1024x1024"}, ImageSizeBreakdown: map[string]int{"1K": 1}, Headers: map[string][]string{"X-Request-Id": {"old"}}, ResponseHeaders: map[string][]string{"Empty": {}}}
	snapshot := CloneGenerationResult(original)
	// Mutate timing, size and upstream-header sources after cloning so value
	// assertions expose retained aliases; the response-header assertion
	// separately protects its present-empty shape.
	original.Timings.Values["upstream_first_output_ms"] = 99
	original.ImageOutputSizes[0] = "changed"
	original.ImageSizeBreakdown["1K"] = 4
	original.Headers["X-Request-Id"][0] = "changed"
	require.Equal(t, 7, *snapshot.Timings.TTFTMs())
	require.Equal(t, "1024x1024", snapshot.ImageOutputSizes[0])
	require.Equal(t, 1, snapshot.ImageSizeBreakdown["1K"])
	require.Equal(t, "old", snapshot.Headers["X-Request-Id"][0])
	require.NotNil(t, snapshot.ResponseHeaders["Empty"])
}
