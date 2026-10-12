package failover

const (
	// OAuth429MaxProviderAttempts bounds OpenAI provider attempts after same-provider retry windows expire.
	OAuth429MaxProviderAttempts   = 3
	OAuth429StormMinSwitches      = 1
	FirstOutputTimeoutMaxSwitches = 1
)

// OAuth429State tracks whether a Grok 429 has scheduled its final follow-up attempt.
type OAuth429State struct{ grokFollowupPending bool }

// OAuth429Provider contains the provider categories determined by the caller.
type OAuth429Provider struct{ OpenAI, Grok bool }

// StopOAuth429 applies platform-specific retry budgets; nil state uses the stateless Grok status/count rule.
func StopOAuth429(provider OAuth429Provider, status, failedSwitches int, state *OAuth429State) bool {
	if failedSwitches < OAuth429StormMinSwitches {
		return false
	}
	if state != nil && state.grokFollowupPending {
		return true
	}
	if provider.Grok {
		if state == nil {
			return status == 429 && failedSwitches >= 2
		}
		if status == 429 {
			state.grokFollowupPending = true
		}
		return false
	}
	if status != 429 || !provider.OpenAI {
		return false
	}
	return failedSwitches >= OAuth429MaxProviderAttempts
}

// FirstOutputExhausted counts eligible first-output recovery switches and reports exhaustion.
func FirstOutputExhausted(eligible bool, switches *int) bool {
	if !eligible || switches == nil {
		return false
	}
	if *switches >= FirstOutputTimeoutMaxSwitches {
		return true
	}
	*switches++
	return false
}
