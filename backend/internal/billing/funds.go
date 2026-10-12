package billing

import (
	"context"
	"fmt"
)

// Request settlement and asynchronous task funding share a storage adapter
// that applies their complete financial operation. Task calls require a
// registered scope and original reservation identity so the adapter can
// find the task and coordinate its funds with persisted allowance state.

// FundsStore applies request settlement and task funding with storage-owned
// transactions and deduplication. The PostgreSQL adapter normalizes supplied
// commands and can write task allocations back. Callers give each call
// exclusive access to its command until the call returns.
type FundsStore interface {
	// Apply settles one request and reports whether its financial identity
	// was newly applied. Matching replay returns Applied false; conflicting
	// fingerprints and persistence failures return errors, leaving deduplication
	// and transaction handling to the storage implementation.
	Apply(context.Context, *UsageBillingCommand) (*UsageBillingApplyResult, error)
	// Reserve records a task hold and returns its source allocations and
	// wallet state. The task reference identifies its scope and original
	// reservation; the PostgreSQL adapter updates held funds and reserved
	// allowance in one transaction.
	Reserve(context.Context, *TaskFundsCommand) (*TaskFundsResult, error)
	// Capture reconciles a completed task with its reserved source allocations.
	// The task reference keeps the original reservation identity, and the
	// PostgreSQL adapter applies the final charge and releases unused holds
	// with the task allowance reconciliation.
	Capture(context.Context, *TaskFundsCommand) (*TaskFundsResult, error)
	// Release returns a task reservation to its recorded funding sources.
	// The task reference identifies the original hold, and the PostgreSQL
	// adapter restores funds and reconciles reserved allowance before
	// committing the operation.
	Release(context.Context, *TaskFundsCommand) (*TaskFundsResult, error)
}

// Funds delegates request settlement and task funding to its configured store.
type Funds struct {
	store FundsStore // financial operation adapter
}

// NewFunds constructs a funding facade using the supplied storage adapter.
// Pass an adapter usable by subsequent calls; the constructor retains it
// as supplied, and each operation forwards its caller context to storage.
func NewFunds(store FundsStore) *Funds { return &Funds{store: store} }

// Settle forwards request settlement to the configured store and returns
// its result. The store owns validation, normalization, persistence, and
// replay detection, including any changes it makes to the supplied command.
func (f *Funds) Settle(ctx context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	return f.store.Apply(ctx, cmd)
}

// Reserve delegates task funding after checking the supplied task reference.
// A nonnil command with empty Scope or ReserveRequestID fails before
// storage; nil commands pass through for the adapter to interpret.
func (f *Funds) Reserve(ctx context.Context, cmd *TaskFundsCommand) (*TaskFundsResult, error) {
	if cmd != nil && (cmd.Task.Scope == "" || cmd.Task.ReserveRequestID == "") {
		return nil, fmt.Errorf("billing task reference is incomplete: %s", cmd.Task.Scope)
	}
	return f.store.Reserve(ctx, cmd)
}

// Capture delegates task settlement after checking the supplied task
// reference. A nonnil command needs Scope and ReserveRequestID; storage
// then interprets the recorded allocations and reports whether the capture
// was applied.
func (f *Funds) Capture(ctx context.Context, cmd *TaskFundsCommand) (*TaskFundsResult, error) {
	if cmd != nil && (cmd.Task.Scope == "" || cmd.Task.ReserveRequestID == "") {
		return nil, fmt.Errorf("billing task reference is incomplete: %s", cmd.Task.Scope)
	}
	return f.store.Capture(ctx, cmd)
}

// Release delegates hold restoration after checking the supplied task
// reference. A nonnil command needs Scope and ReserveRequestID; storage
// then restores the recorded sources and handles repeated release requests
// through its deduplication rules.
func (f *Funds) Release(ctx context.Context, cmd *TaskFundsCommand) (*TaskFundsResult, error) {
	if cmd != nil && (cmd.Task.Scope == "" || cmd.Task.ReserveRequestID == "") {
		return nil, fmt.Errorf("billing task reference is incomplete: %s", cmd.Task.Scope)
	}
	return f.store.Release(ctx, cmd)
}
