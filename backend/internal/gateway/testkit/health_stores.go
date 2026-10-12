package testkit

import (
	"context"
	"errors"
	"time"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
)

// HealthStoreBase reports missing providers and accepts health writes; tests supply other methods as needed.
type HealthStoreBase struct {
	gatewayadapter.ExecutionProviderStore
}

// ErrorPolicyStore records writes triggered by error policy.
type ErrorPolicyStore struct {
	HealthStoreBase
	TempCalls           int
	SetErrCalls         int
	LastErrorMsg        string
	ModelRateLimitCalls []ModelLimitCall
}

// HealthStoreRecorder counts health writes, captures their latest inputs, and supports injected write errors.
type HealthStoreRecorder struct {
	HealthStoreBase
	SetErrorCalls          int
	TempCalls              int
	UpdateCredentialsCalls int
	UpdateExtraCalls       int
	LastCredentials        map[string]any
	LastExtraUpdates       map[string]any
	LastErrorMsg           string
	LastTempUntil          time.Time
	LastTempReason         string
	LastErrorID            int64
	LastTempID             int64
	TempErr                error
}

// ForbiddenCounter returns preset counts and records resets.
type ForbiddenCounter struct {
	Counts     []int64
	ResetCalls []int64
	Err        error
}

// RuntimeBlockRecorder captures scheduling blocks and removals.
type RuntimeBlockRecorder struct {
	Providers  []*gatewayadapter.ExecutionProvider
	Until      []time.Time
	Reasons    []string
	ClearedIDs []int64
}

// ModelLimitCall captures a model rate-limit window write.
type ModelLimitCall struct {
	ProviderID int64
	Scope      string
	ResetAt    time.Time
	Reason     string
}

func (*HealthStoreBase) GetByID(context.Context, int64) (*gatewayadapter.ExecutionProvider, error) {
	return nil, errors.New("provider not found")
}
func (*HealthStoreBase) SetError(context.Context, int64, string) error          { return nil }
func (*HealthStoreBase) SetRateLimited(context.Context, int64, time.Time) error { return nil }
func (*HealthStoreBase) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	return nil
}
func (*HealthStoreBase) SetOverloaded(context.Context, int64, time.Time) error { return nil }
func (*HealthStoreBase) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}

func (*HealthStoreBase) ClearTempUnschedulable(context.Context, int64) error { return nil }

func (*HealthStoreBase) ClearRateLimit(context.Context, int64) error { return nil }

func (*HealthStoreBase) ClearError(context.Context, int64) error { return nil }

func (*HealthStoreBase) UpdateCredentials(context.Context, int64, map[string]any) error { return nil }

func (*HealthStoreBase) UpdateExtra(context.Context, int64, map[string]any) error { return nil }

func (*HealthStoreBase) UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error {
	return nil
}

func (r *ErrorPolicyStore) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.TempCalls++
	return nil
}

func (r *ErrorPolicyStore) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.SetErrCalls++
	r.LastErrorMsg = errorMsg
	return nil
}

func (r *ErrorPolicyStore) SetModelRateLimit(_ context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	call := ModelLimitCall{ProviderID: id, Scope: scope, ResetAt: resetAt}
	if len(reason) > 0 {
		call.Reason = reason[0]
	}
	r.ModelRateLimitCalls = append(r.ModelRateLimitCalls, call)
	return nil
}

func (r *HealthStoreRecorder) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.SetErrorCalls++
	r.LastErrorID = id
	r.LastErrorMsg = errorMsg
	return nil
}

func (r *HealthStoreRecorder) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.TempCalls++
	r.LastTempUntil = until
	r.LastTempID = id
	r.LastTempReason = reason
	return r.TempErr
}

func (r *HealthStoreRecorder) UpdateCredentials(ctx context.Context, id int64, credentials map[string]any) error {
	r.UpdateCredentialsCalls++
	r.LastCredentials = querycache.ShallowMap(credentials)
	return nil
}

func (r *HealthStoreRecorder) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	r.UpdateExtraCalls++
	r.LastExtraUpdates = querycache.ShallowMap(updates)
	return nil
}

func (s *ForbiddenCounter) IncrementOpenAI403Count(_ context.Context, _ int64, _ int) (int64, error) {
	if s.Err != nil {
		return 0, s.Err
	}
	if len(s.Counts) == 0 {
		return 1, nil
	}
	count := s.Counts[0]
	s.Counts = s.Counts[1:]
	return count, nil
}

func (s *ForbiddenCounter) ResetOpenAI403Count(_ context.Context, providerID int64) error {
	s.ResetCalls = append(s.ResetCalls, providerID)
	return nil
}

func (r *RuntimeBlockRecorder) BlockProviderScheduling(provider *gatewayadapter.ExecutionProvider, until time.Time, reason string) {
	r.Providers = append(r.Providers, provider)
	r.Until = append(r.Until, until)
	r.Reasons = append(r.Reasons, reason)
}

func (r *RuntimeBlockRecorder) ClearProviderSchedulingBlock(providerID int64) {
	r.ClearedIDs = append(r.ClearedIDs, providerID)
}
