package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

const (
	usageLogCreateStateQueued int32 = iota
	usageLogCreateStateProcessing
	usageLogCreateStateCompleted
	usageLogCreateStateCanceled

	usageLogCreateBatchMaxSize  = 64
	usageLogCreateBatchWindow   = 3 * time.Millisecond
	usageLogCreateBatchQueueCap = 4096
	usageLogCreateCancelWait    = 2 * time.Second

	usageLogBestEffortBatchMaxSize  = 256
	usageLogBestEffortBatchWindow   = 20 * time.Millisecond
	usageLogBestEffortBatchQueueCap = 32768
	usageLogBestEffortRecentTTL     = 30 * time.Second

	// usageLogInsertColumns 与参数和类型列表按位置对应。
	usageLogInsertColumns = `
			user_id,
			billing_user_id,
			team_id,
			api_key_id,
			provider_id,
			request_id,
			model,
			requested_model,
			upstream_model,
			group_id,
			subscription_id,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			cache_creation_5m_tokens,
			cache_creation_1h_tokens,
			image_output_tokens,
			image_output_cost,
			image_input_tokens,
			image_input_cost,
			input_cost,
			output_cost,
			cache_creation_cost,
			cache_read_cost,
			total_cost,
			actual_cost,
			subscription_amount_usd,
			balance_amount_usd,
			billing_allocations,
			rate_multiplier,
			provider_rate_multiplier,
			billing_type,
			request_type,
			stream,
			openai_ws_mode,
			duration_ms,
			first_token_ms,
			user_agent,
			ip_address,
			image_count,
			image_size,
			image_input_size,
			image_output_size,
			image_size_source,
			image_size_breakdown,
			video_count,
			video_resolution,
			video_duration_seconds,
			service_tier,
			reasoning_effort,
			inbound_endpoint,
			upstream_endpoint,
			cache_ttl_overridden,
			long_context_billing_applied,
			pricing_config_id,
			model_mapping_chain,
			billing_tier,
			billing_mode,
			provider_stats_cost,
			upstream_request_id,
			session_id,
			created_at,
			requested_reasoning_effort,
			native_compaction_v2,
			platform,
			upstream_response_model,
			upstream_model_mismatch,
			billing_key
		`

	usageLogInsertSQL = `
		INSERT INTO usage_logs (` + usageLogInsertColumns + `) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11,
			$12, $13, $14, $15,
			$16, $17, $18, $19,
			$20, $21, $22, $23, $24, $25,
			$26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38, $39, $40, $41, $42, $43, $44, $45, $46, $47, $48, $49, $50, $51, $52, $53, $54, $55, $56, $57, $58, $59, $60, $61, $62, $63, $64, $65, $66, $67, $68, $69
		)
		ON CONFLICT DO NOTHING
	`
)

// usageLogInsertArgTypes 必须与以下位置保持相同顺序：
//  1. prepareUsageLogInsert().args
//  2. usageLogInsertColumns
//  3. usageLogInsertSQL 占位符位置
//  4. scanUsageLog 通过 usageLogSelectColumns 读取的列顺序
//
// 新增 usage_logs 列时必须同步更新以上所有调用点。
var usageLogInsertArgTypes = [...]string{
	"bigint",      // user_id
	"bigint",      // billing_user_id
	"bigint",      // team_id
	"bigint",      // api_key_id
	"bigint",      // provider_id
	"text",        // request_id
	"text",        // model
	"text",        // requested_model
	"text",        // upstream_model
	"bigint",      // group_id
	"bigint",      // subscription_id
	"integer",     // input_tokens
	"integer",     // output_tokens
	"integer",     // cache_creation_tokens
	"integer",     // cache_read_tokens
	"integer",     // cache_creation_5m_tokens
	"integer",     // cache_creation_1h_tokens
	"integer",     // image_output_tokens
	"numeric",     // image_output_cost
	"integer",     // 图片输入 token
	"numeric",     // 图片输入成本
	"numeric",     // input_cost
	"numeric",     // output_cost
	"numeric",     // cache_creation_cost
	"numeric",     // cache_read_cost
	"numeric",     // total_cost
	"numeric",     // actual_cost
	"numeric",     // subscription_amount_usd
	"numeric",     // balance_amount_usd
	"jsonb",       // billing_allocations
	"numeric",     // rate_multiplier
	"numeric",     // provider_rate_multiplier
	"smallint",    // billing_type
	"smallint",    // request_type
	"boolean",     // stream
	"boolean",     // openai_ws_mode
	"integer",     // duration_ms
	"integer",     // first_token_ms
	"text",        // user_agent
	"text",        // ip_address
	"integer",     // image_count
	"text",        // image_size
	"text",        // 图片输入尺寸
	"text",        // 图片输出尺寸
	"text",        // 图片尺寸来源
	"jsonb",       // 图片尺寸明细
	"integer",     // 视频数量
	"text",        // 视频分辨率
	"integer",     // 视频时长（秒）
	"text",        // service_tier
	"text",        // reasoning_effort
	"text",        // inbound_endpoint
	"text",        // upstream_endpoint
	"boolean",     // cache_ttl_overridden
	"boolean",     // long_context_billing_applied
	"bigint",      // pricing_config_id
	"text",        // model_mapping_chain
	"text",        // billing_tier
	"text",        // billing_mode
	"numeric",     // provider_stats_cost
	"text",        // upstream_request_id
	"text",        // session_id
	"timestamptz", // created_at
	"text",        // requested_reasoning_effort
	"boolean",     // native_compaction_v2
	"text",        // 实际执行平台快照
	"text",        // upstream_response_model
	"boolean",     // upstream_model_mismatch
	"text",        // billing_key
}

type usageLogCreateRequest struct {
	log      *usage.UsageLog
	prepared usageLogInsertPrepared
	shared   *usageLogCreateShared
	resultCh chan usageLogCreateResult
}

type usageLogCreateResult struct {
	inserted bool
	err      error
}

type usageLogBestEffortRequest struct {
	prepared usageLogInsertPrepared
	apiKeyID int64
	resultCh chan error
}

type usageLogInsertPrepared struct {
	createdAt      time.Time
	requestID      string
	rateMultiplier float64
	requestType    int16
	args           []any
}

type usageLogBatchState struct {
	ID        int64
	CreatedAt time.Time
}

type usageLogBatchRow struct {
	RequestID string    `json:"request_id"`
	APIKeyID  int64     `json:"api_key_id"`
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Inserted  bool      `json:"inserted"`
}

type usageLogCreateShared struct {
	state atomic.Int32
}

func (r *Store) Create(ctx context.Context, log *usage.UsageLog) (bool, error) {
	if log == nil {
		return false, nil
	}

	if tx := dbent.TxFromContext(ctx); tx != nil {
		return r.createSingle(ctx, tx.Client(), log)
	}
	requestID := strings.TrimSpace(log.RequestID)
	if requestID == "" {
		return r.createSingle(ctx, r.sql, log)
	}
	log.RequestID = requestID
	return r.createBatched(ctx, log)
}

func (r *Store) CreateBestEffort(ctx context.Context, log *usage.UsageLog) error {
	r.batchLifecycleMu.RLock()
	defer r.batchLifecycleMu.RUnlock()
	if r.batchStopped {
		return usage.MarkUsageLogCreateDropped(fmt.Errorf("usage log batcher stopped"))
	}

	if log == nil {
		return nil
	}

	if tx := dbent.TxFromContext(ctx); tx != nil {
		_, err := r.createSingle(ctx, tx.Client(), log)
		return err
	}
	if r.db == nil {
		_, err := r.createSingle(ctx, r.sql, log)
		return err
	}

	r.ensureBestEffortBatcher()
	if r.bestEffortBatchCh == nil {
		_, err := r.createSingle(ctx, r.sql, log)
		return err
	}

	req := usageLogBestEffortRequest{
		prepared: prepareUsageLogInsert(log),
		apiKeyID: log.APIKeyID,
		resultCh: make(chan error, 1),
	}
	if key, ok := r.bestEffortRecentKey(req.prepared.requestID, req.apiKeyID); ok {
		if _, exists := r.bestEffortRecent.Get(key); exists {
			return nil
		}
	}

	// 队列满时等待批处理器腾出空间后入队。
	// 立即丢弃会造成“已扣费但无 usage_log”的永久数据缺口（issue #3656）；
	// 阻塞上限由调用方 ctx 期限约束，超时后由上层同步兜底。
	select {
	case r.bestEffortBatchCh <- req:
	case <-ctx.Done():
		return usage.MarkUsageLogCreateDropped(ctx.Err())
	}

	select {
	case err := <-req.resultCh:
		return err
	case <-ctx.Done():
		return usage.MarkUsageLogCreateDropped(ctx.Err())
	}
}

func (r *Store) createSingle(ctx context.Context, sqlq sqlExecutor, log *usage.UsageLog) (bool, error) {
	prepared := prepareUsageLogInsert(log)
	if sqlq == nil {
		sqlq = r.sql
	}
	if ctx != nil && ctx.Err() != nil {
		return false, usage.MarkUsageLogCreateNotPersisted(ctx.Err())
	}

	query := usageLogInsertSQL + " RETURNING id, created_at"

	if err := scanSingleRow(ctx, sqlq, query, prepared.args, &log.ID, &log.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) && prepared.requestID != "" {
			billingKey := log.BillingKey
			if billingKey == "" {
				billingKey = prepared.requestID
			}
			selectQuery := "SELECT id, created_at FROM usage_logs WHERE (request_id = $1 OR COALESCE(billing_key, request_id) = $3) AND api_key_id = $2"
			if err := scanSingleRow(ctx, sqlq, selectQuery, []any{prepared.requestID, log.APIKeyID, billingKey}, &log.ID, &log.CreatedAt); err != nil {
				return false, err
			}
			log.RateMultiplier = prepared.rateMultiplier
			return false, nil
		}
		return false, err
	}
	log.RateMultiplier = prepared.rateMultiplier
	return true, nil
}

func (r *Store) createBatched(ctx context.Context, log *usage.UsageLog) (bool, error) {
	r.batchLifecycleMu.RLock()
	defer r.batchLifecycleMu.RUnlock()
	if r.batchStopped {
		return false, usage.MarkUsageLogCreateNotPersisted(fmt.Errorf("usage log batcher stopped"))
	}

	if r.db == nil {
		return r.createSingle(ctx, r.sql, log)
	}
	r.ensureCreateBatcher()
	if r.createBatchCh == nil {
		return r.createSingle(ctx, r.sql, log)
	}

	req := usageLogCreateRequest{
		log:      log,
		prepared: prepareUsageLogInsert(log),
		shared:   &usageLogCreateShared{},
		resultCh: make(chan usageLogCreateResult, 1),
	}

	// 同步补写在队列满时等待可用空间，
	// 立即失败会让日志永久丢失；阻塞上限由调用方 ctx 期限约束。
	select {
	case r.createBatchCh <- req:
	case <-ctx.Done():
		return false, usage.MarkUsageLogCreateNotPersisted(ctx.Err())
	}

	select {
	case res := <-req.resultCh:
		return res.inserted, res.err
	case <-ctx.Done():
		if req.shared != nil && req.shared.state.CompareAndSwap(usageLogCreateStateQueued, usageLogCreateStateCanceled) {
			return false, usage.MarkUsageLogCreateNotPersisted(ctx.Err())
		}
		timer := time.NewTimer(usageLogCreateCancelWait)
		defer timer.Stop()
		select {
		case res := <-req.resultCh:
			return res.inserted, res.err
		case <-timer.C:
			return false, ctx.Err()
		}
	}
}

func (r *Store) ensureCreateBatcher() {
	if r == nil || r.db == nil {
		return
	}
	// nil 检查必须在 Once 内部：在外层做无同步快路径读会与 Once 内的写构成数据竞争。
	r.createBatchOnce.Do(func() {
		if r.createBatchCh == nil {
			r.createBatchCh = make(chan usageLogCreateRequest, usageLogCreateBatchQueueCap)
			r.batchWG.Go(func() { ; r.runCreateBatcher(r.db) })
		}
	})
}

func (r *Store) ensureBestEffortBatcher() {
	if r == nil || r.db == nil {
		return
	}
	// 同 ensureCreateBatcher：nil 检查放在 Once 内部以避免数据竞争。
	r.bestEffortBatchOnce.Do(func() {
		if r.bestEffortBatchCh == nil {
			r.bestEffortBatchCh = make(chan usageLogBestEffortRequest, usageLogBestEffortBatchQueueCap)
			r.batchWG.Go(func() { ; r.runBestEffortBatcher(r.db) })
		}
	})
}

func (r *Store) runCreateBatcher(db *sql.DB) {
	for {
		first, ok := <-r.createBatchCh
		if !ok {
			return
		}

		batch := make([]usageLogCreateRequest, 0, usageLogCreateBatchMaxSize)
		batch = append(batch, first)

		timer := time.NewTimer(usageLogCreateBatchWindow)
	batchLoop:
		for len(batch) < usageLogCreateBatchMaxSize {
			select {
			case req, ok := <-r.createBatchCh:
				if !ok {
					break batchLoop
				}
				batch = append(batch, req)
			case <-timer.C:
				break batchLoop
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}

		r.flushCreateBatch(db, batch)
	}
}

func (r *Store) runBestEffortBatcher(db *sql.DB) {
	for {
		first, ok := <-r.bestEffortBatchCh
		if !ok {
			return
		}

		batch := make([]usageLogBestEffortRequest, 0, usageLogBestEffortBatchMaxSize)
		batch = append(batch, first)

		timer := time.NewTimer(usageLogBestEffortBatchWindow)
	bestEffortLoop:
		for len(batch) < usageLogBestEffortBatchMaxSize {
			select {
			case req, ok := <-r.bestEffortBatchCh:
				if !ok {
					break bestEffortLoop
				}
				batch = append(batch, req)
			case <-timer.C:
				break bestEffortLoop
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}

		r.flushBestEffortBatch(db, batch)
	}
}

func (r *Store) flushCreateBatch(db *sql.DB, batch []usageLogCreateRequest) {
	if len(batch) == 0 {
		return
	}

	uniqueOrder := make([]string, 0, len(batch))
	preparedByKey := make(map[string]usageLogInsertPrepared, len(batch))
	requestsByKey := make(map[string][]usageLogCreateRequest, len(batch))
	fallback := make([]usageLogCreateRequest, 0)

	for _, req := range batch {
		if req.log == nil {
			completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: false, err: nil})
			continue
		}
		if req.shared != nil && !req.shared.state.CompareAndSwap(usageLogCreateStateQueued, usageLogCreateStateProcessing) {
			if req.shared.state.Load() == usageLogCreateStateCanceled {
				completeUsageLogCreateRequest(req, usageLogCreateResult{
					inserted: false,
					err:      usage.MarkUsageLogCreateNotPersisted(context.Canceled),
				})
				continue
			}
		}
		prepared := req.prepared
		if prepared.requestID == "" {
			fallback = append(fallback, req)
			continue
		}
		key := usageLogBatchKey(prepared.requestID, req.log.APIKeyID)
		if _, exists := requestsByKey[key]; !exists {
			uniqueOrder = append(uniqueOrder, key)
			preparedByKey[key] = prepared
		}
		requestsByKey[key] = append(requestsByKey[key], req)
	}

	if len(uniqueOrder) > 0 {
		insertedMap, stateMap, safeFallback, err := r.batchInsertUsageLogs(db, uniqueOrder, preparedByKey)
		if err != nil {
			if safeFallback {
				for _, key := range uniqueOrder {
					fallback = append(fallback, requestsByKey[key]...)
				}
			} else {
				for _, key := range uniqueOrder {
					reqs := requestsByKey[key]
					state, hasState := stateMap[key]
					inserted := insertedMap[key]
					for idx, req := range reqs {
						req.log.RateMultiplier = preparedByKey[key].rateMultiplier
						if hasState {
							req.log.ID = state.ID
							req.log.CreatedAt = state.CreatedAt
						}
						switch {
						case inserted && idx == 0:
							completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: true, err: nil})
						case inserted:
							completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: false, err: nil})
						case hasState:
							completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: false, err: nil})
						case idx == 0:
							completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: false, err: err})
						default:
							completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: false, err: nil})
						}
					}
				}
			}
		} else {
			for _, key := range uniqueOrder {
				reqs := requestsByKey[key]
				state, ok := stateMap[key]
				if !ok {
					for _, req := range reqs {
						completeUsageLogCreateRequest(req, usageLogCreateResult{
							inserted: false,
							err:      fmt.Errorf("usage log batch state missing for key=%s", key),
						})
					}
					continue
				}
				for idx, req := range reqs {
					req.log.ID = state.ID
					req.log.CreatedAt = state.CreatedAt
					req.log.RateMultiplier = preparedByKey[key].rateMultiplier
					completeUsageLogCreateRequest(req, usageLogCreateResult{
						inserted: idx == 0 && insertedMap[key],
						err:      nil,
					})
				}
			}
		}
	}

	if len(fallback) == 0 {
		return
	}

	for _, req := range fallback {
		fallbackCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		inserted, err := r.createSingle(fallbackCtx, db, req.log)
		cancel()
		completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: inserted, err: err})
	}
}

func (r *Store) flushBestEffortBatch(db *sql.DB, batch []usageLogBestEffortRequest) {
	if len(batch) == 0 {
		return
	}

	type bestEffortGroup struct {
		prepared usageLogInsertPrepared
		apiKeyID int64
		key      string
		reqs     []usageLogBestEffortRequest
	}

	groupsByKey := make(map[string]*bestEffortGroup, len(batch))
	groupOrder := make([]*bestEffortGroup, 0, len(batch))
	preparedList := make([]usageLogInsertPrepared, 0, len(batch))

	for idx, req := range batch {
		prepared := req.prepared
		key := fmt.Sprintf("__best_effort_%d", idx)
		if prepared.requestID != "" {
			key = usageLogBatchKey(prepared.requestID, req.apiKeyID)
		}
		group, exists := groupsByKey[key]
		if !exists {
			group = &bestEffortGroup{
				prepared: prepared,
				apiKeyID: req.apiKeyID,
				key:      key,
			}
			groupsByKey[key] = group
			groupOrder = append(groupOrder, group)
			preparedList = append(preparedList, prepared)
		}
		group.reqs = append(group.reqs, req)
	}

	if len(preparedList) == 0 {
		for _, req := range batch {
			sendUsageLogBestEffortResult(req.resultCh, nil)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query, args := buildUsageLogBestEffortInsertQuery(preparedList)
	startedAt := time.Now()
	_, err := retryPostgresDeadlock(ctx, "usage_log_best_effort_batch_insert", len(preparedList), func() (sql.Result, error) {
		return db.ExecContext(ctx, query, args...)
	})
	if err != nil {
		sqlState := postgresSQLState(err)
		if sqlState == "" {
			sqlState = "unknown"
		}
		attempts := 1
		if isPostgresDeadlock(err) {
			attempts = postgresDeadlockMaxAttempts
		}
		logger.LegacyPrintf(
			"repository.usage_log",
			"operation=usage_log_best_effort_batch_insert sqlstate=%s attempt=%d batch_size=%d retry_succeeded=false fallback_failed=false elapsed_ms=%d error=%v",
			sqlState,
			attempts,
			len(preparedList),
			time.Since(startedAt).Milliseconds(),
			err,
		)
		fallbackFailures := 0
		for _, group := range groupOrder {
			singleErr := execUsageLogInsertNoResult(ctx, db, group.prepared)
			if singleErr != nil {
				fallbackFailures++
				fallbackSQLState := postgresSQLState(singleErr)
				if fallbackSQLState == "" {
					fallbackSQLState = "unknown"
				}
				logger.LegacyPrintf(
					"repository.usage_log",
					"operation=usage_log_best_effort_single_fallback sqlstate=%s attempt=1 batch_size=1 retry_succeeded=false fallback_failed=true elapsed_ms=%d error=%v",
					fallbackSQLState,
					time.Since(startedAt).Milliseconds(),
					singleErr,
				)
			} else if group.prepared.requestID != "" && r != nil && r.bestEffortRecent != nil {
				r.bestEffortRecent.SetDefault(group.key, struct{}{})
			}
			for _, req := range group.reqs {
				sendUsageLogBestEffortResult(req.resultCh, singleErr)
			}
		}
		logger.LegacyPrintf(
			"repository.usage_log",
			"operation=usage_log_best_effort_single_fallback sqlstate=%s attempt=1 batch_size=%d retry_succeeded=false fallback_failed=%t elapsed_ms=%d fallback_failures=%d",
			sqlState,
			len(groupOrder),
			fallbackFailures > 0,
			time.Since(startedAt).Milliseconds(),
			fallbackFailures,
		)
		return
	}
	for _, group := range groupOrder {
		if group.prepared.requestID != "" && r != nil && r.bestEffortRecent != nil {
			r.bestEffortRecent.SetDefault(group.key, struct{}{})
		}
		for _, req := range group.reqs {
			sendUsageLogBestEffortResult(req.resultCh, nil)
		}
	}
}

func sendUsageLogBestEffortResult(ch chan error, err error) {
	if ch == nil {
		return
	}
	select {
	case ch <- err:
	default:
	}
}

func completeUsageLogCreateRequest(req usageLogCreateRequest, res usageLogCreateResult) {
	if req.shared != nil {
		req.shared.state.Store(usageLogCreateStateCompleted)
	}
	sendUsageLogCreateResult(req.resultCh, res)
}

func (r *Store) batchInsertUsageLogs(db *sql.DB, keys []string, preparedByKey map[string]usageLogInsertPrepared) (map[string]bool, map[string]usageLogBatchState, bool, error) {
	if len(keys) == 0 {
		return map[string]bool{}, map[string]usageLogBatchState{}, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query, args := buildUsageLogBatchInsertQuery(keys, preparedByKey)
	var payload []byte
	if err := db.QueryRowContext(ctx, query, args...).Scan(&payload); err != nil {
		return nil, nil, true, err
	}
	var rows []usageLogBatchRow
	if err := json.Unmarshal(payload, &rows); err != nil {
		return nil, nil, false, err
	}
	insertedMap := make(map[string]bool, len(keys))
	stateMap := make(map[string]usageLogBatchState, len(keys))
	for _, row := range rows {
		key := usageLogBatchKey(row.RequestID, row.APIKeyID)
		insertedMap[key] = row.Inserted
		stateMap[key] = usageLogBatchState{
			ID:        row.ID,
			CreatedAt: row.CreatedAt,
		}
	}
	if len(stateMap) != len(keys) {
		return insertedMap, stateMap, false, fmt.Errorf("usage log batch state count mismatch: got=%d want=%d", len(stateMap), len(keys))
	}
	return insertedMap, stateMap, false, nil
}

func buildUsageLogBatchInsertQuery(keys []string, preparedByKey map[string]usageLogInsertPrepared) (string, []any) {
	var query strings.Builder
	_, _ = query.WriteString(`
		WITH input (
			input_idx,` + usageLogInsertColumns + `) AS (VALUES `)

	args := make([]any, 0, len(keys)*(len(usageLogInsertArgTypes)+1))
	argPos := 1
	for idx, key := range keys {
		if idx > 0 {
			_, _ = query.WriteString(",")
		}
		_, _ = query.WriteString("(")
		_, _ = query.WriteString("$")
		_, _ = query.WriteString(strconv.Itoa(argPos))
		args = append(args, idx)
		argPos++
		prepared := preparedByKey[key]
		for i := range len(prepared.args) {
			_, _ = query.WriteString(",")
			_, _ = query.WriteString("$")
			_, _ = query.WriteString(strconv.Itoa(argPos))
			if i < len(usageLogInsertArgTypes) {
				_, _ = query.WriteString("::")
				_, _ = query.WriteString(usageLogInsertArgTypes[i])
			}
			argPos++
		}
		_, _ = query.WriteString(")")
		args = append(args, prepared.args...)
	}
	_, _ = query.WriteString(`
		),
		inserted AS (
			INSERT INTO usage_logs (` + usageLogInsertColumns + `)
			SELECT` + usageLogInsertColumns + `FROM input
			ON CONFLICT DO NOTHING
			RETURNING request_id, billing_key, api_key_id, id, created_at
		),
		resolved AS (
			SELECT
				input.input_idx,
				input.request_id,
				input.api_key_id,
				COALESCE(inserted.id, existing.id) AS id,
				COALESCE(inserted.created_at, existing.created_at) AS created_at,
				(inserted.request_id = input.request_id) AS inserted
			FROM input
			LEFT JOIN inserted
				ON (inserted.request_id = input.request_id OR COALESCE(inserted.billing_key, inserted.request_id) = COALESCE(input.billing_key, input.request_id))
				AND inserted.api_key_id = input.api_key_id
			LEFT JOIN usage_logs existing
				ON (existing.request_id = input.request_id OR COALESCE(existing.billing_key, existing.request_id) = COALESCE(input.billing_key, input.request_id))
				AND existing.api_key_id = input.api_key_id
		)
		SELECT COALESCE(
			json_agg(
				json_build_object(
					'request_id', resolved.request_id,
					'api_key_id', resolved.api_key_id,
					'id', resolved.id,
					'created_at', resolved.created_at,
					'inserted', resolved.inserted
				)
				ORDER BY resolved.input_idx
			),
			'[]'::json
		)
		FROM resolved
	`)
	return query.String(), args
}

func buildUsageLogBestEffortInsertQuery(preparedList []usageLogInsertPrepared) (string, []any) {
	var query strings.Builder
	_, _ = query.WriteString(`
		WITH input (` + usageLogInsertColumns + `) AS (VALUES `)

	args := make([]any, 0, len(preparedList)*len(usageLogInsertArgTypes))
	argPos := 1
	for idx, prepared := range preparedList {
		if idx > 0 {
			_, _ = query.WriteString(",")
		}
		_, _ = query.WriteString("(")
		for i := range len(prepared.args) {
			if i > 0 {
				_, _ = query.WriteString(",")
			}
			_, _ = query.WriteString("$")
			_, _ = query.WriteString(strconv.Itoa(argPos))
			if i < len(usageLogInsertArgTypes) {
				_, _ = query.WriteString("::")
				_, _ = query.WriteString(usageLogInsertArgTypes[i])
			}
			argPos++
		}
		_, _ = query.WriteString(")")
		args = append(args, prepared.args...)
	}

	_, _ = query.WriteString(`
		)
		INSERT INTO usage_logs (` + usageLogInsertColumns + `)
		SELECT` + usageLogInsertColumns + `FROM input
		ON CONFLICT DO NOTHING
	`)

	return query.String(), args
}

func execUsageLogInsertNoResult(ctx context.Context, sqlq sqlExecutor, prepared usageLogInsertPrepared) error {
	_, err := sqlq.ExecContext(ctx, usageLogInsertSQL, prepared.args...)
	return err
}

func prepareUsageLogInsert(log *usage.UsageLog) usageLogInsertPrepared {
	createdAt := log.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	requestID := strings.TrimSpace(log.RequestID)
	log.RequestID = requestID

	rateMultiplier := log.RateMultiplier
	log.SyncRequestTypeAndLegacyFields()
	requestType := int16(log.RequestType)

	groupID := nullInt64(log.GroupID)
	subscriptionID := nullInt64(log.SubscriptionID)
	duration := nullInt(log.DurationMs)
	firstToken := nullInt(log.FirstTokenMs)
	userAgent := nullString(log.UserAgent)
	ipAddress := nullString(log.IPAddress)
	imageSize := nullString(log.ImageSize)
	imageInputSize := nullString(log.ImageInputSize)
	imageOutputSize := nullString(log.ImageOutputSize)
	imageSizeSource := nullString(log.ImageSizeSource)
	imageSizeBreakdown := nullStringIntMapJSON(log.ImageSizeBreakdown)
	videoResolution := nullString(log.VideoResolution)
	videoDurationSeconds := nullInt(log.VideoDurationSeconds)
	serviceTier := nullString(log.ServiceTier)
	reasoningEffort := nullString(log.ReasoningEffort)
	requestedReasoningEffort := nullString(log.RequestedReasoningEffort)
	inboundEndpoint := nullString(log.InboundEndpoint)
	upstreamEndpoint := nullString(log.UpstreamEndpoint)
	pricingConfigID := nullInt64(log.PricingConfigID)
	modelMappingChain := nullString(log.ModelMappingChain)
	billingTier := nullString(log.BillingTier)
	billingMode := nullString(log.BillingMode)
	upstreamRequestID := nullString(log.UpstreamRequestID)
	sessionID := nullString(log.SessionID)
	requestedModel := strings.TrimSpace(log.RequestedModel)
	if requestedModel == "" {
		requestedModel = strings.TrimSpace(log.Model)
	}
	upstreamModel := nullString(log.UpstreamModel)
	billingAllocations := log.BillingAllocations
	if len(billingAllocations) == 0 {
		billingAllocations = nil
	}
	billingAllocationsJSON, err := json.Marshal(billingAllocations)
	if err != nil {
		billingAllocationsJSON = []byte("[]")
	}
	if len(billingAllocationsJSON) == 0 || string(billingAllocationsJSON) == "null" {
		billingAllocationsJSON = []byte("[]")
	}

	var requestIDArg any
	if requestID != "" {
		requestIDArg = requestID
	}
	billingUserID := log.BillingUserID
	if billingUserID <= 0 {
		billingUserID = log.UserID
	}

	return usageLogInsertPrepared{
		createdAt:      createdAt,
		requestID:      requestID,
		rateMultiplier: rateMultiplier,
		requestType:    requestType,
		args: []any{
			log.UserID,
			billingUserID,
			nullInt64(log.TeamID),
			log.APIKeyID,
			log.ProviderID,
			requestIDArg,
			log.Model,
			nullString(&requestedModel),
			upstreamModel,
			groupID,
			subscriptionID,
			log.InputTokens,
			log.OutputTokens,
			log.CacheCreationTokens,
			log.CacheReadTokens,
			log.CacheCreation5mTokens,
			log.CacheCreation1hTokens,
			log.ImageOutputTokens,
			log.ImageOutputCost,
			log.ImageInputTokens,
			log.ImageInputCost,
			log.InputCost,
			log.OutputCost,
			log.CacheCreationCost,
			log.CacheReadCost,
			log.TotalCost,
			log.ActualCost,
			log.SubscriptionAmountUSD,
			log.BalanceAmountUSD,
			billingAllocationsJSON,
			rateMultiplier,
			log.ProviderRateMultiplier,
			log.BillingType,
			requestType,
			log.Stream,
			log.OpenAIWSMode,
			duration,
			firstToken,
			userAgent,
			ipAddress,
			log.ImageCount,
			imageSize,
			imageInputSize,
			imageOutputSize,
			imageSizeSource,
			imageSizeBreakdown,
			log.VideoCount,
			videoResolution,
			videoDurationSeconds,
			serviceTier,
			reasoningEffort,
			inboundEndpoint,
			upstreamEndpoint,
			log.CacheTTLOverridden,
			log.LongContextBillingApplied,
			pricingConfigID,
			modelMappingChain,
			billingTier,
			billingMode,
			log.ProviderStatsCost, // provider_stats_cost
			upstreamRequestID,     // upstream_request_id
			sessionID,             // session_id
			createdAt,
			requestedReasoningEffort,
			log.NativeCompactionV2,
			usagePlatformSnapshot(log.Platform),
			nullString(log.UpstreamResponseModel),
			log.UpstreamModelMismatch,
			nullStringValue(log.BillingKey),
		},
	}
}

func usageLogBatchKey(requestID string, apiKeyID int64) string {
	return requestID + "\x1f" + strconv.FormatInt(apiKeyID, 10)
}

func sendUsageLogCreateResult(ch chan usageLogCreateResult, res usageLogCreateResult) {
	if ch == nil {
		return
	}
	select {
	case ch <- res:
	default:
	}
}

func (r *Store) bestEffortRecentKey(requestID string, apiKeyID int64) (string, bool) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || r == nil || r.bestEffortRecent == nil {
		return "", false
	}
	return usageLogBatchKey(requestID, apiKeyID), true
}

// usagePlatformSnapshot 不从可变的提供商或分组关系反查历史归属。
func usagePlatformSnapshot(platform string) string {
	if value := strings.TrimSpace(platform); value != "" {
		return value
	}
	return "unknown"
}

// nullStringValue 把缺失的计费键写为 SQL NULL，供历史去重表达式回退。
func nullStringValue(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}
