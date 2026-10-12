package httpapi

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
)

// mediaFailureProbe captures classifications and client errors from terminal
// media presentation.
type mediaFailureProbe struct {
	// MediaFailurePorts leaves unneeded operations unset. Calling any promoted
	// method outside this probe's implemented paths fails the test immediately.
	MediaFailurePorts
	result MediaNoProvider // configured classification return value
	status int             // captured HTTP failure status
	// typ: captured error category. message: captured client error.
	typ, message string
	// stream: captured presentation mode. conditional: captured capacity condition.
	stream, conditional bool
	// classified: classification call count. capacity: capacity observation count.
	classified, capacity int
}

// MediaClassify counts classification requests and returns the configured
// route failure.
func (p *mediaFailureProbe) MediaClassify() MediaNoProvider { p.classified++; return p.result }

// MediaCapacity counts capacity observations and retains their conditional
// marking flag.
func (p *mediaFailureProbe) MediaCapacity(_ error, conditional bool) {
	p.capacity++
	p.conditional = conditional
}

// MediaError captures status and error fields for later test assertions.
func (p *mediaFailureProbe) MediaError(status int, typ, message string, stream bool) {
	p.status = status
	p.typ = typ
	p.message = message
	p.stream = stream
}

// MediaNoAvailable treats every injected selection error as scheduler
// candidate absence.
func (p *mediaFailureProbe) MediaNoAvailable(error) bool { return true }

// TestMediaFailureHTTPPreservesImageCapacityAndGrokEligibility presents empty
// image selection and excluded Grok eligibility failure. Images use generic
// compatible capacity wording, while Grok takes its specialized eligibility
// path.
func TestMediaFailureHTTPPreservesImageCapacityAndGrokEligibility(t *testing.T) {
	p := &mediaFailureProbe{result: MediaNoProvider{Status: 503, Type: "api_error", Message: "original"}}
	WriteGenerationFailure(media.GenerationFailure{Stage: "empty_selection"}, MediaFailureContext{Log: zap.NewNop()}, p)
	require.Equal(t, 503, p.status)
	require.Equal(t, "No available compatible providers", p.message)
	require.True(t, p.stream)
	require.False(t, p.conditional)
	p = &mediaFailureProbe{}
	WriteGenerationFailure(media.GenerationFailure{Stage: "selection", Err: errors.New("no provider"), EligibilityRejected: true, Excluded: 2}, MediaFailureContext{Grok: true, Generation: true, Log: zap.NewNop()}, p)
	require.Equal(t, 503, p.status)
	require.Equal(t, "grok_media_no_eligible_provider", p.typ)
	require.Zero(t, p.classified)
	require.True(t, p.conditional)
}
