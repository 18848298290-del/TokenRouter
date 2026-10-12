package provider

import (
	"sync"
	"testing"
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
