package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// ClientRejectionObservation carries classified upstream failures for provider health decisions.
type ClientRejectionObservation struct {
	Message                      string
	OrganizationDisabled         bool
	CreditBalanceExhausted       bool
	IdentityVerificationRequired bool
	WorkspaceDeactivated         bool
}

// ApplyBadRequest applies account-level 400 failures to provider state and reports whether they were handled.
func (s *HealthService) ApplyBadRequest(ctx context.Context, provider *Record, observation ClientRejectionObservation) bool {
	// A disabled organization requires recovery before scheduling can resume.
	if observation.OrganizationDisabled {
		msg := "Organization disabled (400): " + observation.Message
		s.ApplyAuthenticationFailure(ctx, provider, msg)
		return true
	} else if provider.Platform == capability.PlatformAnthropic && observation.CreditBalanceExhausted {
		// Treat Anthropic credit exhaustion like a payment-required failure.
		msg := "Credit balance exhausted (400): " + observation.Message
		s.ApplyAuthenticationFailure(ctx, provider, msg)
		return true
	} else if observation.IdentityVerificationRequired {
		// Identity verification requires account intervention before recovery.
		msg := "Identity verification required (400): " + observation.Message
		s.ApplyAuthenticationFailure(ctx, provider, msg)
		return true
	}
	// Request-level errors, such as invalid parameters, leave provider health unchanged.
	return false
}

// ApplyPaymentRequired updates provider health for classified billing failures.
func (s *HealthService) ApplyPaymentRequired(ctx context.Context, provider *Record, observation ClientRejectionObservation) bool {
	// Insufficient balance on supported CN platforms is recoverable after a top-up.
	// Use a temporary pause so cooldown or recovery checks can restore scheduling.
	if provider.IsCNProvider() {
		s.ApplyCNInsufficientBalance(ctx, provider, observation.Message)
		return true
	}
	// An OpenAI deactivated workspace is an account error.
	if provider.Platform == capability.PlatformOpenAI && observation.WorkspaceDeactivated {
		msg := "Workspace deactivated (402): workspace has been deactivated"
		s.ApplyAuthenticationFailure(ctx, provider, msg)
		return true
	}
	// Other payment failures stop scheduling until the billing issue is resolved.
	msg := "Payment required (402): insufficient balance or billing issue"
	if observation.Message != "" {
		msg = "Payment required (402): " + observation.Message
	}
	s.ApplyAuthenticationFailure(ctx, provider, msg)
	return true
}
