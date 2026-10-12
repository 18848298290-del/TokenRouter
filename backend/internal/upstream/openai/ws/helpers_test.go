package ws

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// delayedCloseConn blocks the close handshake to test pool access during option updates.
type delayedCloseConn struct {
	openAIWSFakeConn
	entered chan struct{}
	release chan struct{}
}

// wsAcquireTestResult carries a background acquisition's lease or error to the test.
type wsAcquireTestResult struct {
	lease *WSConnLease
	err   error
}

type openAIWSCountingDialer struct {
	mu             sync.Mutex
	dialCount      int
	lastTLSProfile *tlsfingerprint.Profile
}

type openAIWSFakeConn struct {
	mu      sync.Mutex
	closed  bool
	payload [][]byte
}

func (c *delayedCloseConn) Close() error {
	close(c.entered)
	<-c.release
	return nil
}

// ProviderPoolLoad snapshots a provider's leases, waiters, and connections.
func (p *WSConnPool) ProviderPoolLoad(providerID int64) (inflight int, waiters int, conns int) {
	if p == nil || providerID <= 0 {
		return 0, 0, 0
	}
	ap, ok := p.getProviderPool(providerID)
	if !ok || ap == nil {
		return 0, 0, 0
	}
	ap.mu.Lock()
	defer ap.mu.Unlock()
	inflight, waiters = providerPoolLoadLocked(ap)
	return inflight, waiters, len(ap.conns)
}

// newWSReuseTestPool uses a fake dialer and full target utilization to limit spare connections.
func newWSReuseTestPool(capacity, idle int) (*WSConnPool, WSAcquireRequest) {
	pool := NewWSConnPool(&WSPoolOptions{
		MaxConnsPerProvider:   capacity,
		MaxIdlePerProvider:    idle,
		PoolTargetUtilization: 1,
		QueueLimitPerConn:     2,
	})
	pool.SetClientDialerForTest(&openAIWSCountingDialer{})
	return pool, WSAcquireRequest{
		Provider: &WSPoolProvider{ID: 1, Type: "apikey"},
		WSURL:    "wss://example.com/v1/responses",
	}
}

// acquireWSInBackground acquires a lease asynchronously using the supplied context.
func acquireWSInBackground(pool *WSConnPool, ctx context.Context, req WSAcquireRequest) <-chan wsAcquireTestResult {
	result := make(chan wsAcquireTestResult, 1)
	go func() {
		lease, err := pool.Acquire(ctx, req)
		result <- wsAcquireTestResult{lease: lease, err: err}
	}()
	return result
}

func (d *openAIWSCountingDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
	profile *tlsfingerprint.Profile,
) (openai.WSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = headers
	_ = proxyURL
	d.mu.Lock()
	d.dialCount++
	d.lastTLSProfile = profile
	d.mu.Unlock()
	return &openAIWSFakeConn{}, 0, nil, nil
}

func (d *openAIWSCountingDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

func (d *openAIWSCountingDialer) LastTLSProfile() *tlsfingerprint.Profile {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastTLSProfile
}

func (c *openAIWSFakeConn) WriteJSON(ctx context.Context, value any) error {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("closed")
	}
	c.payload = append(c.payload, []byte("ok"))
	_ = value
	return nil
}

func (c *openAIWSFakeConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("closed")
	}
	return []byte(`{"type":"response.completed","response":{"id":"resp_fake"}}`), nil
}

func (c *openAIWSFakeConn) Ping(ctx context.Context) error {
	_ = ctx
	return nil
}

func (c *openAIWSFakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}
