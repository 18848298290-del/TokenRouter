package upstream

import (
	"time"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

type ResponsesObservation struct {
	// UpstreamResponseModel is the upstream model declaration before conversion; empty means none was supplied.
	UpstreamResponseModel                                               string
	HasUsage, Served, HTTPCommitted, RetryCommitted, ClientDisconnected bool
	FirstSemanticOutput                                                 *time.Duration
	Usage                                                               *wire.ForwardUsage
	FirstTokenMs                                                        *int
	ResponseID                                                          string
	SearchCount, ImageCount                                             int
	ImageOutputSizes                                                    []string
}
