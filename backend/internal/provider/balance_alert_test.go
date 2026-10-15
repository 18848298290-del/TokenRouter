package provider

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// balanceAlertFixture models stored provider and deadline state for worker behavior tests.
type balanceAlertFixture struct {
	mu    sync.Mutex         // fixture mutation lock
	p     *Record            // current provider configuration
	state *BalanceAlertState // last persistent attempt
}

// balanceAlertExecution returns controlled query results and optional in-flight mutations.
type balanceAlertExecution struct {
	result *UpstreamUsageInfo // normalized wallet observation
	err    error              // injected query error
	after  func()             // optional configuration mutation
}

// balanceAlertCompletionStore exposes the final write's lifetime to shutdown tests.
type balanceAlertCompletionStore struct {
	*balanceAlertFixture                      // current fixture state
	started              chan context.Context // admitted persistence context
	release              chan struct{}        // test-controlled completion barrier
}

// GetByID returns an independently cloned provider under the fixture lock.
func (f *balanceAlertFixture) GetByID(context.Context, int64) (*Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return CloneRecord(f.p), nil
}

// ListBalanceAlertCandidates supplies the fixture provider for each scheduled scan.
func (f *balanceAlertFixture) ListBalanceAlertCandidates(context.Context) ([]int64, error) {
	return []int64{1}, nil
}

// ClaimBalanceAlert consumes a due deadline under the fixture lock before returning.
// Copies keep worker outcome mutation separate from the stored claim. Real SQL
// concurrency behavior is covered independently by the PostgreSQL integration test.
func (f *balanceAlertFixture) ClaimBalanceAlert(_ context.Context, p *Record, identity string, c BalanceAlertConfig, now time.Time) (*BalanceAlertState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != nil && f.state.Identity == identity && f.state.NextCheckAt.After(now) {
		return nil, nil
	}
	round := int64(1)
	if f.state != nil {
		round = f.state.Round + 1
	}
	next := now.Add(time.Duration(c.IntervalMinutes) * time.Minute)
	state := BalanceAlertState{ProviderID: p.ID, Round: round, Identity: identity, LastCheckedAt: &now, NextCheckAt: &next}
	f.state = &state
	copy := state
	return &copy, nil
}

// FinishBalanceAlert records an outcome only for the fixture's current claim.
// Cancelled contexts reject the write like database/sql, allowing worker tests
// to distinguish a consumed attempt from successfully persisted diagnostic state.
func (f *balanceAlertFixture) FinishBalanceAlert(ctx context.Context, s *BalanceAlertState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state.Round == s.Round {
		copy := *s
		f.state = &copy
	}
	return nil
}

// GetBalanceAlertState returns a detached copy of the current fixture attempt.
func (f *balanceAlertFixture) GetBalanceAlertState(context.Context, int64) (*BalanceAlertState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == nil {
		return nil, nil
	}
	copy := *f.state
	return &copy, nil
}

// Available reports that the fixture execution service can accept queries.
func (*balanceAlertExecution) Available() bool { return true }

// Supports accepts the configured adapter for these worker behavior tests.
func (*balanceAlertExecution) Supports(string) bool { return true }

// BaseURL returns a fixed query root for fixture identity comparisons.
func (*balanceAlertExecution) BaseURL(*Record) string { return "https://api.deepseek.com" }

// Query returns a controlled normalized observation after any configured mutation.
func (e *balanceAlertExecution) Query(context.Context, *Record, UpstreamUsageQueryConfig) (*UpstreamUsageInfo, error) {
	if e.after != nil {
		e.after()
	}
	return e.result, e.err
}

// newBalanceAlertFixture assembles an enabled provider, shared query service, and monitor.
// Each test owns the returned dependencies and stops queries during cleanup so
// no shared upstream operation survives the test's fixture lifetime.
func newBalanceAlertFixture(t *testing.T, balance *float64, send BalanceAlertSend) (*BalanceAlertMonitor, *balanceAlertFixture, *balanceAlertExecution) {
	t.Helper()
	f := &balanceAlertFixture{p: &Record{ID: 1, Name: "Wallet", Platform: PlatformDeepseek, Type: ProviderTypeAPIKey, Status: StatusActive, Extra: map[string]any{UpstreamBalanceAlertExtraKey: BalanceAlertConfig{Enabled: true, Threshold: 1000, IntervalMinutes: 60, RecipientEmail: "alerts@example.com"}}}}
	e := &balanceAlertExecution{result: &UpstreamUsageInfo{Provider: "deepseek", Mode: "balance", Unit: "CNY", Balance: &UpstreamUsageAmount{Remaining: balance}}}
	q := NewUpstreamUsageService(f, e, UpstreamUsageOptions{})
	t.Cleanup(func() { require.NoError(t, q.StopContext(context.Background())) })
	m := NewBalanceAlertMonitor(f, q, send)
	t.Cleanup(func() { require.NoError(t, m.StopContext(context.Background())) })
	return m, f, e
}

// TestBalanceAlertThresholds checks values below, equal to, and above the configured cutoff.
// Missing balances remain unknown, while zero and negative wallet values trigger
// reminders. The upstream unit never changes the numeric comparison.
func TestBalanceAlertThresholds(t *testing.T) {
	for _, tc := range []struct {
		name  string   // threshold scenario label
		value *float64 // optional remaining balance
		want  int      // expected email count
	}{{"below", balanceAlertNumber(999), 1}, {"equal", balanceAlertNumber(1000), 0}, {"above", balanceAlertNumber(1001), 0}, {"zero", balanceAlertNumber(0), 1}, {"negative", balanceAlertNumber(-1), 1}, {"missing", nil, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			sent := 0
			m, f, _ := newBalanceAlertFixture(t, tc.value, func(context.Context, *Record, BalanceAlertConfig, *UpstreamUsageQueryResult) error {
				sent++
				return nil
			})
			m.RunOnce(context.Background())
			require.Equal(t, tc.want, sent)
			if tc.value == nil {
				require.Equal(t, "UPSTREAM_USAGE_INVALID_RESPONSE", f.state.ErrorCode)
			}
		})
	}
}

// balanceAlertNumber returns an independent numeric pointer for wallet observation fixtures.
func balanceAlertNumber(v float64) *float64 { return &v }

// TestBalanceAlertRepeatsAndRestart consumes a round, reconstructs the monitor, then advances time.
// The persisted deadline suppresses an immediate restart send, while the next
// due low-balance observation produces a new email without a daily cooldown.
func TestBalanceAlertRepeatsAndRestart(t *testing.T) {
	sent := 0
	sender := func(context.Context, *Record, BalanceAlertConfig, *UpstreamUsageQueryResult) error {
		sent++
		return nil
	}
	m, f, _ := newBalanceAlertFixture(t, balanceAlertNumber(999), sender)
	now := time.Now()
	m.now = func() time.Time { return now }
	m.RunOnce(context.Background())
	restarted := NewBalanceAlertMonitor(f, m.queries, sender)
	restarted.now = m.now
	restarted.RunOnce(context.Background())
	require.Equal(t, 1, sent)
	now = now.Add(time.Hour)
	restarted.RunOnce(context.Background())
	require.Equal(t, 2, sent)
	require.Equal(t, int64(2), f.state.Round)
	require.NoError(t, restarted.StopContext(context.Background()))
}

// TestBalanceAlertFailuresAndChangedPolicy injects query failures, delivery errors, and changed recipients.
// No failure reuses an earlier balance or retries SMTP in the same round, and
// an in-flight policy change prevents delivery to the captured recipient.
func TestBalanceAlertFailuresAndChangedPolicy(t *testing.T) {
	for _, kind := range []string{"query", "smtp", "recipient", "disabled", "query_config"} {
		t.Run(kind, func(t *testing.T) {
			sent := 0
			m, f, e := newBalanceAlertFixture(t, balanceAlertNumber(1), func(context.Context, *Record, BalanceAlertConfig, *UpstreamUsageQueryResult) error {
				sent++
				return errors.New("SMTP uncertain")
			})
			switch kind {
			case "query":
				e.err = ErrUpstreamUsageTimeout
			case "smtp":
			case "recipient", "disabled":
				e.after = func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					c, err := BalanceAlertConfiguration(f.p)
					if err != nil {
						panic(err)
					}
					c.RecipientEmail = "new@example.com"
					if kind == "disabled" {
						c.Enabled = false
					}
					f.p.Extra[UpstreamBalanceAlertExtraKey] = c
				}
			case "query_config":
				e.err = ErrUpstreamUsageConfigInvalid
			}
			m.RunOnce(context.Background())
			if kind == "smtp" {
				require.Equal(t, 1, sent)
				m.RunOnce(context.Background())
				require.Equal(t, 1, sent)
				require.Equal(t, "UPSTREAM_BALANCE_ALERT_EMAIL_FAILED", f.state.ErrorCode)
			} else {
				require.Zero(t, sent)
			}
		})
	}
}

// TestBalanceAlertStopCancelsDelivery blocks inside the sender until shutdown cancels it.
// Stop waits for the scan to finish, and subsequent scans cannot admit more
// work after the monitor's permanent activity barrier has closed.
func TestBalanceAlertStopCancelsDelivery(t *testing.T) {
	started := make(chan struct{})
	m, _, _ := newBalanceAlertFixture(t, balanceAlertNumber(1), func(ctx context.Context, _ *Record, _ BalanceAlertConfig, _ *UpstreamUsageQueryResult) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	done := make(chan struct{})
	go func() { defer close(done); m.RunOnce(context.Background()) }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, m.StopContext(ctx))
	<-done
	m.RunOnce(context.Background())
	require.ErrorIs(t, m.StartContext(context.Background()), ErrBalanceAlertStopped)
}

// TestBalanceAlertFailureDiscardsPreviousObservation checks a successful low-balance round followed by timeout.
// The new attempt has no usable balance and sends no mail, even though the
// preceding round observed a value below the same configured threshold.
func TestBalanceAlertFailureDiscardsPreviousObservation(t *testing.T) {
	sent := 0
	m, f, e := newBalanceAlertFixture(t, balanceAlertNumber(999), func(context.Context, *Record, BalanceAlertConfig, *UpstreamUsageQueryResult) error {
		sent++
		return nil
	})
	now := time.Now()
	m.now = func() time.Time { return now }
	m.RunOnce(context.Background())
	require.Equal(t, 1, sent)
	now = now.Add(time.Hour)
	e.err = ErrUpstreamUsageTimeout
	m.RunOnce(context.Background())
	require.Equal(t, 1, sent)
	require.Nil(t, f.state.Balance)
	require.Nil(t, f.state.SentAt)
	require.Equal(t, "UPSTREAM_USAGE_TIMEOUT", f.state.ErrorCode)
}

// TestBalanceAlertExpiredDeliveryPersistsFailure exhausts a caller deadline during mail delivery.
// The final status retains the error using a fresh persistence budget, while
// another scan before the next deadline cannot send the consumed round again.
func TestBalanceAlertExpiredDeliveryPersistsFailure(t *testing.T) {
	attempts := 0
	m, f, _ := newBalanceAlertFixture(t, balanceAlertNumber(999), func(ctx context.Context, _ *Record, _ BalanceAlertConfig, _ *UpstreamUsageQueryResult) error {
		attempts++
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m.RunOnce(ctx)
	require.Equal(t, 1, attempts)
	state, err := m.Status(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "UPSTREAM_BALANCE_ALERT_EMAIL_FAILED", state.ErrorCode)
	require.Nil(t, state.SentAt)
	require.Equal(t, float64(999), *state.Balance)
	m.RunOnce(context.Background())
	require.Equal(t, 1, attempts)
	require.Equal(t, int64(1), f.state.Round)
}

// FinishBalanceAlert holds the final write until released or its own budget expires.
// The captured context lets the test inspect cancellation and deadline behavior.
// Callers release the barrier to allow the ordinary fixture write to complete.
func (f *balanceAlertCompletionStore) FinishBalanceAlert(ctx context.Context, state *BalanceAlertState) error {
	f.started <- ctx
	select {
	case <-f.release:
		return f.balanceAlertFixture.FinishBalanceAlert(ctx, state)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestBalanceAlertStopWaitsForOutcomeWrite stops a monitor during its SMTP attempt.
// Shutdown waits for the independent, bounded final database write and retains
// the cancelled delivery's error instead of closing storage while it is pending.
func TestBalanceAlertStopWaitsForOutcomeWrite(t *testing.T) {
	sending := make(chan struct{})
	m, f, _ := newBalanceAlertFixture(t, balanceAlertNumber(999), func(ctx context.Context, _ *Record, _ BalanceAlertConfig, _ *UpstreamUsageQueryResult) error {
		close(sending)
		<-ctx.Done()
		return ctx.Err()
	})
	store := &balanceAlertCompletionStore{balanceAlertFixture: f, started: make(chan context.Context, 1), release: make(chan struct{})}
	m.store = store
	done := make(chan struct{})
	// Keep both scan and shutdown results observable so the test can prove that
	// StopContext waits for persistence, rather than merely signalling SMTP cancellation.
	go func() { defer close(done); m.RunOnce(context.Background()) }()
	<-sending
	stopped := make(chan error, 1)
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { stopped <- m.StopContext(stopCtx) }()
	persistCtx := <-store.started
	require.NoError(t, persistCtx.Err())
	deadline, ok := persistCtx.Deadline()
	require.True(t, ok)
	remaining := time.Until(deadline)
	require.Positive(t, remaining)
	require.LessOrEqual(t, remaining, balanceAlertPersistTimeout)
	select {
	case err := <-stopped:
		t.Fatalf("shutdown finished before outcome persistence: %v", err)
	default:
	}
	close(store.release)
	require.NoError(t, <-stopped)
	<-done
	state, err := m.Status(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "UPSTREAM_BALANCE_ALERT_EMAIL_FAILED", state.ErrorCode)
}

// TestBalanceAlertQueryCancellationPersistsFailure cancels the caller while its query completes.
// Either the shared result or cancellation can reach the waiter first; both
// paths must save a structured query failure and suppress mail delivery.
func TestBalanceAlertQueryCancellationPersistsFailure(t *testing.T) {
	sent := 0
	m, _, e := newBalanceAlertFixture(t, balanceAlertNumber(999), func(context.Context, *Record, BalanceAlertConfig, *UpstreamUsageQueryResult) error {
		sent++
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.after = cancel
	m.RunOnce(ctx)
	state, err := m.Status(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "UPSTREAM_USAGE_REQUEST_FAILED", state.ErrorCode)
	require.Zero(t, sent)
	require.Nil(t, state.SentAt)
}
