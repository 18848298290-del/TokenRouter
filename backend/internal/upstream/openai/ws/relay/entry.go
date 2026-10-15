package openai_ws_v2

import (
	"context"
)

// entry.go passes the first client frame after connection setup and both
// transports to the shared bidirectional WebSocket relay. The wrapper keeps
// the prepared context and options together, so initial model and usage
// observation follows the same frame relay path as subsequent traffic.

// EntryInput contains the prepared first frame and transport inputs for relay
// execution.
type EntryInput struct {
	Ctx                context.Context // prepared relay operation context
	ClientConn         FrameConn       // downstream client frame transport
	UpstreamConn       FrameConn       // selected upstream frame transport
	FirstClientMessage []byte          // already-read first client frame
	Options            RelayOptions    // prepared bidirectional relay options
}

// runCaddyStyleRelay forwards the prepared tunnel to the shared relay.
// It retains the supplied context, connections, first frame, and options so
// callers receive the same result and exit classification as Relay.
//
// The delegated Relay uses the first direction exit to select accounting
// drain or cancellation. It joins the upstream observer before returning
// collected usage and its selected exit classification.
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

// RunEntry forwards the prepared context, both connections, first frame, and
// options to the shared relay. Nil connections return its initialization
// failure, while nil context follows the relay Background default.
func RunEntry(input EntryInput) (RelayResult, *RelayExit) {
	return runCaddyStyleRelay(
		input.Ctx,
		input.ClientConn,
		input.UpstreamConn,
		input.FirstClientMessage,
		input.Options,
	)
}
