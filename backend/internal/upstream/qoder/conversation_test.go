package qoder

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

func TestQoderConversationStoreExpiresState(t *testing.T) {
	store := NewQoderConversationStore(5 * time.Millisecond)
	messages := []QoderMessage{{Role: "user", Text: "hello"}}

	plan := store.PlanWithOptions("key", "", nil, messages, QoderConversationPlanOptions{})
	require.NotNil(t, plan)
	plan.Commit()

	time.Sleep(10 * time.Millisecond)

	next := store.PlanWithOptions("key", "", nil, messages, QoderConversationPlanOptions{})
	require.False(t, next.Reused)
	require.True(t, next.IncludeSystem)
	require.Len(t, next.MessagesToSend, 1)
}

// TestQoderConversationStoreConcurrent checks state and version after concurrent commits.
func TestQoderConversationStoreConcurrent(t *testing.T) {
	store := NewQoderConversationStore(5 * time.Minute)

	const numGoroutines = 50
	const numOpsPerGoroutine = 100

	// All workers share the conversation and commit the same fingerprints.
	key := "test_conversation_key"
	sessionID := "test_session_id"

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Interleave reads, commits, and rollback calls for one conversation.
	for i := range numGoroutines {
		go func(workerID int) {
			defer wg.Done()
			for j := range numOpsPerGoroutine {
				// Capture the state visible to this operation.
				store.Mu.Lock()
				currentState := store.Items[key]
				store.Mu.Unlock()

				// PreviousState is the restoration target if this plan is rolled back.
				plan := &QoderConversationPlan{
					Store:             store,
					Key:               key,
					SessionID:         sessionID,
					SystemFingerprint: "system_v1",
					ToolsFingerprint:  "tools_v1",
					PreviousState:     CloneQoderConversationState(currentState),
				}

				// Each commit increments the version.
				fingerprints := []string{"msg_1", "msg_2", "msg_3"}
				plan.CommitFingerprints(fingerprints)

				// Exercise the rollback guard without an accepted state on selected operations.
				if workerID%2 == 0 && j%10 == 0 {
					plan.AcceptedCommitted = true
					plan.RollbackAccepted()
				}
			}
		}(i)
	}

	wg.Wait()

	// Concurrent commits leave a valid versioned conversation.
	store.Mu.Lock()
	finalState := store.Items[key]
	store.Mu.Unlock()
	if finalState == nil {
		t.Error("expected final state to exist")
		return
	}
	if finalState.SessionID != sessionID {
		t.Errorf("expected sessionID=%s, got=%s", sessionID, finalState.SessionID)
	}
	if finalState.Version <= 0 {
		t.Errorf("expected version > 0, got=%d", finalState.Version)
	}
}

// TestQoderConversationRollbackVersionControl checks that stale rollback cannot overwrite a later commit.
func TestQoderConversationRollbackVersionControl(t *testing.T) {
	store := NewQoderConversationStore(5 * time.Minute)
	key := "test_key"
	sessionID := "session_1"

	// Establish version 1.
	plan1 := &QoderConversationPlan{
		Store:             store,
		Key:               key,
		SessionID:         sessionID,
		SystemFingerprint: "sys_v1",
		ToolsFingerprint:  "tools_v1",
	}
	plan1.CommitFingerprints([]string{"msg_1"})

	store.Mu.Lock()
	state1 := store.Items[key]
	store.Mu.Unlock()
	if state1 == nil || state1.Version != 1 {
		t.Fatalf("expected version=1, got=%v", state1)
	}

	// Record the state accepted by plan1 for the rollback comparison.
	plan1.PreviousState = CloneQoderConversationState(state1)
	plan1.AcceptedState = CloneQoderConversationState(state1)
	plan1.AcceptedCommitted = true

	// A competing plan advances the conversation to version 2.
	plan2 := &QoderConversationPlan{
		Store:             store,
		Key:               key,
		SessionID:         sessionID,
		SystemFingerprint: "sys_v1",
		ToolsFingerprint:  "tools_v1",
	}
	plan2.CommitFingerprints([]string{"msg_1", "msg_2"})

	store.Mu.Lock()
	state2 := store.Items[key]
	store.Mu.Unlock()
	if state2 == nil || state2.Version != 2 {
		t.Fatalf("expected version=2, got=%v", state2)
	}

	// plan1 still refers to accepted version 1.
	// Its rollback must leave version 2 intact.
	plan1.RollbackAccepted()

	store.Mu.Lock()
	stateFinal := store.Items[key]
	store.Mu.Unlock()
	if stateFinal == nil {
		t.Fatal("expected state to exist after rollback")
		return
	}
	if stateFinal.Version != 2 {
		t.Errorf("rollback should be rejected, expected version=2, got=%d", stateFinal.Version)
	}
	if len(stateFinal.MessageFingerprints) != 2 {
		t.Errorf("rollback should not modify state, expected 2 messages, got=%d", len(stateFinal.MessageFingerprints))
	}
}

func TestQoderConversationRollbackAcceptedDeletesOwnNewState(t *testing.T) {
	store := NewQoderConversationStore(5 * time.Minute)
	plan := store.PlanWithOptions("rollback_new_state", "system", nil, []QoderMessage{{Role: "user", Text: "hello"}}, QoderConversationPlanOptions{})

	plan.CommitAccepted()

	store.Mu.Lock()
	accepted := CloneQoderConversationState(store.Items[plan.Key])
	store.Mu.Unlock()
	if accepted == nil || accepted.Version != 1 {
		t.Fatalf("expected accepted version=1, got=%v", accepted)
	}

	plan.RollbackAccepted()

	store.Mu.Lock()
	final := store.Items[plan.Key]
	store.Mu.Unlock()
	if final != nil {
		t.Fatalf("expected rollback to delete own new accepted state, got=%v", final)
	}
}

func TestQoderConversationRollbackAcceptedRestoresPreviousState(t *testing.T) {
	store := NewQoderConversationStore(5 * time.Minute)
	key := "rollback_previous_state"
	system := "system"
	firstMessages := []QoderMessage{{Role: "user", Text: "first"}}
	initialPlan := store.PlanWithOptions(key, system, nil, firstMessages, QoderConversationPlanOptions{})
	initialPlan.Commit(upstream.TokenUsage{InputTokens: 10, OutputTokens: 2})

	store.Mu.Lock()
	previous := CloneQoderConversationState(store.Items[key])
	store.Mu.Unlock()
	if previous == nil || previous.Version != 1 || !previous.HasUsage {
		t.Fatalf("unexpected previous state: %#v", previous)
	}

	nextMessages := []QoderMessage{
		{Role: "user", Text: "first"},
		{Role: "assistant", Text: "answer"},
		{Role: "user", Text: "next"},
	}
	plan := store.PlanWithOptions(key, system, nil, nextMessages, QoderConversationPlanOptions{})
	if !plan.Reused {
		t.Fatal("expected plan to reuse previous conversation")
	}

	plan.CommitAccepted()

	store.Mu.Lock()
	accepted := CloneQoderConversationState(store.Items[key])
	store.Mu.Unlock()
	if accepted == nil || accepted.Version != 2 {
		t.Fatalf("expected accepted version=2, got=%#v", accepted)
	}

	plan.RollbackAccepted()

	store.Mu.Lock()
	final := CloneQoderConversationState(store.Items[key])
	store.Mu.Unlock()
	if !QoderConversationStateEqual(final, previous) {
		t.Fatalf("expected rollback to restore previous state\nprevious=%#v\nfinal=%#v", previous, final)
	}
}

func TestQoderConversationRollbackAcceptedDoesNotClobberConcurrentCommit(t *testing.T) {
	store := NewQoderConversationStore(5 * time.Minute)
	key := "rollback_concurrent_commit"
	system := "system"
	firstMessages := []QoderMessage{{Role: "user", Text: "first"}}
	initialPlan := store.PlanWithOptions(key, system, nil, firstMessages, QoderConversationPlanOptions{})
	initialPlan.Commit()

	plan := store.PlanWithOptions(key, system, nil, []QoderMessage{
		{Role: "user", Text: "first"},
		{Role: "assistant", Text: "answer"},
		{Role: "user", Text: "next"},
	}, QoderConversationPlanOptions{})
	plan.CommitAccepted()

	concurrentPlan := store.PlanWithOptions(key, system, nil, []QoderMessage{
		{Role: "user", Text: "first"},
		{Role: "assistant", Text: "answer"},
		{Role: "user", Text: "next"},
	}, QoderConversationPlanOptions{})
	concurrentPlan.Commit()

	store.Mu.Lock()
	concurrentState := CloneQoderConversationState(store.Items[key])
	store.Mu.Unlock()
	if concurrentState == nil || concurrentState.Version != 3 {
		t.Fatalf("expected concurrent version=3, got=%#v", concurrentState)
	}

	plan.RollbackAccepted()

	store.Mu.Lock()
	final := CloneQoderConversationState(store.Items[key])
	store.Mu.Unlock()
	if !QoderConversationStateEqual(final, concurrentState) {
		t.Fatalf("rollback clobbered concurrent commit\nconcurrent=%#v\nfinal=%#v", concurrentState, final)
	}
}
