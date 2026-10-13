package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// Grok text defaults can select the public API, a named region, or the CLI
// subscription endpoint. Mode normalization chooses the CLI endpoint for
// unsupported settings, and a bound reader consults runtime settings each time
// an execution caller requests the default.

// GrokBaseURLForMode resolves a normalized runtime mode to the corresponding
// Grok endpoint constant. API and regional modes select their endpoints; CLI,
// blank, and invalid modes use the CLI default.
func GrokBaseURLForMode(mode string) string {
	switch gateway.NormalizeGrokDefaultBaseURLMode(mode) {
	case "api":
		return grok.DefaultBaseURL
	case "us-east-1":
		return grok.DefaultUSEast1BaseURL
	case "us-west-2":
		return grok.DefaultUSWest2BaseURL
	case "eu-west-1":
		return grok.DefaultEUWest1BaseURL
	default:
		return grok.DefaultCLIBaseURL
	}
}

// GrokDefaultBaseURLReader binds runtime settings to an endpoint callback that
// resolves the mode on each call. The settings reader supplies its own
// defaults and caching, and callers pass the context for that read.
func GrokDefaultBaseURLReader(settings *gateway.RuntimeSettings) func(context.Context) string {
	return func(ctx context.Context) string { return GrokBaseURLForMode(settings.GetGrokDefaultBaseURLMode(ctx)) }
}
