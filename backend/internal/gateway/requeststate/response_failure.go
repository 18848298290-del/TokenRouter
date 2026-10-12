package requeststate

import "sync"

// A Responses bridge can apply provider health effects before the HTTP output
// adapter handles the terminal error. The adapter consumes recorded evidence to
// avoid applying the same observed effects during that handoff.
//
// The mutex protects one latest status/disabled pair and a separate presence
// flag. Store overwrites any pending pair, and Consume clears presence while
// returning the captured payload under the same lock. A later Store creates
// another consumable observation; repeated reads can still return old payload
// values with present false. Consumers therefore use the presence result to
// decide whether recorded effects apply, and this slot is not a queue of every
// failure event.

// ResponseFailureEffects synchronizes the latest provider-effect evidence
// passed to Responses output.
type ResponseFailureEffects struct {
	// mu protects status, disabled, and present as one observation. Payload values
	// survive consumption, while present determines whether the output adapter
	// can use them as newly available effect evidence.
	mu       sync.Mutex // latest observation synchronization lock
	status   int        // applied upstream policy status
	disabled bool       // provider scheduling disabled result
	present  bool       // latest observation available for consumption
}

// Store replaces the latest applied-effect evidence and marks it available to
// the output adapter. Callers provide a nonnil slot and record effects that the
// producer has already handled.
//
// Store and Consume serialize the payload and presence change under one mutex.
// A new store can replace an unconsumed observation, so consumers see the
// latest status and disabled result. This operation records evidence; the
// producing adapter performs provider health updates before recording that
// result.
func (s *ResponseFailureEffects) Store(status int, disabled bool) {
	s.mu.Lock()
	// Publish payload and presence together so a consuming output adapter cannot
	// pair a newly available marker with an older status or disabled result.
	s.status, s.disabled, s.present = status, disabled, true
	s.mu.Unlock()
}

// Consume returns the latest status/disabled pair and whether it was available,
// then clears presence under the same mutex. Nil slots return zero, false,
// false.
//
// The payload remains stored after consumption, so a subsequent call can return
// those values with false presence. A concurrent Store is ordered before or
// after this complete read-and-clear operation and can make its newer
// observation available again. The HTTP output adapter checks presence before
// deciding that an earlier provider-effect application should replace its own
// handling.
func (s *ResponseFailureEffects) Consume() (int, bool, bool) {
	if s == nil {
		return 0, false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Read and clear the marker within one lock so concurrent consumers cannot
	// both treat this same pending observation as already-applied provider effects.
	status, disabled, present := s.status, s.disabled, s.present
	s.present = false
	return status, disabled, present
}
