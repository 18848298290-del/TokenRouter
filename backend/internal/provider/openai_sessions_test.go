package provider

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// OpenAI authorization cleanup has one worker and one permanent stop channel
// per store. Repeated and concurrent stop calls must all return after that
// worker exits, leaving the same closed signal available for the assertions
// below.

// TestOpenAISessionStore_Stop_Idempotent starts cleanup and stops the same
// store twice. Both calls finish and the stop channel remains closed,
// protecting repeated shutdown of an initialized store.
func TestOpenAISessionStore_Stop_Idempotent(t *testing.T) {
	store := NewOpenAISessionStore()
	store.Start()

	store.Stop()
	store.Stop()

	select {
	case <-store.stopCh:
	case <-time.After(time.Second):
		t.Fatal("stopCh 未关闭")
	}
}

// TestOpenAISessionStore_Stop_Concurrent starts cleanup and launches fifty
// concurrent stoppers. Joining those callers and observing the closed channel
// protect shared shutdown completion and single channel closure.
func TestOpenAISessionStore_Stop_Concurrent(t *testing.T) {
	store := NewOpenAISessionStore()
	store.Start()

	var wg sync.WaitGroup
	// Competing callers exercise the runtime-state guard together; joining their
	// work makes the final channel assertion observe completed Stop calls rather than launches.
	for range 50 {
		wg.Go(func() {
			store.Stop()
		})
	}

	wg.Wait()

	select {
	case <-store.stopCh:
	case <-time.After(time.Second):
		t.Fatal("stopCh 未关闭")
	}
}

// TestOpenAISessionStore_CreatedAtLifetime inserts an authorization session at
// its exact creation-time limit. Lookup returns the same pointer, then rejects
// older proof while retaining its entry until deletion or periodic cleanup.
func TestOpenAISessionStore_CreatedAtLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewOpenAISessionStore()
		defer store.Stop()
		session := &OpenAIOAuthSession{CreatedAt: time.Now().Add(-OpenAISessionTTL)}
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
		future := &OpenAIOAuthSession{CreatedAt: time.Now().Add(time.Hour)}
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

// TestOpenAISessionStore_CleanupLifetime starts a worker with proof that will
// reach its TTL at the first tick. Cleanup retains that exact-age entry and
// removes older proof, then removes the retained entry on the following tick.
func TestOpenAISessionStore_CleanupLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewOpenAISessionStore()
		defer store.Stop()
		boundary := &OpenAIOAuthSession{CreatedAt: time.Now().Add(-OpenAISessionTTL + 5*time.Minute)}
		expired := &OpenAIOAuthSession{CreatedAt: time.Now().Add(-OpenAISessionTTL)}
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

// TestOpenAISessionStore_StopBeforeStart stops an idle store before requesting
// cleanup. The terminal lifecycle keeps entries available to storage methods
// and prevents later starts from admitting a worker.
func TestOpenAISessionStore_StopBeforeStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewOpenAISessionStore()
		store.Stop()
		store.Start()
		store.Stop()
		if store.runtimeStarted || !store.runtimeStopped {
			t.Fatal("stop-before-start did not retain terminal lifecycle state")
		}
		session := &OpenAIOAuthSession{CreatedAt: time.Now()}
		store.Set("stopped", session)
		if got, ok := store.Get("stopped"); !ok || got != session {
			t.Fatalf("stopped store: got (%p, %v), want (%p, true)", got, ok, session)
		}
		// Advance past both expiry and several scheduled sweep times. A store
		// stopped before startup keeps expired membership because no cleanup worker runs.
		time.Sleep(OpenAISessionTTL + 5*time.Minute)
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
