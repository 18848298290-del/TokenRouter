package ops

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// realtime_runtime.go shares a cached traffic sampler across WebSocket
// consumers and coordinates connection limits and sampler shutdown.
// WebSocket handlers reuse one traffic sampler for their pushed frames. Prepare
// starts its first loop on demand, and the last counted global release
// schedules a delayed idle stop. Cached JSON is copied for each reader, and
// failed sampling retains the previous payload for existing connections.
//
// Sampler start and restart wait for a previous loop completion channel, while
// idle callbacks are tracked separately by a timer waiter. Final Stop closes
// timer scheduling, joins callbacks, permanently closes sampling, then joins
// the refresh loop. Connection admission remains separate from those lifecycle
// flags. Idle callback decisions sample the atomic count without locking future
// Prepare or acquisition, and bypassed nonpositive-limit admissions do not
// increment it, so balanced counted releases and application shutdown order
// determine when sampling can be considered idle.

// opsWSQPSCache publishes JSON traffic samples and tracks one restartable
// refresh loop.
type opsWSQPSCache struct {
	// refreshInterval and requestCountWindow are fixed sampler inputs read without
	// mu. NewRealtimeRuntime supplies a positive ticker period and one-minute
	// query range; keep both stable while the loop runs.
	refreshInterval time.Duration // refresh loop ticker period
	// requestCountWindow also supplies the full seconds used for rate division.
	// Nonpositive values leave rates zero if the stats lookup succeeds.
	requestCountWindow time.Duration // traffic query history duration

	// payload and lastUpdatedUnixNano are published by separate atomic stores.
	// Refresh captures UTC query-end time before lookup, stores JSON bytes, then
	// its Unix nanoseconds; no current reader uses the timestamp to establish
	// freshness.
	lastUpdatedUnixNano atomic.Int64 // sample capture Unix nanoseconds
	// payload retains worker-owned byte slices after publication. Treat stored
	// bytes as immutable; getPayload returns separate storage for each nonempty
	// read.
	payload atomic.Value // serialized realtime JSON bytes

	// mu serializes opsService, cancel, done, running, and closed. start and Stop
	// publish their state changes under this mutex, then cancel or wait outside it
	// so a loop can finish.
	// opsService is borrowed by the refresh loop. Stop clears this pointer, while
	// an already captured service can finish its query before the loop join
	// completes.
	opsService *OpsService        // borrowed traffic query service
	cancel     context.CancelFunc // active loop cancellation function
	// done remains present while a cancelled loop exits. start waits on that
	// barrier before creating another loop, and cleanup clears only the same
	// completed generation.
	done chan struct{} // current loop completion channel

	mu sync.Mutex // refresh lifecycle state mutex
	// running is set before the loop goroutine starts and cleared before
	// cancellation/join. False can therefore coexist with an unfinished loop
	// tracked by done.
	running bool // refresh loop admission flag
	// closed is set by RealtimeRuntime.Stop and prevents subsequent start calls.
	// Idle-stop callbacks use Stop without setting this permanent closure flag.
	closed bool // permanent sampling closure flag
}

// RealtimeRuntime owns lazy traffic sampling and the counters used by WebSocket
// admission.
type RealtimeRuntime struct {
	// cache owns the lazy sampler and retained wire payload. Runtime Stop
	// permanently closes sampling; idle callbacks call its restartable Stop
	// instead.
	cache *opsWSQPSCache // shared traffic sampling cache
	// wsConnCount counts positive-limit global acquisitions. ReleaseTotal
	// decrements unconditionally, and Runtime Stop does not gate admission or
	// reset this counter.
	wsConnCount atomic.Int32 // global connection admission count
	// wsConnCountByIPMu protects wsConnCountByIP reads, increments, decrements,
	// and deletion. The map is separate from the global atomic counter, so callers
	// balance each admission independently.
	wsConnCountByIPMu sync.Mutex // per-IP counter map mutex
	// wsConnCountByIP uses the supplied raw IP text as its key.
	// Blank/nonpositive-limit acquisitions bypass storage, and releases need the
	// exact counted key.
	wsConnCountByIP map[string]int32 // raw client-IP admission counts
	// qpsWSIdleStopMu protects qpsWSIdleStopTimer and qpsWSRuntimeClosed.
	// qpsWSIdleStopWG accounts for scheduled callbacks or successful timer
	// cancellation, allowing final Stop to wait after closing new timer admission.
	qpsWSIdleStopMu sync.Mutex // idle callback state mutex
	// qpsWSIdleStopTimer is cleared by cancellation or its matching callback. A
	// nil pointer alone does not establish that an already firing callback
	// finished.
	qpsWSIdleStopTimer *time.Timer    // pending idle callback handle
	qpsWSIdleStopWG    sync.WaitGroup // idle callback completion waiter
	// qpsWSRuntimeClosed prevents new idle scheduling after Runtime Stop begins.
	// Global/per-IP acquisition and payload reads do not check this flag.
	qpsWSRuntimeClosed bool          // permanent idle scheduling closure
	idleDelay          time.Duration // idle sampler callback delay
}

// start admits one refresh loop for the supplied nonnil Ops service, doing
// nothing for nil inputs, an active loop, or permanent closure. The loop owns a
// Background-derived cancellation context, so a WebSocket request context does
// not determine its lifetime.
//
// A retained done channel identifies an older loop still stopping. start
// releases mu and waits for that completion before retrying admission; matching
// generation checks prevent clearing a newer barrier. The service, cancel
// function, completion channel, and running flag are published together under
// mu before the goroutine starts. The goroutine closes its own done channel
// after refreshLoop returns, letting idle stop or final shutdown join actual
// loop completion.
func (c *opsWSQPSCache) start(opsService *OpsService) {
	if c == nil || opsService == nil {
		return
	}

	for {
		c.mu.Lock()
		if c.running || c.closed {
			c.mu.Unlock()
			return
		}

		// A stopping loop still owns its done barrier after running becomes false.
		// Wait outside mu so it can finish before another start installs a new
		// generation.
		done := c.done
		if done != nil {
			c.mu.Unlock()
			<-done

			c.mu.Lock()
			// Waiters can race to restart after done closes. Clear only the old barrier
			// so a loop newly started by another waiter retains its own completion
			// channel.
			if c.done == done && !c.running {
				c.done = nil
			}
			c.mu.Unlock()
			continue
		}

		// Install the borrowed service, loop context, completion barrier, and running
		// claim under the held mutex. Sampling lifetime remains independent of the
		// request that triggered preparation.
		c.opsService = opsService
		ctx, cancel := context.WithCancel(context.Background())
		c.cancel = cancel
		c.done = make(chan struct{})
		done = c.done
		c.running = true
		c.mu.Unlock()

		// Publish loop ownership before starting the goroutine. Its deferred channel
		// close signals that all refreshLoop work exited for callers waiting on this
		// generation.
		go func() {
			defer close(done)
			c.refreshLoop(ctx)
		}()
		return
	}
}

// Stop cancels and joins its selected refresh loop, doing nothing for a nil
// cache. It clears running and the borrowed service under mu before invoking
// cancellation outside the mutex. An in-flight refresh can already hold the old
// service and can publish before the join finishes.
//
// A concurrent stop can find running false while done still names unfinished
// work and waits on that barrier. Cleanup clears only its matching done
// generation when no newer loop is running. A concurrent start can establish
// another loop before this Stop returns; final RealtimeRuntime.Stop sets closed
// first to prevent that restart. Stored bytes and sample time remain available.
func (c *opsWSQPSCache) Stop() {
	if c == nil {
		return
	}

	c.mu.Lock()
	// Running can already be false while another stop is joining the loop. Retain
	// and wait on its completion channel instead of treating that flag as drained.
	if !c.running {
		done := c.done
		c.mu.Unlock()
		if done != nil {
			<-done
		}
		return
	}
	// Detach service/cancellation ownership and mark the loop stopping under mu.
	// Cancel and join after unlocking so refresh can obtain mu and exit.
	cancel := c.cancel
	c.cancel = nil
	c.running = false
	c.opsService = nil
	done := c.done
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}

	// Clear only the barrier belonging to this completed stop. A newer start can
	// publish a different channel or running loop before cleanup reacquires mu.
	c.mu.Lock()
	if c.done == done && !c.running {
		c.done = nil
	}
	c.mu.Unlock()
}

// refreshLoop refreshes immediately, then sequentially on the positive
// refreshInterval ticker until context cancellation is selected. The ticker is
// stopped on return. A ready tick can be chosen alongside cancellation and
// start another refresh with that cancelled context.
func (c *opsWSQPSCache) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(c.refreshInterval)
	defer ticker.Stop()

	// Sample before waiting for the first periodic tick. Later lookup calls run in
	// this same loop, and cancellation is selected between refreshes.
	c.refresh(ctx)
	for {
		select {
		case <-ticker.C:
			c.refresh(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// refresh reads window counts through a captured service and publishes a JSON
// QPS/TPS update when lookup and encoding succeed. Nil cache or missing service
// is a no-op, and nil parent context selects Background. Each lookup receives a
// ten-second child budget; query errors are reported and preserve the earlier
// payload, while a nil result also preserves it.
//
// The UTC sample time is captured before lookup and ends the history range.
// Rates divide usage and final-client-error observations and consumed tokens
// by the entire positive window, rounding to one decimal; nonpositive windows leave
// rates zero after a successful lookup. Bytes and timestamp publish in separate
// atomic stores, and cancellation is not rechecked after query success, so an
// in-flight result can publish during stopping.
func (c *opsWSQPSCache) refresh(parentCtx context.Context) {
	if c == nil {
		return
	}

	c.mu.Lock()
	opsService := c.opsService
	c.mu.Unlock()
	if opsService == nil {
		return
	}

	if parentCtx == nil {
		parentCtx = context.Background()
	}
	// Request each sample under a ten-second child budget so loop cancellation
	// reaches its lookup. Deferred cancel releases that context even after
	// successful lookup and encoding.
	ctx, cancel := context.WithTimeout(parentCtx, 10*time.Second)
	defer cancel()

	now := time.Now().UTC()
	stats, err := opsService.GetWindowStats(ctx, now.Add(-c.requestCountWindow), now)
	// Failed or absent counts leave the earlier message available to connected
	// readers. Report an actual lookup error, but a nil result alone produces no
	// diagnostic.
	if err != nil || stats == nil {
		if err != nil {
			opsService.report("[OpsWS] refresh: get window stats failed: %v", err)
		}
		return
	}

	requestCount := stats.SuccessCount + stats.ErrorCountTotal
	qps := 0.0
	tps := 0.0
	// The positive history duration supplies full-window seconds for both rates.
	// Convert its duration and round each quotient to one decimal; no positive
	// duration leaves zero rates.
	if c.requestCountWindow > 0 {
		seconds := c.requestCountWindow.Seconds()
		qps = RoundTo1DP(float64(requestCount) / seconds)
		tps = RoundTo1DP(float64(stats.TokenConsumed) / seconds)
	}

	payload := map[string]any{
		"type":      "qps_update",
		"timestamp": now.Format(time.RFC3339),
		"data": map[string]any{
			"qps":           qps,
			"tps":           tps,
			"request_count": requestCount,
		},
	}

	msg, err := json.Marshal(payload)
	// Encoding failure preserves the previous wire payload and sample time.
	// Reporting the error does not invalidate bytes already visible to connection
	// handlers.
	if err != nil {
		opsService.report("[OpsWS] refresh: marshal payload failed: %v", err)
		return
	}

	// Publish the completed JSON bytes before its separately stored sample time.
	// Keep these bytes immutable because payload readers clone them outside the
	// lifecycle mutex.
	c.payload.Store(msg)
	c.lastUpdatedUnixNano.Store(now.UnixNano())
}

// getPayload returns separate storage for a nonempty published byte payload.
// Nil cache, absent/wrongly typed storage, or an empty stored slice returns
// nil; callers may modify returned bytes while the worker keeps its published
// slice stable.
func (c *opsWSQPSCache) getPayload() []byte {
	if c == nil {
		return nil
	}
	if cached, ok := c.payload.Load().([]byte); ok && cached != nil {
		return append([]byte(nil), cached...)
	}
	return nil
}

// NewRealtimeRuntime allocates a five-second sampler with a one-minute query
// window, an empty per-IP map, and thirty-second idle delay. It starts no work
// until Prepare; callers use this initialized runtime and balance its counted
// admissions before final Stop.
func NewRealtimeRuntime() *RealtimeRuntime {
	return &RealtimeRuntime{cache: &opsWSQPSCache{refreshInterval: 5 * time.Second, requestCountWindow: time.Minute}, wsConnCountByIP: make(map[string]int32), idleDelay: 30 * time.Second}
}

// Prepare attempts to cancel a pending idle timer, then starts or reuses
// sampling for the supplied service. Nil service still cancels the timer while
// cache start is a no-op. It reserves no connection count; an already firing
// callback can continue its idle decision as the caller proceeds to separate
// admission.
func (r *RealtimeRuntime) Prepare(s *OpsService) { r.cancelQPSWSIdleStop(); r.cache.start(s) }

// Total returns the stored process-wide WebSocket connection admission counter.
func (r *RealtimeRuntime) Total() int32 { return r.wsConnCount.Load() }

// ReleaseTotal decrements the global counter and schedules delayed sampler
// stopping only when the resulting value is exactly zero. Call once for a
// counted positive-limit acquisition when its connection or failed upgrade
// finishes. The operation does not release an independently counted IP slot.
//
// The decrement is atomic with concurrent admission, but timer creation and its
// later zero-count check are separate steps. A Prepare can cancel a pending
// callback while a firing callback continues. Unpaired releases or releasing
// nonpositive-limit bypasses can produce negative counts, preventing the
// exactly-zero idle branch until later counter changes reach zero again.
func (r *RealtimeRuntime) ReleaseTotal() {
	// Use the decrement result to schedule delayed idle checking, not immediate
	// sampler closure. The timer later samples the counter again and leaves per-IP
	// accounting untouched.
	if r.wsConnCount.Add(-1) == 0 {
		r.scheduleQPSWSIdleStop()
	}
}

// Payload returns copied sampler JSON or nil when no nonempty bytes are stored.
// Stopping sampling retains its last payload, so readers can still receive that
// message after idle or permanent shutdown.
func (r *RealtimeRuntime) Payload() []byte { return r.cache.getPayload() }

// Realtime returns the service-owned runtime initialized once on demand, or nil
// for a nil service. Construction allocates sampling state without starting its
// loop; callers use Prepare for new payloads. Repeated reads reuse that same
// runtime, including after its permanent Stop.
func (s *OpsService) Realtime() *RealtimeRuntime {
	if s == nil {
		return nil
	}
	s.realtimeOnce.Do(func() { s.realtime = NewRealtimeRuntime() })
	return s.realtime
}

// cancelQPSWSIdleStop clears the pending timer under the idle-state mutex.
// Successful timer Stop prevents its callback and balances the waiter itself;
// an already firing callback keeps responsibility for Done and can continue
// after this method returns.
//
// Clearing the pointer allows later idle scheduling to publish a different
// timer. Callback cleanup compares identities before clearing that pointer so
// an older callback cannot erase a newer timer handle. The operation does not
// join firing callbacks or change sampling state directly; Prepare proceeds to
// cache.start separately, and final Stop uses the waiter for completion.
func (r *RealtimeRuntime) cancelQPSWSIdleStop() {
	r.qpsWSIdleStopMu.Lock()
	if r.qpsWSIdleStopTimer != nil {
		// A successfully stopped AfterFunc will not execute Done in its callback.
		// Balance that scheduled waiter here; false leaves the firing callback to
		// finish it.
		if r.qpsWSIdleStopTimer.Stop() {
			r.qpsWSIdleStopWG.Done()
		}
		r.qpsWSIdleStopTimer = nil
	}
	r.qpsWSIdleStopMu.Unlock()
}

// scheduleQPSWSIdleStop publishes one delayed idle callback unless a timer is
// already tracked or final shutdown closed scheduling. The waiter count is
// increased before AfterFunc starts and completed by its callback, or by a
// caller that successfully stops that timer.
//
// The callback samples the global counter and calls restartable cache Stop when
// it sees zero. That check is separate from future connection admission and
// Prepare, so it does not prove the count remains zero through stopping.
// Cleanup clears only the matching timer pointer under the mutex, retaining a
// newer handle published while this callback was already running.
func (r *RealtimeRuntime) scheduleQPSWSIdleStop() {
	r.qpsWSIdleStopMu.Lock()
	if r.qpsWSIdleStopTimer != nil || r.qpsWSRuntimeClosed {
		r.qpsWSIdleStopMu.Unlock()
		return
	}
	// Account for this callback before AfterFunc can start it. The idle mutex also
	// prevents final shutdown from admitting another timer while it begins
	// waiting.
	r.qpsWSIdleStopWG.Add(1)
	var timer *time.Timer
	timer = time.AfterFunc(r.idleDelay, func() {
		defer r.qpsWSIdleStopWG.Done()
		// Sample global admission before choosing an idle stop. A future Prepare or
		// acquisition is not locked with this decision, so the observed zero can
		// change afterward.
		if r.wsConnCount.Load() == 0 {
			r.cache.Stop()
		}
		r.qpsWSIdleStopMu.Lock()
		// An already firing callback can outlive pointer cancellation and a newer
		// schedule. Clear the handle only when it still identifies this callback
		// timer.
		if r.qpsWSIdleStopTimer == timer {
			r.qpsWSIdleStopTimer = nil
		}
		r.qpsWSIdleStopMu.Unlock()
	})
	r.qpsWSIdleStopTimer = timer
	r.qpsWSIdleStopMu.Unlock()
}

// AcquireTotal admits a global connection under a positive limit by comparing
// and incrementing the atomic count. At or above the supplied limit it returns
// false. Successful counted acquisition needs one ReleaseTotal when its
// connection or failed upgrade finishes.
//
// A competing count change makes CompareAndSwap retry against the next observed
// value, avoiding overwriting another admission or release. Nonpositive limits
// instead return true without incrementing, so they do not establish a release
// obligation for this counter. Admission does not inspect permanent sampling
// closure; app and HTTP callers close new requests before final shutdown.
func (r *RealtimeRuntime) AcquireTotal(limit int32) bool {
	if limit <= 0 {
		return true
	}
	for {
		current := r.wsConnCount.Load()
		if current >= limit {
			return false
		}
		// Retry a lost comparison against the updated shared count. Writing only
		// after a match retains concurrent admission/release changes instead of
		// overwriting their counter values.
		if r.wsConnCount.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

// AcquireIP admits and counts a raw client-IP key under a positive limit while
// holding the map mutex. Blank text or nonpositive limits return true without
// storing a count. Call ReleaseIP once with the exact counted spelling;
// trimming decides blankness alone.
//
// The mutex keeps lookup, limit comparison, and increment together so competing
// requests for that key cannot overwrite each other. Global admission belongs
// to a separate counter, and this method neither reserves nor releases it.
// Permanent runtime Stop does not close this map admission, so HTTP callers
// stop new requests before final shutdown.
func (r *RealtimeRuntime) AcquireIP(clientIP string, limit int32) bool {
	if strings.TrimSpace(clientIP) == "" || limit <= 0 {
		return true
	}
	// Keep lookup and counted admission in one map-lock interval. Global admission
	// is separate, so a caller releasing a failed IP admission still balances only
	// its global slot.
	r.wsConnCountByIPMu.Lock()
	defer r.wsConnCountByIPMu.Unlock()
	current := r.wsConnCountByIP[clientIP]
	if current >= limit {
		return false
	}
	r.wsConnCountByIP[clientIP] = current + 1
	return true
}

// ReleaseIP releases the exact raw client-IP key when it was counted, ignoring
// blank or absent keys. A count at or below one removes the entry; higher
// counts decrement. Callers balance counted admissions once and avoid using
// bypassed acquisitions to release another connection.
func (r *RealtimeRuntime) ReleaseIP(clientIP string) {
	if strings.TrimSpace(clientIP) == "" {
		return
	}
	// Read and update the exact counted key under its map mutex. Removing the
	// final entry releases stored identity without changing the independent global
	// connection counter.
	r.wsConnCountByIPMu.Lock()
	defer r.wsConnCountByIPMu.Unlock()
	current, ok := r.wsConnCountByIP[clientIP]
	if !ok {
		return
	}
	if current <= 1 {
		delete(r.wsConnCountByIP, clientIP)
		return
	}
	r.wsConnCountByIP[clientIP] = current - 1
}

// Stop permanently closes idle scheduling, cancels its pending timer when
// possible, and waits for firing idle callbacks before closing and joining
// sampling. It has no cancellation parameter; callers stop HTTP admission and
// finish connection handlers before invoking final shutdown.
//
// A successfully stopped timer never calls its callback, so Stop balances its
// waiter count itself. Already firing callbacks finish through their own Done.
// Only after that join does the cache receive permanent closure, followed by
// restartable cache Stop to cancel and join its current loop. Repeated Stop
// calls can join the same completed state, while admission counters and the
// retained payload remain accessible.
func (r *RealtimeRuntime) Stop() {
	r.qpsWSIdleStopMu.Lock()
	// Close timer admission before inspecting its handle and joining callbacks. A
	// cancelled timer needs manual waiter completion; a firing callback supplies
	// that completion itself.
	r.qpsWSRuntimeClosed = true
	if r.qpsWSIdleStopTimer != nil {
		if r.qpsWSIdleStopTimer.Stop() {
			r.qpsWSIdleStopWG.Done()
		}
		r.qpsWSIdleStopTimer = nil
	}
	r.qpsWSIdleStopMu.Unlock()
	// Join idle callbacks before permanently closing the sampler. They may
	// themselves be joining cache Stop, so final cache cancellation follows their
	// actual completion.
	r.qpsWSIdleStopWG.Wait()
	r.cache.mu.Lock()
	r.cache.closed = true
	r.cache.mu.Unlock()
	r.cache.Stop()
}
