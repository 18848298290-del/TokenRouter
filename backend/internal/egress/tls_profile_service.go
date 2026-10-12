package egress

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// TLSFingerprintProfileRepository persists TLS fingerprint profiles.
type TLSFingerprintProfileRepository interface {
	List(ctx context.Context) ([]*TLSFingerprintProfile, error)
	GetByID(ctx context.Context, id int64) (*TLSFingerprintProfile, error)
	Create(ctx context.Context, profile *TLSFingerprintProfile) (*TLSFingerprintProfile, error)
	Update(ctx context.Context, profile *TLSFingerprintProfile) (*TLSFingerprintProfile, error)
	Delete(ctx context.Context, id int64) error
}

// TLSFingerprintProfileCache caches profiles and distributes refresh notifications.
type TLSFingerprintProfileCache interface {
	Get(ctx context.Context) ([]*TLSFingerprintProfile, bool)
	Set(ctx context.Context, profiles []*TLSFingerprintProfile) error
	Invalidate(ctx context.Context) error
	NotifyUpdate(ctx context.Context) error
	SubscribeUpdates(ctx context.Context, handler func())
}

// TLSFingerprintProfileService manages TLS profiles and cached runtime selection.
type TLSFingerprintProfileService struct {
	diagnostics Diagnostics
	repo        TLSFingerprintProfileRepository
	cache       TLSFingerprintProfileCache

	// Local ID-to-profile lookup for outbound request selection.
	localCache map[int64]*TLSFingerprintProfile
	localMu    sync.RWMutex
	startOnce  sync.Once
}

// NewTLSFingerprintProfileService creates an idle TLS profile service.
func NewTLSFingerprintProfileService(
	repo TLSFingerprintProfileRepository,
	cache TLSFingerprintProfileCache,
	diagnostics ...Diagnostics,
) *TLSFingerprintProfileService {
	svc := &TLSFingerprintProfileService{
		diagnostics: diagnosticsOption(diagnostics),
		repo:        repo,
		cache:       cache,
		localCache:  make(map[int64]*TLSFingerprintProfile),
	}

	return svc
}

// Stop stops the cache subscriber when supported; call it before closing Redis.
func (s *TLSFingerprintProfileService) Stop() {
	if s == nil || s.cache == nil {
		return
	}
	if stopper, ok := s.cache.(interface{ StopSubscription() }); ok {
		stopper.StopSubscription()
	}
}

// List returns all stored TLS profiles.
func (s *TLSFingerprintProfileService) List(ctx context.Context) ([]*TLSFingerprintProfile, error) {
	return s.repo.List(ctx)
}

// GetByID retrieves a TLS profile from the repository.
func (s *TLSFingerprintProfileService) GetByID(ctx context.Context, id int64) (*TLSFingerprintProfile, error) {
	return s.repo.GetByID(ctx, id)
}

// Create validates and persists a profile, then refreshes caches.
func (s *TLSFingerprintProfileService) Create(ctx context.Context, profile *TLSFingerprintProfile) (*TLSFingerprintProfile, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}

	created, err := s.repo.Create(ctx, profile)
	if err != nil {
		return nil, err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return created, nil
}

// Update validates and replaces a profile, then refreshes caches.
func (s *TLSFingerprintProfileService) Update(ctx context.Context, profile *TLSFingerprintProfile) (*TLSFingerprintProfile, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}

	updated, err := s.repo.Update(ctx, profile)
	if err != nil {
		return nil, err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return updated, nil
}

// Delete deletes a profile and refreshes caches.
func (s *TLSFingerprintProfileService) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return nil
}

// GetProfileByID returns an independent copy from the local cache.
// A nil result indicates a missing profile; callers choose the fallback.
func (s *TLSFingerprintProfileService) GetProfileByID(id int64) *TLSFingerprintProfile {
	if s == nil {
		return nil
	}
	s.localMu.RLock()
	p, ok := s.localCache[id]
	s.localMu.RUnlock()

	if ok && p != nil {
		return CloneTLSFingerprintProfile(p)
	}
	return nil
}

// getRandomProfile returns an independent copy of a randomly selected cached profile.
func (s *TLSFingerprintProfileService) getRandomProfile() *TLSFingerprintProfile {
	if s == nil {
		return nil
	}
	s.localMu.RLock()
	defer s.localMu.RUnlock()

	if len(s.localCache) == 0 {
		return nil
	}

	// Exclude nil entries from random selection.
	profiles := make([]*TLSFingerprintProfile, 0, len(s.localCache))
	for _, p := range s.localCache {
		if p != nil {
			profiles = append(profiles, p)
		}
	}
	if len(profiles) == 0 {
		return nil
	}

	return CloneTLSFingerprintProfile(profiles[rand.IntN(len(profiles))])
}

// ResolveTLSProfileByID selects a cached profile, or the built-in default when unavailable.
// The caller supplies enabled after checking provider eligibility and its TLS switch.
func (s *TLSFingerprintProfileService) ResolveTLSProfileByID(enabled bool, id int64) *TLSFingerprintProfile {
	if !enabled {
		return nil
	}
	if id > 0 {
		if p := s.GetProfileByID(id); p != nil {
			return CloneTLSFingerprintProfile(p)
		}
	}
	if id == -1 {
		// The -1 sentinel requests a random cached profile.
		if p := s.getRandomProfile(); p != nil {
			return CloneTLSFingerprintProfile(p)
		}
	}
	// Empty TLS settings let the dialer supply its built-in defaults.
	return &TLSFingerprintProfile{Name: "Built-in Default (Node.js 24.x)"}
}

// ResolveRoutableTLSProfileByID resolves a router rule's profile selection.
// A missing positive ID returns false so the caller can try the provider's bound profile.
func (s *TLSFingerprintProfileService) ResolveRoutableTLSProfileByID(enabled bool, id int64) (*TLSFingerprintProfile, bool) {
	if !enabled {
		return nil, false
	}
	if id > 0 {
		if p := s.GetProfileByID(id); p != nil {
			return p, true
		}
		return nil, false
	}
	if id == -1 {
		if p := s.getRandomProfile(); p != nil {
			return p, true
		}
		return nil, false
	}
	// Zero selects the built-in fingerprint and counts as a successful route.
	return &TLSFingerprintProfile{Name: "Built-in Default (Node.js 24.x)"}, true
}

// ResolveTokenTLSProfileByID resolves TLS settings for ChatGPT OAuth token requests.
// Token requests can precede provider creation, so this path does not require a provider TLS switch.
func (s *TLSFingerprintProfileService) ResolveTokenTLSProfileByID(id int64) (*TLSFingerprintProfile, bool) {
	if s == nil {
		return nil, false
	}
	if id > 0 {
		if p := s.GetProfileByID(id); p != nil {
			return p, true
		}
		return nil, false
	}
	if id == -1 {
		if p := s.getRandomProfile(); p != nil {
			return p, true
		}
		return nil, false
	}
	// Zero selects the built-in fingerprint.
	return &TLSFingerprintProfile{Name: "Built-in Default (Node.js 24.x)"}, true
}

func (s *TLSFingerprintProfileService) refreshLocalCache(ctx context.Context) error {
	if s.cache != nil {
		if profiles, ok := s.cache.Get(ctx); ok {
			s.setLocalCache(profiles)
			return nil
		}
	}
	return s.reloadFromDB(ctx)
}

func (s *TLSFingerprintProfileService) reloadFromDB(ctx context.Context) error {
	profiles, err := s.repo.List(ctx)
	if err != nil {
		return err
	}

	if s.cache != nil {
		if err := s.cache.Set(ctx, profiles); err != nil {
			s.diagnostics.Log("service.tls_fp_profile", "[TLSFPProfileService] Failed to set cache: %v", err)
		}
	}

	s.setLocalCache(profiles)
	return nil
}

func (s *TLSFingerprintProfileService) setLocalCache(profiles []*TLSFingerprintProfile) {
	m := make(map[int64]*TLSFingerprintProfile, len(profiles))
	for _, p := range profiles {
		if p != nil {
			m[p.ID] = CloneTLSFingerprintProfile(p)
		}
	}

	s.localMu.Lock()
	s.localCache = m
	s.localMu.Unlock()
}

func (s *TLSFingerprintProfileService) newCacheRefreshContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}

func (s *TLSFingerprintProfileService) invalidateAndNotify(ctx context.Context) {
	if s.cache != nil {
		if err := s.cache.Invalidate(ctx); err != nil {
			s.diagnostics.Log("service.tls_fp_profile", "[TLSFPProfileService] Failed to invalidate cache: %v", err)
		}
	}

	if err := s.reloadFromDB(ctx); err != nil {
		s.diagnostics.Log("service.tls_fp_profile", "[TLSFPProfileService] Failed to refresh local cache: %v", err)
		s.localMu.Lock()
		s.localCache = make(map[int64]*TLSFingerprintProfile)
		s.localMu.Unlock()
	}

	if s.cache != nil {
		if err := s.cache.NotifyUpdate(ctx); err != nil {
			s.diagnostics.Log("service.tls_fp_profile", "[TLSFPProfileService] Failed to notify cache update: %v", err)
		}
	}
}

// Start warms the cache and installs its update subscription once.
func (s *TLSFingerprintProfileService) Start() {
	s.startOnce.Do(func() {
		ctx := context.Background()
		if err := s.reloadFromDB(ctx); err != nil {
			s.diagnostics.Log("service.tls_fp_profile", "[TLSFPProfileService] Failed to load profiles from DB on startup: %v", err)
			if fallbackErr := s.refreshLocalCache(ctx); fallbackErr != nil {
				s.diagnostics.Log("service.tls_fp_profile", "[TLSFPProfileService] Failed to load profiles from cache fallback on startup: %v", fallbackErr)
			}
		}

		if s.cache != nil {
			s.cache.SubscribeUpdates(ctx, func() {
				if err := s.refreshLocalCache(context.Background()); err != nil {
					s.diagnostics.Log("service.tls_fp_profile", "[TLSFPProfileService] Failed to refresh cache on notification: %v", err)
				}
			})
		}
	})
}
