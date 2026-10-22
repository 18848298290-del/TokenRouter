package provider

import (
	"sync"
	"testing"
	"testing/synctest"
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

// TestClaudeAuthorizationSessions_CreatedAtLifetime inserts an authorization
// session at its exact creation-time limit. Lookup returns the same pointer,
// then rejects older proof while retaining its entry until deletion or periodic
// cleanup.
func TestClaudeAuthorizationSessions_CreatedAtLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewClaudeAuthorizationSessions()
		defer store.Stop()
		session := &ClaudeAuthorizationSession{CreatedAt: time.Now().Add(-ClaudeAuthorizationSessionTTL)}
		store.Set("boundary", session)
		if got, ok := store.Get("boundary"); !ok || got != session {
			t.Fatalf("at TTL: got (%p, %v), want (%p, true)", got, ok, session)
		}

		// Move beyond the exact age limit while cleanup remains idle, separating
		// lookup expiry from physical eviction and from the recent insertion time.
		time.Sleep(time.Nanosecond)
		if got, ok := store.Get("boundary"); ok || got != nil {
			t.Fatalf("after TTL: got (%p, %v), want (nil, false)", got, ok)
		}
		if store.sessions["boundary"] != session {
			t.Fatal("expired lookup removed or replaced the stored pointer")
		}
		future := &ClaudeAuthorizationSession{CreatedAt: time.Now().Add(time.Hour)}
		store.Set("boundary", future)
		if got, ok := store.Get("boundary"); !ok || got != future {
			t.Fatalf("replacement: got (%p, %v), want (%p, true)", got, ok, future)
		}
		store.Delete("boundary")
		store.Delete("boundary")
		if got, ok := store.Get("boundary"); ok || got != nil {
			t.Fatalf("deleted: got (%p, %v), want (nil, false)", got, ok)
		}
	})
}

// TestClaudeAuthorizationSessions_CleanupLifetime starts a worker with proof
// that will reach its TTL at the first tick. Cleanup retains that exact-age
// entry and removes older proof, then removes the retained entry on the
// following tick.
func TestClaudeAuthorizationSessions_CleanupLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewClaudeAuthorizationSessions()
		defer store.Stop()
		boundary := &ClaudeAuthorizationSession{
			CreatedAt: time.Now().Add(-ClaudeAuthorizationSessionTTL + 5*time.Minute),
		}
		expired := &ClaudeAuthorizationSession{CreatedAt: time.Now().Add(-ClaudeAuthorizationSessionTTL)}
		store.Set("boundary", boundary)
		store.Set("expired", expired)
		if store.runtimeStarted {
			t.Fatal("constructor started cleanup")
		}
		store.Start()
		store.Start()
		// Wait until the admitted worker owns its ticker, then advance exactly
		// one interval so the boundary entry's age equals the configured TTL.
		synctest.Wait()
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		store.mu.RLock()
		retained := store.sessions["boundary"]
		_, expiredRetained := store.sessions["expired"]
		store.mu.RUnlock()
		if retained != boundary || expiredRetained {
			t.Fatalf("first sweep: boundary=%p expired=%v", retained, expiredRetained)
		}
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		store.mu.RLock()
		_, boundaryRetained := store.sessions["boundary"]
		store.mu.RUnlock()
		if boundaryRetained {
			t.Fatal("second sweep retained expired proof")
		}
	})
}

// TestClaudeAuthorizationSessions_StopBeforeStart stops an idle store before
// requesting cleanup. The terminal lifecycle keeps entries available to storage
// methods and prevents later starts from admitting a worker.
func TestClaudeAuthorizationSessions_StopBeforeStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewClaudeAuthorizationSessions()
		store.Stop()
		store.Start()
		store.Stop()
		if store.runtimeStarted || !store.runtimeStopped {
			t.Fatal("stop-before-start did not retain terminal lifecycle state")
		}
		session := &ClaudeAuthorizationSession{CreatedAt: time.Now()}
		store.Set("stopped", session)
		if got, ok := store.Get("stopped"); !ok || got != session {
			t.Fatalf("stopped store: got (%p, %v), want (%p, true)", got, ok, session)
		}
		// Advance past both expiry and several scheduled sweep times. A store
		// stopped before startup keeps expired membership because no cleanup worker runs.
		time.Sleep(ClaudeAuthorizationSessionTTL + 5*time.Minute)
		if got, ok := store.Get("stopped"); ok || got != nil {
			t.Fatalf("stopped expired entry: got (%p, %v), want (nil, false)", got, ok)
		}
		if store.sessions["stopped"] != session {
			t.Fatal("stopped cleanup removed retained proof")
		}
		store.Delete("stopped")
		if len(store.sessions) != 0 {
			t.Fatal("Delete did not remove proof after Stop")
		}
	})
}
