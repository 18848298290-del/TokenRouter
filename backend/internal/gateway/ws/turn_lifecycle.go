package ws

import "sync"

// TurnLifecycle serializes terminal writes with next-turn admission while allowing bidirectional relay.
type TurnLifecycle struct {
	mu       sync.Mutex
	inFlight bool
}

func NewTurnLifecycle(inFlight bool) *TurnLifecycle {
	return &TurnLifecycle{inFlight: inFlight}
}

func (l *TurnLifecycle) BeginResponseCreate(onAccepted func()) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight {
		return false
	}
	l.inFlight = true
	if onAccepted != nil {
		onAccepted()
	}
	return true
}

func (l *TurnLifecycle) CancelResponseCreate() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.inFlight = false
	l.mu.Unlock()
}

// BeginTerminalWrite locks admission until FinishTerminalWrite is called.
func (l *TurnLifecycle) BeginTerminalWrite() {
	if l != nil {
		l.mu.Lock()
	}
}

// FinishTerminalWrite releases the lock taken by BeginTerminalWrite.
// onSucceeded runs under that lock and must not reenter TurnLifecycle.
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
