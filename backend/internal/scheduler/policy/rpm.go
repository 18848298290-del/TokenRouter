package policy

// rpm.go determines provider RPM allowance and calculates the extra capacity
// reserved for sticky requests.
// Provider RPM admission separates ordinary capacity from an affinity-only
// reserve. Counts below the configured base admit ordinary requests; reaching
// that base changes which requests can use the provider. The sticky_exempt
// strategy keeps existing affinity eligible after the reserve would otherwise
// be exhausted. Other strategies block at base plus the buffer.
//
// RPMStickyBuffer derives the reserve from concurrency and session settings
// unless a positive explicit override is supplied. Negative capacity values
// contribute zero, and a positive base imposes a whole-request minimum. This
// policy classifies a soft observation and returns an allowance category;
// consumers determine whether the request has affinity and record its RPM
// increment separately.

// RPM allowance values tell provider eligibility whether ordinary requests,
// existing affinity, or no requests can proceed at the observed count.
const (
	RPMAllowed    RPMAllowance = iota // ordinary requests allowed
	RPMStickyOnly                     // existing affinity required
	RPMBlocked                        // all requests blocked
)

// RPMAllowance identifies the provider admission decision returned by the RPM
// policy.
type RPMAllowance int

// CheckRPM classifies a soft observed count against the provider base and
// reserve. A disabled base admits ordinary requests.
//
// Counts at the base admit sticky requests while below base plus buffer;
// sticky_exempt keeps that affinity-only result regardless of the reserve
// ceiling. Other strategies block at the ceiling. Callers decide whether a
// request has the qualifying affinity and perform recording separately from
// this read-based decision.
func CheckRPM(current, base, buffer int, strategy string) RPMAllowance {
	if base <= 0 || current < base {
		return RPMAllowed
	}
	if strategy == "sticky_exempt" {
		return RPMStickyOnly
	}
	if current < base+buffer {
		return RPMStickyOnly
	}
	return RPMBlocked
}

// RPMStickyBuffer calculates reserve capacity from provider concurrency and
// active-session settings. A positive override takes precedence even when the
// base is disabled.
//
// Without an override, nonpositive base returns zero, negative capacity inputs
// are clamped to zero, and the reserve is their sum with a minimum of one
// fifth of base or one request. This keeps a positive reserve available when
// the configured capacity inputs contribute nothing.
func RPMStickyBuffer(base, concurrency, sessions, override int) int {
	if override > 0 {
		return override
	}
	if base <= 0 {
		return 0
	}
	if concurrency < 0 {
		concurrency = 0
	}
	if sessions < 0 {
		sessions = 0
	}
	buffer := concurrency + sessions
	// Round the proportional minimum down to whole requests, with one
	// request retained for small positive base allowances.
	floor := max(base/5, 1)
	if buffer < floor {
		buffer = floor
	}
	return buffer
}
