package creative

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Delivery fixtures represent a provider fact with a shared boolean and
// retained metadata before SaveOutput can accept calls. The store checks that
// ordering, counts attempts, and injects the first configured failures,
// allowing tests to observe retry counts and the resulting delivery code
// without real Redis or SQL storage.
//
// Cancellation can be injected after the outcome flag and metadata are
// assigned but before RecordProviderOutcome returns. The delivery call then
// observes its cancelled parent context, and tests require no byte save or
// metadata update. Read fixtures separately return unavailable or missing
// sentinels to compare uncertainty with confirmed loss. These shared fields
// are used synchronously by the test; the recorded flag establishes call order
// rather than database durability.

// deliveryContractStore observes byte-save ordering and supplies configured
// delivery read/write errors.
type deliveryContractStore struct {
	CreativeTransientStore       // unused transient storage methods
	loadErr                error // configured output read error
	attempts               int   // observed byte save attempt count
	failures               int   // initial unavailable save count
	// recorded points to deliveryContractOutcomes.recorded and is required by
	// SaveOutput. Tests keep that pointer stable while delivery writes and save
	// observations share the same synchronous simulated provider fact.
	recorded *bool // shared simulated outcome recorded flag
}

// deliveryContractOutcomes records the simulated provider fact and optionally
// cancels the delivery context.
type deliveryContractOutcomes struct {
	recorded bool // simulated provider fact recorded flag
	// cancel runs after the simulated fact and metadata are assigned. A nil hook
	// leaves the parent context active for the subsequent byte-save observations.
	cancel context.CancelFunc // optional injected parent cancellation
	// metadata retains the supplied slice from RecordProviderOutcome. Tests read it
	// after synchronous delivery and keep its storage stable while inspecting it.
	metadata []CreativeRunOutput // retained simulated output metadata
}

// deliveryContractRepo observes output metadata update counts and the last
// supplied delivery code.
type deliveryContractRepo struct {
	CreativeRunRepository        // unused task persistence methods
	updates               int    // observed output metadata update count
	code                  string // last observed delivery error code
}

// LoadOutput returns the configured error with no bytes for every fixture
// lookup. Lost tests use unavailable and missing sentinels to compare
// retryable uncertainty with confirmed byte absence.
func (s *deliveryContractStore) LoadOutput(context.Context, string, int) ([]byte, error) {
	return nil, s.loadErr
}

// SaveOutput checks the shared recorded flag before counting an attempted
// save. Callers supply its nonnil pointer and keep the fixture stable during
// synchronous delivery.
//
// A missing provider fact returns an ordering error before attempts increases.
// Otherwise the first failures count returns unavailable and later calls
// succeed, letting assertions observe the retry threshold directly. Success
// stores no bytes, so these observations establish calls and returned codes
// rather than persistent media delivery.
func (s *deliveryContractStore) SaveOutput(context.Context, string, int, []byte, time.Duration) error {
	if !*s.recorded {
		return errors.New("success fact missing")
	}
	s.attempts++
	if s.attempts <= s.failures {
		return ErrCreativeTransientUnavailable
	}
	return nil
}

// RecordProviderOutcome sets the shared fact flag and retains supplied
// metadata before optional cancellation. The callback runs synchronously
// before success returns, letting cancellation tests inspect the recorded
// values while saves are skipped.
func (s *deliveryContractOutcomes) RecordProviderOutcome(_ context.Context, _ string, _ int64, metadata []CreativeRunOutput, _ time.Time) error {
	s.recorded = true
	s.metadata = metadata
	// The cancellation hook runs after assigning the flag and metadata, making
	// parent cancellation occur between outcome recording and byte-save observation
	// in this test without a real persistence transaction.
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

// CompleteProviderOutcome accepts completion calls with no state change in
// this delivery-only fixture.
func (s *deliveryContractOutcomes) CompleteProviderOutcome(context.Context, string, float64, bool, time.Time) error {
	return nil
}

// UpdateCreativeRunOutput counts the call and retains its code argument for
// delivery assertions. Other metadata inputs are ignored, and the fake returns
// success without persisting or updating a task row.
func (r *deliveryContractRepo) UpdateCreativeRunOutput(_ context.Context, _ string, _ int, _ string, _ string, _ int64, _ *time.Time, code, _ string) error {
	r.updates++
	r.code = code
	return nil
}

// TestResultDeliveryDistinguishesUnavailableFromMissing injects the listed
// unavailable and missing read failures for succeeded output. It checks loss
// without error for absence and the unchanged availability error for uncertain
// storage.
func TestResultDeliveryDistinguishesUnavailableFromMissing(t *testing.T) {
	output := []*CreativeRunOutput{{Status: CreativeRunOutputStatusSucceeded, OutputIndex: 0}}
	for _, test := range []struct {
		name    string // case display name text
		failure error  // injected transient read error
		lost    bool   // expected confirmed delivery loss flag
	}{
		{name: "redis_unavailable", failure: ErrCreativeTransientUnavailable},
		{name: "confirmed_missing", failure: ErrCreativeTransientNotFound, lost: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			delivery := ResultDelivery{Store: &deliveryContractStore{loadErr: test.failure}}
			lost, err := delivery.Lost(context.Background(), "run", output)
			require.Equal(t, test.lost, lost)
			if test.lost {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrCreativeTransientUnavailable)
			}
		})
	}
}

// TestResultDeliveryRetriesOnlySaveAfterDurableFact injects one or three
// failed saves after the simulated outcome flag is set. It checks metadata,
// exact attempt/update counts, and the empty or lost code.
func TestResultDeliveryRetriesOnlySaveAfterDurableFact(t *testing.T) {
	for _, test := range []struct {
		name string // case display name text
		// failures counts injected errors; attempts names expected save calls
		failures, attempts int
		code               string // expected final delivery error code
	}{
		{name: "retry_then_saved", failures: 1, attempts: 2},
		{name: "exhausted", failures: 3, attempts: 3, code: OutputDeliveryLost},
	} {
		t.Run(test.name, func(t *testing.T) {
			outcome := &deliveryContractOutcomes{}
			store := &deliveryContractStore{failures: test.failures, recorded: &outcome.recorded}
			repo := &deliveryContractRepo{}
			delivery := ResultDelivery{Outcomes: outcome, Store: store, Repo: repo, TTL: time.Minute}
			require.NoError(t, delivery.Record(context.Background(), "run", 1, []ProviderOutput{{Index: 0, Success: true, Bytes: []byte("image"), Mime: "image/png"}}))
			require.True(t, outcome.recorded)
			require.Len(t, outcome.metadata, 1)
			require.Equal(t, test.attempts, store.attempts)
			require.Equal(t, 1, repo.updates)
			require.Equal(t, test.code, repo.code)
		})
	}
}

// TestResultDeliveryCancellationStopsSaving cancels synchronously after
// simulated outcome recording. Record returns context cancellation while the
// fact flag remains true and both save and metadata-update counters stay zero.
func TestResultDeliveryCancellationStopsSaving(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := &deliveryContractOutcomes{cancel: cancel}
	store := &deliveryContractStore{recorded: &outcome.recorded}
	repo := &deliveryContractRepo{}
	delivery := ResultDelivery{Outcomes: outcome, Store: store, Repo: repo, TTL: time.Minute}
	require.ErrorIs(t, delivery.Record(ctx, "run", 1, []ProviderOutput{{Index: 0, Success: true, Bytes: []byte("image"), Mime: "image/png"}}), context.Canceled)
	require.True(t, outcome.recorded)
	require.Zero(t, store.attempts)
	require.Zero(t, repo.updates)
}
