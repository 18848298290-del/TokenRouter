package live

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
)

const (
	liveLeaseRefreshInterval    = 20 * time.Second
	liveRedisOperationTimeout   = 3 * time.Second
	liveClosedRecordTTL         = 24 * time.Hour
	liveObserverPollInterval    = 250 * time.Millisecond
	liveObserverStoreRetryLimit = 5
)

// ProxyLiveSideband gives an authenticated client the control connection; media uses a separate path.
func (s *Service) ProxyLiveSideband(
	ctx context.Context,
	record *session.LiveCallRecord,
	downstream FrameConn,
) error {
	if record == nil || downstream == nil {
		return session.ErrLiveCallNotFound
	}
	store, err := s.ports.Store()
	if err != nil {
		return err
	}
	owner := uuid.NewString()
	claimed, err := store.ClaimLiveController(ctx, record.CallHash, session.LiveControllerProxy, owner)
	if err != nil {
		return err
	}
	if !claimed {
		return session.ErrLiveControllerChanged
	}

	// Use a separate cleanup budget so cancellation still allows the session observer to resume.
	releaseController := func() {
		cleanup, done := context.WithTimeout(context.Background(), liveRedisOperationTimeout)
		defer done()
		_, _ = store.ReleaseLiveController(cleanup, record.CallHash, owner)
	}
	// Allow the observer to notice takeover and close its connection before dialing a replacement.
	if !waitLiveObserver(ctx, liveObserverPollInterval) || ctx.Err() != nil {
		releaseController()
		go s.Observe(record)
		return context.Cause(ctx)
	}
	provider, err := s.ports.Target(ctx, record)
	if err != nil {
		releaseController()
		go s.Observe(record)
		return err
	}
	if err := ctx.Err(); err != nil {
		releaseController()
		go s.Observe(record)
		return context.Cause(ctx)
	}
	upstream, err := provider.Dial(ctx)
	if err != nil {
		releaseController()
		go s.Observe(record)
		return err
	}
	defer func() { _ = upstream.Close() }()
	downstream.SetReadLimit(s.readLimit)

	proxyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, 2)
	modelState := newLiveSidebandModelState(record)
	go func() {
		for {
			messageType, payload, readErr := downstream.ReadFrame(proxyCtx)
			if readErr != nil {
				errCh <- readErr
				return
			}
			if messageType == TextFrame {
				rewritten, clientModel, internalModels, rewriteErr := provider.Rewrite(proxyCtx, payload)
				if rewriteErr != nil {
					errCh <- rewriteErr
					return
				}
				payload = rewritten
				if clientModel != "" {
					modelState.update(clientModel, internalModels...)
				}
			}
			if writeErr := upstream.WriteFrame(proxyCtx, messageType, payload); writeErr != nil {
				errCh <- writeErr
				return
			}
		}
	}()
	go func() {
		for {
			messageType, payload, readErr := upstream.ReadFrame(proxyCtx)
			if readErr != nil {
				errCh <- readErr
				return
			}
			if messageType == TextFrame {
				clientModel, internalModels := modelState.snapshot()
				payload = RestoreServerPayload(payload, clientModel, internalModels)
			}
			if writeErr := downstream.WriteFrame(proxyCtx, messageType, payload); writeErr != nil {
				errCh <- writeErr
				return
			}
			if messageType == TextFrame {
				eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
				if eventType == "session.closed" || eventType == "session.ended" {
					errCh <- session.ErrLiveCallNotFound
					return
				}
			}
		}
	}()

	runErr := s.RunController(proxyCtx, record, upstream, errCh)
	cancel()
	releaseController()
	if SessionEnded(runErr) || !time.Now().Before(record.ExpiresAt) {
		s.Finalize(record)
		return runErr
	}
	go s.Observe(record)
	return runErr
}

// SessionEnded reports whether to finalize usage and release the session lease.
// session.ErrLiveUnavailable includes renewal failure after a lease has been reclaimed.
// Reconnecting after lease loss would repeatedly open an unreserved connection until ExpiresAt.
func SessionEnded(err error) bool {
	return errors.Is(err, session.ErrLiveCallNotFound) ||
		errors.Is(err, session.ErrLiveUnavailable) ||
		errors.Is(err, context.DeadlineExceeded)
}

func (s *Service) RunController(
	ctx context.Context,
	record *session.LiveCallRecord,
	upstream FrameConn,
	errCh <-chan error,
) error {
	refreshTicker := time.NewTicker(liveLeaseRefreshInterval)
	defer refreshTicker.Stop()
	maxTimer := time.NewTimer(time.Until(record.ExpiresAt))
	defer maxTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case err := <-errCh:
			return err
		case <-maxTimer.C:
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = upstream.WriteFrame(closeCtx, TextFrame, []byte(`{"type":"session.close"}`))
			cancel()
			return context.DeadlineExceeded
		case <-refreshTicker.C:
			if !s.RefreshLease(record) {
				return session.ErrLiveUnavailable
			}
		}
	}
}

func (s *Service) Observe(record *session.LiveCallRecord) {
	if record == nil {
		return
	}
	owner := uuid.NewString()
	ctx, finish, ok := s.ports.BeginObserver(owner)
	if !ok {
		return
	}
	defer finish()

	store, err := s.ports.Store()
	if err != nil {
		return
	}
	claimed, claimErr := store.ClaimLiveController(ctx, record.CallHash, session.LiveControllerObserver, owner)
	if ctx.Err() != nil {
		return
	}
	if claimErr != nil {
		// If ownership cannot be confirmed, retain the snapshot and attempt finalization after expiry.
		s.FinalizeAfterExpiry(ctx, record)
		return
	}
	if !claimed {
		return
	}
	storeErrStreak := 0
	for {
		if ctx.Err() != nil {
			return
		}
		latest, getErr := store.GetLiveCall(ctx, record.CallHash)
		if ctx.Err() != nil {
			return
		}
		if getErr != nil {
			if errors.Is(getErr, session.ErrLiveCallNotFound) {
				return
			}
			// Bound Redis read retries, then wait for session expiry before finalization.
			storeErrStreak++
			if storeErrStreak >= liveObserverStoreRetryLimit {
				s.FinalizeAfterExpiry(ctx, record)
				return
			}
			if !waitLiveObserver(ctx, s.retryInterval) {
				return
			}
			continue
		}
		storeErrStreak = 0
		record = latest
		if record.Controller != session.LiveControllerObserver {
			return
		}
		if !time.Now().Before(record.ExpiresAt) {
			s.Finalize(record)
			return
		}
		upstream, dialErr := s.dial(ctx, record)
		if ctx.Err() != nil {
			if upstream != nil {
				_ = upstream.Close()
			}
			return
		}
		if dialErr != nil {
			if !s.WaitForObserverRetry(ctx, record) {
				return
			}
			continue
		}
		runErr := s.RunObserverConnection(ctx, record, upstream)
		_ = upstream.Close()
		if ctx.Err() != nil {
			return
		}
		if errors.Is(runErr, session.ErrLiveControllerChanged) {
			return
		}
		if SessionEnded(runErr) {
			s.Finalize(record)
			return
		}
		if !s.WaitForObserverRetry(ctx, record) {
			return
		}
	}
}

func (s *Service) RunObserverConnection(parent context.Context, record *session.LiveCallRecord, upstream FrameConn) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	frameCh := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		for {
			messageType, payload, err := upstream.ReadFrame(ctx)
			if err != nil {
				select {
				case errCh <- err:
				case <-ctx.Done():
				}
				return
			}
			if messageType == TextFrame {
				select {
				case frameCh <- payload:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	refreshTicker := time.NewTicker(liveLeaseRefreshInterval)
	defer refreshTicker.Stop()
	controllerTicker := time.NewTicker(liveObserverPollInterval)
	defer controllerTicker.Stop()
	maxTimer := time.NewTimer(time.Until(record.ExpiresAt))
	defer maxTimer.Stop()
	store, _ := s.ports.Store()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case payload := <-frameCh:
			eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
			if eventType == "session.closed" || eventType == "session.ended" {
				return session.ErrLiveCallNotFound
			}
		case err := <-errCh:
			return err
		case <-controllerTicker.C:
			controller, err := store.GetLiveController(context.Background(), record.CallHash)
			if err != nil {
				return err
			}
			if controller != session.LiveControllerObserver {
				return session.ErrLiveControllerChanged
			}
		case <-refreshTicker.C:
			if !s.RefreshLease(record) {
				return session.ErrLiveUnavailable
			}
		case <-maxTimer.C:
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = upstream.WriteFrame(closeCtx, TextFrame, []byte(`{"type":"session.close"}`))
			closeCancel()
			return context.DeadlineExceeded
		}
	}
}

func (s *Service) WaitForObserverRetry(ctx context.Context, record *session.LiveCallRecord) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
	}
	store, err := s.ports.Store()
	if err != nil {
		return false
	}
	controller, getErr := store.GetLiveController(context.Background(), record.CallHash)
	if getErr != nil && !errors.Is(getErr, session.ErrLiveCallNotFound) {
		// Let the observer loop retry store reads and finalize expired sessions.
		return true
	}
	// Return to the loop's expiry check so it can write usage and release the lease.
	// Returning false here on expiry would silently skip finalization.
	return getErr == nil && controller == session.LiveControllerObserver
}

// FinalizeAfterExpiry waits for expiry using the last snapshot when observer storage reads fail.
// Finalize uses MarkLiveCallClosed to deduplicate competing cleanup paths; cancellation skips the wait.
func (s *Service) FinalizeAfterExpiry(ctx context.Context, record *session.LiveCallRecord) {
	if record == nil {
		return
	}
	if wait := time.Until(record.ExpiresAt); wait > 0 {
		if !waitLiveObserver(ctx, wait) {
			return
		}
	}
	s.Finalize(record)
}

func (s *Service) RefreshLease(record *session.LiveCallRecord) bool {
	cache, err := s.ports.Leases()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveRedisOperationTimeout)
	defer cancel()
	refreshed, err := cache.RefreshLiveLease(ctx, record.ProviderID, record.UserID, record.APIKeyID, record.LeaseID)
	return err == nil && refreshed
}

func (s *Service) ReleaseLease(providerID, userID, apiKeyID int64, leaseID string) {
	cache, err := s.ports.Leases()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveRedisOperationTimeout)
	defer cancel()
	_ = cache.ReleaseLiveLease(ctx, providerID, userID, apiKeyID, leaseID)
}

func (s *Service) Finalize(record *session.LiveCallRecord) {
	if record == nil {
		return
	}
	store, err := s.ports.Store()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveRedisOperationTimeout)
	first, err := store.MarkLiveCallClosed(ctx, record.CallHash, liveClosedRecordTTL)
	cancel()
	if err != nil || !first {
		return
	}
	s.ReleaseLease(record.ProviderID, record.UserID, record.APIKeyID, record.LeaseID)
	duration := max(int(time.Since(record.CreatedAt).Milliseconds()), 0)
	s.ports.RecordZeroUsage(context.Background(), record, duration)
}

func waitLiveObserver(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Lookup validates call ID, API key, payer, and group bindings; closed records return not found.
func (s *Service) Lookup(ctx context.Context, callID string, identity session.LiveCallIdentity) (*session.LiveCallRecord, error) {
	store, err := s.ports.Store()
	if err != nil {
		return nil, err
	}
	record, err := store.GetLiveCall(ctx, HashCallID(callID))
	if err != nil {
		return nil, err
	}
	if record.CallID != callID || record.APIKeyID != identity.APIKeyID || record.UserID != identity.UserID || record.GroupID != liveGroupID(identity.GroupID) {
		return nil, session.ErrLiveIdentityMismatch
	}
	if record.Controller == session.LiveControllerClosed {
		return nil, session.ErrLiveCallNotFound
	}
	return record, nil
}
