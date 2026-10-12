package querycache

import (
	"context"
	"fmt"
)

// contextLoad ties a shared load's lifetime to its active waiters.
type contextLoad struct {
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	waiters  int
	finished bool
	entry    Entry
	hit      bool
	err      error
}

// GetOrLoadContext lets waiters cancel independently and cancels a shared load
// when its final waiter leaves. The loader must use the supplied shared context;
// each waiter receives a cloned result.
func (c *Cache) GetOrLoadContext(ctx context.Context, key string, load func(context.Context) (any, error)) (Entry, bool, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, false, err
	}
	if load == nil {
		return Entry{}, false, nil
	}
	if entry, ok := c.Get(key); ok {
		return entry, true, nil
	}
	if c == nil || key == "" {
		payload, err := load(ctx)
		if err != nil {
			return Entry{}, false, err
		}
		if err = ctx.Err(); err != nil {
			return Entry{}, false, err
		}
		if c != nil {
			return c.Set(key, payload), false, nil
		}
		return Entry{Payload: Clone(payload)}, false, nil
	}
	c.loadMu.Lock()
	if c.loads == nil {
		c.loads = make(map[string]*contextLoad)
	}
	flight := c.loads[key]
	if flight == nil {
		shared, cancel := context.WithCancel(context.WithoutCancel(ctx))
		flight = &contextLoad{ctx: shared, cancel: cancel, done: make(chan struct{})}
		c.loads[key] = flight
		go c.runContextLoad(key, flight, load)
	}
	flight.waiters++
	c.loadMu.Unlock()
	defer c.leaveContextLoad(key, flight)
	select {
	case <-ctx.Done():
		return Entry{}, false, ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return Entry{}, false, err
		}
		return Clone(flight.entry), flight.hit, flight.err
	}
}

// leaveContextLoad removes abandoned loads so later callers can start a fresh one.
func (c *Cache) leaveContextLoad(key string, flight *contextLoad) {
	c.loadMu.Lock()
	defer c.loadMu.Unlock()
	flight.waiters--
	if flight.waiters == 0 && !flight.finished {
		if c.loads[key] == flight {
			delete(c.loads, key)
		}
		flight.cancel()
	}
}

// runContextLoad runs the loader in a goroutine and discards results after cancellation.
func (c *Cache) runContextLoad(key string, flight *contextLoad, load func(context.Context) (any, error)) {
	var entry Entry
	var payload any
	var hit bool
	var err error
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("缓存查询加载异常: %v", recovered)
		}
		c.loadMu.Lock()
		defer c.loadMu.Unlock()
		if flight.ctx.Err() != nil {
			err = flight.ctx.Err()
		}
		if err == nil && !hit && flight.waiters > 0 {
			entry = c.Set(key, payload)
		}
		flight.entry, flight.hit, flight.err = entry, hit, err
		flight.finished = true
		if c.loads[key] == flight {
			delete(c.loads, key)
		}
		flight.cancel()
		close(flight.done)
	}()
	if entry, hit = c.Get(key); hit {
		return
	}
	payload, err = load(flight.ctx)
}
