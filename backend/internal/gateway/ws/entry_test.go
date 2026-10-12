package ws

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/pkg/requestcontext"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

var errKeyLimitRetry = errors.New("retry provider")

// keyLimitEntry runs entry orchestration with observable request-slot substitutes.
type keyLimitEntry struct {
	cancelClient context.CancelFunc
	EntryPorts
	t                                              *testing.T
	mode                                           string
	acquired, active, attempts, authorized, closed int
	target                                         *keyLimitTarget
	abortLease                                     context.CancelFunc
}

type quietEntryLog struct{}

// keyLimitTarget drives turn admission, completion, and failover through the entry callbacks.
type keyLimitTarget struct {
	EntryTarget
	root *keyLimitEntry
}

func (quietEntryLog) With(...EntryField) EntryLogger { return quietEntryLog{} }
func (quietEntryLog) Info(string, ...EntryField)     {}
func (quietEntryLog) Warn(string, ...EntryField)     {}
func (quietEntryLog) Debug(string, ...EntryField)    {}
func (quietEntryLog) Error(string, ...EntryField)    {}

func (p *keyLimitEntry) Logger() EntryLogger                       { return quietEntryLog{} }
func (p *keyLimitEntry) SetLogger(EntryLogger)                     {}
func (p *keyLimitEntry) Prompt(_ context.Context, b []byte) []byte { return b }
func (p *keyLimitEntry) Redirect(ctx context.Context, m string) (context.Context, string) {
	return ctx, m
}
func (p *keyLimitEntry) BindContext(context.Context)            {}
func (p *keyLimitEntry) ClassifyPrevious(string) string         { return "" }
func (p *keyLimitEntry) ObserveFirst(string)                    {}
func (p *keyLimitEntry) CaptureCyber([]byte) EntryCyberSnapshot { return EntryCyberSnapshot{} }

func (p *keyLimitEntry) Moderate(context.Context, string, []byte) *moderation.Decision { return nil }
func (p *keyLimitEntry) BlockedSession(context.Context, []byte) string                 { return "" }
func (p *keyLimitEntry) Plan(ctx context.Context, _ string) (context.Context, routing.GroupMappingResult) {
	return ctx, routing.GroupMappingResult{}
}

func (p *keyLimitEntry) ImageIntent(m string, b []byte, _ routing.GroupMappingResult) ([]byte, string, bool) {
	return b, m, false
}
func (p *keyLimitEntry) ExplicitImage(string, []byte) bool { return false }
func (p *keyLimitEntry) AcquireUser(context.Context) (func(), bool, error) {
	return func() {}, true, nil
}

func (p *keyLimitEntry) AcquireProvider(context.Context, int64, int) (func(), bool, error) {
	return func() {}, true, nil
}
func (p *keyLimitEntry) WrapRelease(_ context.Context, f func()) func() { return f }
func (p *keyLimitEntry) LoadSubscription()                              {}
func (p *keyLimitEntry) Platform() string                               { return "openai" }
func (p *keyLimitEntry) Eligibility(context.Context) error              { return nil }
func (p *keyLimitEntry) AuthorizeTurn(context.Context) error {
	p.authorized++
	return nil
}

func (p *keyLimitEntry) SessionHash([]byte, string) string { return "" }

func (p *keyLimitEntry) ExplicitHash([]byte) string { return "" }

func (p *keyLimitEntry) Guardian(ctx context.Context, _ []byte, _ string) context.Context { return ctx }

func (p *keyLimitEntry) BindSticky(context.Context, string, int64) error { return nil }
func (p *keyLimitEntry) ClearCyber()                                     {}
func (p *keyLimitEntry) Close(status int, _ string)                      { p.closed = status }

func (p *keyLimitEntry) SessionPreempted(error) bool { return false }

func (p *keyLimitEntry) LocalPolicyError(error) bool { return false }

func (p *keyLimitEntry) EndedByClient(error) bool { return false }

func (p *keyLimitEntry) ReportFailure(error) bool { return false }

func (p *keyLimitEntry) CloseInfo(err error) EntryClose {
	if errors.Is(err, apikey.ErrKeyRPMExceeded) {
		return EntryClose{Status: 1013, Reason: err.Error(), Present: true}
	}
	return EntryClose{}
}

func (p *keyLimitEntry) AcquireKey(ctx context.Context) (context.Context, func(), error) {
	if p.mode == "rpm" && p.acquired == 1 {
		return ctx, nil, apikey.ErrKeyRPMExceeded
	}
	require.Zero(p.t, p.active, "上一轮完成后才能取得下一轮的 Key 槽")
	p.acquired++
	p.active++
	ctx, abort := requestcontext.WithAbort(ctx)
	p.abortLease = abort
	return ctx, func() {
		p.active--
		abort()
	}, nil
}

func (p *keyLimitEntry) Select(context.Context, string, string, string, map[int64]struct{}, bool, bool, string) (*EntrySelection, EntryDecision, error) {
	return &EntrySelection{Provider: &EntryProvider{ProviderSnapshot: provider.ProviderSnapshot{ID: 1, Concurrency: 1, Type: "apikey"}}, Acquired: true, ReleaseFunc: func() {}, Target: p.target}, EntryDecision{}, nil
}

func (p *keyLimitEntry) Failover(err error) (*EntryFailure, bool) {
	if errors.Is(err, errKeyLimitRetry) {
		return &EntryFailure{Err: err, RetryNext: true}, true
	}
	return nil, false
}

func (*keyLimitTarget) Credential(context.Context) error            { return nil }
func (*keyLimitTarget) EnforceClient(context.Context, []byte) error { return nil }
func (*keyLimitTarget) BeginPreemption(ctx context.Context, _ []byte) (context.Context, func(), bool) {
	return ctx, nil, false
}

func (*keyLimitTarget) RecordMarked(context.Context, string, bool, []byte, routing.PricingUsageFields, string) bool {
	return false
}
func (*keyLimitTarget) Switched()                                      {}
func (*keyLimitTarget) Stop429(int, int, *failover.OAuth429State) bool { return false }
func (*keyLimitTarget) LogFailure(error)                               {}
func (target *keyLimitTarget) Run(ctx context.Context, _ ClientSocket, _ []byte, hooks *EntryHooks) error {
	p := target.root
	p.attempts++
	require.Equal(p.t, 1, p.active)
	require.NoError(p.t, hooks.BeforeTurn(1))
	if p.mode == "client_disconnect" {
		detached := requestcontext.Detach(ctx)
		p.cancelClient()
		synctest.Wait()
		require.NoError(p.t, detached.Err(), "客户端断连后继续收集 HTTP 上游结果")
		return nil
	}
	if p.mode == "lost_lease" {
		detached := requestcontext.Detach(ctx)
		p.abortLease()
		select {
		case <-detached.Done():
		case <-time.After(time.Second):
			p.t.Fatal("HTTP 桥接未收到租约失效信号")
		}
		return detached.Err()
	}

	if p.mode == "local_warmup" {
		hooks.AfterTurn(TurnCapture{Turn: 1, Result: &ForwardResult{LocalWarmup: true, RequestID: "resp_warmup"}})
		require.Zero(p.t, p.active)
		return nil
	}
	if p.mode == "retry" && p.attempts == 1 {
		hooks.AfterTurn(TurnCapture{Turn: 1, Err: errKeyLimitRetry})
		require.Equal(p.t, 1, p.active, "故障转移期间持有本轮预占")
		return errKeyLimitRetry
	}
	hooks.AfterTurn(TurnCapture{Turn: 1})
	require.Zero(p.t, p.active, "空闲连接应释放 Key 槽")
	require.NoError(p.t, ctx.Err(), "正常释放一轮不能取消会话")
	if p.mode == "retry" {
		return nil
	}
	if err := hooks.BeforeTurn(2); err != nil {
		return err
	}
	require.Equal(p.t, 1, p.active)
	hooks.AfterTurn(TurnCapture{Turn: 2})
	require.Zero(p.t, p.active)
	return nil
}

// TestEntryKeyLimits covers first and later turns, idle release, and retries within a turn.
func TestEntryKeyLimits(t *testing.T) {
	for _, mode := range []string{"two_turns", "rpm", "retry", "lost_lease", "local_warmup"} {
		t.Run(mode, func(t *testing.T) {
			p := &keyLimitEntry{t: t, mode: mode}
			p.target = &keyLimitTarget{root: p}
			RunEntry(context.Background(), p, EntryInput{Key: &EntryKey{ID: 1}, MaxProviderSwitches: 1}, nil, []byte(`{"model":"test","type":"response.create"}`))
			require.Zero(t, p.active)
			if mode == "two_turns" {
				require.Equal(t, 2, p.acquired)
			} else {
				require.Equal(t, 1, p.acquired)
			}
			if mode == "rpm" {
				require.Equal(t, 1013, p.closed)
			}
			if mode == "retry" {
				require.Equal(t, 2, p.attempts)
			} else if mode == "local_warmup" {
				require.Zero(t, p.authorized)
			} else if mode != "lost_lease" {
				require.Equal(t, 1, p.authorized)
			}
		})
	}
}

// TestEntryClientDisconnectKeepsUpstream verifies that client disconnect does not trigger an internal abort.
func TestEntryClientDisconnectKeepsUpstream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		p := &keyLimitEntry{t: t, mode: "client_disconnect", cancelClient: cancel}
		p.target = &keyLimitTarget{root: p}
		RunEntry(ctx, p, EntryInput{Key: &EntryKey{ID: 1}}, nil, []byte(`{"model":"test","type":"response.create"}`))
		require.Zero(t, p.active)
	})
}
