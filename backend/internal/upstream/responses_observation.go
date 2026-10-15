package upstream

import (
	"time"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// responses_observation.go defines the usage, commitment, timing, and
// disconnect evidence returned by Responses readers.
// A Responses reader reports usage, output timing, and client-write failures
// alongside its parser result. Platform executors transfer these observations
// into attempt results, keeping HTTP commitment, retry commitment, and
// semantic content available as separate decisions for gateway completion.

// ResponsesObservation carries the evidence retained by one Responses output
// reader.
type ResponsesObservation struct {
	// UpstreamResponseModel records the upstream model before client text is
	// restored. Empty means the reader retained no vendor model declaration.
	UpstreamResponseModel string // observed vendor model name
	// HasUsage, Served, HTTPCommitted, RetryCommitted, and ClientDisconnected
	// record independent reader outcomes. Usage and served output can exist
	// alongside a read error; HTTP commitment alone does not close the retry window.
	// HasUsage: usage evidence present. Served: output served by reader.
	// HTTPCommitted: HTTP output started. RetryCommitted: retry window closed.
	// ClientDisconnected: client write failed.
	HasUsage, Served, HTTPCommitted, RetryCommitted, ClientDisconnected bool
	// FirstSemanticOutput measures elapsed time to content in the stream reader.
	// Nil means that reader retained no semantic-output duration for this response.
	FirstSemanticOutput *time.Duration // elapsed semantic output latency
	// Usage retains the reader's parsed usage pointer. HasUsage records its
	// consumer-specific presence test, so a nonnil pointer alone is insufficient.
	Usage *wire.ForwardUsage // parsed Responses usage values
	// FirstTokenMs carries the reader's first-token timing in milliseconds.
	// Nil means that reader supplied no first-token timing for this response.
	FirstTokenMs *int   // milliseconds
	ResponseID   string // observed Responses object identifier
	// SearchCount: observed search operations. ImageCount: observed image results.
	SearchCount, ImageCount int
	// ImageOutputSizes retains per-image size labels for later completion pricing.
	// The executor can share this slice; copy before modifying retained observations.
	ImageOutputSizes []string // observed image size labels
}
