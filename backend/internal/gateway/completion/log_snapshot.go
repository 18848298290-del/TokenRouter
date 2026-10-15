package completion

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
)

// log_snapshot.go wraps usage writers with mutable-row cloning for each storage
// attempt. A rejected best-effort usage write can be retried synchronously with
// the same logical record. Each storage call receives cloned mutable row data,
// so changes to those copied values cannot alter the recorder's retained row or
// retry inputs. References retained in unexported fields stay shared; the
// recorder and repository treat those references as immutable.

// snapshotLogWriter delegates synchronous writes with separately cloned row data.
type snapshotLogWriter struct {
	repo LogWriter // synchronous storage delegate
}

// snapshotBestEffortLogWriter retains best-effort capability alongside cloned
// synchronous writes for recorder fallback.
type snapshotBestEffortLogWriter struct {
	snapshotLogWriter                     // cloned synchronous delegate
	best              BestEffortLogWriter // best-effort storage delegate
}

// SnapshotLogWriter wraps a repository with per-call row cloning.
// Nil returns nil, and the returned writer exposes BestEffortLogWriter only when
// the repository does, allowing WriteUsage to select its retry policy.
func SnapshotLogWriter(repo LogWriter) LogWriter {
	if repo == nil {
		return nil
	}
	writer := snapshotLogWriter{repo: repo}
	if best, ok := repo.(BestEffortLogWriter); ok {
		return snapshotBestEffortLogWriter{snapshotLogWriter: writer, best: best}
	}
	return writer
}

// Create passes a recursively cloned usage row to the synchronous repository.
// The repository determines deduplication and errors. Callers leave the input
// unchanged while cloning reads it, supply acyclic row data, and treat retained
// unexported references as immutable.
func (w snapshotLogWriter) Create(ctx context.Context, row *UsageLog) (bool, error) {
	return w.repo.Create(ctx, querycache.Clone(row))
}

// CreateBestEffort passes a recursively cloned row to the best-effort repository.
// The delegate controls queue acceptance and errors; a rejected call can therefore
// be retried from the recorder's separately retained row. Inputs must be acyclic
// and stable during cloning, with retained unexported references treated as immutable.
func (w snapshotBestEffortLogWriter) CreateBestEffort(ctx context.Context, row *UsageLog) error {
	return w.best.CreateBestEffort(ctx, querycache.Clone(row))
}
