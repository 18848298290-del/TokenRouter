package openai_ws_v2

import (
	"context"
)

// EntryInput supplies the relay's connections, first frame, context, and options.
type EntryInput struct {
	Ctx                context.Context
	ClientConn         FrameConn
	UpstreamConn       FrameConn
	FirstClientMessage []byte
	Options            RelayOptions
}

// runCaddyStyleRelay follows Caddy reverseproxy's bidirectional tunnel model:
// copy frames concurrently in both directions and finish when either direction exits.
//
// Reference:
// - Project: caddyserver/caddy (Apache-2.0)
// - Commit: f283062d37c50627d53ca682ebae2ce219b35515
// - Files:
//   - modules/caddyhttp/reverseproxy/streaming.go
//   - modules/caddyhttp/reverseproxy/reverseproxy.go
func runCaddyStyleRelay(
	ctx context.Context,
	clientConn FrameConn,
	upstreamConn FrameConn,
	firstClientMessage []byte,
	options RelayOptions,
) (RelayResult, *RelayExit) {
	return Relay(ctx, clientConn, upstreamConn, firstClientMessage, options)
}

// RunEntry starts a bidirectional relay from the supplied entry parameters.
func RunEntry(input EntryInput) (RelayResult, *RelayExit) {
	return runCaddyStyleRelay(
		input.Ctx,
		input.ClientConn,
		input.UpstreamConn,
		input.FirstClientMessage,
		input.Options,
	)
}
