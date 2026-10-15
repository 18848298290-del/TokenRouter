package ws

import (
	"sync"
	"time"
)

// turn.go queues accepted request snapshots for passthrough completion and
// output restoration.
// A passthrough terminal event can arrive after the client-side reader has
// already captured request metadata. Matching completion to the oldest accepted
// response.create retains that turn's client model, upstream model, acceptance
// time, and policy values while later session defaults change independently.
//
// TurnPayloadQueue freezes mutable input bytes and optional string values on
// insertion, then orders them under one mutex. Peek leaves the first turn
// available for warning and output rewriting before Pop consumes it for
// completion. Both retrieval operations return shallow values containing
// queue-owned references, so consumers treat those bytes and pointers as
// immutable; an empty queue identifies missing metadata with the
// passthrough_missing source.

// TurnPayload records accepted request metadata used when its passthrough turn
// completes.
type TurnPayload struct {
	StartedAt time.Time // client frame acceptance time
	// RequestBody is copied by Push. Peek and Pop expose that stored slice, which
	// completion and response-rewriting consumers must treat as immutable.
	RequestBody   []byte // accepted upstream request bytes
	OriginalModel string // client requested model identifier
	RoutingModel  string // group resolved model identifier
	UpstreamModel string // provider resolved model identifier
	// ServiceTier, ReasoningEffort, and RequestedReasoningEffort are copied by
	// Push when present. Nil retains absence; retrieval shares these stored
	// pointers with read-only consumers.
	ServiceTier              *string // accepted request service tier
	ReasoningEffort          *string // accepted request reasoning effort
	RequestedReasoningEffort *string // client effort before policy
	PreviousResponseID       string  // client supplied continuation identifier
	Source                   string  // completion payload source label
}

// TurnPayloadQueue matches accepted passthrough requests to terminal callbacks
// in FIFO order.
type TurnPayloadQueue struct {
	// mu protects the order and membership of items. Stored payload bytes and
	// optional strings become immutable after Push publishes the copied item.
	mu    sync.Mutex    // queue access serialization lock
	items []TurnPayload // accepted turns awaiting completion
}

// NewTurnPayloadQueue constructs an empty FIFO for passthrough request
// snapshots.
func NewTurnPayloadQueue() *TurnPayloadQueue {
	return &TurnPayloadQueue{}
}

// Push appends a snapshot after copying its request bytes and present optional
// strings. A nil queue discards the item; callers may reuse their supplied
// mutable storage after Push returns.
func (q *TurnPayloadQueue) Push(item TurnPayload) {
	if q == nil {
		return
	}
	item.RequestBody = append([]byte(nil), item.RequestBody...)
	if item.ServiceTier != nil {
		value := *item.ServiceTier
		item.ServiceTier = &value
	}
	if item.ReasoningEffort != nil {
		value := *item.ReasoningEffort
		item.ReasoningEffort = &value
	}
	if item.RequestedReasoningEffort != nil {
		value := *item.RequestedReasoningEffort
		item.RequestedReasoningEffort = &value
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, item)
}

// Len reports how many accepted turns await terminal completion under the queue
// mutex. A nil or empty queue returns zero; Peek and Pop provide the associated
// snapshots separately.
func (q *TurnPayloadQueue) Len() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Peek returns the oldest queued turn while leaving it available for
// completion. A nil or empty queue returns Source passthrough_missing; returned
// bytes and pointers remain shared and require read-only use.
func (q *TurnPayloadQueue) Peek() TurnPayload {
	if q == nil {
		return TurnPayload{Source: "passthrough_missing"}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return TurnPayload{Source: "passthrough_missing"}
	}
	return q.items[0]
}

// Pop removes and returns the oldest queued turn under the queue mutex. A nil
// or empty queue returns Source passthrough_missing; returned bytes and
// pointers are the stored snapshot's references.
func (q *TurnPayloadQueue) Pop() TurnPayload {
	if q == nil {
		return TurnPayload{Source: "passthrough_missing"}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return TurnPayload{Source: "passthrough_missing"}
	}
	item := q.items[0]
	q.items = q.items[1:]
	return item
}
