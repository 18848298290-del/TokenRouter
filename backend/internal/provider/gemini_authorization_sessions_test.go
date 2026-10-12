package provider

import (
	"sync"
	"testing"
	"time"
)

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

func TestSessionStore_GetNotFound(t *testing.T) {
	store := NewGeminiAuthorizationSessions()
	store.Start()
	defer store.Stop()

	_, ok := store.Get("不存在的ID")
	if ok {
		t.Error("期望不存在的 sessionID 返回 ok=false")
	}
}

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

func TestSessionStore_Stop_Idempotent(t *testing.T) {
	store := NewGeminiAuthorizationSessions()
	store.Start()

	// Repeated Stop calls must not panic.
	store.Stop()
	store.Stop()
	store.Stop()
}

func TestSessionStore_ConcurrentAccess(t *testing.T) {
	store := NewGeminiAuthorizationSessions()
	store.Start()
	defer store.Stop()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	// Race writes with reads and deletions of the same session IDs.
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
