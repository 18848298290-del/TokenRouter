package policy

const (
	RPMAllowed RPMAllowance = iota
	RPMStickyOnly
	RPMBlocked
)

// RPMAllowance distinguishes ordinary admission, sticky-only admission, and rejection.
type RPMAllowance int

// CheckRPM restricts admission to sticky requests at base and rejects at base+buffer.
// The sticky_exempt strategy keeps admitting sticky requests above base without a buffer ceiling.
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

// RPMStickyBuffer uses a positive override; otherwise it adds nonnegative concurrency and session counts.
// With no override, a positive base sets a minimum of max(base/5, 1); a nonpositive base returns zero.
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
	floor := max(base/5, 1)
	if buffer < floor {
		buffer = floor
	}
	return buffer
}
