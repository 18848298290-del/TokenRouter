package httpapi

import (
	"sync/atomic"

	"go.uber.org/zap"
)

// Session compatibility metrics can be observed from both text and count-token
// entries. A process-wide sampling counter limits snapshot reads and log output,
// while the supplied reader owns the counters and hit-rate calculation included
// in each sampled event.

// compatibilityLogCounter shares the sampling cadence across callers so each
// endpoint contributes to one process-wide interval between emitted metric snapshots.
var compatibilityLogCounter atomic.Uint64

// CompatibilityLogSnapshot contains session compatibility counters and the read hit rate.
type CompatibilityLogSnapshot struct {
	// ReadTotal and ReadHit describe legacy-session reads, DualWrite counts writes
	// to both representations, and MetadataTotal counts metadata fallback. The
	// reader supplies these counters in one result; this adapter performs no
	// synchronization between the source observations used to compute them.
	// ReadTotal: attempted legacy fallback reads. ReadHit: successful fallback reads.
	// DualWrite: writes to both representations. MetadataTotal: legacy metadata fallback.
	ReadTotal, ReadHit, DualWrite, MetadataTotal int64
	ReadHitRate                                  float64 // hit fraction supplied by reader
}

// LogCompatibilityFallback samples once every 1024 calls across text and token-count endpoints.
// A nil logger skips counting and reading. For a usable logger, callers provide
// a snapshot reader that is invoked only on the sampled call; the function
// logs its values directly and does not calculate a hit rate.
func LogCompatibilityFallback(log *zap.Logger, read func() CompatibilityLogSnapshot) {
	if log == nil {
		return
	}
	if compatibilityLogCounter.Add(1)%1024 != 0 {
		// Unsampled calls avoid the reader as well as logging, limiting snapshot
		// work when many endpoints observe compatibility during the same process lifetime.
		return
	}
	value := read()
	log.Info("gateway.compatibility_fallback_metrics",
		zap.Int64("session_hash_legacy_read_fallback_total", value.ReadTotal),
		zap.Int64("session_hash_legacy_read_fallback_hit", value.ReadHit),
		zap.Int64("session_hash_legacy_dual_write_total", value.DualWrite),
		zap.Float64("session_hash_legacy_read_hit_rate", value.ReadHitRate),
		zap.Int64("metadata_legacy_fallback_total", value.MetadataTotal),
	)
}
