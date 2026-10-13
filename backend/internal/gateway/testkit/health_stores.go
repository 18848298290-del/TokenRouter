package testkit

import (
	"context"
	"errors"
	"time"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
)

// Health tests observe provider error, cooldown, credential, and runtime-block
// writes through serial fixtures. Permissive base operations let each test
// override the decisions it needs, while recorders keep counters and latest
// inputs for assertions against the production health service.

// HealthStoreBase reports missing providers and accepts inherited health write
// operations.
type HealthStoreBase struct {
	// ExecutionProviderStore supplies methods not overridden by this base. Tests
	// provide a usable embedded implementation before invoking those promoted
	// operations.
	gatewayadapter.ExecutionProviderStore // optional inherited execution-store operations
}

// ErrorPolicyStore counts serial policy writes and retains their observed
// diagnostic inputs.
type ErrorPolicyStore struct {
	HealthStoreBase                      // permissive inherited health write fixture
	TempCalls           int              // temporary cooldown write count
	SetErrCalls         int              // provider error write count
	LastErrorMsg        string           // latest provider error message
	ModelRateLimitCalls []ModelLimitCall // recorded model cooldown write inputs
}

// HealthStoreRecorder captures latest serial health writes and optional
// cooldown failures.
type HealthStoreRecorder struct {
	HealthStoreBase            // permissive inherited health write fixture
	SetErrorCalls          int // provider error write count
	TempCalls              int // temporary cooldown write count
	UpdateCredentialsCalls int // credential replacement write count
	UpdateExtraCalls       int // provider extra update count
	// LastCredentials and LastExtraUpdates receive fresh top-level maps from
	// querycache.ShallowMap. Nested mutable values retain their supplied references,
	// so tests keep those values stable while inspecting recorded inputs.
	LastCredentials  map[string]any // latest top-level credential map copy
	LastExtraUpdates map[string]any // latest top-level extra map copy
	LastErrorMsg     string         // latest provider error message
	LastTempUntil    time.Time      // latest temporary cooldown deadline
	LastTempReason   string         // latest temporary cooldown reason
	LastErrorID      int64          // latest error target provider identifier
	LastTempID       int64          // latest cooldown target provider identifier
	TempErr          error          // injected temporary cooldown write error
}

// ForbiddenCounter supplies queued forbidden counts and records provider reset
// calls.
type ForbiddenCounter struct {
	// Counts supplies successive increment results and is consumed from the front.
	// An empty queue selects one, while Err returns before queue consumption.
	Counts     []int64 // queued forbidden counter results
	ResetCalls []int64 // reset target provider identifiers
	// Err rejects an increment before Counts is consumed. Resets still append
	// provider IDs because ResetOpenAI403Count does not consult this injected error.
	Err error // injected forbidden counter increment error
}

// RuntimeBlockRecorder retains serial runtime block inputs and provider
// removal history.
type RuntimeBlockRecorder struct {
	// Providers, Until, and Reasons append corresponding inputs for each runtime
	// block call. Provider pointers are retained, so later source mutation is
	// visible through this serial fixture's history; tests control that ownership.
	Providers  []*gatewayadapter.ExecutionProvider // runtime block target provider pointers
	Until      []time.Time                         // runtime block expiry time history
	Reasons    []string                            // runtime block reason text history
	ClearedIDs []int64                             // cleared runtime block provider identifiers
}

// ModelLimitCall stores one model cooldown write and its first optional
// reason.
type ModelLimitCall struct {
	ProviderID int64     // target model cooldown provider identifier
	Scope      string    // model cooldown scope identifier
	ResetAt    time.Time // model cooldown reset wall-clock time
	Reason     string    // first supplied cooldown reason string
}

// GetByID reports a missing provider to production health readers.
func (*HealthStoreBase) GetByID(context.Context, int64) (*gatewayadapter.ExecutionProvider, error) {
	return nil, errors.New("provider not found")
}

// SetError accepts a provider error write in the permissive base fixture.
func (*HealthStoreBase) SetError(context.Context, int64, string) error { return nil }

// SetRateLimited accepts a provider rate-limit deadline in the base fixture.
func (*HealthStoreBase) SetRateLimited(context.Context, int64, time.Time) error { return nil }

// SetModelRateLimit accepts model cooldown data in the permissive base
// fixture.
func (*HealthStoreBase) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	return nil
}

// SetOverloaded accepts an overload deadline in the base health fixture.
func (*HealthStoreBase) SetOverloaded(context.Context, int64, time.Time) error { return nil }

// SetTempUnschedulable accepts temporary cooldown data in the permissive base
// fixture.
func (*HealthStoreBase) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}

// ClearTempUnschedulable accepts temporary cooldown cleanup in the base health
// fixture.
func (*HealthStoreBase) ClearTempUnschedulable(context.Context, int64) error { return nil }

// ClearRateLimit accepts rate-limit removal in the permissive base health
// fixture.
func (*HealthStoreBase) ClearRateLimit(context.Context, int64) error { return nil }

// ClearError accepts provider error cleanup in the base health fixture.
func (*HealthStoreBase) ClearError(context.Context, int64) error { return nil }

// UpdateCredentials accepts credential persistence in the permissive base
// health fixture.
func (*HealthStoreBase) UpdateCredentials(context.Context, int64, map[string]any) error { return nil }

// UpdateExtra accepts provider extra persistence in the permissive base health
// fixture.
func (*HealthStoreBase) UpdateExtra(context.Context, int64, map[string]any) error { return nil }

// UpdateSessionWindow accepts provider window observations in the base health
// fixture.
func (*HealthStoreBase) UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error {
	return nil
}

// SetTempUnschedulable counts a temporary cooldown requested by the error
// policy.
func (r *ErrorPolicyStore) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.TempCalls++
	return nil
}

// SetError counts provider error writes and retains the latest message.
func (r *ErrorPolicyStore) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.SetErrCalls++
	r.LastErrorMsg = errorMsg
	return nil
}

// SetModelRateLimit appends a model cooldown observation with its first
// optional reason. Further reason values are ignored; callers serialize writes
// and inspect the retained history after the policy call.
func (r *ErrorPolicyStore) SetModelRateLimit(_ context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	call := ModelLimitCall{ProviderID: id, Scope: scope, ResetAt: resetAt}
	if len(reason) > 0 {
		call.Reason = reason[0]
	}
	r.ModelRateLimitCalls = append(r.ModelRateLimitCalls, call)
	return nil
}

// SetError counts the write and stores its provider identifier and message.
func (r *HealthStoreRecorder) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.SetErrorCalls++
	r.LastErrorID = id
	r.LastErrorMsg = errorMsg
	return nil
}

// SetTempUnschedulable records the provider, deadline, and reason before
// returning TempErr. Even injected failures increase the call count; tests
// serialize the fixture while observing that attempted write.
func (r *HealthStoreRecorder) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.TempCalls++
	r.LastTempUntil = until
	r.LastTempID = id
	r.LastTempReason = reason
	return r.TempErr
}

// UpdateCredentials records a top-level copy of the supplied credentials and
// increments its count. Nested mutable values remain shared, so tests leave
// those values stable when inspecting the saved observation.
func (r *HealthStoreRecorder) UpdateCredentials(ctx context.Context, id int64, credentials map[string]any) error {
	r.UpdateCredentialsCalls++
	r.LastCredentials = querycache.ShallowMap(credentials)
	return nil
}

// UpdateExtra records a top-level copy of provider extra updates and
// increments its count. Nested mutable values remain shared; the fixture
// accepts the attempted write for production health assertions.
func (r *HealthStoreRecorder) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	r.UpdateExtraCalls++
	r.LastExtraUpdates = querycache.ShallowMap(updates)
	return nil
}

// IncrementOpenAI403Count returns Err before consuming queued results, or
// consumes the first configured count. An empty queue returns one; the fixture
// ignores provider and threshold arguments and callers serialize queue access.
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

// ResetOpenAI403Count appends the provider identifier to the fixture reset
// history.
func (s *ForbiddenCounter) ResetOpenAI403Count(_ context.Context, providerID int64) error {
	s.ResetCalls = append(s.ResetCalls, providerID)
	return nil
}

// BlockProviderScheduling appends corresponding provider, deadline, and reason
// inputs to parallel histories. The provider pointer is retained; tests
// serialize updates and keep mutable provider data stable while checking
// observations.
func (r *RuntimeBlockRecorder) BlockProviderScheduling(provider *gatewayadapter.ExecutionProvider, until time.Time, reason string) {
	r.Providers = append(r.Providers, provider)
	r.Until = append(r.Until, until)
	r.Reasons = append(r.Reasons, reason)
}

// ClearProviderSchedulingBlock appends the provider identifier to the removal
// history.
func (r *RuntimeBlockRecorder) ClearProviderSchedulingBlock(providerID int64) {
	r.ClearedIDs = append(r.ClearedIDs, providerID)
}
