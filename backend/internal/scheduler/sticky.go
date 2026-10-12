package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cespare/xxhash/v2"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// StickyCache stores session-to-provider bindings scoped by group.
type StickyCache interface {
	GetSessionProviderID(context.Context, int64, string) (int64, error)
	SetSessionProviderID(context.Context, int64, string, int64, time.Duration) error
	RefreshSessionTTL(context.Context, int64, string, time.Duration) error
	DeleteSessionProviderID(context.Context, int64, string) error
}

// StickyStats holds process-local sticky observations shared by compatible entry points.
type StickyStats struct {
	readFallbackTotal atomic.Int64
	readFallbackHit   atomic.Int64
	dualWriteTotal    atomic.Int64
}

type StickyOptions struct {
	Prefix          string
	ReadLegacy      bool
	DualWriteLegacy bool
	DefaultTTL      time.Duration
}

// StickySession applies sticky options supplied by the platform entry point.
type StickySession struct {
	cache   StickyCache
	options StickyOptions
	stats   *StickyStats
}

func (s *StickyStats) Snapshot() (int64, int64, int64) {
	return s.readFallbackTotal.Load(), s.readFallbackHit.Load(), s.dualWriteTotal.Load()
}

func NewStickySession(cache StickyCache, options StickyOptions, stats *StickyStats) *StickySession {
	return &StickySession{cache: cache, options: options, stats: stats}
}

func (s *StickySession) SessionKey(hash string) string {
	v := strings.TrimSpace(hash)
	if v == "" {
		return ""
	}
	return s.options.Prefix + v
}

func (s *StickySession) LegacyKey(hash, legacy string) string {
	v := strings.TrimSpace(legacy)
	if v == "" {
		return ""
	}
	key := s.options.Prefix + v
	if key == s.SessionKey(hash) {
		return ""
	}
	return key
}

func (s *StickySession) LegacyTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		ttl = s.options.DefaultTTL
	}
	if ttl > 10*time.Minute {
		return 10 * time.Minute
	}
	return ttl
}

func DeriveSessionHashes(sessionID string) (currentHash string, legacyHash string) {
	normalized := strings.TrimSpace(sessionID)
	if normalized == "" {
		return "", ""
	}

	currentHash = fmt.Sprintf("%016x", xxhash.Sum64String(normalized))
	sum := sha256.Sum256([]byte(normalized))
	legacyHash = hex.EncodeToString(sum[:])
	return currentHash, legacyHash
}

func (s *StickySession) Get(ctx context.Context, groupID int64, sessionHash, legacyHash string) (int64, error) {
	if s == nil || s.cache == nil {
		return 0, nil
	}

	primaryKey := s.SessionKey(sessionHash)
	if primaryKey == "" {
		return 0, nil
	}

	providerID, err := s.cache.GetSessionProviderID(ctx, groupID, primaryKey)
	if err == nil && providerID > 0 {
		return providerID, nil
	}
	if !s.options.ReadLegacy {
		return providerID, err
	}

	legacyKey := s.LegacyKey(sessionHash, legacyHash)
	if legacyKey == "" {
		return providerID, err
	}

	s.stats.readFallbackTotal.Add(1)
	legacyProviderID, legacyErr := s.cache.GetSessionProviderID(ctx, groupID, legacyKey)
	if legacyErr == nil && legacyProviderID > 0 {
		s.stats.readFallbackHit.Add(1)
		return legacyProviderID, nil
	}
	return providerID, err
}

func (s *StickySession) Set(ctx context.Context, groupID int64, sessionHash, legacyHash string, providerID int64, ttl time.Duration) error {
	if s == nil || s.cache == nil || providerID <= 0 {
		return nil
	}
	primaryKey := s.SessionKey(sessionHash)
	if primaryKey == "" {
		return nil
	}

	if err := s.cache.SetSessionProviderID(ctx, groupID, primaryKey, providerID, ttl); err != nil {
		return err
	}

	if !s.options.DualWriteLegacy {
		return nil
	}
	legacyKey := s.LegacyKey(sessionHash, legacyHash)
	if legacyKey == "" {
		return nil
	}
	if err := s.cache.SetSessionProviderID(ctx, groupID, legacyKey, providerID, s.LegacyTTL(ttl)); err != nil {
		return err
	}
	s.stats.dualWriteTotal.Add(1)
	return nil
}

func (s *StickySession) Refresh(ctx context.Context, groupID int64, sessionHash, legacyHash string, ttl time.Duration) error {
	if s == nil || s.cache == nil {
		return nil
	}
	primaryKey := s.SessionKey(sessionHash)
	if primaryKey == "" {
		return nil
	}

	err := s.cache.RefreshSessionTTL(ctx, groupID, primaryKey, ttl)
	if !s.options.ReadLegacy && !s.options.DualWriteLegacy {
		return err
	}

	legacyKey := s.LegacyKey(sessionHash, legacyHash)
	if legacyKey != "" {
		_ = s.cache.RefreshSessionTTL(ctx, groupID, legacyKey, s.LegacyTTL(ttl))
	}
	return err
}

func (s *StickySession) Delete(ctx context.Context, groupID int64, sessionHash, legacyHash string) error {
	if s == nil || s.cache == nil {
		return nil
	}
	primaryKey := s.SessionKey(sessionHash)
	if primaryKey == "" {
		return nil
	}

	err := s.cache.DeleteSessionProviderID(ctx, groupID, primaryKey)
	if !s.options.ReadLegacy && !s.options.DualWriteLegacy {
		return err
	}

	legacyKey := s.LegacyKey(sessionHash, legacyHash)
	if legacyKey != "" {
		_ = s.cache.DeleteSessionProviderID(ctx, groupID, legacyKey)
	}
	return err
}

// ShouldEscapeSticky checks shared feedback against request policy, testing TTFT before error rate.
func ShouldEscapeSticky(stats *RuntimeStats, providerID int64, cfg policy.StickyEscapeConfig) (reason string, errorRate float64, ttft float64, shouldEscape bool) {
	if !cfg.Enabled || stats == nil || providerID <= 0 {
		return "", 0, 0, false
	}
	errorRate, ttft, hasTTFT := stats.Snapshot(providerID)
	if hasTTFT && ttft > cfg.TtftMs {
		return "ttft", errorRate, ttft, true
	}
	if errorRate > cfg.ErrorRate {
		return "error_rate", errorRate, ttft, true
	}
	return "", errorRate, ttft, false
}
