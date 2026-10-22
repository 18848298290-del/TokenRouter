package provider

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// TestSessionStore_SetAndGet stores a fresh Code Assist authorization session.
// Get must find its identifier and return the saved state used by later code
// exchange.
func TestSessionStore_SetAndGet(t *testing.T) {
	store := NewGeminiAuthorizationSessions()
	store.Start()
	defer store.Stop()

	session := &GeminiOAuthSession{
		State:     "test-state",
		OAuthType: "code_assist",
		CreatedAt: time.Now(),
	}
	store.Set("sid-1", session)

	got, ok := store.Get("sid-1")
	if !ok {
		t.Fatal("期望 Get 返回 ok=true，实际返回 false")
	}
	if got.State != "test-state" {
		t.Errorf("期望 State=%q，实际=%q", "test-state", got.State)
	}
}

// TestSessionStore_GetNotFound queries an absent identifier in initialized
// Gemini storage. Get must report false so missing proof remains
// distinguishable from a usable authorization session.
func TestSessionStore_GetNotFound(t *testing.T) {
	store := NewGeminiAuthorizationSessions()
	store.Start()
	defer store.Stop()

	_, ok := store.Get("不存在的ID")
	if ok {
		t.Error("期望不存在的 sessionID 返回 ok=false")
	}
}

// TestSessionStore_GetExpired inserts a session one minute beyond its TTL.
// Lookup must reject that stale proof immediately, independently of the
// cleanup worker's next removal tick.
func TestSessionStore_GetExpired(t *testing.T) {
	store := NewGeminiAuthorizationSessions()
	store.Start()
	defer store.Stop()

	// Place the session one minute beyond its TTL.
	session := &GeminiOAuthSession{
		State:     "expired-state",
		OAuthType: "code_assist",
		CreatedAt: time.Now().Add(-(GeminiAuthorizationSessionTTL + 1*time.Minute)),
	}
	store.Set("expired-sid", session)

	_, ok := store.Get("expired-sid")
	if ok {
		t.Error("期望过期的 session 返回 ok=false")
	}
}

// TestSessionStore_Delete confirms a fresh session exists before removing it.
// Get must report false after deletion so the previously stored identifier
// cannot supply consumed proof.
func TestSessionStore_Delete(t *testing.T) {
	store := NewGeminiAuthorizationSessions()
	store.Start()
	defer store.Stop()

	session := &GeminiOAuthSession{
		State:     "to-delete",
		OAuthType: "code_assist",
		CreatedAt: time.Now(),
	}
	store.Set("del-sid", session)

	if _, ok := store.Get("del-sid"); !ok {
		t.Fatal("删除前 session 应该存在")
	}

	store.Delete("del-sid")

	if _, ok := store.Get("del-sid"); ok {
		t.Error("删除后 session 不应该存在")
	}
}

// TestSessionStore_Stop_Idempotent starts the cleanup worker and invokes Stop
// three times. Each call must finish normally, exercising shutdown with an
// already-closed cleanup signal.
func TestSessionStore_Stop_Idempotent(t *testing.T) {
	store := NewGeminiAuthorizationSessions()
	store.Start()

	// Repeated Stop calls must not panic.
	store.Stop()
	store.Stop()
	store.Stop()
}

// TestSessionStore_ConcurrentAccess races fresh writes, lookups, and deletions
// across overlapping identifiers. All workers must finish normally; this
// scenario permits either lookup outcome during concurrent replacement and
// removal.
func TestSessionStore_ConcurrentAccess(t *testing.T) {
	store := NewGeminiAuthorizationSessions()
	store.Start()
	defer store.Stop()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	// Overlapping identifiers make map readers compete with replacement and
	// deletion. Join every worker before cleanup, while accepting either read
	// outcome during those races.
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			sid := "concurrent-" + string(rune('A'+idx%26))
			store.Set(sid, &GeminiOAuthSession{
				State:     sid,
				OAuthType: "code_assist",
				CreatedAt: time.Now(),
			})
		}(i)
	}

	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			sid := "concurrent-" + string(rune('A'+idx%26))
			store.Get(sid) // Either lookup result is valid while writers and deleters race.
		}(i)
	}

	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			sid := "concurrent-" + string(rune('A'+idx%26))
			store.Delete(sid)
		}(i)
	}

	wg.Wait()
}

// TestGeminiAuthorizationSessions_CreatedAtLifetime inserts an authorization
// session at its exact creation-time limit. Lookup returns the same pointer,
// then rejects older proof while retaining its entry until deletion or periodic
// cleanup.
func TestGeminiAuthorizationSessions_CreatedAtLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewGeminiAuthorizationSessions()
		defer store.Stop()
		session := &GeminiOAuthSession{CreatedAt: time.Now().Add(-GeminiAuthorizationSessionTTL)}
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
		future := &GeminiOAuthSession{CreatedAt: time.Now().Add(time.Hour)}
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

// TestGeminiAuthorizationSessions_CleanupLifetime starts a worker with proof
// that will reach its TTL at the first tick. Cleanup retains that exact-age
// entry and removes older proof, then removes the retained entry on the
// following tick.
func TestGeminiAuthorizationSessions_CleanupLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewGeminiAuthorizationSessions()
		defer store.Stop()
		boundary := &GeminiOAuthSession{
			CreatedAt: time.Now().Add(-GeminiAuthorizationSessionTTL + 5*time.Minute),
		}
		expired := &GeminiOAuthSession{CreatedAt: time.Now().Add(-GeminiAuthorizationSessionTTL)}
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

// TestGeminiAuthorizationSessions_StopBeforeStart stops an idle store before
// requesting cleanup. The terminal lifecycle keeps entries available to storage
// methods and prevents later starts from admitting a worker.
func TestGeminiAuthorizationSessions_StopBeforeStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewGeminiAuthorizationSessions()
		store.Stop()
		store.Start()
		store.Stop()
		if store.runtimeStarted || !store.runtimeStopped {
			t.Fatal("stop-before-start did not retain terminal lifecycle state")
		}
		session := &GeminiOAuthSession{CreatedAt: time.Now()}
		store.Set("stopped", session)
		if got, ok := store.Get("stopped"); !ok || got != session {
			t.Fatalf("stopped store: got (%p, %v), want (%p, true)", got, ok, session)
		}
		// Advance past both expiry and several scheduled sweep times. A store
		// stopped before startup keeps expired membership because no cleanup worker runs.
		time.Sleep(GeminiAuthorizationSessionTTL + 5*time.Minute)
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
