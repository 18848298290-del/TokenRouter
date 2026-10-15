package identity

import "time"

// clock.go supplies operation-time reads for authentication and administrator
// services.
// Authentication decisions read time at each operation so signing, expiry and
// challenge checks can share an injected clock. Missing clock functions select
// time.Now, while optional clock lists take their first entry even when later
// entries contain a usable function.

// operationClock reads the injected clock on each call, defaulting to time.Now.
type operationClock struct { /* read supplies operation time; nil selects time.Now whenever
	the clock is read by an authentication or challenge operation. */read func() time.Time
}

// now reads this operation clock or the current wall time.
// A nil read function selects time.Now on every call, so constructing the clock
// does not capture a fixed timestamp.
func (c operationClock) now() time.Time {
	if c.read != nil {
		return c.read()
	}
	return time.Now()
}

// clockFromOptional selects the first function from an optional clock list.
// Empty lists leave the clock unset, and a nil first entry uses the wall-time
// behavior of operationClock.now; later entries are ignored.
func clockFromOptional(clocks []func() time.Time) operationClock {
	if len(clocks) > 0 {
		return operationClock{read: clocks[0]}
	}
	return operationClock{}
}

// now reads the session service's configured clock for this operation.
// A nil service or nil clock selects time.Now, allowing signing and validation
// to use the same configured source when present.
func (s *SessionService) now() time.Time {
	if s != nil && s.options.Now != nil {
		return s.options.Now()
	}
	return time.Now()
}

// now reads the administrator service's clock for this operation.
// The service must be non-nil; an unset Now function selects time.Now each time
// an administrative timestamp is needed.
func (s *UserAdmin) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
