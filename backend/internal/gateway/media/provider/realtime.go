package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// RealtimeOptions holds Grok realtime credentials, header writers, and the dial function.
type RealtimeOptions struct {
	BaseURL, Model           string
	Token                    string `json:"-"`
	CLIHeaders, ApplyHeaders func(http.Header)
	Dial                     func(context.Context, string, http.Header) (upstream.FrameConn, int, error)
	Enter                    func() (func(), error)
}

func (o RealtimeOptions) String() string   { return "media RealtimeOptions" }
func (o RealtimeOptions) GoString() string { return o.String() }
func (o RealtimeOptions) native() grok.RealtimeDialOptions {
	return grok.RealtimeDialOptions{BaseURL: o.BaseURL, Model: o.Model, Token: o.Token, CLIHeaders: o.CLIHeaders, ApplyHeaders: o.ApplyHeaders, Dial: o.Dial, Enter: o.Enter}
}

// DialRealtime opens a Grok realtime connection with the caller's resource options.
func DialRealtime(ctx context.Context, options RealtimeOptions) (*grok.RealtimeSession, error) {
	return grok.DialRealtime(ctx, options.native())
}
