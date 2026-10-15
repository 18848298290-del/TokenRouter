package provider

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// background_refresh.go runs periodic credential scans and Grok reconciliation
// through shared platform gates and coordinated shutdown.
// Periodic refresh and Grok reconciliation compete for the same platform rate
// and concurrency gates. Each entry point creates its own failure state, so a
// platform trip in one execution does not carry a failure streak into the next
// cycle. The periodic scan also keeps its last completed page cursor across
// bounded cycles.
//
// RefreshLoop owns repeated startup, while separate operationActivity holders
// track scans and reconciliation through actual completion. StopContext first
// closes service admission, then cancels and joins all three holders
// concurrently using the caller's budget. Its first completed result is retained
// even when work remains unfinished after a timeout. Construction copies tuning
// and the registration slice, but keeps their service dependencies shared with
// application assembly.

// BackgroundTokenRefresher supplies background credential eligibility, expiry
// and direct exchange behavior.
type BackgroundTokenRefresher interface {
	RefreshTokenOperation // direct platform exchange behavior
	// CanRefresh reports whether a candidate can use this background platform
	// exchange.
	CanRefresh(*Record) bool
	// NeedsRefresh reports whether an eligible provider requires exchange within
	// the supplied expiry window. Page processing calls it before queuing work,
	// and the coordinated executor checks current state again after rereading.
	NeedsRefresh(*Record, time.Duration) bool
}

// RefreshRegistration binds a platform name to background checks and optional
// coordinated execution.
type RefreshRegistration struct {
	Platform string // exact registered platform name
	// Refresher and Executor borrow platform clients from application assembly.
	// Their implementations remain usable through all scans and reconciliation
	// calls, including waits for actual completion during service shutdown.
	// candidate checks and direct exchange
	Refresher BackgroundTokenRefresher
	// Executor enables coordinated locking and rereads when Attempts.API is
	// also configured; a nil executor selects the direct Refresher path.
	Executor OAuthRefreshExecutor
}

// BackgroundRefreshOptions supplies scan settings and shared platform services
// for background maintenance.
type BackgroundRefreshOptions struct {
	// Tuning is copied at construction, and the captured copy replaces
	// Attempts.Tuning so scanning and attempts read the same settings.
	Tuning *RefreshTuning
	Pager  OAuthRefreshCandidatePager // shared candidate page reader
	// Registrations has a copied slice with shared refresher and executor
	// dependencies. Periodic grouping uses the last duplicate platform entry,
	// while reconciliation selects the first usable Grok registration.
	Registrations []RefreshRegistration
	Attempts      RefreshAttempts // captured provider attempt callbacks
	// Reconciliation borrows its injected dependencies; construction replaces
	// its Pager and Execution callbacks to use this service's registered
	// platforms.
	Reconciliation GrokReconciliationOptions
	// Debug reports quiet scan totals. Info reports service lifecycle, provider
	// success and active/failed cycle totals. Warn reports canceled scans, failed
	// providers and incomplete shutdown. Error reports paging and configuration faults.
	// NewBackgroundRefreshService replaces each nil callback with a no-op before
	// passing the callbacks to scan and page consumers, allowing shared optional
	// logging configuration for those entry points.
	Debug, Info, Warn, Error func(string, ...any)
}

// @project-doc docs/operations/provider_maintenance.md#account_credential_refresh
// BackgroundRefreshService shares platform admission and coordinates scans,
// reconciliation and shutdown.
type BackgroundRefreshService struct {
	options BackgroundRefreshOptions // captured settings and dependencies
	loop    *RefreshLoop             // periodic scan startup holder
	scan    RefreshCandidateScan     // retained candidate resume cursor
	gates   RefreshProviderGates     // shared platform admission registry
	// on-demand Grok reconciliation holder
	reconciliation *GrokReconciliationService
	activity       operationActivity // active scan cancellation and join
	// stopMu protects stopDone and publication of stopErr. A nonnil stopDone
	// rejects later entry-point checks and closes after all stop holders return;
	// channel closure makes the retained result visible to waiting callers.
	stopMu   sync.Mutex    // service stop publication mutex
	stopDone chan struct{} // first stop completion signal
	stopErr  error         // retained first completed stop result
}

// NewBackgroundRefreshService captures a copy of tuning and the registration
// slice while borrowing platform service dependencies. Nil loggers become
// no-ops, and Attempts.Tuning uses the captured settings. Construction starts no
// work and binds Grok reconciliation to the shared pager and first usable Grok
// registration; other reached attempt callbacks remain the caller's
// responsibility.
func NewBackgroundRefreshService(options BackgroundRefreshOptions) *BackgroundRefreshService {
	noop := func(string, ...any) {}
	if options.Info == nil {
		options.Info = noop
	}
	if options.Warn == nil {
		options.Warn = noop
	}
	if options.Error == nil {
		options.Error = noop
	}
	if options.Debug == nil {
		options.Debug = noop
	}
	if options.Tuning != nil {
		copy := *options.Tuning
		options.Tuning = &copy
	}
	options.Registrations = slices.Clone(options.Registrations)
	options.Attempts.Tuning = options.Tuning
	s := &BackgroundRefreshService{options: options}
	s.loop = NewRefreshLoop(s.ScanCycle)
	options.Reconciliation.Pager = options.Pager
	options.Reconciliation.Execution = func() *RefreshProviderExecution {
		for _, registration := range options.Registrations {
			if registration.Platform == PlatformGrok && registration.Refresher != nil {
				return s.execution(registration)
			}
		}
		return nil
	}
	s.reconciliation = NewGrokReconciliationService(options.Reconciliation)
	return s
}

// StartContext starts periodic maintenance when captured tuning is enabled and
// service shutdown has not begun. Disabled or absent tuning returns nil,
// intervals below one minute select five minutes, and RefreshLoop prevents
// repeated startup or restart after stop.
func (s *BackgroundRefreshService) StartContext(ctx context.Context) error {
	if s.isStopping() {
		return nil
	}
	tuning := s.options.Tuning
	if tuning == nil || !tuning.Enabled {
		s.options.Info("token_refresh.service_disabled")
		return nil
	}
	interval := time.Duration(tuning.CheckIntervalMinutes) * time.Minute
	// Subminute configured intervals select the standard service cadence for
	// periodic scans before RefreshLoop validates and creates its ticker.
	if interval < time.Minute {
		interval = 5 * time.Minute
	}
	started, err := s.loop.StartContext(ctx, interval)
	if started {
		s.options.Info("token_refresh.service_started", "check_interval_minutes", tuning.CheckIntervalMinutes, "refresh_before_expiry_hours", tuning.RefreshBeforeExpiryHours)
	}
	return err
}

// StopContext closes service admission, cancels periodic work, active scans and
// reconciliation, and waits for all three stop holders. Callers pass a nonnil
// context whose budget applies to each holder.
//
// Stop holders run concurrently so the shared budget covers their waits
// together, and their errors are joined before the completion channel closes.
// The first completed result is retained, including timeout with unfinished
// work. A concurrent caller may return its own context error while awaiting that
// first result; after completion, later calls receive the retained result.
// Application lifecycle uses this outcome before deciding whether shared
// dependencies can be released.
func (s *BackgroundRefreshService) StopContext(ctx context.Context) error {
	s.stopMu.Lock()
	if s.stopDone != nil {
		done := s.stopDone
		s.stopMu.Unlock()
		select {
		case <-done:
			return s.stopErr
		default:
		}
		select {
		case <-done:
			return s.stopErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Reject later entry-point checks before canceling holders. Their activity
	// barriers also cancel and join calls that raced this service-level check.
	s.stopDone = make(chan struct{})
	done := s.stopDone
	s.stopMu.Unlock()
	// Cancel and join all holders with the same budget concurrently, then retain
	// their joined outcome before waking other callers waiting for the first
	// stop.
	results := make([]error, 3)
	var workers sync.WaitGroup
	stops := []func(context.Context) error{s.loop.StopContext, func(ctx context.Context) error { return s.activity.stop(ctx, "provider refresh scans") }, s.reconciliation.StopContext}
	for i, stop := range stops {
		workers.Go(func() { ; results[i] = stop(ctx) })
	}
	workers.Wait()
	err := errors.Join(results...)
	s.stopMu.Lock()
	s.stopErr = err
	close(done)
	s.stopMu.Unlock()
	if err != nil {
		s.options.Warn("token_refresh.service_stop_incomplete", "error", err)
	} else {
		s.options.Info("token_refresh.service_stopped")
	}
	return err
}

// isStopping reports whether the first service stop has published its admission
// barrier.
func (s *BackgroundRefreshService) isStopping() bool {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	return s.stopDone != nil
}

// ReconcileGrokOAuth delegates administrative work to the registered Grok
// reconciliation holder. A begun service stop returns ErrRefreshStopped before
// delegation, while the holder supplies input validation and its cancellation
// registration.
func (s *BackgroundRefreshService) ReconcileGrokOAuth(ctx context.Context, input GrokOAuthReconcileInput) (*GrokOAuthReconcileResult, error) {
	if s.isStopping() {
		return nil, ErrRefreshStopped
	}
	return s.reconciliation.ReconcileGrokOAuth(ctx, input)
}

// CandidatePosition returns the last completed periodic scan cursor through its
// synchronized accessor.
func (s *BackgroundRefreshService) CandidatePosition() int64 { return s.scan.Position() }

// ProviderRateGate returns the shared rate schedule for the exact platform
// string. Captured tuning supplies its initial QPS; later calls return the first
// published gate, including when another entry point created it.
func (s *BackgroundRefreshService) ProviderRateGate(platform string) *RefreshRateGate {
	return s.gates.Rate(platform, s.options.Tuning.QPS())
}

// ProviderConcurrencyGate returns shared capacity for the exact platform string
// using captured tuning. The first published gate retains its initial limit and
// is borrowed by both scans and reconciliation.
func (s *BackgroundRefreshService) ProviderConcurrencyGate(platform string) *RefreshConcurrencyGate {
	return s.gates.Pool(platform, s.options.Tuning.Concurrency())
}

// execution creates one platform execution state with an independent failure
// streak and shared admission gates. Registration.Refresher must be nonnil; its
// checks and exchange are captured with the optional coordinated executor for
// RefreshAttempts.
func (s *BackgroundRefreshService) execution(registration RefreshRegistration) *RefreshProviderExecution {
	state := NewRefreshProviderState(s.ProviderRateGate(registration.Platform), s.ProviderConcurrencyGate(registration.Platform), s.options.Tuning.FailureThreshold(), s.options.Attempts.NonRetryable)
	return &RefreshProviderExecution{
		Platform: registration.Platform, State: state, CanRefresh: registration.Refresher.CanRefresh, NeedsRefresh: registration.Refresher.NeedsRefresh,
		Execute: func(ctx context.Context, v *Record, window time.Duration, state *RefreshProviderState) error {
			return s.options.Attempts.Run(ctx, v, registration.Refresher, registration.Executor, window, state)
		},
	}
}

// ScanCycle registers and runs one bounded candidate pass using captured
// platform services. Nil parent selects context.Background, while stopped or
// canceled registration returns before paging; this explicit entry point runs
// regardless of the periodic Enabled setting.
//
// Each usable registration creates independent cycle failure state backed by
// shared platform gates. The page processor joins admitted work before the
// scanner advances its cursor, and an interrupted page remains positioned for
// another cycle. The activity finish callback runs after the complete scan
// returns so shutdown tracks actual completion. Callers coordinate concurrent
// explicit ScanCycle invocations because the scan mutex protects cursor access
// rather than whole cycles.
func (s *BackgroundRefreshService) ScanCycle(parent context.Context) {
	if parent == nil {
		parent = context.Background()
	}
	if s.isStopping() {
		return
	}
	// Register the complete scan before paging so shutdown can cancel repository
	// work and join exchanges that outlive cancellation until their actual return.
	parent, finish, err := s.activity.begin(parent, ErrRefreshStopped)
	if err != nil {
		return
	}
	defer finish()
	platforms := make([]string, 0, len(s.options.Registrations))
	states := make(map[string]*RefreshProviderExecution, len(s.options.Registrations))
	for _, registration := range s.options.Registrations {
		if registration.Platform != "" && registration.Refresher != nil {
			platforms = append(platforms, registration.Platform)
			states[registration.Platform] = s.execution(registration)
		}
	}
	window := time.Duration(0)
	if s.options.Tuning != nil {
		window = time.Duration(s.options.Tuning.RefreshBeforeExpiryHours * float64(time.Hour))
	}
	processor := RefreshPageProcessor{Concurrency: s.options.Tuning.Concurrency(), Info: s.options.Info, Warn: s.options.Warn}
	s.scan.Run(parent, s.options.Pager, RefreshScanOptions{Timeout: s.options.Tuning.CycleTimeout(), PageSize: s.options.Tuning.PageSize(), Platforms: platforms, Debug: s.options.Debug, Info: s.options.Info, Warn: s.options.Warn, Error: s.options.Error}, func(ctx context.Context, values []Record) RefreshPageStats {
		return processor.ProcessPage(ctx, values, states, window)
	})
}
