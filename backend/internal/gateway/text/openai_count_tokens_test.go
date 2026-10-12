package text

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// singleCountProbe supplies one selection/count result and records callback
// ordering for auxiliary counting.
type singleCountProbe struct {
	available             bool     // configured counting target availability
	selectErr, forwardErr error    // injected selection error and injected forwarding error
	events                []string // expected or observed callback event order
}

// Select records selection and returns the configured availability/error pair.
func (p *singleCountProbe) Select() (bool, error) {
	p.events = append(p.events, "select")
	return p.available, p.selectErr
}

// Selected records the selection-latency callback regardless of target
// availability.
func (p *singleCountProbe) Selected() { p.events = append(p.events, "latency") }

// SelectionFailed records terminal selection handling in the ordered fixture
// events.
func (p *singleCountProbe) SelectionFailed(error) { p.events = append(p.events, "selection-failed") }

// Forward records one auxiliary execution and returns its configured error.
func (p *singleCountProbe) Forward() error {
	p.events = append(p.events, "forward")
	return p.forwardErr
}

// ForwardFailed records terminal execution-error notification in fixture event
// order.
func (p *singleCountProbe) ForwardFailed(error) { p.events = append(p.events, "forward-failed") }

// TestRunSingleCountTokens supplies successful, failed, unavailable and
// forwarding-error fixtures to one-count execution. The ordered callbacks must
// record selection latency on every branch and forward at most once.
func TestRunSingleCountTokens(t *testing.T) {
	for _, tc := range []struct {
		name                  string   // case label for diagnostics
		available             bool     // configured target availability
		selectErr, forwardErr error    // injected selection error and injected forwarding error
		events                []string // expected callback event order
	}{
		{name: "success", available: true, events: []string{"select", "latency", "forward"}},
		{name: "selection error", selectErr: errors.New("unavailable"), events: []string{"select", "latency", "selection-failed"}},
		{name: "empty selection", events: []string{"select", "latency", "selection-failed"}},
		{name: "upstream failure", available: true, forwardErr: errors.New("failed"), events: []string{"select", "latency", "forward", "forward-failed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &singleCountProbe{available: tc.available, selectErr: tc.selectErr, forwardErr: tc.forwardErr}
			RunSingleCountTokens(p)
			require.Equal(t, tc.events, p.events)
		})
	}
}
