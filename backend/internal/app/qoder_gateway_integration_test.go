//go:build integration

package app

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	keypg "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpg "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	billingredis "github.com/TokenFlux/TokenRouter/internal/billing/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewaytelemetry "github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	providerpg "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	routingpg "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	schedulerredis "github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
	"github.com/TokenFlux/TokenRouter/internal/usage"
	usagepg "github.com/TokenFlux/TokenRouter/internal/usage/postgres"
)

// balanceReader reads a database balance for billing.
type balanceReader struct{ db *sql.DB }

// storageQoderRuntime supplies test callbacks while execution, leases, settlement, and usage storage use production components.
type storageQoderRuntime struct {
	prepare        func(context.Context, gateway.Request) (gateway.Request, error)
	check          func(context.Context) error
	selectProvider func(context.Context, gateway.Request, map[int64]struct{}) (*gateway.Selection, error)
}

func (r balanceReader) GetByID(ctx context.Context, id int64) (*billing.UserSummary, error) {
	u := &billing.UserSummary{ID: id}
	err := r.db.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", id).Scan(&u.Balance)
	return u, err
}

// TestQoderHTTPStorageChain uses PostgreSQL, Redis, a completion worker, and a local upstream HTTP server.
func TestQoderHTTPStorageChain(t *testing.T) {
	f := newDatabaseFixture(t)
	ctx := context.Background()
	container, err := rediscontainer.Run(ctx, "redis:8.4-alpine")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	addr, err := container.Endpoint(ctx, "")
	require.NoError(t, err)
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	concurCache := schedulerredis.NewConcurrencyCache(rdb, 5, 60)
	concur := scheduler.NewConcurrencyService(concurCache, scheduler.Diagnostics{})
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		require.NoError(t, concur.StopContext(stopCtx))
	})

	// Redis tracks waiting requests. Cancellation, heartbeat write failure, or failed funding rechecks stop execution before provider selection.
	for _, kind := range []string{"wait-cancel", "wait-heartbeat-failure", "wait-billing-recheck"} {
		t.Run(kind, func(t *testing.T) {
			const id int64 = 990099
			held, e := concur.AcquireUserSlot(ctx, id, 1)
			require.NoError(t, e)
			require.True(t, held.Acquired)
			defer held.ReleaseFunc()
			requestCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			expected := errors.New("fixture waiting rejected")
			checks := 0
			ports := gateway.RequestPorts{Concurrency: concur, Check: func(context.Context, bool) error {
				checks++
				if checks == 2 {
					return expected
				}
				return nil
			}, Select: func(context.Context, map[int64]struct{}) (*gateway.Selection, error) {
				t.Fatal("等待未通过不应选择提供商")
				return nil, nil
			}}
			ports.WaitObserver = func(string) scheduler.WaitObserver {
				return scheduler.WaitObserver{Interval: time.Millisecond, Begin: func() error {
					switch kind {
					case "wait-cancel":
						cancel()
					case "wait-heartbeat-failure":
						return expected
					case "wait-billing-recheck":
						held.ReleaseFunc()
					}
					return nil
				}}
			}
			e = gateway.NewQoderUseCase(3, time.Second).Run(requestCtx, gateway.Request{UserID: id, Concurrency: 1, Stream: true}, ports, &gateway.OutputTracker{Sink: gatewayhttp.ResponseSink{Writer: httptest.NewRecorder()}})
			if kind == "wait-cancel" {
				require.ErrorIs(t, e, context.Canceled)
			} else {
				require.ErrorIs(t, e, expected)
			}
			if kind == "wait-billing-recheck" {
				require.Equal(t, 2, checks)
			} else {
				require.Equal(t, 1, checks)
			}
			held.ReleaseFunc()
			count, e := concurCache.GetUserConcurrency(ctx, id)
			require.NoError(t, e)
			require.Zero(t, count)
		})
	}
	funds := billing.NewFunds(billingpg.NewSettlementStore(f.db, timezone.NewCalendar(time.Local), nil))
	facts := usagepg.NewUsageLogRepository(f.client, f.db, nil, timezone.NewCalendar(time.Local))
	t.Cleanup(facts.StopUsageBatchers)
	pool := completion.NewUsageRecordWorkerPoolWithOptions(completion.UsageRecordWorkerPoolOptions{Observe: gatewaytelemetry.Completion, WorkerCount: 1, QueueSize: 8, TaskTimeout: 5 * time.Second})
	pool.Start()
	t.Cleanup(pool.Stop)
	keys := keypg.NewKeyStore(f.client, f.db, nil)
	auth := apikey.NewAPIKeyService(keys, nil, nil, nil, nil, nil, &apikey.Options{})
	groups := routingpg.NewGroupStore(f.client, f.db, routingpg.GroupStoreOptions{})
	providers := providerpg.NewProviderStore(f.client, f.db, providerpg.ProviderStoreOptions{Group: func(g *dbent.Group) *accessview.GroupConfig {
		return (*accessview.GroupConfig)(routingpg.GroupFromEnt(g))
	}})
	eligibility := billing.NewEligibility(billingredis.NewBillingCache(rdb), balanceReader{f.db}, nil, func() billing.EligibilityOptions { return billing.EligibilityOptions{} }, nil)
	eligibility.Start()
	t.Cleanup(eligibility.Stop)
	price := &pricing.ResolvedPricing{Mode: pricing.BillingModeToken, Source: pricing.PricingSourceConfig, BasePricing: &pricing.ModelPricing{InputPricePerToken: 0.01, OutputPricePerToken: 0.02}}
	for _, tc := range []struct {
		name                string
		stream, partial     bool
		timeout, disconnect bool
	}{{name: "nonstream"}, {name: "stream", stream: true}, {name: "partial-failure", stream: true, partial: true}, {name: "upstream-timeout", stream: true, partial: true, timeout: true}, {name: "client-disconnect-tail-usage", stream: true, disconnect: true}} {
		t.Run(tc.name, func(t *testing.T) {
			uid := uuid.NewString()
			user, err := f.client.User.Create().SetEmail(uid + "@test.test").SetPasswordHash("fixture-only").SetBalance(10).SetConcurrency(1).Save(ctx)
			require.NoError(t, err)
			group, err := f.client.Group.Create().SetName("test-" + uid).SetAllowedProtocols([]protocol.ProtocolID{protocol.ProtocolOpenAIChatCompletions}).SetProtocolFallbacks(map[protocol.ProtocolID][]protocol.ProtocolID{protocol.ProtocolOpenAIChatCompletions: {protocol.ProtocolQoderChat}}).Save(ctx)
			require.NoError(t, err)
			key, err := f.client.APIKey.Create().SetUserID(user.ID).SetGroupID(group.ID).SetKey("sk-test-" + uid).SetName("fixture").SetQuota(100).SetBillingMode("balance").Save(ctx)
			require.NoError(t, err)
			acc, err := f.client.Provider.Create().SetName("test-" + uid).SetPlatform("qoder").SetType("cosy").SetCredentials(map[string]any{"upstream_protocols": []string{"qoder_chat"}}).SetConcurrency(1).AddGroupIDs(group.ID).Save(ctx)
			require.NoError(t, err)
			var calls, completed atomic.Int32
			nativeBody := successfulQoderStream
			if tc.partial {
				nativeBody = strings.Replace(nativeBody, "data: {\"body\":\"[DONE]\"}\n\n", qoderFailureFrame, 1)
			}
			tailAllowed := make(chan struct{})
			clientGone := make(chan struct{})
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "auto", r.Header.Get("x-model-key"))
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.timeout || tc.disconnect {
					first, tail, _ := strings.Cut(successfulQoderStream, "\n\n")
					_, _ = io.WriteString(w, first+"\n\n")
					flush, ok := w.(http.Flusher)
					require.True(t, ok)
					flush.Flush()
					if tc.timeout {
						_, _ = io.WriteString(w, strings.Replace(tail, "data: {\"body\":\"[DONE]\"}\n\n", "", 1))
						flush.Flush()
						<-r.Context().Done()
						return
					}
					select {
					case <-tailAllowed:
					case <-r.Context().Done():
						return
					}
					_, _ = io.WriteString(w, tail)
					return
				}
				_, _ = io.WriteString(w, nativeBody)
			}))
			defer provider.Close()
			profile := qoder.MustProfileForSite(qoder.SiteGlobal)
			profile.GatewayBaseURL = provider.URL
			client := qoder.NewClientForProfile(profile)
			session, err := qoder.NewSessionForSite(&qoder.AuthIdentity{UID: "fixture", SecurityOauthToken: "fixture"}, qoder.NewMachine(), qoder.SiteGlobal)
			require.NoError(t, err)
			options := qoder.ExecuteOptions{}
			if tc.timeout {
				options.Timeout = 100 * time.Millisecond
			}
			executor := qoder.NewExecutor(options)
			done := make(chan error, 2)
			var last upstream.AttemptResult
			complete := func(_ context.Context, result upstream.AttemptResult) {
				last = result
				pool.Submit(func(taskCtx context.Context) {
					cost, costErr := pricing.CalculateCost(price, pricing.CostInput{Model: "auto", Tokens: pricing.UsageTokens{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens}, RateMultiplier: 1})
					if costErr != nil {
						done <- costErr
						return
					}
					command := &billing.UsageBillingCommand{RequestID: uid, APIKeyID: key.ID, UserID: user.ID, ActorUserID: user.ID, ProviderID: acc.ID, ProviderType: "cosy", GroupID: &group.ID, APIKeyBillingMode: "balance", Model: "auto", InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens, BillableAmountUSD: cost.ActualCost, BaseAmountUSD: cost.TotalCost, BalanceRateMultiplier: 1, APIKeyQuotaCost: cost.ActualCost, ProviderQuotaCost: cost.TotalCost * 2}
					settled, settleErr := funds.Settle(taskCtx, command)
					if settleErr != nil {
						done <- settleErr
						return
					}
					if settled.Applied {
						completed.Add(1)
					}
					providerCost := cost.TotalCost * 2
					_, createErr := facts.Create(taskCtx, &usage.UsageLog{UserID: user.ID, BillingUserID: user.ID, APIKeyID: key.ID, ProviderID: acc.ID, GroupID: &group.ID, RequestID: uid, Model: "auto", InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens, TotalCost: cost.TotalCost, ActualCost: cost.ActualCost, BalanceAmountUSD: cost.ActualCost, ProviderStatsCost: &providerCost, RateMultiplier: 1, Stream: tc.stream, CreatedAt: time.Now()})
					done <- createErr
				})
			}

			// The HTTP executor receives authentication and body data for each request.
			runtime := &storageQoderRuntime{}
			runtime.prepare = func(callCtx context.Context, request gateway.Request) (gateway.Request, error) {
				grp, err := groups.GetByID(callCtx, group.ID)
				if err != nil {
					return gateway.Request{}, err
				}
				request.Route = routing.Plan(routing.PlanInput{Group: grp, GroupID: &group.ID, RequestedModel: request.Model, ClientProtocol: protocol.ProtocolOpenAIChatCompletions, GroupMapping: routing.GroupMappingResult{ClientModel: request.Model, MappedModel: request.Model}})
				return request, nil
			}
			runtime.check = func(callCtx context.Context) error {
				return eligibility.Check(callCtx, billing.CheckInput{Payer: &billing.UserSummary{ID: user.ID}, Key: &billing.KeySnapshot{ID: key.ID, BillingMode: "balance"}, Group: &billing.GroupSnapshot{ID: group.ID}, Platform: "qoder"})
			}

			runtime.selectProvider = func(callCtx context.Context, request gateway.Request, excluded map[int64]struct{}) (*gateway.Selection, error) {
				record, loadErr := providers.GetByID(callCtx, acc.ID)
				if loadErr != nil {
					return nil, loadErr
				}
				snapshot := record.RoutingSnapshot()
				selectionInput := scheduler.SelectionInput{RoutePlan: request.Route, Candidates: []scheduler.SelectionCandidate{{Snapshot: &snapshot}}, ExcludedIDs: excluded}
				fresh := func(ctx context.Context, candidate scheduler.SelectionCandidate) (scheduler.SelectionCandidate, bool) {
					r, e := providers.GetByID(ctx, acc.ID)
					if e != nil {
						return candidate, false
					}
					value := r.RoutingSnapshot()
					candidate.Snapshot = &value
					cp, ok := request.Route.ResolveCandidate(value)
					candidate.Plan = &cp
					return candidate, ok
				}
				selected, ok, selectErr := scheduler.RequestLease(callCtx).Select(callCtx, selectionInput, scheduler.AttemptSelectionPorts{Acquire: func(ctx context.Context, id int64, limit int) (*scheduler.AcquireResult, bool, error) {
					r, e := concur.AcquireProviderSlot(ctx, id, limit)
					return r, true, e
				}, Fresh: fresh, CanRecheck: func() bool { return true }, Recheck: fresh, CompactAllowed: func(scheduler.SelectionCandidate) bool { return true }})
				if selectErr != nil {
					return nil, selectErr
				}
				if !ok {
					return nil, fmt.Errorf("fixture candidate unavailable")
				}
				target := &qoder.Target{ProviderID: acc.ID, Site: qoder.SiteGlobal, UserType: "personal_standard", Metadata: qoder.RequestMetadata{APIKeyID: key.ID}, Session: func(context.Context) (*qoder.SessionContext, error) { return session, nil }, Client: func() (qoder.StreamClient, error) { return client, nil }}
				return &gateway.Selection{Snapshot: snapshot, Plan: *selected.Candidate.Plan, Acquired: true, Release: selected.Attempt.Release, Executor: executor, Input: upstream.AttemptInput{Protocol: protocol.ProtocolOpenAIChatCompletions, Body: request.Body, Stream: request.Stream, ResponseModel: request.Model, Target: target}, Complete: complete}, nil
			}
			handler := &gatewayhttp.QoderChatHandler{Executor: gateway.NewQoderExecutor(3, time.Second, concur, runtime)}
			handler.PrepareRequest = func(c *gin.Context, p gatewayhttp.ParsedRequest) (gateway.Request, error) {
				if tc.disconnect {
					requestCtx := c.Request.Context()
					go func() { <-requestCtx.Done(); close(clientGone) }()
				}
				value, _ := c.Get("access")
				access, ok := value.(*apikey.AccessSnapshot)
				require.True(t, ok)
				return gateway.Request{Access: access, UserID: user.ID, Concurrency: 1, Stream: p.Stream, Body: p.Body, Model: p.Model}, nil
			}

			router := gin.New()
			router.Use(func(c *gin.Context) {
				a, ok := keyhttp.Authenticate(c, auth, keyhttp.AuthenticationOptions{})
				if !ok {
					c.Abort()
					return
				}
				c.Set("access", a)
				c.Next()
			})
			router.POST("/v1/chat/completions", handler.ChatCompletions)
			server := httptest.NewServer(router)
			defer server.Close()
			req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"auto","stream":%t,"messages":[{"role":"user","content":"hi"}]}`, tc.stream)))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+key.Key)
			response, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			if tc.disconnect {
				reader := bufio.NewReader(response.Body)
				for {
					line, readErr := reader.ReadString('\n')
					require.NoError(t, readErr)
					if strings.Contains(line, "served") {
						break
					}
				}
				require.NoError(t, response.Body.Close())
				select {
				case <-clientGone:
				case <-time.After(time.Second):
					t.Fatal("client cancellation not observed")
				}
				userSlots, readErr := concurCache.GetUserConcurrency(ctx, user.ID)
				require.NoError(t, readErr)
				require.Equal(t, 1, userSlots)
				providerSlots, readErr := concurCache.GetProviderConcurrency(ctx, acc.ID)
				require.NoError(t, readErr)
				require.Equal(t, 1, providerSlots)
				close(tailAllowed)
			} else {
				data, readErr := io.ReadAll(response.Body)
				require.NoError(t, readErr)
				require.NoError(t, response.Body.Close())
				require.Equal(t, 200, response.StatusCode, string(data))
				require.Contains(t, string(data), "served")
				if tc.partial {
					require.Contains(t, string(data), "upstream_error")
					require.Equal(t, 1, strings.Count(string(data), "data: [DONE]"))
				}
			}

			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("completion did not finish")
			}
			// Replaying completion checks persistent idempotency; the upstream request count remains one.
			complete(ctx, last)
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("completion replay did not finish")
			}
			require.EqualValues(t, 1, calls.Load())
			require.EqualValues(t, 1, completed.Load())
			var balance, quota float64
			var count int
			require.NoError(t, f.db.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", user.ID).Scan(&balance))
			require.InDelta(t, 9.82, balance, 1e-8)
			require.NoError(t, f.db.QueryRowContext(ctx, "SELECT quota_used FROM api_keys WHERE id=$1", key.ID).Scan(&quota))
			require.InDelta(t, 0.18, quota, 1e-8)
			require.NoError(t, f.db.QueryRowContext(ctx, "SELECT count(*) FROM usage_logs WHERE request_id=$1", uid).Scan(&count))
			require.Equal(t, 1, count)
			var actual, providerCost float64
			require.NoError(t, f.db.QueryRowContext(ctx, "SELECT actual_cost,provider_stats_cost FROM usage_logs WHERE request_id=$1", uid).Scan(&actual, &providerCost))
			require.InDelta(t, 0.18, actual, 1e-8)
			require.InDelta(t, 0.36, providerCost, 1e-8)
			slots, err := concurCache.GetUserConcurrency(ctx, user.ID)
			require.NoError(t, err)
			require.Zero(t, slots)
			slots, err = concurCache.GetProviderConcurrency(ctx, acc.ID)
			require.NoError(t, err)
			require.Zero(t, slots)
		})
	}
}

func (r *storageQoderRuntime) Prepare(ctx context.Context, v gateway.Request) (gateway.Request, error) {
	return r.prepare(ctx, v)
}

func (r *storageQoderRuntime) Check(ctx context.Context, _ gateway.Request, _ bool) error {
	return r.check(ctx)
}

func (r *storageQoderRuntime) Select(ctx context.Context, v gateway.Request, excluded map[int64]struct{}) (*gateway.Selection, error) {
	return r.selectProvider(ctx, v, excluded)
}
func (*storageQoderRuntime) CanRefresh(error) bool      { return false }
func (*storageQoderRuntime) CanFailover(error) bool     { return true }
func (*storageQoderRuntime) RefreshPending(error) bool  { return false }
func (*storageQoderRuntime) QueueFailure(string, error) {}
