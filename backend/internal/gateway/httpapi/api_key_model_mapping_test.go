package httpapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/modeltrace"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// A key redirect can target a model that itself appears as another mapping
// source. Context-based entry points stop after the first rule and keep the
// original client identity beside the chosen target for later restoration.

// TestAPIKeyModelRedirectContextAppliesExactlyOneRule configures a target with
// another matching source rule. The result must stop at the first target, and
// both telemetry and the redirect trace must retain the client alias.
func TestAPIKeyModelRedirectContextAppliesExactlyOneRule(t *testing.T) {
	apiKey := &apikey.APIKey{ModelMapping: map[string]string{
		"codex-auto-review": "gpt-5.6-luna",
		"gpt-5.6-luna":      "must-not-chain",
	}}

	ctx, model := APIKeyModelRedirectContext(context.Background(), apiKey, "codex-auto-review")
	require.Equal(t, "gpt-5.6-luna", model)
	require.Equal(t, "codex-auto-review", ctx.Value(telemetry.ClientModel))
	trace, ok := modeltrace.FromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "codex-auto-review", trace.ClientModel)
	require.Equal(t, "gpt-5.6-luna", trace.TargetModel)
}
