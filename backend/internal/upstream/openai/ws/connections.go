package ws

import (
	"errors"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// OpenAIWSConnections owns an on-demand pool and shared dialer; Close prevents pool creation.
type OpenAIWSConnections struct {
	Options    *WSPoolOptions
	dialer     openai.WSClientDialer
	dialerOnce sync.Once
	pool       *WSConnPool
	poolOnce   sync.Once
	poolMu     sync.Mutex
	// updateMu serializes updates and shutdown; poolMu protects pool creation and access.
	updateMu sync.Mutex
	closed   bool
}

// NewOpenAIWSConnections stores the options and dialer; Pool creates and starts a pool on first use.
func NewOpenAIWSConnections(options *WSPoolOptions, dialer openai.WSClientDialer, pools ...*WSConnPool) *OpenAIWSConnections {
	value := &OpenAIWSConnections{Options: options, dialer: dialer}
	if len(pools) > 0 {
		value.pool = pools[0]
	}
	return value
}

func (s *OpenAIWSConnections) Pool() *WSConnPool {
	if s == nil {
		return nil
	}
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	if s.closed {
		return s.pool
	}
	s.poolOnce.Do(func() {
		if s.pool == nil {
			s.pool = NewWSConnPool(s.Options)
			s.pool.Start()
		}
	})
	return s.pool
}

func (s *OpenAIWSConnections) Dialer() openai.WSClientDialer {
	if s == nil {
		return nil
	}
	s.dialerOnce.Do(func() {
		if s.dialer == nil {
			s.dialer = openai.NewDefaultWSClientDialer()
		}
	})
	return s.dialer
}

func (s *OpenAIWSConnections) InvalidateProvider(providerID int64) {
	if pool := s.Pool(); pool != nil {
		pool.ClearProvider(providerID)
	}
}

// Close stops the existing pool and prevents creation. Later Pool calls return that pool or nil.
func (s *OpenAIWSConnections) Close() {
	if s == nil {
		return
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	s.poolMu.Lock()
	s.closed = true
	pool := s.pool
	s.poolMu.Unlock()
	if pool != nil {
		pool.Close()
	}
}

// UpdateOptions serializes option changes while allowing pool access during connection cleanup.
func (s *OpenAIWSConnections) UpdateOptions(options WSPoolOptions) error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	s.poolMu.Lock()
	if s.closed {
		s.poolMu.Unlock()
		return errors.New("responses websocket connections are stopped")
	}
	s.Options = &options
	pool := s.pool
	s.poolMu.Unlock()
	if pool != nil {
		return pool.UpdateOptions(options)
	}
	return nil
}
