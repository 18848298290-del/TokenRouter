package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

const (
	usageLogBestEffortBatchSQL  = `(?s)^\s*WITH input .*INSERT INTO usage_logs`
	usageLogBestEffortSingleSQL = `(?s)^\s*INSERT INTO usage_logs`
)

var (
	usageLogStaticInsertShapeRe = regexp.MustCompile(`(?s)INSERT INTO usage_logs \((.*?)\) VALUES \((.*?)\)`)
	usageLogPlaceholderRe       = regexp.MustCompile(`\$(\d+)`)
)

func TestFlushBestEffortBatch_RetriesDeadlockBeforeFallback(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() {
		_ = db.Close()
	}()

	for attempt := 1; attempt <= 3; attempt++ {
		expectation := mock.ExpectExec(usageLogBestEffortBatchSQL)
		if attempt < 3 {
			expectation.WillReturnError(&pq.Error{Code: "40P01"})
			continue
		}
		expectation.WillReturnResult(sqlmock.NewResult(0, 1))
	}

	req := newUsageLogBestEffortRequestForTest()
	repo := &Store{}
	repo.flushBestEffortBatch(db, []usageLogBestEffortRequest{req})

	require.NoError(t, <-req.resultCh)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFlushBestEffortBatch_NonDeadlockUsesSingleFallbackImmediately(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() {
		_ = db.Close()
	}()

	mock.ExpectExec(usageLogBestEffortBatchSQL).WillReturnError(errors.New("batch unavailable"))
	mock.ExpectExec(usageLogBestEffortSingleSQL).WillReturnResult(sqlmock.NewResult(0, 1))

	req := newUsageLogBestEffortRequestForTest()
	repo := &Store{}
	repo.flushBestEffortBatch(db, []usageLogBestEffortRequest{req})

	require.NoError(t, <-req.resultCh)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFlushBestEffortBatch_DeadlockRetryExhaustedUsesSingleFallback(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() {
		_ = db.Close()
	}()

	for attempt := 1; attempt <= 3; attempt++ {
		mock.ExpectExec(usageLogBestEffortBatchSQL).WillReturnError(&pq.Error{Code: "40P01"})
	}
	mock.ExpectExec(usageLogBestEffortSingleSQL).WillReturnResult(sqlmock.NewResult(0, 1))

	req := newUsageLogBestEffortRequestForTest()
	repo := &Store{}
	repo.flushBestEffortBatch(db, []usageLogBestEffortRequest{req})

	require.NoError(t, <-req.resultCh)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogStaticInsertShape_PlaceholdersMatchArgTypes 覆盖两条不经占位符生成器、
// 直接手写 $1..$N 的 INSERT 路径，防止加列后漏补占位符只在集成测试才暴露。
func TestUsageLogStaticInsertShape_PlaceholdersMatchArgTypes(t *testing.T) {
	upstreamRequestID := "20260902080329-oneapi"
	log := &usage.UsageLog{
		UserID:            1,
		APIKeyID:          2,
		ProviderID:        3,
		RequestID:         "client:insert-shape",
		UpstreamRequestID: &upstreamRequestID,
		Model:             "claude-3",
		InputTokens:       10,
		CreatedAt:         time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
	}
	prepared := prepareUsageLogInsert(log)
	args := anySliceToDriverValues(prepared.args)

	t.Run("createSingle", func(t *testing.T) {
		var captured []string
		db, mock := newSQLCapturingMock(t, &captured)
		repo := &Store{sql: db}

		mock.ExpectQuery("INSERT INTO usage_logs").
			WithArgs(args...).
			WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), log.CreatedAt))

		inserted, err := repo.Create(context.Background(), log)
		require.NoError(t, err)
		require.True(t, inserted)
		require.NoError(t, mock.ExpectationsWereMet())
		require.Len(t, captured, 1)
		requireStaticInsertMatchesArgTypes(t, captured[0])
	})

	t.Run("execUsageLogInsertNoResult", func(t *testing.T) {
		var captured []string
		db, mock := newSQLCapturingMock(t, &captured)

		mock.ExpectExec("INSERT INTO usage_logs").
			WithArgs(args...).
			WillReturnResult(sqlmock.NewResult(0, 1))

		require.NoError(t, execUsageLogInsertNoResult(context.Background(), db, prepared))
		require.NoError(t, mock.ExpectationsWereMet())
		require.Len(t, captured, 1)
		requireStaticInsertMatchesArgTypes(t, captured[0])
	})
}

// TestPrepareUsageLogInsert_UpstreamRequestIDArgWiring 把 upstream_request_id 钉在
// session_id 之前，与参数类型表位置一致，缺失值写入 NULL。
func TestPrepareUsageLogInsert_UpstreamRequestIDArgWiring(t *testing.T) {
	upstreamRequestID := "req_upstream_123"
	prepared := prepareUsageLogInsert(&usage.UsageLog{
		UserID:            1,
		APIKeyID:          2,
		RequestID:         "client:wiring",
		Model:             "gpt-5",
		UpstreamRequestID: &upstreamRequestID,
		CreatedAt:         time.Now().UTC(),
	})
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))

	idx := len(prepared.args) - 9
	arg, ok := prepared.args[idx].(sql.NullString)
	require.True(t, ok, "upstream_request_id arg should be sql.NullString, got %T", prepared.args[idx])
	require.True(t, arg.Valid)
	require.Equal(t, upstreamRequestID, arg.String)
	require.Equal(t, "text", usageLogInsertArgTypes[idx])

	absent := prepareUsageLogInsert(&usage.UsageLog{UserID: 1, APIKeyID: 2, RequestID: "client:absent", Model: "gpt-5", CreatedAt: time.Now().UTC()})
	nullArg, ok := absent.args[idx].(sql.NullString)
	require.True(t, ok)
	require.False(t, nullArg.Valid, "absent upstream request id must be NULL")

	require.Contains(t, usageLogSelectColumns, "upstream_request_id")
}

func TestUsageLogRepositoryCreateSyncRequestTypeAndLegacyFields(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	createdAt := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	log := &usage.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		ProviderID:     3,
		RequestID:      "req-1",
		Model:          "gpt-5",
		RequestedModel: "gpt-5",
		InputTokens:    10,
		OutputTokens:   20,
		TotalCost:      1,
		ActualCost:     1,
		BillingType:    usage.BillingTypeBalance,
		RequestType:    usage.RequestTypeWSV2,
		Stream:         false,
		OpenAIWSMode:   false,
		CreatedAt:      createdAt,
	}

	mock.ExpectQuery("INSERT INTO usage_logs").
		WithArgs(
			log.UserID,
			log.UserID,       // billing_user_id 默认与调用者一致
			sqlmock.AnyArg(), // team_id
			log.APIKeyID,
			log.ProviderID,
			log.RequestID,
			log.Model,
			log.RequestedModel,
			sqlmock.AnyArg(), // upstream_model
			sqlmock.AnyArg(), // group_id
			sqlmock.AnyArg(), // subscription_id
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
			sqlmock.AnyArg(), // billing_allocations
			log.RateMultiplier,
			log.ProviderRateMultiplier,
			log.BillingType,
			int16(usage.RequestTypeWSV2),
			true,
			true,
			sqlmock.AnyArg(), // duration_ms
			sqlmock.AnyArg(), // first_token_ms
			sqlmock.AnyArg(), // user_agent
			sqlmock.AnyArg(), // ip_address
			log.ImageCount,
			sqlmock.AnyArg(), // image_size
			sqlmock.AnyArg(), // 图片输入尺寸
			sqlmock.AnyArg(), // 图片输出尺寸
			sqlmock.AnyArg(), // 图片尺寸来源
			sqlmock.AnyArg(), // 图片尺寸明细
			sqlmock.AnyArg(), // 视频数量
			sqlmock.AnyArg(), // 视频分辨率
			sqlmock.AnyArg(), // 视频时长
			sqlmock.AnyArg(), // service_tier
			sqlmock.AnyArg(), // reasoning_effort
			sqlmock.AnyArg(), // inbound_endpoint
			sqlmock.AnyArg(), // upstream_endpoint
			log.CacheTTLOverridden,
			log.LongContextBillingApplied,
			sqlmock.AnyArg(), // pricing_config_id
			sqlmock.AnyArg(), // model_mapping_chain
			sqlmock.AnyArg(), // billing_tier
			sqlmock.AnyArg(), // billing_mode
			sqlmock.AnyArg(), // provider_stats_cost
			sqlmock.AnyArg(), // upstream_request_id
			sqlmock.AnyArg(), // session_id
			createdAt,
			sqlmock.AnyArg(), // requested_reasoning_effort
			false,            // native_compaction_v2
			"unknown",        // 平台快照
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			nil, // billing_key
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(99), createdAt))

	inserted, err := repo.Create(context.Background(), log)
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, int64(99), log.ID)
	require.Nil(t, log.ServiceTier)
	require.Equal(t, usage.RequestTypeWSV2, log.RequestType)
	require.True(t, log.Stream)
	require.True(t, log.OpenAIWSMode)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryCreate_PersistsServiceTier(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	createdAt := time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC)
	serviceTier := "priority"
	log := &usage.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		ProviderID:     3,
		RequestID:      "req-service-tier",
		Model:          "gpt-5.4",
		RequestedModel: "gpt-5.4",
		ServiceTier:    &serviceTier,
		CreatedAt:      createdAt,
	}

	mock.ExpectQuery("INSERT INTO usage_logs").
		WithArgs(
			log.UserID,
			log.UserID,       // billing_user_id 默认与调用者一致
			sqlmock.AnyArg(), // team_id
			log.APIKeyID,
			log.ProviderID,
			log.RequestID,
			log.Model,
			log.RequestedModel,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
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
			sqlmock.AnyArg(), // billing_allocations
			log.RateMultiplier,
			log.ProviderRateMultiplier,
			log.BillingType,
			int16(usage.RequestTypeSync),
			false,
			false,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			log.ImageCount,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(), // 图片输入尺寸
			sqlmock.AnyArg(), // 图片输出尺寸
			sqlmock.AnyArg(), // 图片尺寸来源
			sqlmock.AnyArg(), // 图片尺寸明细
			sqlmock.AnyArg(), // 视频数量
			sqlmock.AnyArg(), // 视频分辨率
			sqlmock.AnyArg(), // 视频时长
			serviceTier,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			log.CacheTTLOverridden,
			log.LongContextBillingApplied,
			sqlmock.AnyArg(), // pricing_config_id
			sqlmock.AnyArg(), // model_mapping_chain
			sqlmock.AnyArg(), // billing_tier
			sqlmock.AnyArg(), // billing_mode
			sqlmock.AnyArg(), // provider_stats_cost
			sqlmock.AnyArg(), // upstream_request_id
			sqlmock.AnyArg(), // session_id
			createdAt,
			sqlmock.AnyArg(), // requested_reasoning_effort
			false,            // native_compaction_v2
			"unknown",        // 平台快照
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			nil, // billing_key
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(100), createdAt))

	inserted, err := repo.Create(context.Background(), log)
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBuildUsageLogBestEffortInsertQuery_IncludesRequestedModelColumn(t *testing.T) {
	prepared := prepareUsageLogInsert(&usage.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		ProviderID:     3,
		RequestID:      "req-best-effort-query",
		Model:          "gpt-5",
		RequestedModel: "gpt-5",
		CreatedAt:      time.Date(2025, 1, 3, 12, 0, 0, 0, time.UTC),
	})

	query, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})

	require.Contains(t, query, "INSERT INTO usage_logs (")
	require.Contains(t, query, "\n\t\t\tmodel,\n\t\t\trequested_model,\n\t\t\tupstream_model,")
	require.Contains(t, query, "\n\t\t\trequest_id,\n\t\t\tmodel,\n\t\t\trequested_model,\n\t\t\tupstream_model,")
	require.Len(t, args, len(prepared.args))
	require.Equal(t, prepared.args[5], args[5])
}

func TestUsageLogBatchInsertShape_ColumnsAndArguments(t *testing.T) {
	prepared := make([]usageLogInsertPrepared, 2)
	keys := []string{"first", "second"}
	preparedByKey := make(map[string]usageLogInsertPrepared, len(keys))
	for i, key := range keys {
		prepared[i] = prepareUsageLogInsert(&usage.UsageLog{
			UserID:    int64(i + 1),
			APIKeyID:  int64(i + 2),
			RequestID: key,
			Model:     "gpt-5",
			CreatedAt: time.Date(2026, 10, 11, 0, i, 0, 0, time.UTC),
		})
		preparedByKey[key] = prepared[i]
	}
	batchQuery, batchArgs := buildUsageLogBatchInsertQuery(keys, preparedByKey)
	bestEffortQuery, bestEffortArgs := buildUsageLogBestEffortInsertQuery(prepared)
	inputShape := regexp.MustCompile(`(?s)WITH input \((.*?)\) AS \(VALUES (.*?)\n\s*\)`)
	insertShape := regexp.MustCompile(`(?s)INSERT INTO usage_logs \((.*?)\)\s*SELECT(.*?)FROM input`)
	placeholderShape := regexp.MustCompile(`\$(\d+)(?:::(\w+))?`)
	normalizeColumns := func(columns string) string {
		return strings.Join(strings.Fields(columns), "")
	}
	wantColumns := normalizeColumns(usageLogInsertColumns)

	for _, tt := range []struct {
		name       string
		query      string
		args       []any
		inputIndex bool
	}{
		{name: "batch", query: batchQuery, args: batchArgs, inputIndex: true},
		{name: "best-effort", query: bestEffortQuery, args: bestEffortArgs},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := inputShape.FindStringSubmatch(tt.query)
			require.Len(t, input, 3)
			inputColumns := wantColumns
			if tt.inputIndex {
				inputColumns = "input_idx," + inputColumns
			}
			require.Equal(t, inputColumns, normalizeColumns(input[1]))
			insert := insertShape.FindStringSubmatch(tt.query)
			require.Len(t, insert, 3)
			require.Equal(t, wantColumns, normalizeColumns(insert[1]))
			require.Equal(t, wantColumns, normalizeColumns(insert[2]))

			var wantArgs []any
			var wantTypes []string
			for i, row := range prepared {
				if tt.inputIndex {
					wantArgs = append(wantArgs, i)
					wantTypes = append(wantTypes, "")
				}
				wantArgs = append(wantArgs, row.args...)
				wantTypes = append(wantTypes, usageLogInsertArgTypes[:]...)
			}
			require.Equal(t, wantArgs, tt.args)
			placeholders := placeholderShape.FindAllStringSubmatch(input[2], -1)
			require.Len(t, placeholders, len(wantArgs))
			for i, placeholder := range placeholders {
				require.Equal(t, strconv.Itoa(i+1), placeholder[1])
				require.Equal(t, wantTypes[i], placeholder[2])
			}
		})
	}
}

func TestExecUsageLogInsertNoResult_PersistsRequestedModel(t *testing.T) {
	db, mock := newSQLMock(t)
	prepared := prepareUsageLogInsert(&usage.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		ProviderID:     3,
		RequestID:      "req-best-effort-exec",
		Model:          "gpt-5",
		RequestedModel: "gpt-5",
		CreatedAt:      time.Date(2025, 1, 4, 12, 0, 0, 0, time.UTC),
	})

	mock.ExpectExec("INSERT INTO usage_logs").
		WithArgs(anySliceToDriverValues(prepared.args)...).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := execUsageLogInsertNoResult(context.Background(), db, prepared)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPrepareUsageLogInsert_ArgCountMatchesTypes(t *testing.T) {
	prepared := prepareUsageLogInsert(&usage.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		ProviderID:     3,
		RequestID:      "req-arg-count",
		Model:          "gpt-5",
		RequestedModel: "gpt-5",
		CreatedAt:      time.Date(2025, 1, 5, 12, 0, 0, 0, time.UTC),
	})

	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
}

func TestPrepareUsageLogInsert_PersistsImageSizeMetadata(t *testing.T) {
	imageSize := "4K"
	inputSize := "1024x1024"
	outputSize := "3840x2160"
	source := "output"
	prepared := prepareUsageLogInsert(&usage.UsageLog{
		UserID:             1,
		APIKeyID:           2,
		ProviderID:         3,
		RequestID:          "req-image-metadata",
		Model:              "gpt-image-2",
		RequestedModel:     "gpt-image-2",
		ImageCount:         2,
		ImageSize:          &imageSize,
		ImageInputSize:     &inputSize,
		ImageOutputSize:    &outputSize,
		ImageSizeSource:    &source,
		ImageSizeBreakdown: map[string]int{"1K": 1, "4K": 1},
		CreatedAt:          time.Date(2025, 1, 6, 12, 0, 0, 0, time.UTC),
	})

	require.Equal(t, sql.NullString{String: imageSize, Valid: true}, prepared.args[41])
	require.Equal(t, sql.NullString{String: inputSize, Valid: true}, prepared.args[42])
	require.Equal(t, sql.NullString{String: outputSize, Valid: true}, prepared.args[43])
	require.Equal(t, sql.NullString{String: source, Valid: true}, prepared.args[44])
	breakdownJSON, ok := prepared.args[45].(string)
	require.True(t, ok)
	require.JSONEq(t, `{"1K":1,"4K":1}`, breakdownJSON)
}

func TestBuildUsageLogBatchInsertQuery_UsesConflictDoNothing(t *testing.T) {
	log := &usage.UsageLog{
		UserID:       1,
		APIKeyID:     2,
		ProviderID:   3,
		RequestID:    "req-batch-no-update",
		Model:        "gpt-5",
		InputTokens:  10,
		OutputTokens: 5,
		TotalCost:    1.2,
		ActualCost:   1.2,
		CreatedAt:    time.Now().UTC(),
	}
	prepared := prepareUsageLogInsert(log)

	query, _ := buildUsageLogBatchInsertQuery([]string{usageLogBatchKey(log.RequestID, log.APIKeyID)}, map[string]usageLogInsertPrepared{
		usageLogBatchKey(log.RequestID, log.APIKeyID): prepared,
	})

	require.Contains(t, query, "ON CONFLICT DO NOTHING")
	require.NotContains(t, strings.ToUpper(query), "DO UPDATE")
}

// TestPrepareUsageLogInsert_SessionIDArgWiring 检查 session_id 在参数切片、类型表
// 和 INSERT 列表中的位置。字段扩展时追加在末尾。
func TestPrepareUsageLogInsert_SessionIDArgWiring(t *testing.T) {
	require.Len(t, usageLogInsertArgTypes, 69, "arg-type table must include billing key")

	sessionID := "sess-persisted-123"
	prepared := prepareUsageLogInsert(newSessionIDUsageLog(&sessionID))

	require.Len(t, prepared.args, len(usageLogInsertArgTypes),
		"prepared args must match the arg-type table length")

	// session_id 位于 created_at 之前，字段扩展时按顺序追加在末尾。
	sessionArg := prepared.args[len(prepared.args)-8]
	ns, ok := sessionArg.(sql.NullString)
	require.True(t, ok, "session_id arg should be a sql.NullString, got %T", sessionArg)
	require.True(t, ns.Valid)
	require.Equal(t, sessionID, ns.String)

	require.Equal(t, "text", usageLogInsertArgTypes[len(usageLogInsertArgTypes)-8],
		"session_id arg type must be text")
	require.Equal(t, "timestamptz", usageLogInsertArgTypes[len(usageLogInsertArgTypes)-7],
		"created_at arg type must remain timestamptz")
	require.Equal(t, "text", usageLogInsertArgTypes[len(usageLogInsertArgTypes)-6],
		"requested reasoning effort arg type must be text")
	require.Equal(t, "boolean", usageLogInsertArgTypes[len(usageLogInsertArgTypes)-5],
		"native compaction arg type must be boolean")
}

// TestPrepareUsageLogInsert_SessionIDNullWhenAbsent 验证缺失的会话标识会持久化为
// SQL NULL。
func TestPrepareUsageLogInsert_SessionIDNullWhenAbsent(t *testing.T) {
	prepared := prepareUsageLogInsert(newSessionIDUsageLog(nil))
	sessionArg := prepared.args[len(prepared.args)-8]
	ns, ok := sessionArg.(sql.NullString)
	require.True(t, ok, "session_id arg should be a sql.NullString, got %T", sessionArg)
	require.False(t, ns.Valid, "absent session id must be NULL, not empty string")

	empty := ""
	preparedEmpty := prepareUsageLogInsert(newSessionIDUsageLog(&empty))
	nsEmpty := testassert.MustType[sql.NullString](preparedEmpty.args[len(preparedEmpty.args)-8])
	require.False(t, nsEmpty.Valid, "empty session id must also be NULL")
}

// TestUsageLogInsertQueries_IncludeSessionID 验证每条 INSERT 路径和 SELECT 列表
// 都包含 session_id。
func TestUsageLogInsertQueries_IncludeSessionID(t *testing.T) {
	require.Contains(t, usageLogSelectColumns, "session_id",
		"SELECT column list must include session_id")
	require.Contains(t, usageLogSelectColumns, "billing_user_id",
		"SELECT column list must include billing attribution")
	require.Contains(t, usageLogSelectColumns, "team_id",
		"SELECT column list must include team attribution")

	sessionID := "sess-in-query"
	log := newSessionIDUsageLog(&sessionID)
	prepared := prepareUsageLogInsert(log)
	key := usageLogBatchKey(log.RequestID, log.APIKeyID)

	batchQuery, batchArgs := buildUsageLogBatchInsertQuery([]string{key},
		map[string]usageLogInsertPrepared{key: prepared})
	require.Contains(t, batchQuery, "session_id")
	// 两处列引用（INSERT 列表和 SELECT ... FROM input）加上一处 CTE 定义。
	require.GreaterOrEqual(t, strings.Count(batchQuery, "session_id"), 3)
	require.Len(t, batchArgs, len(prepared.args)+1,
		"batch args include the synthetic input_index before usage-log values")

	bestEffortQuery, bestEffortArgs := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
	require.Contains(t, bestEffortQuery, "session_id")
	require.Len(t, bestEffortArgs, len(prepared.args))
}

// TestPrepareUsageLogInsert_RequestedReasoningEffortArgWiring 检查 requested_reasoning_effort 在参数末尾的位置。
func TestPrepareUsageLogInsert_RequestedReasoningEffortArgWiring(t *testing.T) {
	requested := "max"
	prepared := prepareUsageLogInsert(&usage.UsageLog{
		UserID: 1, APIKeyID: 2, ProviderID: 3, RequestID: "req-effort", Model: "gpt-5",
		RequestedReasoningEffort: &requested,
	})
	value, ok := prepared.args[len(prepared.args)-6].(sql.NullString)
	require.True(t, ok)
	require.True(t, value.Valid)
	require.Equal(t, requested, value.String)
}

func newUsageLogBestEffortRequestForTest() usageLogBestEffortRequest {
	log := &usage.UsageLog{
		UserID:        1,
		BillingUserID: 1,
		APIKeyID:      2,
		ProviderID:    3,
		RequestID:     "req-best-effort-deadlock",
		Model:         "gpt-5",
		InputTokens:   10,
		OutputTokens:  5,
		TotalCost:     1,
		ActualCost:    1,
		CreatedAt:     time.Now().UTC(),
	}
	return usageLogBestEffortRequest{
		prepared: prepareUsageLogInsert(log),
		apiKeyID: log.APIKeyID,
		resultCh: make(chan error, 1),
	}
}

// newSQLCapturingMock 把执行的 SQL 记录到 captured，并接受所有语句。
// WithArgs 校验查询参数。
func newSQLCapturingMock(t *testing.T, captured *[]string) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	matcher := sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
		*captured = append(*captured, actualSQL)
		return nil
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// requireStaticInsertMatchesArgTypes 断言手写的 INSERT：列清单长度与 VALUES 占位符数量
// 都等于 usageLogInsertArgTypes，且占位符恰为 $1..$N 各出现一次。
func requireStaticInsertMatchesArgTypes(t *testing.T, query string) {
	t.Helper()
	m := usageLogStaticInsertShapeRe.FindStringSubmatch(query)
	require.Len(t, m, 3, "unrecognised INSERT shape:\n%s", query)

	want := len(usageLogInsertArgTypes)
	columns := 0
	for col := range strings.SplitSeq(m[1], ",") {
		if strings.TrimSpace(col) != "" {
			columns++
		}
	}
	require.Equal(t, want, columns, "INSERT column list must match usageLogInsertArgTypes")

	seen := make(map[int]struct{}, want)
	for _, ph := range usageLogPlaceholderRe.FindAllStringSubmatch(m[2], -1) {
		n, err := strconv.Atoi(ph[1])
		require.NoError(t, err)
		_, dup := seen[n]
		require.False(t, dup, "duplicate placeholder $%d", n)
		seen[n] = struct{}{}
	}
	require.Len(t, seen, want, "VALUES placeholder count must match usageLogInsertArgTypes")
	for i := 1; i <= want; i++ {
		_, ok := seen[i]
		require.True(t, ok, "missing placeholder $%d", i)
	}
}

func anySliceToDriverValues(values []any) []driver.Value {
	out := make([]driver.Value, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func newSessionIDUsageLog(sessionID *string) *usage.UsageLog {
	return &usage.UsageLog{
		UserID:       1,
		APIKeyID:     2,
		ProviderID:   3,
		RequestID:    "req-session-id",
		Model:        "claude-3",
		InputTokens:  10,
		OutputTokens: 5,
		TotalCost:    1.0,
		ActualCost:   1.0,
		SessionID:    sessionID,
		CreatedAt:    time.Now().UTC(),
	}
}
