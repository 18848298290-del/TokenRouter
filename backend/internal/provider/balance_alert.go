package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// balance_alert.go polls wallet balances and dispatches per-provider email
// reminders. PostgreSQL claims advance the next deadline before remote work,
// allowing multiple application instances to scan without sending the same
// scheduled reminder. Each claim has its own bounded execution lifetime and
// increasing round number. Interrupted attempts remain consumed until the next
// deadline, rather than being replayed with an uncertain SMTP outcome.
//
// Query identity and reminder choices are captured before claiming and checked
// again before delivery. Missing balances and failed queries produce diagnostic
// state, never a low-balance decision. The monitor owns cancellation and waits
// for its workers before shutdown; upstream query activity retains its own
// lifecycle registration for shared requests that outlive a waiting worker.

// Balance reminder budgets leave time between one attempt and the minimum
// configured interval; each scan admits at most four simultaneous remote checks.
const (
	balanceAlertScanInterval   = time.Minute
	balanceAlertAttemptTimeout = 2 * time.Minute
	balanceAlertConcurrency    = 4
)

// ErrBalanceAlertStopped rejects new work after monitor shutdown begins.
var ErrBalanceAlertStopped = errors.New("upstream balance alert monitor stopped")

// BalanceAlertState describes the latest attempt and its next scheduled deadline.
type BalanceAlertState struct {
	ProviderID    int64      `json:"provider_id"`               // monitored provider identifier
	Round         int64      `json:"round"`                     // increasing attempt identifier
	Identity      string     `json:"-"`                         // private configuration fingerprint
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"` // UTC
	NextCheckAt   *time.Time `json:"next_check_at,omitempty"`   // UTC
	Balance       *float64   `json:"balance,omitempty"`         // observed primary balance
	Unit          string     `json:"unit,omitempty"`            // upstream display unit
	ObservedAt    *time.Time `json:"observed_at,omitempty"`     // UTC
	SentAt        *time.Time `json:"sent_at,omitempty"`         // SMTP confirmation time
	ErrorCode     string     `json:"error_code,omitempty"`      // sanitized attempt failure
}

// BalanceAlertStore owns durable scheduling claims and the latest attempt state.
type BalanceAlertStore interface {
	// GetByID returns the current provider with hydrated query transport settings.
	GetByID(context.Context, int64) (*Record, error)
	// ListBalanceAlertCandidates lists active providers with explicitly enabled reminders.
	ListBalanceAlertCandidates(context.Context) ([]int64, error)
	// ClaimBalanceAlert consumes a due round before queries or SMTP begin.
	// A nil state means another instance owns the deadline or the provider changed.
	ClaimBalanceAlert(context.Context, *Record, string, BalanceAlertConfig, time.Time) (*BalanceAlertState, error)
	// FinishBalanceAlert stores an outcome only for the still-current round and identity.
	FinishBalanceAlert(context.Context, *BalanceAlertState) error
	// GetBalanceAlertState returns the latest state, or nil before the first claim.
	GetBalanceAlertState(context.Context, int64) (*BalanceAlertState, error)
}

// BalanceAlertSend delivers one selected low-balance observation using the caller's context.
// It performs one delivery attempt and returns uncertain outcomes as errors;
// the monitor records them and waits until the next scheduled observation.
type BalanceAlertSend func(context.Context, *Record, BalanceAlertConfig, *UpstreamUsageQueryResult) error

// BalanceAlertMonitor coordinates persistent claims, queries, and email delivery for providers.
type BalanceAlertMonitor struct {
	store    BalanceAlertStore        // persistent claim storage
	queries  *UpstreamUsageService    // shared upstream query owner
	send     BalanceAlertSend         // context-aware notification callback
	hosts    egress.MonitorHostPolicy // automatic query target policy
	now      func() time.Time         // injectable observation clock
	mu       sync.Mutex               // startup publication lock
	started  bool                     // guarded startup flag
	activity operationActivity        // admitted work lifetime
}

// NewBalanceAlertMonitor constructs an idle periodic wallet monitor with shared dependencies.
// The host policy is copied. Call StartContext after assembly and StopContext
// before stopping the query service, notification sender, and database connections.
func NewBalanceAlertMonitor(store BalanceAlertStore, queries *UpstreamUsageService, send BalanceAlertSend, hosts egress.MonitorHostPolicy) *BalanceAlertMonitor {
	return &BalanceAlertMonitor{store: store, queries: queries, send: send, hosts: hosts.Clone(), now: time.Now}
}

// StartContext starts one minute-based scan loop and registers its entire lifetime.
// Repeated starts are harmless. Shutdown admission and loop registration are
// coordinated by operationActivity, so a racing stop either cancels the registered
// loop or prevents its creation. The first scan waits one minute, allowing
// administrator configuration to settle after application startup. The loop
// remains registered while its workers drain, which keeps shared dependencies
// available through the enclosing application's shutdown ordering.
func (s *BalanceAlertMonitor) StartContext(parent context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	ctx, finish, err := s.activity.begin(parent, ErrBalanceAlertStopped)
	if err != nil {
		return err
	}
	s.started = true
	// Register before launching so shutdown cannot miss the loop or the worker
	// rounds it starts while consuming scheduled deadlines.
	go func() {
		defer finish()
		ticker := time.NewTicker(balanceAlertScanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.RunOnce(ctx)
			}
		}
	}()
	return nil
}

// StopContext cancels admitted scans and waits for actual worker completion.
// The first shutdown result is retained by operationActivity. A timeout reports
// unfinished work, and subsequent starts cannot admit another polling loop.
func (s *BalanceAlertMonitor) StopContext(ctx context.Context) error {
	return s.activity.stop(ctx, "upstream balance alerts")
}

// RunOnce scans enabled providers and performs claimed attempts with bounded concurrency.
// Every worker has its own timeout and handles its own failures. Admission is
// registered before listing candidates, so shutdown cancels database reads as
// well as remote queries and mail delivery. Waiting for the worker group keeps
// the scan registered until all admitted work has finished. Distributed claims
// select which instance executes each provider; this method creates no leader
// lease and does not replay consumed rounds after process failure.
func (s *BalanceAlertMonitor) RunOnce(parent context.Context) {
	ctx, finish, err := s.activity.begin(parent, ErrBalanceAlertStopped)
	if err != nil {
		return
	}
	defer finish()
	ids, err := s.store.ListBalanceAlertCandidates(ctx)
	if err != nil {
		slog.Warn("balance alert candidate scan failed", "error", err)
		return
	}
	group, ctx := errgroup.WithContext(ctx)
	// Bound all remote checks, including SMTP, under one worker limit so a
	// large provider list cannot create an unbounded set of waiting goroutines.
	group.SetLimit(balanceAlertConcurrency)
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		group.Go(func() error { s.check(ctx, id); return nil })
	}
	_ = group.Wait()
}

// configuration captures an eligible provider and its normalized reminder policy.
// Disabled queries and inactive providers suspend work while retaining settings.
// The returned identity includes both transport choices and notification settings.
func (s *BalanceAlertMonitor) configuration(ctx context.Context, id int64) (*Record, BalanceAlertConfig, string, error) {
	p, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, BalanceAlertConfig{}, "", err
	}
	c, err := BalanceAlertConfiguration(p)
	if err != nil {
		return nil, c, "", err
	}
	q, err := EffectiveUpstreamUsageConfig(p)
	if err != nil {
		return nil, c, "", err
	}
	if p == nil || p.Status != StatusActive || !c.Enabled || !q.Enabled || !BalanceAlertSupported(p) {
		return nil, c, "", ErrUpstreamUsageDisabled
	}
	return p, c, s.identity(p, c, q), nil
}

// identity hashes query transport and reminder choices into one private observation identity.
// The normalized configuration is JSON-safe after validation. Hash input stays
// inside the service; administrator status responses omit the resulting digest.
func (s *BalanceAlertMonitor) identity(p *Record, c BalanceAlertConfig, q UpstreamUsageQueryConfig) string {
	queryIdentity := UpstreamUsageContextFingerprint(p, q, s.queries.execution.BaseURL(p))
	payload, _ := json.Marshal(c)
	digest := sha256.Sum256(append([]byte(queryIdentity), payload...))
	return hex.EncodeToString(digest[:])
}

// check claims a due attempt, queries its balance, and conditionally sends a reminder.
// The claim is durable before any remote call, so uncertain SMTP results are
// never retried within this round. Query and reminder identity are rechecked
// before delivery, and each persisted outcome names its original claim. A
// configuration change invalidates the observation instead of sending it to an
// earlier recipient or storing it over a newer attempt. Missing balance data
// records a query failure and cannot reuse an older successful observation.
func (s *BalanceAlertMonitor) check(parent context.Context, id int64) {
	ctx, cancel := context.WithTimeout(parent, balanceAlertAttemptTimeout)
	defer cancel()
	p, c, identity, err := s.configuration(ctx, id)
	if err != nil {
		return
	}
	state, err := s.store.ClaimBalanceAlert(ctx, p, identity, c, s.now())
	if err != nil {
		slog.Warn("balance alert claim failed", "provider_id", id, "error", err)
		return
	}
	if state == nil {
		return
	}
	q, _ := EffectiveUpstreamUsageConfig(p)
	base := q.BaseURL
	if base == "" {
		base = s.queries.execution.BaseURL(p)
	}
	if err := s.hosts.Validate(base, func(host string) bool { return CNUsageOfficialHost(p.Platform, host) }); err != nil {
		state.ErrorCode = "UPSTREAM_BALANCE_ALERT_HOST_DENIED"
		s.finish(ctx, state)
		return
	}
	result, err := s.queries.QueryProvider(ctx, id)
	if err != nil {
		state.ErrorCode = apperror.FromError(err).Reason
	} else if result == nil || result.Balance == nil || result.Balance.Remaining == nil || math.IsNaN(*result.Balance.Remaining) || math.IsInf(*result.Balance.Remaining, 0) {
		state.ErrorCode = "UPSTREAM_USAGE_WALLET_UNAVAILABLE"
	} else {
		// Re-read before sending: credentials, recipient, threshold, and enablement
		// may have changed while the remote service was preparing this observation.
		current, _, currentIdentity, currentErr := s.configuration(ctx, id)
		if currentErr != nil || currentIdentity != identity || ctx.Err() != nil {
			return
		}
		state.Balance = result.Balance.Remaining
		state.Unit = result.Unit
		state.ObservedAt = &result.ObservedAt
		if *state.Balance < c.Threshold {
			if err := s.send(ctx, current, c, result); err != nil {
				state.ErrorCode = "UPSTREAM_BALANCE_ALERT_EMAIL_FAILED"
				slog.Warn("balance alert email failed", "provider_id", id)
			} else {
				sent := s.now().UTC()
				state.SentAt = &sent
			}
		}
	}
	s.finish(ctx, state)
}

// finish persists a sanitized attempt result and reports database failures through logging.
// The scheduling claim is already durable. Failed completion writes leave its
// consumed deadline intact and never initiate another query or email attempt.
func (s *BalanceAlertMonitor) finish(ctx context.Context, state *BalanceAlertState) {
	if err := s.store.FinishBalanceAlert(ctx, state); err != nil {
		slog.Warn("balance alert result persistence failed", "provider_id", state.ProviderID, "error", err)
	}
}

// Status returns the latest observation matching the current query and reminder configuration.
// Before the first check, or after identity changes, it returns an empty status
// bearing the provider ID. Provider lookup errors propagate to the administrator API.
func (s *BalanceAlertMonitor) Status(ctx context.Context, id int64) (*BalanceAlertState, error) {
	p, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	c, err := BalanceAlertConfiguration(p)
	if err != nil {
		return nil, err
	}
	q, err := EffectiveUpstreamUsageConfig(p)
	if err != nil {
		return nil, err
	}
	state, err := s.store.GetBalanceAlertState(ctx, id)
	if err != nil {
		return nil, err
	}
	if state == nil || state.Identity != s.identity(p, c, q) {
		return &BalanceAlertState{ProviderID: id}, nil
	}
	return state, nil
}
