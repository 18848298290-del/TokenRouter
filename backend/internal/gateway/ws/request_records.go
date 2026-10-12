package ws

import (
	"context"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// entryRequestRecords 在提供商切换期间持有尚未完成的逻辑请求。
type entryRequestRecords struct {
	mu      sync.Mutex
	root    context.Context
	next    context.Context
	pending map[string]context.Context
}

type entryAttemptRecords struct {
	mu      sync.Mutex
	session *entryRequestRecords
	turns   map[int]context.Context
}

func newEntryRequestRecords(ctx context.Context) *entryRequestRecords {
	records := &entryRequestRecords{root: ctx, pending: make(map[string]context.Context)}
	records.next = records.newTurn(time.Now())
	return records
}

func (r *entryRequestRecords) newTurn(at time.Time) context.Context {
	ctx := telemetry.WithChildRequest(r.root, telemetry.RequestRecord{RequestID: telemetry.NewRequestID(), State: "running", StartedAt: at.UTC()})
	ctx = routing.RenewModelAttributes(ctx)
	r.pending[telemetry.RequestIDValue(ctx)] = ctx
	return ctx
}

func (r *entryRequestRecords) attempt() *entryAttemptRecords {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.next == nil {
		r.next = r.newTurn(time.Now())
	}
	return &entryAttemptRecords{session: r, turns: map[int]context.Context{1: r.next}}
}

func (a *entryAttemptRecords) context(base context.Context, turn int) context.Context {
	if turn < 1 {
		turn = 1
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ctx := a.turns[turn]
	if ctx == nil {
		a.session.mu.Lock()
		ctx = a.session.newTurn(time.Now())
		a.session.next = ctx
		a.session.mu.Unlock()
		a.turns[turn] = ctx
	}
	base = context.WithValue(base, telemetry.RequestID, telemetry.RequestIDValue(ctx))
	base = routing.CopyModelAttributes(base, ctx)
	return telemetry.CopyRequestCapture(base, ctx)
}

// finish 保留重试中的 ID，终态之后释放内存里的上下文。
func (a *entryAttemptRecords) finish(base context.Context, capture TurnCapture, providerID int64, platform string, retry bool) context.Context {
	ctx := a.context(base, capture.Turn)
	telemetry.UpdateRequest(ctx, func(record *telemetry.RequestRecord) {
		record.Model = capture.OriginalModel
		record.ProviderID = providerID
		record.Platform = platform
		record.State = "running"
		outcome := "completed"
		if capture.Err != nil || capture.Result == nil || !entrySucceeded(capture.Result) {
			outcome = "failed"
		}
		if len(record.Attempts) < 256 {
			record.Attempts = append(record.Attempts, telemetry.RequestAttempt{Number: len(record.Attempts) + 1, ProviderID: providerID, Outcome: outcome})
		}
		if !retry {
			now := time.Now().UTC()
			record.FinishedAt = &now
			record.DurationMs = now.Sub(record.StartedAt).Milliseconds()
			record.State = outcome
			if outcome == "failed" {
				record.ErrorCode = "websocket_turn_failed"
			}
		}
	})
	if capture.Result != nil {
		telemetry.AddRequestAlias(ctx, "upstream", capture.Result.RequestID)
	}
	a.session.mu.Lock()
	if retry {
		a.session.next = ctx
	} else {
		delete(a.session.pending, telemetry.RequestIDValue(ctx))
		if telemetry.RequestIDValue(a.session.next) == telemetry.RequestIDValue(ctx) {
			a.session.next = nil
		}
	}
	a.session.mu.Unlock()
	if !retry {
		a.mu.Lock()
		delete(a.turns, capture.Turn)
		a.mu.Unlock()
	}
	return ctx
}

// close 记录在鉴权、解析或连接退出时提前结束的轮次。
func (r *entryRequestRecords) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ctx := range r.pending {
		telemetry.UpdateRequest(ctx, func(record *telemetry.RequestRecord) {
			now := time.Now().UTC()
			record.FinishedAt = &now
			record.DurationMs = now.Sub(record.StartedAt).Milliseconds()
			record.State = "failed"
			record.ErrorCode = "websocket_turn_incomplete"
			if r.root.Err() != nil {
				record.State = "canceled"
			}
		})
	}
}
