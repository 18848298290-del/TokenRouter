package audit

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// blockingAudit pauses batch insertion so concurrent writer launches become observable.
type blockingAudit struct {
	// AuditLogRepository supplies unused methods by embedding a nil interface. This
	// scenario invokes only the overridden batch insertion while cleanup is disabled.
	AuditLogRepository
	calls atomic.Int64 // observed batch insertion count
	// entered signals each blocked insert; release frees all waiting inserts after
	// the test has checked that duplicate Start calls did not launch concurrent writers.
	entered chan struct{} // batch arrival notification
	release chan struct{} // blocked writer release signal
}

// BatchInsert signals that the audit writer reached persistence and waits for the
// test's shared release channel. It returns a fixed batch-size count so the
// service can finish its normal successful flush path after the wait ends.
//
// Signalling before blocking makes an extra writer observable independently of
// the first writer's progress. The test closes release after observing the second
// arrival window, allowing every blocked call to complete before service shutdown
// waits for the writer.
func (r *blockingAudit) BatchInsert(context.Context, []*AuditLog) (int64, error) {
	// Notify the test before waiting for release, so an independently launched
	// duplicate writer would produce its own notification while this call stays blocked.
	r.calls.Add(1)
	r.entered <- struct{}{}
	<-r.release
	return 100, nil
}

// TestRegressionAuditDuplicateStart calls Start twice before filling two audit
// batches. A blocked first insert must not overlap another writer, and releasing
// it must let service shutdown finish.
func TestRegressionAuditDuplicateStart(t *testing.T) {
	r := &blockingAudit{entered: make(chan struct{}, 4), release: make(chan struct{})}
	s := NewAuditLogService(r, nil)
	s.Start()
	s.Start()
	for range 200 {
		s.Record(&AuditLog{})
	}
	// Wait for the first blocked insert before checking for another arrival, making
	// a duplicate worker observable while the queue holds enough records for both.
	<-r.entered
	select {
	case <-r.entered:
		t.Error("duplicate Start created two concurrent audit writers")
	case <-time.After(100 * time.Millisecond):
	}
	// Release every blocked insertion before Stop waits for worker completion;
	// leaving release unclosed would make shutdown depend on an unfinished test gate.
	close(r.release)
	s.Stop()
}
