package billing

import (
	"bytes"
	"context"
	"errors"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// A reminder send deadline must leave later page contexts usable, while worker
// shutdown can time out on an uncooperative status update. These fixtures
// isolate transport/settings gates, lease release, warning observation and the
// lifetime signal used for a later successful join.

// subscriptionExpiryRepoStub serves configured reminder pages and records list
// calls after initial context validation.
type subscriptionExpiryRepoStub struct {
	// UserSubscriptionRepository is an uninitialized embedding for operations
	// outside this fixture's override. Unexpected methods reach that nil
	// dependency, so scenarios configure only the operation they exercise.
	UserSubscriptionRepository

	// pages returns shared slices and nested grant pointers for each requested
	// page. Tests configure them before scanning; out-of-range pages return nil
	// rows with synthesized page metadata.
	pages [][]UserSubscription // configured reminder page rows
	// listCalls and listContextErrs advance after initial cancellation and
	// parameter checks. List appends a later ctx.Err observation, which may
	// differ if cancellation intervenes; instrumentation access is
	// unsynchronized.
	listCalls       int     // successful list invocation count
	listContextErrs []error // post-validation context error observations
}

// subscriptionExpiryBlockingSender records send calls and blocks
// valid-threshold invocation number one until its context completes.
type subscriptionExpiryBlockingSender struct {
	readyErr error // configured transport readiness result
	// mu protects calls and errs together. Send assigns each call number while
	// locked, then releases before blocking; recordErr reacquires it to append
	// the completion observation.
	mu    sync.Mutex // sender instrumentation coordination mutex
	calls int        // assigned send invocation number
	errs  []error    // recorded send completion errors
}

// subscriptionExpirySettingRepoStub provides scalar/multi-setting values with
// independent injected read failures.
type subscriptionExpirySettingRepoStub struct {
	// values is read directly by scalar lookup, while GetMultiple copies matching
	// strings into a fresh map. Set and deletion methods report success without
	// mutating these programmed values.
	values map[string]string // configured setting text values
	// err takes precedence over programmed scalar values in GetValue. It is
	// independent of multiErr, which is read by the separate GetMultiple fixture
	// method.
	err      error // injected scalar lookup error
	multiErr error // injected multiple-setting read error
}

// expiryLifecycleRepo exposes first operation entry and selectable
// cancellation cooperation for worker tests.
type expiryLifecycleRepo struct {
	// UserSubscriptionRepository is an uninitialized embedding for operations
	// outside this fixture's override. Unexpected methods reach that nil
	// dependency, so scenarios configure only the operation they exercise.
	UserSubscriptionRepository
	// entered, release and ignoreCancel coordinate the held operation: entered
	// publishes start, then ignoreCancel chooses waiting for release instead of
	// context completion. The final join follows the selected wait ending.
	// entered is closed on every overridden status-update entry. These scenarios
	// permit one invocation; another worker/update call would close the signal
	// again and panic.
	entered chan struct{} // first update entry signal
	// release is awaited when ignoreCancel is true, allowing StopContext to time
	// out while the operation remains held. Tests close it before the final
	// successful join.
	release      chan struct{} // uncooperative operation release signal
	ignoreCancel bool          // hold operation past cancellation
}

// TestSubscriptionExpiryReminderSendTimeoutDoesNotPoisonNextPageList supplies
// two due grants on separate pages and blocks the first send until its
// deadline. It checks both sends/lists occur, both page contexts remain usable
// and the first delivery reports deadline exceeded.
func TestSubscriptionExpiryReminderSendTimeoutDoesNotPoisonNextPageList(t *testing.T) {
	now := time.Now()
	repo := &subscriptionExpiryRepoStub{
		pages: [][]UserSubscription{
			{
				{
					ID:        1,
					UserID:    10,
					PlanID:    100,
					StartsAt:  now.Add(-24 * time.Hour),
					ExpiresAt: now.Add(7 * SubscriptionDailyWindow),
					Status:    SubscriptionStatusActive,
					User:      &UserSummary{ID: 10, Email: "first@example.com", Username: "first"},
					Plan:      &SubscriptionPlan{Name: "Pro"},
				},
			},
			{
				{
					ID:        2,
					UserID:    20,
					PlanID:    100,
					StartsAt:  now.Add(-24 * time.Hour),
					ExpiresAt: now.Add(3 * SubscriptionDailyWindow),
					Status:    SubscriptionStatusActive,
					User:      &UserSummary{ID: 20, Email: "second@example.com", Username: "second"},
					Plan:      &SubscriptionPlan{Name: "Pro"},
				},
			},
		},
	}
	sender := &subscriptionExpiryBlockingSender{}
	svc := NewSubscriptionExpiryService(repo, ExpiryOptions{Interval: time.Minute, Observe: log.Printf})
	svc.notificationEmailService = sender
	// Shorten the send budget while keeping page reads independent and longer.
	// The first sender blocks, exposing any reuse of its canceled send context by
	// a later page.
	svc.reminderSendTimeout = 10 * time.Millisecond
	svc.reminderListTimeout = time.Second

	svc.sendExpiryReminders(context.Background())

	require.Equal(t, 2, sender.calls)
	require.Equal(t, 2, repo.listCalls)
	require.Len(t, repo.listContextErrs, 2)
	require.NoError(t, repo.listContextErrs[0])
	require.NoError(t, repo.listContextErrs[1])
	require.ErrorIs(t, sender.errs[0], context.DeadlineExceeded)
}

// TestSubscriptionExpiryService_ExpiryReminderEnabledDefaultsToTrue supplies
// an empty setting map to an unstarted service. It checks the missing enable
// key inherits true rather than disabling reminder selection.
func TestSubscriptionExpiryService_ExpiryReminderEnabledDefaultsToTrue(t *testing.T) {
	svc := NewSubscriptionExpiryService(nil, ExpiryOptions{Interval: time.Minute, Observe: log.Printf})
	svc.settingRepo = &subscriptionExpirySettingRepoStub{values: map[string]string{}}

	require.True(t, svc.expiryReminderEnabled(context.Background()))
}

// TestSubscriptionExpiryService_ExpiryReminderDisabledSkipsSubscriptionScan
// supplies false switch text with a ready notifier. It requires zero list
// calls, checking the setting gate stops scanning before reminder row lookup.
func TestSubscriptionExpiryService_ExpiryReminderDisabledSkipsSubscriptionScan(t *testing.T) {
	repo := &subscriptionExpiryRepoStub{}
	settingRepo := &subscriptionExpirySettingRepoStub{
		values: map[string]string{"subscription_expiry_notify_enabled": "false"},
	}
	svc := NewSubscriptionExpiryService(repo, ExpiryOptions{Interval: time.Minute, Observe: log.Printf})
	svc.settingRepo = settingRepo
	svc.notificationEmailService = &subscriptionExpiryBlockingSender{}

	svc.sendExpiryReminders(context.Background())

	require.Zero(t, repo.listCalls)
}

// TestSubscriptionExpiryService_ExpiryReminderSettingReadErrorFailsClosed
// injects a scalar settings error into the enable lookup. It checks false is
// returned, distinguishing unavailable settings from a missing key's enabled
// default.
func TestSubscriptionExpiryService_ExpiryReminderSettingReadErrorFailsClosed(t *testing.T) {
	svc := NewSubscriptionExpiryService(nil, ExpiryOptions{Interval: time.Minute, Observe: log.Printf})
	svc.settingRepo = &subscriptionExpirySettingRepoStub{err: errors.New("db down")}

	require.False(t, svc.expiryReminderEnabled(context.Background()))
}

// TestSubscriptionExpiryService_ReminderSkipsScanWhenNotLeader supplies a
// ready notifier and unsuccessful lease callback. It checks the service
// performs no active grant list, protecting lease admission before scan work.
func TestSubscriptionExpiryService_ReminderSkipsScanWhenNotLeader(t *testing.T) {
	repo := &subscriptionExpiryRepoStub{}
	svc := NewSubscriptionExpiryService(repo, ExpiryOptions{Interval: time.Minute, Notifier: &subscriptionExpiryBlockingSender{}, Lease: func(context.Context, string, string, time.Duration) (func(), bool) { return nil, false }})
	svc.sendExpiryReminders(context.Background())
	require.Zero(t, repo.listCalls)
}

// TestSubscriptionExpiryService_ReminderRunsEveryCycleSingleInstance invokes
// three empty-page scans with default and custom successful leases. It checks
// one list per cycle and three custom releases, without asserting notification
// replay suppression.
func TestSubscriptionExpiryService_ReminderRunsEveryCycleSingleInstance(t *testing.T) {
	for _, withLease := range []bool{false, true} {
		repo := &subscriptionExpiryRepoStub{}
		released := 0
		options := ExpiryOptions{Interval: time.Minute, Notifier: &subscriptionExpiryBlockingSender{}}
		if withLease {
			options.Lease = func(context.Context, string, string, time.Duration) (func(), bool) {
				return func() { released++ }, true
			}
		}
		svc := NewSubscriptionExpiryService(repo, options)
		for range 3 {
			svc.sendExpiryReminders(context.Background())
		}
		require.Equal(t, 3, repo.listCalls)
		if withLease {
			require.Equal(t, 3, released)
		}
	}
}

// TestSubscriptionExpiryService_MissingSMTPSkipsReminderScanAndLogsOncePerInterval
// performs two immediate checks against unconfigured transport while capturing
// global logs. It requires no row scan and one observed warning; it does not
// advance the warning interval clock.
func TestSubscriptionExpiryService_MissingSMTPSkipsReminderScanAndLogsOncePerInterval(t *testing.T) {
	repo := &subscriptionExpiryRepoStub{}
	settingRepo := &subscriptionExpirySettingRepoStub{values: map[string]string{}}

	svc := NewSubscriptionExpiryService(repo, ExpiryOptions{Interval: time.Minute, Observe: log.Printf})
	svc.settingRepo = settingRepo
	svc.notificationEmailService = &subscriptionExpiryBlockingSender{readyErr: ErrReminderTransportUnconfigured}

	// Capture global logger output for the two immediate readiness checks and
	// restore its writer/flags during cleanup; this scenario intentionally runs
	// without parallel logger mutation.
	var logs bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	svc.sendExpiryReminders(context.Background())
	svc.sendExpiryReminders(context.Background())

	require.Zero(t, repo.listCalls)
	require.Equal(t, 1, bytes.Count(logs.Bytes(), []byte("SMTP is not configured")))
}

// TestSubscriptionExpiryService_SMTPConfigReadErrorSkipsReminderScan supplies
// a notifier readiness error with missing enable settings. It requires zero
// row scans, protecting transport-read failure handling before lease and
// pagination work.
func TestSubscriptionExpiryService_SMTPConfigReadErrorSkipsReminderScan(t *testing.T) {
	repo := &subscriptionExpiryRepoStub{}
	settingRepo := &subscriptionExpirySettingRepoStub{
		values:   map[string]string{},
		multiErr: errors.New("db down"),
	}

	svc := NewSubscriptionExpiryService(repo, ExpiryOptions{Interval: time.Minute, Observe: log.Printf})
	svc.settingRepo = settingRepo
	svc.notificationEmailService = &subscriptionExpiryBlockingSender{readyErr: errors.New("db down")}

	svc.sendExpiryReminders(context.Background())

	require.Zero(t, repo.listCalls)
}

// TestSubscriptionExpiryLifecycle checks construction leaves its
// operation-entry signal untouched, then starts twice and waits for first
// update entry. Cooperative cancellation allows repeated successful stops,
// followed by a restart attempt on the stopped service.
func TestSubscriptionExpiryLifecycle(t *testing.T) {
	repo := &expiryLifecycleRepo{entered: make(chan struct{})}
	svc := NewSubscriptionExpiryService(repo, ExpiryOptions{Interval: time.Hour})
	select {
	case <-repo.entered:
		t.Fatal("constructor started work")
	default:
	}
	// Start twice, then wait for repository entry before stopping; this proves
	// cancellation reaches an admitted worker instead of merely sealing an unstarted service.
	svc.Start()
	svc.Start()
	<-repo.entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, svc.StopContext(ctx))
	require.NoError(t, svc.StopContext(ctx))
	svc.Start()
}

// TestSubscriptionExpiryBlockedStopReturnsBudget holds a status update after
// ignoring worker cancellation. A short StopContext wait must time out;
// releasing the operation then permits a later background-context stop to
// finish.
func TestSubscriptionExpiryBlockedStopReturnsBudget(t *testing.T) {
	repo := &expiryLifecycleRepo{entered: make(chan struct{}), release: make(chan struct{}), ignoreCancel: true}
	svc := NewSubscriptionExpiryService(repo, ExpiryOptions{Interval: time.Hour})
	// Wait for the worker's held repository operation before imposing the stop
	// budget, so the timeout scenario includes work already running beyond cancellation.
	svc.Start()
	<-repo.entered
	// Bound the caller's wait rather than releasing the deliberately
	// uncooperative update; only the later explicit release makes final worker
	// joining possible.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, svc.StopContext(ctx), context.DeadlineExceeded)
	// Release the blocked operation after observing the caller timeout, then wait
	// without a deadline to confirm that the same stopped worker eventually
	// publishes completion.
	close(repo.release)
	require.NoError(t, svc.StopContext(context.Background()))
}

// List validates active/expiry-ascending page parameters after checking caller
// cancellation, then records its invocation. It returns shared programmed page
// rows and fresh pagination metadata; canceled calls return before
// instrumentation and out-of-range pages yield nil rows.
func (r *subscriptionExpiryRepoStub) List(ctx context.Context, params pagination.PaginationParams, _userID, _planID *int64, status, _platform, sortBy, sortOrder string) ([]UserSubscription, *pagination.PaginationResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	requireSubscriptionExpiryListParams(ctx, params, status, sortBy, sortOrder)
	r.listCalls++
	r.listContextErrs = append(r.listContextErrs, ctx.Err())
	pageIndex := params.Page - 1
	if pageIndex < 0 || pageIndex >= len(r.pages) {
		return nil, &pagination.PaginationResult{Page: params.Page, PageSize: params.PageSize, Pages: len(r.pages)}, nil
	}
	return r.pages[pageIndex], &pagination.PaginationResult{Page: params.Page, PageSize: params.PageSize, Pages: len(r.pages)}, nil
}

// requireSubscriptionExpiryListParams panics for nil context or any page
// size/status/sort mismatch against the scanner's expected request. It checks
// two-hundred rows, active status and expiry ascending, leaving page number
// and caller cancellation to List.
func requireSubscriptionExpiryListParams(ctx context.Context, params pagination.PaginationParams, status, sortBy, sortOrder string) {
	if ctx == nil {
		panic("subscription expiry list ctx is nil")
	}
	if params.PageSize != 200 || status != SubscriptionStatusActive || sortBy != "expires_at" || sortOrder != "asc" {
		panic("unexpected subscription expiry list params")
	}
}

// Send assigns a call number before validating the seven/three/one reminder
// threshold. A valid invocation numbered one waits for context completion; an
// invalid first invocation still advances numbering and returns an error
// without recording errs. Callers supply a usable context.
//
// Later valid invocations record their current context error and return a
// fresh context-error observation immediately, so concurrent cancellation can
// occur between those reads. Instrumentation locks cover numbering and error
// appends separately; a later first-valid invocation does not inherit
// invocation one's blocking behavior.
func (s *subscriptionExpiryBlockingSender) Send(ctx context.Context, input ExpiryReminder) error {
	// Assign the first-call blocking behavior under the instrumentation mutex,
	// then release it so waiting for context cancellation does not block
	// recording other call numbers.
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if input.DaysRemaining != 7 && input.DaysRemaining != 3 && input.DaysRemaining != 1 {
		return errors.New("unexpected event")
	}
	if call == 1 {
		// Hold valid invocation number one until its own context completes,
		// reproducing the timed first send while the scan retains independent parent
		// and page contexts.
		<-ctx.Done()
		s.recordErr(ctx.Err())
		return ctx.Err()
	}
	s.recordErr(ctx.Err())
	return ctx.Err()
}

// recordErr appends one send-completion error under the sender mutex. The
// slice preserves append order, which can differ from call numbering under
// concurrent sends; tests inspect it after their synchronous scan.
func (s *subscriptionExpiryBlockingSender) recordErr(err error) {
	// Serialize error-slice appends independently of the blocking send phase,
	// preventing concurrent completion observations from mutating the same slice
	// unsafely.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err)
}

// Ready returns the programmed notifier readiness error unchanged for every
// context. It performs no transport I/O or cancellation check, allowing
// scenarios to isolate scanner gating from mail delivery setup.
func (s *subscriptionExpiryBlockingSender) Ready(context.Context) error { return s.readyErr }

// Get reports setting-not-found with no record for every key and context. It
// ignores the fixture's value map, so scalar reminder switch scenarios use the
// separate GetValue override.
func (r *subscriptionExpirySettingRepoStub) Get(context.Context, string) (*settings.Setting, error) {
	return nil, settings.ErrSettingNotFound
}

// GetValue returns an injected scalar read error before consulting programmed
// values. Missing keys report setting-not-found; successful results copy
// string values, and context cancellation is not interpreted by this fixture
// method.
func (r *subscriptionExpirySettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	value, ok := r.values[key]
	if !ok {
		return "", settings.ErrSettingNotFound
	}
	return value, nil
}

// Set reports successful fixture mutation for every key/value without storing
// either. Tests program read results through values directly, so this method
// cannot establish that a settings write persisted.
func (r *subscriptionExpirySettingRepoStub) Set(context.Context, string, string) error {
	return nil
}

// GetMultiple returns its injected multi-read error or a fresh map containing
// only requested programmed keys. Missing entries are omitted without error;
// it copies strings and ignores scalar err and context cancellation.
func (r *subscriptionExpirySettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	if r.multiErr != nil {
		return nil, r.multiErr
	}
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

// SetMultiple reports successful fixture mutation without retaining the
// supplied map or changing programmed values. Reminder scenarios configure
// reads directly, so this stub result supplies no persistence or copied-value
// observation.
func (r *subscriptionExpirySettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	return nil
}

// GetAll returns nil map with nil error for every context. It exposes none of
// the programmed values, distinguishing this unused fixture operation from the
// implemented requested-key copying in GetMultiple.
func (r *subscriptionExpirySettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return nil, nil
}

// Delete reports success for every key without removing programmed state.
// Tests using this fixture configure reads directly; a successful stub delete
// therefore does not change subsequent GetValue results.
func (r *subscriptionExpirySettingRepoStub) Delete(context.Context, string) error {
	return nil
}

// BatchUpdateExpiredStatus publishes entry, then waits for context completion
// or the explicit release chosen by ignoreCancel. This fixture permits one
// invocation because entry closes the same channel each time; it changes no
// grant records and returns zero with the current context error.
//
// When ignoreCancel is true, cancellation cannot end the held operation; the
// test closes release after observing its short stop budget expire. A later
// stop then joins that original worker. Otherwise context cancellation
// releases the wait directly, allowing cooperative lifecycle tests to finish.
func (r *expiryLifecycleRepo) BatchUpdateExpiredStatus(ctx context.Context) (int64, error) {
	// Publish update entry before waiting, so lifecycle tests can coordinate
	// cancellation or release after proving that the worker has reached its
	// repository operation.
	close(r.entered)
	if r.ignoreCancel {
		<-r.release
	} else {
		<-ctx.Done()
	}
	return 0, ctx.Err()
}
