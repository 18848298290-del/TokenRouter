package provider

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// Grok media generation uses provider overrides and observed billing
// eligibility before an upstream attempt. Missing OAuth billing observations
// trigger an active probe; other eligibility decisions return directly, and an
// absent probe dependency keeps the unobserved target rejected.

// GrokMediaEligibilityProber supplies an active eligibility observation for a
// selected provider.
type GrokMediaEligibilityProber interface {
	// ProbeMediaEligibility checks the selected provider when local billing data
	// is unobserved. It returns eligibility, its reason, and the probe error for
	// forwarding callers to handle before generation begins.
	ProbeMediaEligibility(context.Context, int64) (bool, string, error)
}

// CheckGrokMediaEligibility evaluates provider media policy, probing only when
// its reason is billing_unobserved. Nil targets and missing required probes
// return errors; all other policy results return directly, and probe outcomes
// pass through unchanged.
func CheckGrokMediaEligibility(ctx context.Context, value *ExecutionProvider, probe GrokMediaEligibilityProber) (bool, string, error) {
	if value == nil {
		return false, "missing_provider", errors.New("grok media provider is required")
	}
	eligible, reason := provider.GrokMediaGenerationEligibility(ExecutionRecord(value), provideradapter.GrokTierRules())
	if eligible || reason != "billing_unobserved" {
		return eligible, reason, nil
	}
	// Existing policy decisions already carry their reason. Probe only unobserved
	// billing so forwarding cannot treat absent account evidence as media approval.
	if probe == nil {
		return false, "billing_probe_unavailable", errors.New("grok media eligibility probe is not configured")
	}
	return probe.ProbeMediaEligibility(ctx, value.Record.ID)
}
