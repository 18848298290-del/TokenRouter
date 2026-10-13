package provider

import (
	"testing"
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
