package ws

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

const (
	openAIWSConnHealthCheckIdle = 90 * time.Second
	// coder/websocket consumes pong frames through its reader. Recycle idle connections
	// that cannot ping without a reader before the upstream keepalive window expires.
	openAIWSConnIdleRecycleAfter   = 90 * time.Second
	openAIWSConnHealthCheckTO      = 2 * time.Second
	openAIWSConnPrewarmExtraDelay  = 2 * time.Second
	openAIWSAcquireCleanupInterval = 3 * time.Second
	openAIWSBackgroundPingInterval = 30 * time.Second
	openAIWSBackgroundSweepTicker  = 30 * time.Second

	openAIWSPrewarmFailureWindow   = 30 * time.Second
	openAIWSPrewarmFailureSuppress = 2

	codexFingerprintOff          = "off"
	codexFingerprintDevice       = "device"
	openAICodexRoutingHintHeader = "x-codex-routing-hint"

	WSConnHealthCheckTimeout = openAIWSConnHealthCheckTO
)

var errOpenAIWSConnClosed = openai.ErrWSConnClosed

type WSAcquireRequest struct {
	Provider *WSPoolProvider
	WSURL    string
	Headers  http.Header
	// HeadersFactory creates authentication headers and an Agent Assertion before each dial.
	HeadersFactory  func(context.Context, http.Header) (http.Header, error)
	ProxyURL        string
	TLSProfile      *tlsfingerprint.Profile
	TLSProfileKey   string
	PreferredConnID string
	// ForceNewConn avoids sharing connection-local continuation state with an earlier request.
	ForceNewConn bool
	// ForcePreferredConn requires the connection identified by PreferredConnID.
	ForcePreferredConn bool
}
type openAIWSHandshakeCompatibilityKey struct {
	betaFeatures        string
	codexInstallationID string
	sessionIDHyphen     string
	sessionIDUnderscore string
	threadID            string
	clientRequestID     string
	codexWindowID       string
}
type WSConnLease struct {
	pool       *WSConnPool
	ProviderID int64
	Conn       *WSConn
	queueWait  time.Duration
	connPick   time.Duration
	reused     bool
	released   atomic.Bool
}

// WSConn holds an upstream connection and its exclusive lease state.
type WSConn struct {
	id string
	ws openai.WSClientConn

	handshakeHeaders http.Header
	compatibility    wsConnCompatibility
	routingAffinity  string

	leaseCh   chan struct{}
	closedCh  chan struct{}
	closeOnce sync.Once

	readMu  sync.Mutex
	writeMu sync.Mutex

	waiters       atomic.Int32
	createdAtNano atomic.Int64
	lastUsedNano  atomic.Int64
	prewarmed     atomic.Bool
}

// openAIWSProviderPool tracks a provider's connections, queue slots, and prewarming.
type openAIWSProviderPool struct {
	mu            sync.Mutex
	conns         map[string]*WSConn
	pinnedConns   map[string]int
	changedCh     chan struct{}
	waiters       map[wsConnCompatibility]int
	creating      int
	generation    uint64
	lastCleanupAt time.Time
	lastAcquire   *WSAcquireRequest
	prewarmActive bool
	prewarmUntil  time.Time
	prewarmFails  int
	prewarmFailAt time.Time
}

type WSPoolMetricsSnapshot struct {
	AcquireTotal            int64
	AcquireReuseTotal       int64
	AcquireCreateTotal      int64
	AcquireQueueWaitTotal   int64
	AcquireQueueWaitMsTotal int64
	ConnPickTotal           int64
	ConnPickMsTotal         int64
	ScaleUpTotal            int64
	ScaleDownTotal          int64
}
type openAIWSPoolMetrics struct {
	acquireTotal          atomic.Int64
	acquireReuseTotal     atomic.Int64
	acquireCreateTotal    atomic.Int64
	acquireQueueWaitTotal atomic.Int64
	acquireQueueWaitMs    atomic.Int64
	connPickTotal         atomic.Int64
	connPickMs            atomic.Int64
	scaleUpTotal          atomic.Int64
	scaleDownTotal        atomic.Int64
}
type WSConnPool struct {
	started       bool
	runtimeMu     sync.Mutex
	closed        bool
	acquireWG     sync.WaitGroup
	prewarmWG     sync.WaitGroup
	prewarmCtx    context.Context
	prewarmCancel context.CancelFunc
	cfg           atomic.Pointer[WSPoolOptions]
	// The default dialer uses coder/websocket; tests can supply another implementation.
	clientDialer openai.WSClientDialer

	providers sync.Map // key: int64(providerID), value: *openAIWSProviderPool
	seq       atomic.Uint64

	metrics openAIWSPoolMetrics

	workerStopCh  chan struct{}
	cleanupWakeCh chan struct{}
	workerWg      sync.WaitGroup
	closeOnce     sync.Once
}

type openAIWSIdlePingCandidate struct {
	providerID int64
	conn       *WSConn
}

type codexFingerprintMode = string

// WSPoolProviderState counts a provider's connections, active leases, and pinned connections.
type WSPoolProviderState struct{ Connections, LeasedConnections, PinnedConnections int }

// wsConnCompatibility captures the destination, proxy, TLS profile, and handshake identity.
type wsConnCompatibility struct {
	wsURL         string
	proxyURL      string
	tlsProfileKey string
	handshake     openAIWSHandshakeCompatibilityKey
}

func (l *WSConnLease) activeConn() (*WSConn, error) {
	if l == nil || l.Conn == nil {
		return nil, errOpenAIWSConnClosed
	}
	if l.released.Load() {
		return nil, errOpenAIWSConnClosed
	}
	return l.Conn, nil
}

func (l *WSConnLease) ConnID() string {
	if l == nil || l.Conn == nil {
		return ""
	}
	return l.Conn.id
}

func (l *WSConnLease) QueueWaitDuration() time.Duration {
	if l == nil {
		return 0
	}
	return l.queueWait
}

func (l *WSConnLease) ConnPickDuration() time.Duration {
	if l == nil {
		return 0
	}
	return l.connPick
}

func (l *WSConnLease) Reused() bool {
	if l == nil {
		return false
	}
	return l.reused
}

func (l *WSConnLease) HandshakeHeader(name string) string {
	if l == nil || l.Conn == nil {
		return ""
	}
	return l.Conn.handshakeHeader(name)
}

func (l *WSConnLease) HandshakeHeaders() http.Header {
	if l == nil || l.Conn == nil {
		return nil
	}
	return cloneHeader(l.Conn.handshakeHeaders)
}

func (l *WSConnLease) IsPrewarmed() bool {
	if l == nil || l.Conn == nil {
		return false
	}
	return l.Conn.isPrewarmed()
}

func (l *WSConnLease) MarkPrewarmed() {
	if l == nil || l.Conn == nil {
		return
	}
	l.Conn.markPrewarmed()
}

func (l *WSConnLease) WriteJSON(value any, timeout time.Duration) error {
	conn, err := l.activeConn()
	if err != nil {
		return err
	}
	return conn.writeJSONWithTimeout(context.Background(), value, timeout)
}

func (l *WSConnLease) WriteJSONWithContextTimeout(ctx context.Context, value any, timeout time.Duration) error {
	conn, err := l.activeConn()
	if err != nil {
		return err
	}
	return conn.writeJSONWithTimeout(ctx, value, timeout)
}

func (l *WSConnLease) WriteJSONContext(ctx context.Context, value any) error {
	conn, err := l.activeConn()
	if err != nil {
		return err
	}
	return conn.writeJSON(value, ctx)
}

func (l *WSConnLease) ReadMessage(timeout time.Duration) ([]byte, error) {
	conn, err := l.activeConn()
	if err != nil {
		return nil, err
	}
	return conn.readMessageWithTimeout(timeout)
}

func (l *WSConnLease) ReadMessageContext(ctx context.Context) ([]byte, error) {
	conn, err := l.activeConn()
	if err != nil {
		return nil, err
	}
	return conn.readMessage(ctx)
}

func (l *WSConnLease) ReadMessageWithContextTimeout(ctx context.Context, timeout time.Duration) ([]byte, error) {
	conn, err := l.activeConn()
	if err != nil {
		return nil, err
	}
	return conn.readMessageWithContextTimeout(ctx, timeout)
}

func (l *WSConnLease) PingWithTimeout(timeout time.Duration) error {
	conn, err := l.activeConn()
	if err != nil {
		return err
	}
	return conn.pingWithTimeout(timeout)
}

func (l *WSConnLease) SupportsIdlePingWithoutReader() bool {
	conn, err := l.activeConn()
	if err != nil {
		return false
	}
	return conn.supportsIdlePingWithoutReader()
}

func (l *WSConnLease) MarkBroken() {
	if l == nil || l.pool == nil || l.Conn == nil || l.released.Load() {
		return
	}
	l.pool.evictConn(l.ProviderID, l.Conn.id)
}

func (l *WSConnLease) Release() {
	if l == nil || l.Conn == nil {
		return
	}
	if !l.released.CompareAndSwap(false, true) {
		return
	}
	l.Conn.release()
	if l.pool != nil {
		l.pool.runtimeMu.Lock()
		closed := l.pool.closed
		l.pool.runtimeMu.Unlock()
		if closed {
			l.pool.evictConn(l.ProviderID, l.Conn.id)
			return
		}
		l.pool.reconcileProvider(l.ProviderID)
		l.pool.notifyProviderPoolChanged(l.ProviderID)
	}
}

// NewWSConn creates an available connection; the pool fills in its outbound configuration after dialing.
func NewWSConn(id string, _ int64, ws openai.WSClientConn, handshakeHeaders http.Header, profile *tlsfingerprint.Profile, profileKey string) *WSConn {
	now := time.Now()
	conn := &WSConn{
		id: id,

		ws: ws,

		handshakeHeaders: cloneHeader(handshakeHeaders),

		compatibility: wsConnCompatibility{tlsProfileKey: openAIWSTLSProfileKey(profile, profileKey)},

		leaseCh: make(chan struct{}, 1),

		closedCh: make(chan struct{}),
	}
	conn.leaseCh <- struct{}{}
	conn.createdAtNano.Store(now.UnixNano())
	conn.lastUsedNano.Store(now.UnixNano())
	return conn
}

func (c *WSConn) tryAcquire() bool {
	if c == nil {
		return false
	}
	select {
	case <-c.closedCh:
		return false
	default:
	}
	select {
	case <-c.leaseCh:
		select {
		case <-c.closedCh:
			c.release()
			return false
		default:
		}
		return true
	default:
		return false
	}
}

func (c *WSConn) release() {
	if c == nil {
		return
	}
	select {
	case c.leaseCh <- struct{}{}:
	default:
	}
	c.touch()
}

func (c *WSConn) close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		close(c.closedCh)
		if c.ws != nil {
			_ = c.ws.Close()
		}
		select {
		case c.leaseCh <- struct{}{}:
		default:
		}
	})
}

func (c *WSConn) writeJSONWithTimeout(parent context.Context, value any, timeout time.Duration) error {
	if c == nil {
		return errOpenAIWSConnClosed
	}
	select {
	case <-c.closedCh:
		return errOpenAIWSConnClosed
	default:
	}

	writeCtx := parent
	if writeCtx == nil {
		writeCtx = context.Background()
	}
	if timeout <= 0 {
		return c.writeJSON(value, writeCtx)
	}
	var cancel context.CancelFunc
	writeCtx, cancel = context.WithTimeout(writeCtx, timeout)
	defer cancel()
	return c.writeJSON(value, writeCtx)
}

func (c *WSConn) writeJSON(value any, writeCtx context.Context) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.ws == nil {
		return errOpenAIWSConnClosed
	}
	if writeCtx == nil {
		writeCtx = context.Background()
	}
	if err := c.ws.WriteJSON(writeCtx, value); err != nil {
		return err
	}
	c.touch()
	return nil
}

func (c *WSConn) readMessageWithTimeout(timeout time.Duration) ([]byte, error) {
	return c.readMessageWithContextTimeout(context.Background(), timeout)
}

func (c *WSConn) readMessageWithContextTimeout(parent context.Context, timeout time.Duration) ([]byte, error) {
	if c == nil {
		return nil, errOpenAIWSConnClosed
	}
	select {
	case <-c.closedCh:
		return nil, errOpenAIWSConnClosed
	default:
	}

	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		return c.readMessage(parent)
	}
	readCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return c.readMessage(readCtx)
}

func (c *WSConn) readMessage(readCtx context.Context) ([]byte, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if c.ws == nil {
		return nil, errOpenAIWSConnClosed
	}
	if readCtx == nil {
		readCtx = context.Background()
	}
	payload, err := c.ws.ReadMessage(readCtx)
	if err != nil {
		return nil, err
	}
	c.touch()
	return payload, nil
}

func (c *WSConn) pingWithTimeout(timeout time.Duration) error {
	if c == nil {
		return errOpenAIWSConnClosed
	}
	select {
	case <-c.closedCh:
		return errOpenAIWSConnClosed
	default:
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.ws == nil {
		return errOpenAIWSConnClosed
	}
	if timeout <= 0 {
		timeout = openAIWSConnHealthCheckTO
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return c.ws.Ping(pingCtx)
}

func (c *WSConn) supportsIdlePingWithoutReader() bool {
	if c == nil || c.ws == nil {
		return false
	}
	capable, ok := c.ws.(openai.WSIdlePingCapable)
	// Implementations without WSIdlePingCapable are assumed to support idle pings without a reader.
	return !ok || capable.SupportsIdlePingWithoutReader()
}

func (c *WSConn) touch() {
	if c == nil {
		return
	}
	c.lastUsedNano.Store(time.Now().UnixNano())
}

func (c *WSConn) createdAt() time.Time {
	if c == nil {
		return time.Time{}
	}
	nano := c.createdAtNano.Load()
	if nano <= 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}

func (c *WSConn) lastUsedAt() time.Time {
	if c == nil {
		return time.Time{}
	}
	nano := c.lastUsedNano.Load()
	if nano <= 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}

func (c *WSConn) idleDuration(now time.Time) time.Duration {
	if c == nil {
		return 0
	}
	last := c.lastUsedAt()
	if last.IsZero() {
		return 0
	}
	return now.Sub(last)
}

func (c *WSConn) age(now time.Time) time.Duration {
	if c == nil {
		return 0
	}
	created := c.createdAt()
	if created.IsZero() {
		return 0
	}
	return now.Sub(created)
}

func (c *WSConn) isLeased() bool {
	if c == nil {
		return false
	}
	return len(c.leaseCh) == 0
}

func (c *WSConn) handshakeHeader(name string) string {
	if c == nil || c.handshakeHeaders == nil {
		return ""
	}
	return strings.TrimSpace(c.handshakeHeaders.Get(strings.TrimSpace(name)))
}

func (c *WSConn) matchesRoutingAffinity(routingAffinity string) bool {
	return c != nil && c.routingAffinity == routingAffinity
}

func (c *WSConn) isPrewarmed() bool {
	if c == nil {
		return false
	}
	return c.prewarmed.Load()
}

func (c *WSConn) markPrewarmed() {
	if c == nil {
		return
	}
	c.prewarmed.Store(true)
}

func openAIWSTLSProfileKey(profile *tlsfingerprint.Profile, profileKey string) string {
	if key := stringsTrim(profileKey); key != "" {
		return key
	}
	return tlsfingerprint.CacheKey(profile)
}

// changeChannelLocked returns a channel closed on the next pool change. The caller holds ap.mu.
func (ap *openAIWSProviderPool) changeChannelLocked() chan struct{} {
	if ap.changedCh == nil {
		ap.changedCh = make(chan struct{})
	}
	return ap.changedCh
}

// signalChangedLocked wakes pool-change waiters. The caller holds ap.mu.
func (ap *openAIWSProviderPool) signalChangedLocked() {
	if ap == nil {
		return
	}
	if ap.changedCh != nil {
		close(ap.changedCh)
	}
	ap.changedCh = make(chan struct{})
}

func NewWSConnPool(cfg *WSPoolOptions) *WSConnPool {
	pool := &WSConnPool{
		clientDialer:  openai.NewDefaultWSClientDialer(),
		workerStopCh:  make(chan struct{}),
		cleanupWakeCh: make(chan struct{}, 1),
	}
	if cfg != nil {
		copy := *cfg
		pool.cfg.Store(&copy)
	}
	pool.prewarmCtx, pool.prewarmCancel = context.WithCancel(context.Background())
	return pool
}

func (p *WSConnPool) SnapshotMetrics() WSPoolMetricsSnapshot {
	if p == nil {
		return WSPoolMetricsSnapshot{}
	}
	return WSPoolMetricsSnapshot{
		AcquireTotal: p.metrics.acquireTotal.Load(),

		AcquireReuseTotal: p.metrics.acquireReuseTotal.Load(),

		AcquireCreateTotal: p.metrics.acquireCreateTotal.Load(),

		AcquireQueueWaitTotal: p.metrics.acquireQueueWaitTotal.Load(),

		AcquireQueueWaitMsTotal: p.metrics.acquireQueueWaitMs.Load(),

		ConnPickTotal: p.metrics.connPickTotal.Load(),

		ConnPickMsTotal: p.metrics.connPickMs.Load(),

		ScaleUpTotal: p.metrics.scaleUpTotal.Load(),

		ScaleDownTotal: p.metrics.scaleDownTotal.Load(),
	}
}

func (p *WSConnPool) SnapshotTransportMetrics() openai.WSTransportMetricsSnapshot {
	if p == nil {
		return openai.WSTransportMetricsSnapshot{}
	}
	if dialer, ok := p.clientDialer.(openai.WSTransportMetricsDialer); ok {
		return dialer.SnapshotTransportMetrics()
	}
	return openai.WSTransportMetricsSnapshot{}
}

func (p *WSConnPool) SetClientDialerForTest(dialer openai.WSClientDialer) {
	if p == nil || dialer == nil {
		return
	}
	p.clientDialer = dialer
}

// Close stops background workers and acquisitions and closes idle connections. Active leases close on release.
func (p *WSConnPool) Close() {
	if p == nil {
		return
	}

	p.closeOnce.Do(func() {
		p.runtimeMu.Lock()
		p.closed = true
		if p.prewarmCancel != nil {
			p.prewarmCancel()
		}
		if p.workerStopCh != nil {
			close(p.workerStopCh)
		}
		p.runtimeMu.Unlock()
		closeConnections := func() {
			var idle []*WSConn
			p.providers.Range(func(_, value any) bool {
				ap, ok := value.(*openAIWSProviderPool)
				if !ok || ap == nil {
					return true
				}
				ap.mu.Lock()
				for id, conn := range ap.conns {
					if conn != nil && !conn.isLeased() {
						delete(ap.conns, id)
						delete(ap.pinnedConns, id)
						idle = append(idle, conn)
					}
				}
				ap.signalChangedLocked()
				ap.mu.Unlock()
				return true
			})
			closeOpenAIWSConns(idle)
		}
		// Active leases close on release; sweep again after workers and dials finish to collect late connections.
		closeConnections()
		p.workerWg.Wait()
		p.acquireWG.Wait()
		p.prewarmWG.Wait()
		closeConnections()
	})
}

func (p *WSConnPool) startBackgroundWorkers() {
	if p == nil || p.workerStopCh == nil {
		return
	}
	p.workerWg.Add(2)
	go func() {
		defer p.workerWg.Done()
		p.runBackgroundPingWorker()
	}()
	go func() {
		defer p.workerWg.Done()
		p.runBackgroundCleanupWorker()
	}()
}

func (p *WSConnPool) runBackgroundPingWorker() {
	if p == nil {
		return
	}
	ticker := time.NewTicker(openAIWSBackgroundPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.runBackgroundPingSweep()
		case <-p.workerStopCh:
			return
		}
	}
}

func (p *WSConnPool) runBackgroundPingSweep() {
	if p == nil {
		return
	}
	candidates := p.snapshotIdleConnsForPing()
	var g errgroup.Group
	g.SetLimit(10)
	for _, item := range candidates {
		if item.conn == nil || item.conn.isLeased() || item.conn.waiters.Load() > 0 || !item.conn.supportsIdlePingWithoutReader() {
			continue
		}
		g.Go(func() error {
			if err := item.conn.pingWithTimeout(openAIWSConnHealthCheckTO); err != nil {
				p.evictConn(item.providerID, item.conn.id)
			}
			return nil
		})
	}
	_ = g.Wait()
}

func (p *WSConnPool) snapshotIdleConnsForPing() []openAIWSIdlePingCandidate {
	if p == nil {
		return nil
	}
	candidates := make([]openAIWSIdlePingCandidate, 0)
	p.providers.Range(func(key, value any) bool {
		providerID, ok := key.(int64)
		if !ok || providerID <= 0 {
			return true
		}
		ap, ok := value.(*openAIWSProviderPool)
		if !ok || ap == nil {
			return true
		}
		ap.mu.Lock()
		for _, conn := range ap.conns {
			if conn == nil || conn.isLeased() || conn.waiters.Load() > 0 || hasCompatibleWaiterLocked(ap, conn) {
				continue
			}
			candidates = append(candidates, openAIWSIdlePingCandidate{
				providerID: providerID,
				conn:       conn,
			})
		}
		ap.mu.Unlock()
		return true
	})
	return candidates
}

func (p *WSConnPool) runBackgroundCleanupWorker() {
	if p == nil {
		return
	}
	ticker := time.NewTicker(openAIWSBackgroundSweepTicker)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.runBackgroundCleanupSweep(time.Now())
		case <-p.cleanupWakeCh:
			p.runBackgroundCleanupSweep(time.Now())
		case <-p.workerStopCh:
			return
		}
	}
}

func (p *WSConnPool) runBackgroundCleanupSweep(now time.Time) {
	if p == nil {
		return
	}
	type cleanupResult struct {
		evicted []*WSConn
	}
	results := make([]cleanupResult, 0)
	p.providers.Range(func(_ any, value any) bool {
		ap, ok := value.(*openAIWSProviderPool)
		if !ok || ap == nil {
			return true
		}
		maxConns := p.maxConnsHardCap()
		ap.mu.Lock()
		if ap.lastAcquire != nil && ap.lastAcquire.Provider != nil {
			maxConns = p.effectiveMaxConnsByProvider(ap.lastAcquire.Provider)
		}
		evicted := p.cleanupProviderLocked(ap, now, maxConns)
		ap.lastCleanupAt = now
		ap.mu.Unlock()
		if len(evicted) > 0 {
			results = append(results, cleanupResult{evicted: evicted})
		}
		return true
	})
	for _, result := range results {
		closeOpenAIWSConns(result.evicted)
	}
}

// Acquire returns an exclusive lease. The caller releases it when finished; ctx bounds selection, queuing, and dialing.
func (p *WSConnPool) Acquire(ctx context.Context, req WSAcquireRequest) (*WSConnLease, error) {
	if p != nil {
		p.runtimeMu.Lock()
		if p.closed {
			p.runtimeMu.Unlock()
			return nil, errOpenAIWSConnClosed
		}
		p.acquireWG.Add(1)
		p.runtimeMu.Unlock()
		defer p.acquireWG.Done()
	}
	if p != nil {
		p.metrics.acquireTotal.Add(1)
	}
	return p.acquire(ctx, CloneWSAcquireRequest(req))
}

// acquire selects, dials, or waits for pool changes within the same context budget.
func (p *WSConnPool) acquire(ctx context.Context, req WSAcquireRequest) (lease *WSConnLease, err error) {
	if p == nil || req.Provider == nil || req.Provider.ID <= 0 {
		return nil, errors.New("invalid ws acquire request")
	}
	if stringsTrim(req.WSURL) == "" {
		return nil, errors.New("ws url is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	providerID := req.Provider.ID
	compatibility := wsCompatibilityForRequest(req)
	routingAffinity := normalizeOpenAIWSRoutingAffinity(req.Headers)
	forcePreferred := !req.ForceNewConn && req.ForcePreferredConn
	ap := p.getOrCreateProviderPool(providerID)
	var waiter *wsPoolWaiter
	var queueWait time.Duration
	queued := false
	retry := 0
	finishWaitingLocked := func() bool {
		if waiter == nil {
			return false
		}
		queueWait += removeWaiterLocked(ap, waiter)
		waiter = nil
		return true
	}
	defer func() {
		ap.mu.Lock()
		removed := finishWaitingLocked()
		ap.mu.Unlock()
		if removed {
			// Reclaim idle connections left behind when a waiter cancels in the background.
			p.requestCleanup()
		}
		if queued {
			p.metrics.acquireQueueWaitMs.Add(queueWait.Milliseconds())
		}
		if lease != nil {
			lease.queueWait = queueWait
		}
	}()

	for {
		p.runtimeMu.Lock()
		closed := p.closed
		p.runtimeMu.Unlock()
		if closed {
			return nil, errOpenAIWSConnClosed
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		maxConns := p.effectiveMaxConnsByProvider(req.Provider)
		ap.mu.Lock()
		generation := ap.generation
		now := time.Now()
		evicted := p.retireExpiredConnsLocked(ap, now)
		if ap.lastCleanupAt.IsZero() || now.Sub(ap.lastCleanupAt) >= openAIWSAcquireCleanupInterval {
			evicted = append(evicted, p.cleanupProviderLocked(ap, now, maxConns)...)
			ap.lastCleanupAt = now
		}
		pickStarted := time.Now()
		var preferred, selected *WSConn
		if forcePreferred {
			preferred = ap.conns[req.PreferredConnID]
			if !preferred.matchesCompatibility(compatibility) || p.connMaxAgeReached(preferred, now) {
				ap.mu.Unlock()
				closeOpenAIWSConns(evicted)
				return nil, openai.ErrOpenAIWSPreferredConnUnavailable
			}
			if preferred.tryAcquire() {
				selected = preferred
			}
		} else if !req.ForceNewConn {
			allowAny := routingAffinity == "" || waiter != nil || len(ap.conns)+ap.creating >= maxConns
			selected = p.pickAvailableConnLocked(ap, req.PreferredConnID, compatibility, routingAffinity, allowAny)
		}
		if selected != nil {
			if finishWaitingLocked() {
				// Continue handing off the lease while background cleanup reclaims surplus idle connections.
				p.requestCleanup()
			}
			connPick := time.Since(pickStarted)
			p.recordConnPickDuration(connPick)
			ap.mu.Unlock()
			closeOpenAIWSConns(evicted)
			if p.shouldHealthCheckConn(selected) {
				if pingErr := selected.pingWithTimeout(openAIWSConnHealthCheckTO); pingErr != nil {
					p.evictConn(providerID, selected.id)
					if retry < 1 {
						retry++
						continue
					}
					return nil, pingErr
				}
			}
			return p.deliverLease(ctx, req, generation, selected, true, connPick)
		}

		if !forcePreferred && len(ap.conns)+ap.creating >= maxConns {
			var idle *WSConn
			if req.ForceNewConn {
				idle = p.pickOldestIdleConnLocked(ap)
			} else {
				idle = p.pickOldestIncompatibleIdleConnLocked(ap, compatibility)
			}
			if idle != nil {
				delete(ap.conns, idle.id)
				evicted = append(evicted, idle)
				p.metrics.scaleDownTotal.Add(1)
			}
		}
		connPick := time.Since(pickStarted)
		p.recordConnPickDuration(connPick)
		if !forcePreferred && len(ap.conns)+ap.creating < maxConns {
			finishWaitingLocked()
			ap.creating++
			ap.mu.Unlock()
			closeOpenAIWSConns(evicted)
			conn, dialErr := p.dialConn(ctx, req)
			ap.mu.Lock()
			ap.creating--
			ap.signalChangedLocked()
			if ap.generation != generation {
				ap.mu.Unlock()
				conn.close()
				if retry < 1 {
					retry++
					continue
				}
				return nil, errOpenAIWSConnClosed
			}
			if dialErr != nil {
				ap.prewarmFails++
				ap.prewarmFailAt = time.Now()
				ap.mu.Unlock()
				return nil, dialErr
			}
			if len(ap.conns) >= p.effectiveMaxConnsByProvider(req.Provider) {
				ap.mu.Unlock()
				conn.close()
				return nil, openai.ErrOpenAIWSConnQueueFull
			}
			// Reserve the new connection for its dialing request before making it visible to other waiters.
			if !conn.tryAcquire() {
				ap.mu.Unlock()
				conn.close()
				return nil, errOpenAIWSConnClosed
			}
			ap.conns[conn.id] = conn
			ap.prewarmFails = 0
			ap.prewarmFailAt = time.Time{}
			ap.mu.Unlock()
			return p.deliverLease(ctx, req, generation, conn, false, connPick)
		}
		if req.ForceNewConn {
			ap.mu.Unlock()
			closeOpenAIWSConns(evicted)
			return nil, openai.ErrOpenAIWSConnQueueFull
		}
		if waiter == nil {
			waiter, err = p.addWaiterLocked(ap, ctx, compatibility, preferred, maxConns)
			if err != nil {
				ap.mu.Unlock()
				closeOpenAIWSConns(evicted)
				return nil, err
			}
			if !queued {
				p.metrics.acquireQueueWaitTotal.Add(1)
				queued = true
			}
		}
		changed := ap.changeChannelLocked()
		ap.mu.Unlock()
		closeOpenAIWSConns(evicted)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.workerStopCh:
			return nil, errOpenAIWSConnClosed
		case <-changed:
		}
	}
}

// deliverLease checks cancellation and shutdown before recording a successful acquisition.
func (p *WSConnPool) deliverLease(ctx context.Context, req WSAcquireRequest, generation uint64, conn *WSConn, reused bool, pick time.Duration) (*WSConnLease, error) {
	lease := &WSConnLease{pool: p, ProviderID: req.Provider.ID, Conn: conn, connPick: pick, reused: reused}
	p.runtimeMu.Lock()
	closed := p.closed
	p.runtimeMu.Unlock()
	if closed {
		p.releaseUndeliveredConn(req.Provider.ID, conn)
		return nil, errOpenAIWSConnClosed
	}
	if err := ctx.Err(); err != nil {
		p.releaseUndeliveredConn(req.Provider.ID, conn)
		return nil, err
	}
	if reused {
		p.metrics.acquireReuseTotal.Add(1)
	} else {
		p.metrics.acquireCreateTotal.Add(1)
	}
	p.recordLastSuccessfulAcquire(req.Provider.ID, generation, req)
	p.ensureTargetIdleAsync(req.Provider.ID)
	return lease, nil
}

func (p *WSConnPool) recordConnPickDuration(duration time.Duration) {
	if p == nil {
		return
	}
	if duration < 0 {
		duration = 0
	}
	p.metrics.connPickTotal.Add(1)
	p.metrics.connPickMs.Add(duration.Milliseconds())
}

func (p *WSConnPool) recordLastSuccessfulAcquire(providerID int64, generation uint64, req WSAcquireRequest) {
	if p == nil || providerID <= 0 {
		return
	}
	ap, ok := p.getProviderPool(providerID)
	if !ok || ap == nil {
		return
	}
	ap.mu.Lock()
	if ap.generation != generation {
		ap.mu.Unlock()
		return
	}
	ap.lastAcquire = CloneWSAcquireRequestPtr(&req)
	ap.mu.Unlock()
}

// pickOldestIdleConnLocked selects the least recently used reclaimable connection. The caller holds ap.mu.
func (p *WSConnPool) pickOldestIdleConnLocked(ap *openAIWSProviderPool) *WSConn {
	if ap == nil || len(ap.conns) == 0 {
		return nil
	}
	var oldest *WSConn
	for _, conn := range ap.conns {
		if conn == nil || conn.isLeased() || conn.waiters.Load() > 0 || p.isConnPinnedLocked(ap, conn.id) || hasCompatibleWaiterLocked(ap, conn) {
			continue
		}
		if oldest == nil || conn.lastUsedAt().Before(oldest.lastUsedAt()) {
			oldest = conn
		}
	}
	return oldest
}

// pickOldestIncompatibleIdleConnLocked selects a reclaimable connection with different outbound settings. The caller holds ap.mu.
func (p *WSConnPool) pickOldestIncompatibleIdleConnLocked(ap *openAIWSProviderPool, compatibility wsConnCompatibility) *WSConn {
	var oldest *WSConn
	for _, conn := range ap.conns {
		if conn == nil || conn.matchesCompatibility(compatibility) || conn.isLeased() ||
			conn.waiters.Load() > 0 || p.isConnPinnedLocked(ap, conn.id) || hasCompatibleWaiterLocked(ap, conn) {
			continue
		}
		if oldest == nil || conn.lastUsedAt().Before(oldest.lastUsedAt()) {
			oldest = conn
		}
	}
	return oldest
}

func (p *WSConnPool) getOrCreateProviderPool(providerID int64) *openAIWSProviderPool {
	if p == nil || providerID <= 0 {
		return nil
	}
	if existing, ok := p.providers.Load(providerID); ok {
		if ap, typed := existing.(*openAIWSProviderPool); typed && ap != nil {
			return ap
		}
	}
	ap := &openAIWSProviderPool{
		conns:       make(map[string]*WSConn),
		pinnedConns: make(map[string]int),
		changedCh:   make(chan struct{}),
	}
	actual, _ := p.providers.LoadOrStore(providerID, ap)
	if typed, ok := actual.(*openAIWSProviderPool); ok && typed != nil {
		return typed
	}
	return ap
}

func (p *WSConnPool) getProviderPool(providerID int64) (*openAIWSProviderPool, bool) {
	if p == nil || providerID <= 0 {
		return nil, false
	}
	value, ok := p.providers.Load(providerID)
	if !ok || value == nil {
		return nil, false
	}
	ap, typed := value.(*openAIWSProviderPool)
	return ap, typed && ap != nil
}

// notifyProviderPoolChanged wakes requests waiting for a compatible connection in this provider's pool.
func (p *WSConnPool) notifyProviderPoolChanged(providerID int64) {
	ap, ok := p.getProviderPool(providerID)
	if !ok || ap == nil {
		return
	}
	ap.mu.Lock()
	ap.signalChangedLocked()
	ap.mu.Unlock()
}

func (p *WSConnPool) isConnPinnedLocked(ap *openAIWSProviderPool, connID string) bool {
	if ap == nil || connID == "" || len(ap.pinnedConns) == 0 {
		return false
	}
	return ap.pinnedConns[connID] > 0
}

// cleanupProviderLocked reclaims connections by age, idle limit, and capacity. The caller holds ap.mu.
func (p *WSConnPool) cleanupProviderLocked(ap *openAIWSProviderPool, now time.Time, maxConns int) []*WSConn {
	if ap == nil {
		return nil
	}
	evicted := p.retireExpiredConnsLocked(ap, now)

	if maxConns <= 0 {
		maxConns = p.maxConnsHardCap()
	}
	maxIdle := p.maxIdlePerProvider()
	if maxIdle < 0 || maxIdle > maxConns {
		maxIdle = maxConns
	}
	if maxIdle >= 0 && len(ap.conns) > maxIdle {
		idleConns := make([]*WSConn, 0, len(ap.conns))
		for id, conn := range ap.conns {
			if conn == nil {
				delete(ap.conns, id)
				if len(ap.pinnedConns) > 0 {
					delete(ap.pinnedConns, id)
				}
				continue
			}
			// Let compatible waiters take these connections; check the idle limit again when their leases return.
			if conn.isLeased() || conn.waiters.Load() > 0 || p.isConnPinnedLocked(ap, conn.id) || hasCompatibleWaiterLocked(ap, conn) {
				continue
			}
			idleConns = append(idleConns, conn)
		}
		sort.SliceStable(idleConns, func(i, j int) bool {
			return idleConns[i].lastUsedAt().Before(idleConns[j].lastUsedAt())
		})
		redundant := min(max(0, len(idleConns)-maxIdle, len(ap.conns)-maxConns), len(idleConns))
		for i := range redundant {
			conn := idleConns[i]
			delete(ap.conns, conn.id)
			if len(ap.pinnedConns) > 0 {
				delete(ap.pinnedConns, conn.id)
			}
			evicted = append(evicted, conn)
		}
		if redundant > 0 {
			p.metrics.scaleDownTotal.Add(int64(redundant))
		}
	}
	if len(evicted) > 0 {
		ap.signalChangedLocked()
	}

	return evicted
}

// pickAvailableConnLocked prefers the requested connection, then routing affinity. The caller holds ap.mu.
func (p *WSConnPool) pickAvailableConnLocked(ap *openAIWSProviderPool, preferredID string, compatibility wsConnCompatibility, routingAffinity string, allowAny bool) *WSConn {
	now := time.Now()
	available := func(conn *WSConn) bool {
		return conn.matchesCompatibility(compatibility) && !conn.isLeased() &&
			conn.waiters.Load() == 0 && !p.connMaxAgeReached(conn, now)
	}
	if preferred := ap.conns[preferredID]; available(preferred) && preferred.tryAcquire() {
		return preferred
	}
	var affine, other *WSConn
	for _, conn := range ap.conns {
		if !available(conn) {
			continue
		}
		if conn.matchesRoutingAffinity(routingAffinity) {
			if affine == nil || conn.lastUsedAt().Before(affine.lastUsedAt()) {
				affine = conn
			}
		} else if allowAny && (other == nil || conn.lastUsedAt().Before(other.lastUsedAt())) {
			other = conn
		}
	}
	if affine != nil && affine.tryAcquire() {
		return affine
	}
	if other != nil && other.tryAcquire() {
		return other
	}
	return nil
}

// providerPoolLoadLocked counts leases and both kinds of waiter. The caller holds ap.mu.
func providerPoolLoadLocked(ap *openAIWSProviderPool) (inflight int, waiters int) {
	if ap == nil {
		return 0, 0
	}
	for _, conn := range ap.conns {
		if conn == nil {
			continue
		}
		if conn.isLeased() {
			inflight++
		}
		waiters += int(conn.waiters.Load())
	}
	for _, count := range ap.waiters {
		waiters += count
	}
	return inflight, waiters
}

func (p *WSConnPool) ensureTargetIdleAsync(providerID int64) {
	if p == nil || providerID <= 0 {
		return
	}

	p.runtimeMu.Lock()
	defer p.runtimeMu.Unlock()
	if p.closed {
		return
	}

	var req WSAcquireRequest
	generation := uint64(0)
	need := 0
	ap, ok := p.getProviderPool(providerID)
	if !ok || ap == nil {
		return
	}
	ap.mu.Lock()
	defer ap.mu.Unlock()
	if ap.lastAcquire == nil {
		return
	}
	if ap.prewarmActive {
		return
	}
	now := time.Now()
	if !ap.prewarmUntil.IsZero() && now.Before(ap.prewarmUntil) {
		return
	}
	if p.shouldSuppressPrewarmLocked(ap, now) {
		return
	}
	if !p.prewarmNeededLocked(ap) {
		return
	}
	effectiveMaxConns := p.maxConnsHardCap()
	if ap.lastAcquire != nil && ap.lastAcquire.Provider != nil {
		effectiveMaxConns = p.effectiveMaxConnsByProvider(ap.lastAcquire.Provider)
	}
	target := p.targetConnCountLocked(ap, effectiveMaxConns)
	current := len(ap.conns) + ap.creating
	if current >= target {
		return
	}
	need = target - current
	if need <= 0 {
		return
	}
	req = CloneWSAcquireRequest(*ap.lastAcquire)
	generation = ap.generation
	ap.prewarmActive = true
	if cooldown := p.prewarmCooldown(); cooldown > 0 {
		ap.prewarmUntil = now.Add(cooldown)
	}
	ap.creating += need
	p.metrics.scaleUpTotal.Add(int64(need))

	p.prewarmWG.Go(func() { ; p.prewarmConns(providerID, req, need, generation) })
}

func (p *WSConnPool) targetConnCountLocked(ap *openAIWSProviderPool, maxConns int) int {
	if ap == nil {
		return 0
	}

	if maxConns <= 0 {
		return 0
	}

	minIdle := min(max(p.minIdlePerProvider(), 0), maxConns)

	inflight, waiters := providerPoolLoadLocked(ap)
	utilization := p.targetUtilization()
	demand := inflight + waiters
	if demand <= 0 {
		return minIdle
	}

	target := 1
	if demand > 1 {
		target = int(math.Ceil(float64(demand) / utilization))
	}
	if waiters > 0 && target < len(ap.conns)+1 {
		target = len(ap.conns) + 1
	}
	if target < minIdle {
		target = minIdle
	}
	if target > maxConns {
		target = maxConns
	}
	return target
}

// prewarmNeededLocked checks demand, capacity, and idle limits. The caller holds ap.mu.
func (p *WSConnPool) prewarmNeededLocked(ap *openAIWSProviderPool) bool {
	if ap.lastAcquire == nil {
		return false
	}
	limit := p.effectiveMaxConnsByProvider(ap.lastAcquire.Provider)
	if len(ap.conns) >= p.targetConnCountLocked(ap, limit) {
		return false
	}
	idle := 0
	for id, conn := range ap.conns {
		if conn != nil && !conn.isLeased() && conn.waiters.Load() == 0 && !p.isConnPinnedLocked(ap, id) {
			idle++
		}
	}
	return idle < p.maxIdlePerProvider()
}

func (p *WSConnPool) prewarmConns(providerID int64, req WSAcquireRequest, total int, generations ...uint64) {
	generation := uint64(0)
	if len(generations) > 0 {
		generation = generations[0]
	}
	staleTarget := false
	remaining := total
	defer func() {
		if ap, ok := p.getProviderPool(providerID); ok && ap != nil {
			ap.mu.Lock()
			// Return unused dial reservations so subsequent requests can use the updated capacity.
			ap.creating = max(0, ap.creating-remaining)
			ap.prewarmActive = false
			ap.signalChangedLocked()
			ap.mu.Unlock()
		}
		if staleTarget {
			// Recompute demand using the latest request's credentials and handshake settings.
			p.ensureTargetIdleAsync(providerID)
		}
	}()

	for remaining > 0 {
		parent := p.prewarmCtx
		if parent == nil {
			parent = context.Background()
		}
		if parent.Err() != nil {
			return
		}
		ap, ok := p.getProviderPool(providerID)
		if !ok || ap == nil {
			return
		}
		ap.mu.Lock()
		if ap.generation != generation || ap.lastAcquire == nil {
			ap.mu.Unlock()
			return
		}
		if !sameOpenAIWSPrewarmTarget(req, *ap.lastAcquire) {
			staleTarget = true
			ap.mu.Unlock()
			return
		}
		if !p.prewarmNeededLocked(ap) {
			ap.mu.Unlock()
			return
		}
		ap.mu.Unlock()
		ctx, cancel := context.WithTimeout(parent, p.dialTimeout()+openAIWSConnPrewarmExtraDelay)
		conn, err := p.dialConn(ctx, req)
		cancel()

		ap.mu.Lock()
		remaining--
		if ap.creating > 0 {
			ap.creating--
		}
		if err != nil {
			ap.prewarmFails++
			ap.prewarmFailAt = time.Now()
			ap.signalChangedLocked()
			ap.mu.Unlock()
			continue
		}
		if ap.generation != generation || ap.lastAcquire == nil {
			ap.mu.Unlock()
			conn.close()
			return
		}
		if !sameOpenAIWSPrewarmTarget(req, *ap.lastAcquire) {
			staleTarget = true
			ap.signalChangedLocked()
			ap.mu.Unlock()
			conn.close()
			return
		}
		// Demand and settings can change during dialing; recheck before inserting a late connection.
		if parent.Err() != nil || !p.prewarmNeededLocked(ap) {
			ap.signalChangedLocked()
			ap.mu.Unlock()
			conn.close()
			return
		}
		ap.conns[conn.id] = conn
		ap.prewarmFails = 0
		ap.prewarmFailAt = time.Time{}
		ap.signalChangedLocked()
		ap.mu.Unlock()
	}
}

// ClearProvider closes pooled connections and clears prewarm state. Generation checks reject dials started before the reset.
func (p *WSConnPool) ClearProvider(providerID int64) {
	if p == nil || providerID <= 0 {
		return
	}
	ap, ok := p.getProviderPool(providerID)
	if !ok || ap == nil {
		return
	}
	ap.mu.Lock()
	ap.generation++
	conns := make([]*WSConn, 0, len(ap.conns))
	for id, conn := range ap.conns {
		delete(ap.conns, id)
		delete(ap.pinnedConns, id)
		if conn != nil {
			conns = append(conns, conn)
		}
	}
	ap.lastAcquire = nil
	ap.prewarmUntil = time.Time{}
	ap.prewarmFails = 0
	ap.prewarmFailAt = time.Time{}
	ap.signalChangedLocked()
	ap.mu.Unlock()
	closeOpenAIWSConns(conns)
}

func (p *WSConnPool) evictConn(providerID int64, connID string) {
	if p == nil || providerID <= 0 || stringsTrim(connID) == "" {
		return
	}
	var conn *WSConn
	ap, ok := p.getProviderPool(providerID)
	if ok && ap != nil {
		ap.mu.Lock()
		if c, exists := ap.conns[connID]; exists {
			conn = c
			delete(ap.conns, connID)
			if len(ap.pinnedConns) > 0 {
				delete(ap.pinnedConns, connID)
			}
			ap.signalChangedLocked()
		}
		ap.mu.Unlock()
	}
	if conn != nil {
		conn.close()
	}
}

func (p *WSConnPool) PinConn(providerID int64, connID string) bool {
	if p == nil || providerID <= 0 {
		return false
	}
	connID = stringsTrim(connID)
	if connID == "" {
		return false
	}
	ap, ok := p.getProviderPool(providerID)
	if !ok || ap == nil {
		return false
	}
	ap.mu.Lock()
	defer ap.mu.Unlock()
	if _, exists := ap.conns[connID]; !exists {
		return false
	}
	if ap.pinnedConns == nil {
		ap.pinnedConns = make(map[string]int)
	}
	ap.pinnedConns[connID]++
	return true
}

func (p *WSConnPool) UnpinConn(providerID int64, connID string) {
	if p == nil || providerID <= 0 {
		return
	}
	connID = stringsTrim(connID)
	if connID == "" {
		return
	}
	ap, ok := p.getProviderPool(providerID)
	if !ok || ap == nil {
		return
	}
	ap.mu.Lock()
	defer ap.mu.Unlock()
	if len(ap.pinnedConns) == 0 {
		return
	}
	count := ap.pinnedConns[connID]
	if count <= 1 {
		delete(ap.pinnedConns, connID)
		ap.signalChangedLocked()
		return
	}
	ap.pinnedConns[connID] = count - 1
	ap.signalChangedLocked()
}

func (p *WSConnPool) dialConn(ctx context.Context, req WSAcquireRequest) (*WSConn, error) {
	if p == nil || p.clientDialer == nil {
		return nil, errors.New("openai ws client dialer is nil")
	}
	headers := cloneHeader(req.Headers)
	var err error
	if req.HeadersFactory != nil {
		headers, err = req.HeadersFactory(ctx, headers)
		if err != nil {
			return nil, err
		}
	}
	conn, status, handshakeHeaders, err := p.clientDialer.Dial(ctx, req.WSURL, headers, req.ProxyURL, req.TLSProfile)
	if err != nil {
		var handshakeErr *openai.WSHandshakeError
		var responseBody []byte
		if errors.As(err, &handshakeErr) && handshakeErr != nil {
			responseBody = append([]byte(nil), handshakeErr.Body...)
		}
		return nil, &openai.WSDialError{
			StatusCode: status,

			ResponseHeaders: cloneHeader(handshakeHeaders),

			ResponseBody: responseBody,

			Err: err,
		}
	}
	if conn == nil {
		return nil, &openai.WSDialError{
			StatusCode: status,

			ResponseHeaders: cloneHeader(handshakeHeaders),

			Err: errors.New("openai ws dialer returned nil connection"),
		}
	}
	id := p.nextConnID(req.Provider.ID)
	pooledConn := NewWSConn(id, req.Provider.ID, conn, handshakeHeaders, req.TLSProfile, req.TLSProfileKey)
	pooledConn.compatibility = wsCompatibilityForRequest(req)
	pooledConn.routingAffinity = normalizeOpenAIWSRoutingAffinity(req.Headers)
	return pooledConn, nil
}

func (p *WSConnPool) nextConnID(providerID int64) string {
	seq := p.seq.Add(1)
	buf := make([]byte, 0, 32)
	buf = append(buf, "oa_ws_"...)
	buf = strconv.AppendInt(buf, providerID, 10)
	buf = append(buf, '_')
	buf = strconv.AppendUint(buf, seq, 10)
	return string(buf)
}

func (p *WSConnPool) shouldHealthCheckConn(conn *WSConn) bool {
	if conn == nil || !conn.supportsIdlePingWithoutReader() {
		return false
	}
	return conn.idleDuration(time.Now()) >= openAIWSConnHealthCheckIdle
}
func (p *WSConnPool) maxConnsHardCap() int { return p.nativeOptions().MaxConnsHardCap() }
func (p *WSConnPool) effectiveMaxConnsByProvider(provider *WSPoolProvider) int {
	return p.nativeOptions().EffectiveMaxConnsByProvider(provider)
}
func (p *WSConnPool) minIdlePerProvider() int    { return p.nativeOptions().MinIdle() }
func (p *WSConnPool) maxIdlePerProvider() int    { return p.nativeOptions().MaxIdle() }
func (p *WSConnPool) maxConnAge() time.Duration  { return p.nativeOptions().MaxConnAge() }
func (p *WSConnPool) queueLimitPerConn() int     { return p.nativeOptions().QueueLimit() }
func (p *WSConnPool) targetUtilization() float64 { return p.nativeOptions().TargetUtilization() }
func (p *WSConnPool) prewarmCooldown() time.Duration {
	return p.nativeOptions().PrewarmCooldown()
}

func (p *WSConnPool) shouldSuppressPrewarmLocked(ap *openAIWSProviderPool, now time.Time) bool {
	if ap == nil {
		return true
	}
	if ap.prewarmFails <= 0 {
		return false
	}
	if ap.prewarmFailAt.IsZero() {
		ap.prewarmFails = 0
		return false
	}
	if now.Sub(ap.prewarmFailAt) > openAIWSPrewarmFailureWindow {
		ap.prewarmFails = 0
		ap.prewarmFailAt = time.Time{}
		return false
	}
	return ap.prewarmFails >= openAIWSPrewarmFailureSuppress
}
func (p *WSConnPool) dialTimeout() time.Duration { return p.nativeOptions().DialTimeout() }
func CloneWSAcquireRequest(req WSAcquireRequest) WSAcquireRequest {
	copied := req
	copied.Headers = cloneHeader(req.Headers)
	copied.WSURL = stringsTrim(req.WSURL)
	copied.ProxyURL = stringsTrim(req.ProxyURL)
	copied.PreferredConnID = stringsTrim(req.PreferredConnID)
	return copied
}

func CloneWSAcquireRequestPtr(req *WSAcquireRequest) *WSAcquireRequest {
	if req == nil {
		return nil
	}
	copied := CloneWSAcquireRequest(*req)
	return &copied
}

// sameOpenAIWSPrewarmTarget reports whether the dial target remains compatible.
// Routing hints express a preference, so changing one does not invalidate a compatible connection.
func sameOpenAIWSPrewarmTarget(a, b WSAcquireRequest) bool {
	return wsCompatibilityForRequest(a) == wsCompatibilityForRequest(b)
}

// normalizeOpenAIWSBetaFeatures deduplicates and sorts handshake features for a stable compatibility key.
func normalizeOpenAIWSBetaFeatures(headers http.Header) string {
	features := make(map[string]struct{})
	for name, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(name), "x-codex-beta-features") {
			continue
		}
		for _, value := range values {
			for feature := range strings.SplitSeq(value, ",") {
				if feature = strings.TrimSpace(feature); feature != "" {
					features[feature] = struct{}{}
				}
			}
		}
	}
	if len(features) == 0 {
		return ""
	}
	normalized := make([]string, 0, len(features))
	for feature := range features {
		normalized = append(normalized, feature)
	}
	sort.Strings(normalized)
	return strings.Join(normalized, ",")
}

func normalizeOpenAIWSHandshakeCompatibility(provider *WSPoolProvider, headers http.Header) openAIWSHandshakeCompatibilityKey {
	key := openAIWSHandshakeCompatibilityKey{
		betaFeatures: normalizeOpenAIWSBetaFeatures(headers),
	}
	mode := activeCodexFingerprintMode(provider)
	if mode == codexFingerprintOff {
		return key
	}
	key.codexInstallationID = normalizeOpenAIWSStableIdentityHeader(headers, "x-codex-installation-id")
	if mode == codexFingerprintDevice {
		return key
	}
	key.sessionIDHyphen = normalizeOpenAIWSStableIdentityHeader(headers, "session-id")
	key.sessionIDUnderscore = normalizeOpenAIWSStableIdentityHeader(headers, "session_id")
	key.threadID = normalizeOpenAIWSStableIdentityHeader(headers, "thread-id")
	key.clientRequestID = normalizeOpenAIWSStableIdentityHeader(headers, "x-client-request-id")
	key.codexWindowID = normalizeOpenAIWSStableIdentityHeader(headers, "x-codex-window-id")
	return key
}

func normalizeOpenAIWSStableIdentityHeader(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get(name))
}

func normalizeOpenAIWSRoutingAffinity(headers http.Header) string {
	canonicalName := http.CanonicalHeaderKey(openAICodexRoutingHintHeader)
	if values, ok := headers[canonicalName]; ok {
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}

	variantNames := make([]string, 0)
	for name := range headers {
		if name != canonicalName && strings.EqualFold(strings.TrimSpace(name), openAICodexRoutingHintHeader) {
			variantNames = append(variantNames, name)
		}
	}
	sort.Strings(variantNames)
	for _, name := range variantNames {
		for _, value := range headers[name] {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}
func cloneHeader(src http.Header) http.Header { return upstream.CloneHeader(src) }
func closeOpenAIWSConns(conns []*WSConn) {
	if len(conns) == 0 {
		return
	}
	for _, conn := range conns {
		if conn == nil {
			continue
		}
		conn.close()
	}
}

func stringsTrim(value string) string {
	return strings.TrimSpace(value)
}

// Start starts background workers once; calls after Close have no effect.
func (p *WSConnPool) Start() {
	if p == nil {
		return
	}
	p.runtimeMu.Lock()
	defer p.runtimeMu.Unlock()
	if p.closed || p.started {
		return
	}
	p.started = true
	p.startBackgroundWorkers()
}

func (p *WSConnPool) nativeOptions() *WSPoolOptions {
	if p == nil {
		return nil
	}
	return p.cfg.Load()
}

func activeCodexFingerprintMode(provider *WSPoolProvider) codexFingerprintMode {
	if provider == nil || provider.FingerprintMode == "" {
		return codexFingerprintOff
	}
	return provider.FingerprintMode
}

func (p *WSConnPool) SnapshotProviderState(id int64) (WSPoolProviderState, bool) {
	provider, ok := p.getProviderPool(id)
	if !ok {
		return WSPoolProviderState{}, false
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	state := WSPoolProviderState{Connections: len(provider.conns), PinnedConnections: len(provider.pinnedConns)}
	for _, connection := range provider.conns {
		if connection != nil && connection.isLeased() {
			state.LeasedConnections++
		}
	}
	return state, true
}

// EvictConnection closes the selected connection. The caller decides whether to reconnect or retry.
func (p *WSConnPool) EvictConnection(id int64, connectionID string) { p.evictConn(id, connectionID) }

// UpdateOptions publishes an immutable configuration snapshot and reclaims idle connections above its limits.
func (p *WSConnPool) UpdateOptions(options WSPoolOptions) error {
	p.runtimeMu.Lock()
	if p.closed {
		p.runtimeMu.Unlock()
		return errOpenAIWSConnClosed
	}
	p.cfg.Store(&options)
	p.runtimeMu.Unlock()
	p.providers.Range(func(key, _ any) bool {
		id, ok := key.(int64)
		if !ok {
			return true
		}
		p.reconcileProvider(id)
		p.ensureTargetIdleAsync(id)
		return true
	})
	return nil
}

func (p *WSConnPool) reconcileProvider(id int64) {
	ap, ok := p.getProviderPool(id)
	if !ok {
		return
	}
	ap.mu.Lock()
	limit := p.maxConnsHardCap()
	if ap.lastAcquire != nil {
		limit = p.effectiveMaxConnsByProvider(ap.lastAcquire.Provider)
	}
	evicted := p.cleanupProviderLocked(ap, time.Now(), limit)
	ap.signalChangedLocked()
	ap.mu.Unlock()
	closeOpenAIWSConns(evicted)
}

// wsCompatibilityForRequest builds the compatibility key shared by dialing, reuse, and prewarming.
func wsCompatibilityForRequest(req WSAcquireRequest) wsConnCompatibility {
	return wsConnCompatibility{
		wsURL:         stringsTrim(req.WSURL),
		proxyURL:      stringsTrim(req.ProxyURL),
		tlsProfileKey: openAIWSTLSProfileKey(req.TLSProfile, req.TLSProfileKey),
		handshake:     normalizeOpenAIWSHandshakeCompatibility(req.Provider, req.Headers),
	}
}

// matchesCompatibility reports whether the connection uses the requested outbound configuration.
func (c *WSConn) matchesCompatibility(compatibility wsConnCompatibility) bool {
	return c != nil && c.compatibility == compatibility
}
