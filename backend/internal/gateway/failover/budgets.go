package failover

// budgets.go limits OAuth rate-limit recovery and first-output provider
// switches. OpenAI OAuth 429 failures can consume several complete provider
// retry windows before the outer loop stops. Grok OAuth uses a different rule:
// the first eligible 429 schedules one final attempt, and a later failed
// attempt stops even when it has another status or provider category.
//
// The caller supplies category flags and an already counted failed switch
// total, keeping platform identification outside this budget. A request-local
// state remembers the Grok follow-up across those failures. Without that state
// the Grok rule checks the current status and count alone. First-output
// recovery has a separate counter so its single permitted switch does not
// consume this OAuth rule implicitly.

// These limits govern outer provider recovery after inner retry windows finish;
// first-output failures use a separate switch counter supplied by their caller.
const (
	// OAuth429MaxProviderAttempts bounds OpenAI provider attempts after
	// same-provider retry windows expire.
	OAuth429MaxProviderAttempts   = 3
	OAuth429StormMinSwitches      = 1 // minimum eligible failed switch count
	FirstOutputTimeoutMaxSwitches = 1 // permitted first-output recovery switches
)

// OAuth429State tracks whether a Grok 429 has scheduled its final follow-up attempt.
type OAuth429State struct {
	// grokFollowupPending records an eligible Grok 429. Later failures with a
	// positive switch count stop before their current category or status is checked.
	grokFollowupPending bool // final Grok follow-up is pending
}

// OAuth429Provider contains the provider categories determined by the caller.
type OAuth429Provider struct {
	// OpenAI identifies an OAuth-like OpenAI provider; Grok identifies a Grok OAuth
	// provider. The execution adapter determines both flags, and Grok wins if both
	// flags are supplied together for the same failure.
	OpenAI, Grok bool
}

// StopOAuth429 reports whether an outer failure reaches the caller's
// platform-specific recovery budget. A count below one is ignored, and callers reuse
// one state for the request when tracking Grok follow-up attempts.
//
// An eligible Grok 429 arms the state and permits one further attempt; its
// next eligible failure stops regardless of status or provider type. Nil state
// uses a stateless two-failure 429 rule instead. OpenAI OAuth 429 failures
// stop at three failed switches, while other current failures pass unless the
// shared Grok state was already armed.
func StopOAuth429(provider OAuth429Provider, status, failedSwitches int, state *OAuth429State) bool {
	if failedSwitches < OAuth429StormMinSwitches {
		return false
	}
	// Consult the earlier Grok failure before current category checks, because its
	// final follow-up budget applies even when reselection changes provider type.
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

// FirstOutputExhausted consumes one eligible first-output switch and reports
// exhaustion on the next eligible call. Ineligible failures and nil counters
// return false unchanged; the caller owns request-local counter serialization.
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
