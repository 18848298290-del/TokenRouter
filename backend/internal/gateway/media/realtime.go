package media

import (
	"context"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// DefaultRealtimeDialTimeout bounds the upstream handshake before downstream upgrade, not session duration.
const DefaultRealtimeDialTimeout = 12 * time.Second

// RealtimePorts supplies selection, credentials, and dialing before the downstream connection is upgraded.
type RealtimePorts interface {
	SelectRealtime(context.Context, map[int64]struct{}) (provider.ProviderSnapshot, bool, error)
	AcquireRealtime(context.Context, provider.ProviderSnapshot) (func(), bool)
	RealtimeCredential(context.Context, provider.ProviderSnapshot) (string, error)
	OpenRealtime(context.Context, provider.ProviderSnapshot, string, string) (upstream.FrameConn, error)
	RealtimeOpenFailed(context.Context, provider.ProviderSnapshot, error)
}

// RealtimeLease owns the upstream connection and provider slot; the HTTP adapter closes the downstream.
type RealtimeLease struct {
	Provider provider.ProviderSnapshot
	Conn     upstream.FrameConn
	release  func()
	once     sync.Once
	err      error
}

// RealtimeAdmission distinguishes missing candidates, upstream failure, and a wait-layer rejection.
type RealtimeAdmission struct {
	Lease         *RealtimeLease
	CandidateSeen bool
	WaitRejected  bool
}

func (l *RealtimeLease) Close() error {
	l.once.Do(func() {
		if l.Conn != nil {
			l.err = l.Conn.Close()
		}
		if l.release != nil {
			l.release()
		}
	})
	return l.err
}

func OpenRealtime(ctx context.Context, model string, dialTimeout time.Duration, ports RealtimePorts) RealtimeAdmission {
	excluded := make(map[int64]struct{})
	result := RealtimeAdmission{}
	for range 4 {
		candidate, present, err := ports.SelectRealtime(ctx, excluded)
		if err != nil || !present {
			break
		}
		result.CandidateSeen = true
		release, acquired := ports.AcquireRealtime(ctx, candidate)
		if !acquired {
			result.WaitRejected = true
			return result
		}
		token, err := ports.RealtimeCredential(ctx, candidate)
		if err != nil {
			release()
			excluded[candidate.ID] = struct{}{}
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, dialTimeout)
		conn, err := ports.OpenRealtime(probeCtx, candidate, token, model)
		cancel()
		if err != nil {
			ports.RealtimeOpenFailed(ctx, candidate, err)
			release()
			excluded[candidate.ID] = struct{}{}
			continue
		}
		if release == nil || conn == nil {
			return result
		}
		result.Lease = &RealtimeLease{Provider: candidate, Conn: conn, release: release}
		return result
	}
	return result
}
