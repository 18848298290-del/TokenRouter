package ws

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// delayedCloseConn blocks its handshake to expose pool access during socket
// cleanup.
type delayedCloseConn struct {
	openAIWSFakeConn               // embedded fake transport fixture
	entered          chan struct{} // close handshake arrival channel
	release          chan struct{} // close handshake release channel
}

// wsAcquireTestResult carries the lease or failure from one background
// acquisition.
type wsAcquireTestResult struct {
	lease *WSConnLease // background acquisition lease value
	err   error        // background acquisition failure value
}

// openAIWSCountingDialer records synchronized dial count and the latest TLS
// profile.
type openAIWSCountingDialer struct {
	// mu protects dialCount and lastTLSProfile for concurrent pool dial paths
	// and test snapshots; the profile pointer remains shared with the caller fixture.
	mu             sync.Mutex              // fixture mutable state mutex
	dialCount      int                     // observed fake dial count
	lastTLSProfile *tlsfingerprint.Profile // latest supplied TLS profile
}

// openAIWSFakeConn records fake writes and closed-state checks under one mutex.
type openAIWSFakeConn struct {
	// mu protects closed and payload so concurrent fixture operations observe
	// one connection state; writes append markers while reads check the same closure flag.
	mu      sync.Mutex // fixture mutable state mutex
	closed  bool       // closed fake connection flag
	payload [][]byte   // recorded fake write markers
}

// Close signals entry and blocks until the fixture release channel closes. Call
// this fixture close once; tests own release so a pool update can finish after
// concurrent lookup assertions.
func (c *delayedCloseConn) Close() error {
	close(c.entered)
	<-c.release
	return nil
}

// ProviderPoolLoad snapshots lease, waiter, and connection counts under the
// provider mutex. Nil pools, nonpositive IDs, and absent provider pools return
// zero counts, allowing cleanup tests to inspect missing state safely.
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

// newWSReuseTestPool creates a fake-dialer pool and API-key acquisition target
// for reuse assertions. Target utilization one suppresses utilization-based
// spare connections; callers close the returned pool and release acquired
// leases.
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

// acquireWSInBackground starts one asynchronous Acquire and returns its
// buffered result channel. The caller owns context cancellation, reads the
// result, and releases a successful lease so test shutdown can finish.
func acquireWSInBackground(pool *WSConnPool, ctx context.Context, req WSAcquireRequest) <-chan wsAcquireTestResult {
	result := make(chan wsAcquireTestResult, 1)
	// Buffer the acquisition outcome so completion cannot block waiting for
	// the test reader, which may first inspect queue counts or cancellation effects.
	go func() {
		lease, err := pool.Acquire(ctx, req)
		result <- wsAcquireTestResult{lease: lease, err: err}
	}()
	return result
}

// Dial increments the fixture dial count and stores profile under its mutex. It
// ignores transport/context arguments and returns an open fake connection with
// zero handshake status and no headers for pool tests.
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

// DialCount returns the recorded dial count under the fixture mutex.
func (d *openAIWSCountingDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

// LastTLSProfile returns the recorded profile pointer under the fixture mutex.
func (d *openAIWSCountingDialer) LastTLSProfile() *tlsfingerprint.Profile {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastTLSProfile
}

// WriteJSON records one fake payload marker while the connection remains open.
// It ignores ctx and value content; closed fixtures return an error and callers
// use the marker count for pool observations.
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

// ReadMessage returns a fixed completed-response fixture while the connection
// is open. It ignores ctx and returns an error after Close, keeping transport
// timing under the surrounding test fixture.
func (c *openAIWSFakeConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("closed")
	}
	return []byte(`{"type":"response.completed","response":{"id":"resp_fake"}}`), nil
}

// Ping reports successful fake liveness while ignoring the supplied context. It
// performs no transport read or write, allowing tests to use the same fixture
// for pool idle-ping checks.
func (c *openAIWSFakeConn) Ping(ctx context.Context) error {
	_ = ctx
	return nil
}

// Close marks the fake connection closed under its state mutex. Repeated calls
// retain that state, and later fake reads or writes return the closed error
// without performing transport work.
func (c *openAIWSFakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}
