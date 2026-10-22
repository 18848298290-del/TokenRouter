package provider

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// TestAuthorizationSessionStore_ConcurrentLifecycleAndStorage overlaps lifecycle
// calls with session replacement and deletion. All callers finish, shutdown
// remains terminal, and storage still returns the pointer published afterward.
func TestAuthorizationSessionStore_ConcurrentLifecycleAndStorage(t *testing.T) {
	store := newAuthorizationSessionStore(time.Minute, func(session *time.Time) time.Time { return *session })
	store.start()
	defer store.stop()
	var workers sync.WaitGroup
	// Exercise both locks concurrently with immutable session values. Joining all
	// callers makes the final assertions observe completed shutdown and map operations.
	for range 64 {
		workers.Go(func() {
			session := time.Now()
			store.start()
			store.set("shared", &session)
			store.get("shared")
			store.stop()
			store.delete("shared")
		})
	}
	workers.Wait()
	store.start()
	if !store.runtimeStopped {
		t.Fatal("concurrent shutdown did not retain its terminal state")
	}
	session := time.Now()
	store.set("after-stop", &session)
	if got, ok := store.get("after-stop"); !ok || got != &session {
		t.Fatalf("storage after shutdown: got (%p, %v), want (%p, true)", got, ok, &session)
	}
}

// TestAuthorizationSessionStore_StopAfterStart joins a running cleanup worker
// and then requests another start. A later expired insertion stays stored
// through a sweep interval because shutdown prevents another worker from starting.
func TestAuthorizationSessionStore_StopAfterStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newAuthorizationSessionStore(time.Minute, func(session *time.Time) time.Time { return *session })
		store.start()
		synctest.Wait()
		store.stop()
		session := time.Now().Add(-time.Hour)
		store.set("expired", &session)
		store.start()
		// Advance beyond the cleanup interval after the worker has joined. Retained
		// membership proves later startup cannot create an effective replacement worker.
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if store.sessions["expired"] != &session {
			t.Fatal("cleanup restarted after shutdown")
		}
		if got, ok := store.get("expired"); ok || got != nil {
			t.Fatalf("expired lookup after shutdown: got (%p, %v), want (nil, false)", got, ok)
		}
		store.stop()
	})
}
