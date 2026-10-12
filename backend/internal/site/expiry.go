package site

import (
	"context"
	"log"
	"sync"
	"time"
)

const announcementExpiryUpdateTimeout = 5 * time.Second

// AnnouncementExpiryService periodically archives active announcements that have reached their end time.
type AnnouncementExpiryService struct {
	lifecycleMu      sync.Mutex
	started          bool
	stopped          bool
	announcementRepo AnnouncementRepository
	interval         time.Duration
	stopCh           chan struct{}
	stopOnce         sync.Once
	wg               sync.WaitGroup
}

// NewAnnouncementExpiryService creates an expiry worker; Start launches it.
func NewAnnouncementExpiryService(announcementRepo AnnouncementRepository, interval time.Duration) *AnnouncementExpiryService {
	return &AnnouncementExpiryService{
		announcementRepo: announcementRepo,
		interval:         interval,
		stopCh:           make(chan struct{}),
	}
}

// Start launches an immediate scan followed by periodic scans. A stopped service cannot restart.
func (s *AnnouncementExpiryService) Start() {
	if s == nil || s.announcementRepo == nil || s.interval <= 0 {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true

	s.wg.Go(func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		s.runOnce()
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.stopCh:
				return
			}
		}
	})
}

// Stop stops the expiry worker and waits for its current scan to finish.
func (s *AnnouncementExpiryService) Stop() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	s.stopped = true

	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.lifecycleMu.Unlock()
	s.wg.Wait()
}

func (s *AnnouncementExpiryService) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), announcementExpiryUpdateTimeout)
	defer cancel()

	updated, err := s.announcementRepo.ArchiveExpired(ctx, time.Now())
	if err != nil {
		log.Printf("[AnnouncementExpiry] archive expired announcements failed: %v", err)
		return
	}
	if updated > 0 {
		log.Printf("[AnnouncementExpiry] archived %d expired announcements", updated)
	}
}
