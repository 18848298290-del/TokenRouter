package provider

import (
	"testing"
	"testing/synctest"
	"time"
)

// TestNewAntigravitySessionStore constructs, starts, and stops session
// storage. The constructor must provide a nonnil store and initialized entry
// map for subsequent authorization session operations.
func TestNewAntigravitySessionStore(t *testing.T) {
	store := NewAntigravityAuthorizationSessions()
	store.Start()
	defer store.Stop()

	if store == nil {
		t.Fatal("NewAntigravityAuthorizationSessions 返回 nil")
	}
	if store.sessions == nil {
		t.Error("sessions map 不应为 nil")
	}
}

// TestAntigravitySessionStore_SetAndGet stores fresh OAuth proof and proxy
// data under one identifier. Lookup must find that entry and return its state,
// verifier, and selected proxy.
func TestAntigravitySessionStore_SetAndGet(t *testing.T) {
	store := NewAntigravityAuthorizationSessions()
	store.Start()
	defer store.Stop()

	session := &AntigravityAuthorizationSession{
		State:        "test-state",
		CodeVerifier: "test-verifier",
		ProxyURL:     "http://proxy.example.com",
		CreatedAt:    time.Now(),
	}

	store.Set("session-1", session)

	got, ok := store.Get("session-1")
	if !ok {
		t.Fatal("Get 应返回 true")
	}
	if got.State != "test-state" {
		t.Errorf("State 不匹配: got %s", got.State)
	}
	if got.CodeVerifier != "test-verifier" {
		t.Errorf("CodeVerifier 不匹配: got %s", got.CodeVerifier)
	}
	if got.ProxyURL != "http://proxy.example.com" {
		t.Errorf("ProxyURL 不匹配: got %s", got.ProxyURL)
	}
}

// TestAntigravitySessionStore_Get_不存在 requests an identifier absent from
// initialized storage. Get must report false so the exchange caller can reject
// missing authorization proof.
func TestAntigravitySessionStore_Get_不存在(t *testing.T) {
	store := NewAntigravityAuthorizationSessions()
	store.Start()
	defer store.Stop()

	_, ok := store.Get("nonexistent")
	if ok {
		t.Error("不存在的 session 应返回 false")
	}
}

// TestAntigravitySessionStore_Get_过期 stores proof older than the configured
// TTL. Lookup must report false even though the entry was just inserted and
// periodic cleanup may not have removed it.
func TestAntigravitySessionStore_Get_过期(t *testing.T) {
	store := NewAntigravityAuthorizationSessions()
	store.Start()
	defer store.Stop()

	session := &AntigravityAuthorizationSession{
		State:     "expired-state",
		CreatedAt: time.Now().Add(-AntigravitySessionTTL - time.Minute), // Already expired.
	}

	store.Set("expired-session", session)

	_, ok := store.Get("expired-session")
	if ok {
		t.Error("过期的 session 应返回 false")
	}
}

// TestAntigravitySessionStore_Delete inserts and removes one fresh session. A
// later Get must report false for that identifier so consumed proof cannot be
// retrieved again.
func TestAntigravitySessionStore_Delete(t *testing.T) {
	store := NewAntigravityAuthorizationSessions()
	store.Start()
	defer store.Stop()

	session := &AntigravityAuthorizationSession{
		State:     "to-delete",
		CreatedAt: time.Now(),
	}

	store.Set("del-session", session)
	store.Delete("del-session")

	_, ok := store.Get("del-session")
	if ok {
		t.Error("删除后 Get 应返回 false")
	}
}

// TestAntigravitySessionStore_Delete_不存在 removes an absent identifier from
// initialized storage. The operation must return normally while the test-owned
// cleanup worker remains available for final Stop.
func TestAntigravitySessionStore_Delete_不存在(t *testing.T) {
	store := NewAntigravityAuthorizationSessions()
	store.Start()
	defer store.Stop()

	// Deleting a missing session is safe.
	store.Delete("nonexistent")
}

// TestAntigravitySessionStore_Stop starts cleanup and invokes Stop twice. Both
// calls must return normally, exercising repeated shutdown after the store has
// closed its cleanup signal.
func TestAntigravitySessionStore_Stop(t *testing.T) {
	store := NewAntigravityAuthorizationSessions()
	store.Start()
	store.Stop()

	// Repeated Stop calls are safe.
	store.Stop()
}

// TestAntigravitySessionStore_多个Session inserts ten fresh sessions under
// separate identifiers. Every identifier must remain retrievable so one entry
// cannot replace another differently keyed authorization session.
func TestAntigravitySessionStore_多个Session(t *testing.T) {
	store := NewAntigravityAuthorizationSessions()
	store.Start()
	defer store.Stop()

	for i := range 10 {
		session := &AntigravityAuthorizationSession{
			State:     "state-" + string(rune('0'+i)),
			CreatedAt: time.Now(),
		}
		store.Set("session-"+string(rune('0'+i)), session)
	}

	// Every stored session remains independently retrievable.
	for i := range 10 {
		_, ok := store.Get("session-" + string(rune('0'+i)))
		if !ok {
			t.Errorf("session-%d 应存在", i)
		}
	}
}

// TestAntigravityAuthorizationSessions_CreatedAtLifetime inserts an
// authorization session at its exact creation-time limit. Lookup returns the
// same pointer, then rejects older proof while retaining its entry until
// deletion or periodic cleanup.
func TestAntigravityAuthorizationSessions_CreatedAtLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewAntigravityAuthorizationSessions()
		defer store.Stop()
		session := &AntigravityAuthorizationSession{CreatedAt: time.Now().Add(-AntigravitySessionTTL)}
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
		future := &AntigravityAuthorizationSession{CreatedAt: time.Now().Add(time.Hour)}
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

// TestAntigravityAuthorizationSessions_CleanupLifetime starts a worker with
// proof that will reach its TTL at the first tick. Cleanup retains that
// exact-age entry and removes older proof, then removes the retained entry on
// the following tick.
func TestAntigravityAuthorizationSessions_CleanupLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewAntigravityAuthorizationSessions()
		defer store.Stop()
		boundary := &AntigravityAuthorizationSession{
			CreatedAt: time.Now().Add(-AntigravitySessionTTL + 5*time.Minute),
		}
		expired := &AntigravityAuthorizationSession{CreatedAt: time.Now().Add(-AntigravitySessionTTL)}
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

// TestAntigravityAuthorizationSessions_StopBeforeStart stops an idle store
// before requesting cleanup. The terminal lifecycle keeps entries available to
// storage methods and prevents later starts from admitting a worker.
func TestAntigravityAuthorizationSessions_StopBeforeStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewAntigravityAuthorizationSessions()
		store.Stop()
		store.Start()
		store.Stop()
		if store.runtimeStarted || !store.runtimeStopped {
			t.Fatal("stop-before-start did not retain terminal lifecycle state")
		}
		session := &AntigravityAuthorizationSession{CreatedAt: time.Now()}
		store.Set("stopped", session)
		if got, ok := store.Get("stopped"); !ok || got != session {
			t.Fatalf("stopped store: got (%p, %v), want (%p, true)", got, ok, session)
		}
		// Advance past both expiry and several scheduled sweep times. A store
		// stopped before startup keeps expired membership because no cleanup worker runs.
		time.Sleep(AntigravitySessionTTL + 5*time.Minute)
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
