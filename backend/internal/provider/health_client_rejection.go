package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// health_client_rejection.go selects provider health actions for classified
// bad-request and payment-required failures.
// Classified 400 failures can describe an account problem even when their HTTP
// status resembles an invalid request. Organization failure has precedence,
// Anthropic credit exhaustion has its own platform check, and
// identity-verification failure selects account handling after those cases.
// Other 400 classifications return false for later request handling.
//
// A 402 decision first routes CN providers to their recoverable balance-pause
// command. An OpenAI workspace classification gets its specific diagnostic;
// remaining platforms enter ordinary payment-required authentication failure.
// These methods choose health scope and message, while the delegated commands
// own runtime notification, storage and logged failures. Their true result
// identifies handled classification, so callers retain that choice
// independently of persistence outcomes.

// ClientRejectionObservation holds upstream account and billing classifications
// used by provider health selection.
type ClientRejectionObservation struct {
	Message string // classified upstream diagnostic
	// OrganizationDisabled, CreditBalanceExhausted and
	// IdentityVerificationRequired supply ApplyBadRequest's ordered checks.
	// Organization failure wins; credit exhaustion applies to Anthropic, then
	// identity verification can select the remaining account-error path.
	OrganizationDisabled         bool // organization access disabled
	CreditBalanceExhausted       bool // credit balance exhausted
	IdentityVerificationRequired bool // account verification required
	// WorkspaceDeactivated selects the specific OpenAI diagnostic in
	// ApplyPaymentRequired after CN balance handling; TeamLinkedHealth receives
	// its own separately classified deactivation flag.
	WorkspaceDeactivated bool
}

// ApplyBadRequest selects account handling for classified HTTP 400 failures and
// returns whether a classification was handled. Supply a usable service and
// provider. OrganizationDisabled wins, Anthropic credit exhaustion is next, and
// IdentityVerificationRequired is considered afterward.
//
// Each selected branch invokes ApplyAuthenticationFailure with its matching
// diagnostic. Its storage outcome is logged by that command while this method
// still returns true. Other observations return false and leave the caller to
// classify the request failure, so ordinary invalid input cannot acquire an
// account error through these flags.
func (s *HealthService) ApplyBadRequest(ctx context.Context, provider *Record, observation ClientRejectionObservation) bool {
	if observation.OrganizationDisabled {
		msg := "Organization disabled (400): " + observation.Message
		s.ApplyAuthenticationFailure(ctx, provider, msg)
		return true
	} else if provider.Platform == capability.PlatformAnthropic && observation.CreditBalanceExhausted {
		msg := "Credit balance exhausted (400): " + observation.Message
		s.ApplyAuthenticationFailure(ctx, provider, msg)
		return true
	} else if observation.IdentityVerificationRequired {
		msg := "Identity verification required (400): " + observation.Message
		s.ApplyAuthenticationFailure(ctx, provider, msg)
		return true
	}
	return false
}

// ApplyPaymentRequired selects provider health handling for HTTP 402 and
// returns true for the handled response. Supply a usable service and provider.
// CN records enter ApplyCNInsufficientBalance first, retaining that command's
// temporary recovery policy.
//
// OpenAI workspace deactivation gets a dedicated account-error message; other
// payment failures use the supplied message or built-in billing diagnostic. The
// delegated command controls runtime blocking and persistence. Logged write
// failures do not change this handled result, and a true return therefore
// identifies response classification rather than an affected database row.
func (s *HealthService) ApplyPaymentRequired(ctx context.Context, provider *Record, observation ClientRejectionObservation) bool {
	// Insufficient balance on supported CN platforms is recoverable after a top-up.
	// Use a temporary pause so cooldown or recovery checks can restore scheduling.
	if provider.IsCNProvider() {
		s.ApplyCNInsufficientBalance(ctx, provider, observation.Message)
		return true
	}
	if provider.Platform == capability.PlatformOpenAI && observation.WorkspaceDeactivated {
		msg := "Workspace deactivated (402): workspace has been deactivated"
		s.ApplyAuthenticationFailure(ctx, provider, msg)
		return true
	}
	msg := "Payment required (402): insufficient balance or billing issue"
	if observation.Message != "" {
		msg = "Payment required (402): " + observation.Message
	}
	s.ApplyAuthenticationFailure(ctx, provider, msg)
	return true
}
