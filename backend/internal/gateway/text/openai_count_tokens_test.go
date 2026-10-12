package text

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// singleCountProbe checks that counting failures stop after one attempt without generation or billing work.
type singleCountProbe struct {
	available             bool
	selectErr, forwardErr error
	events                []string
}

func (p *singleCountProbe) Select() (bool, error) {
	p.events = append(p.events, "select")
	return p.available, p.selectErr
}
func (p *singleCountProbe) Selected()             { p.events = append(p.events, "latency") }
func (p *singleCountProbe) SelectionFailed(error) { p.events = append(p.events, "selection-failed") }

func (p *singleCountProbe) Forward() error {
	p.events = append(p.events, "forward")
	return p.forwardErr
}
func (p *singleCountProbe) ForwardFailed(error) { p.events = append(p.events, "forward-failed") }
func TestRunSingleCountTokens(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		available             bool
		selectErr, forwardErr error
		events                []string
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
