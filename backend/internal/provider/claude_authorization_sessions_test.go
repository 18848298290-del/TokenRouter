package provider

import (
	"sync"
	"testing"
	"time"
)

// TestClaudeAuthorizationSessions_Stop_Idempotent starts cleanup and stops it
// twice. Both calls must finish and the retained stop signal must be closed
// for lifecycle observers.
func TestClaudeAuthorizationSessions_Stop_Idempotent(t *testing.T) {
	store := NewClaudeAuthorizationSessions()
	store.Start()

	store.Stop()
	store.Stop()

	select {
	case <-store.stopCh:
		// ok
	case <-time.After(time.Second):
		t.Fatal("stopCh 未关闭")
	}
}

// TestClaudeAuthorizationSessions_Stop_Concurrent runs fifty Stop callers
// against one started store. Every caller must return and the stop signal must
// be closed after their shared completion.
func TestClaudeAuthorizationSessions_Stop_Concurrent(t *testing.T) {
	store := NewClaudeAuthorizationSessions()
	store.Start()

	var wg sync.WaitGroup
	// Compete on the one close operation, then join all stop callers before
	// inspecting the signal so duplicate closure or waiting failures remain
	// observable.
	for range 50 {
		wg.Go(func() {
			store.Stop()
		})
	}

	wg.Wait()

	select {
	case <-store.stopCh:
		// ok
	case <-time.After(time.Second):
		t.Fatal("stopCh 未关闭")
	}
}
