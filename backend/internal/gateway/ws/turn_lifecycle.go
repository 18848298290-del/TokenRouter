package ws

import "sync"

// A client can send its next response.create while the relay is delivering the
// previous turn's terminal frame. Admission has to remain closed through that
// write, because completion metadata may already be consumed before the client
// actually receives the terminal event.
//
// TurnLifecycle shares one mutex between admission and terminal delivery.
// BeginResponseCreate records an accepted turn under that lock, and
// BeginTerminalWrite holds the lock across the caller's synchronous write.
// FinishTerminalWrite clears inFlight only after a successful write, then
// unlocks admission. Callbacks run with the lock held and therefore cannot call
// back into the lifecycle; rejected preforward work uses CancelResponseCreate
// to release its reservation.

// TurnLifecycle serializes accepted response.create reservations with
// successful terminal frame delivery.
type TurnLifecycle struct {
	// mu protects inFlight and spans each paired terminal write. Admission and
	// success callbacks execute while this same mutex remains held.
	mu       sync.Mutex // admission and delivery lock
	inFlight bool       // accepted turn awaiting delivery
}

// NewTurnLifecycle initializes the admission barrier with the supplied
// active-turn state.
func NewTurnLifecycle(inFlight bool) *TurnLifecycle {
	return &TurnLifecycle{inFlight: inFlight}
}

// BeginResponseCreate reserves the next turn when no accepted turn remains in
// flight. Nil receivers and overlapping requests return false.
//
// The mutex shared with terminal delivery chooses whether a new response.create
// can proceed. Reservation sets inFlight before onAccepted publishes
// active-turn state, so competing admission cannot claim the same idle
// connection. The callback runs while the mutex is held and must not reenter
// TurnLifecycle. The client filter cancels this reservation if later preforward
// validation fails.
func (l *TurnLifecycle) BeginResponseCreate(onAccepted func()) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight {
		return false
	}
	// Reserve before notifying the idle reader, so another admission cannot
	// observe the connection as free during that notification.
	l.inFlight = true
	if onAccepted != nil {
		onAccepted()
	}
	return true
}

// CancelResponseCreate clears a reservation after the client filter rejects
// preforward work. Nil receivers are ignored; the filter calls it only for its
// own failed admission, before accepting another turn.
func (l *TurnLifecycle) CancelResponseCreate() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.inFlight = false
	l.mu.Unlock()
}

// BeginTerminalWrite locks admission across the caller's terminal frame write.
// Pair every call on a nonnil lifecycle with FinishTerminalWrite, even when
// delivery fails, so later readers can acquire the mutex.
func (l *TurnLifecycle) BeginTerminalWrite() {
	if l != nil {
		l.mu.Lock()
	}
}

// FinishTerminalWrite completes the caller's paired terminal write and releases
// the admission mutex. Every BeginTerminalWrite on a nonnil lifecycle needs
// this call, including failed writes.
//
// Successful delivery invokes onSucceeded and clears inFlight before unlocking,
// allowing the next waiting response.create to reserve its turn. Failure leaves
// the accepted turn in flight because the terminal frame was not delivered.
// onSucceeded executes inside the critical section and cannot reenter
// TurnLifecycle; the pairing keeps completion-state publication ordered with
// opening admission.
func (l *TurnLifecycle) FinishTerminalWrite(succeeded bool, onSucceeded func()) {
	if l == nil {
		return
	}
	if succeeded {
		if onSucceeded != nil {
			onSucceeded()
		}
		l.inFlight = false
	}
	l.mu.Unlock()
}
