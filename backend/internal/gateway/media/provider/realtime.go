package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// Grok realtime connections are opened with bearer authorization and
// provider-selected header policy. The adapter forwards dial and activity
// callbacks to Grok, whose returned session keeps the connection and
// activity registration until its caller closes it.

// RealtimeOptions supplies the Grok WebSocket destination and gateway
// handshake callbacks.
type RealtimeOptions struct {
	// BaseURL is parsed and forced to the wss scheme by Grok DialRealtime.
	// Model replaces the query model, using grok-voice-latest when its trimmed
	// value is empty.
	BaseURL, Model string // upstream WebSocket URL and WebSocket model query
	// Token initializes the Authorization header before either header callback
	// runs. JSON omits this credential, while the final callback can replace
	// the constructed authorization value.
	Token string `json:"-"` // WebSocket bearer credential
	// CLIHeaders and ApplyHeaders are optional callbacks that mutate the newly
	// constructed headers. CLIHeaders runs for recognized Grok CLI proxy
	// targets; ApplyHeaders runs afterward for every target when present.
	CLIHeaders, ApplyHeaders func(http.Header) // CLI proxy headers and final header policy
	// Dial receives ctx, the constructed URL and headers. Nil error requires a
	// nonnil FrameConn; Grok closes a returned connection on dial failure, and
	// callers close successful sessions.
	Dial func(context.Context, string, http.Header) (upstream.FrameConn, int, error) // required WebSocket handshake callback
	// Enter optionally admits connection activity before Dial. Its completion
	// callback transfers to a successful session and runs during session
	// close, or immediately after a failed dial.
	Enter func() (func(), error) // optional handshake activity registration
}

// String returns a fixed realtime-option label during diagnostic
// formatting. Destination, model and credential fields stay out of this
// representation, so callers can format the captured options without
// expanding authorization data.
func (o RealtimeOptions) String() string { return "media RealtimeOptions" }

// GoString uses the fixed realtime label for Go-style diagnostic
// formatting.
func (o RealtimeOptions) GoString() string { return o.String() }

// native copies realtime option values into the Grok dial representation.
// Function captures remain shared with the caller, which keeps their state
// valid for the handshake and eventual activity completion.
func (o RealtimeOptions) native() grok.RealtimeDialOptions {
	return grok.RealtimeDialOptions{BaseURL: o.BaseURL, Model: o.Model, Token: o.Token, CLIHeaders: o.CLIHeaders, ApplyHeaders: o.ApplyHeaders, Dial: o.Dial, Enter: o.Enter}
}

// DialRealtime delegates an authorized Grok WebSocket handshake and returns
// its session. Callers supply a nonnil context and Dial callback, check
// Ready, and close a successful session to release connection activity. A
// nil connection on nil dial error produces an unready session whose Close
// does not release activity.
func DialRealtime(ctx context.Context, options RealtimeOptions) (*grok.RealtimeSession, error) {
	return grok.DialRealtime(ctx, options.native())
}
