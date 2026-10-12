package postgres

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	identitypg "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
)

// Repository fixtures use SQL mocks to observe predicate text, bound
// attribution, returned review shapes, and transaction call order. Cyber cases
// bind the production identity participant to that mock transaction, while
// rollback and atomicity descriptions refer to expected calls rather than live
// database behavior.

// TestBuildContentModerationLogWhere_BlockFiltersStandardBlocksOnly supplies
// the singular block selector to the predicate builder. It checks the standard
// action predicate and absence of keyword/hash action text in the generated
// query fragment.
func TestBuildContentModerationLogWhere_BlockFiltersStandardBlocksOnly(t *testing.T) {
	where, args := buildContentModerationLogWhere(moderation.ContentModerationLogFilter{Result: "block"})

	require.Empty(t, args)
	sql := strings.Join(where, " AND ")
	// The singular selector selects one action so administrator review can separate
	// API score blocks from keyword or repeated-hash rejections in its result tabs.
	require.Contains(t, sql, "l.action = 'block'")
	require.NotContains(t, sql, "keyword_block")
	require.NotContains(t, sql, "hash_block")
}

// TestBuildContentModerationLogWhere_LegacyBlockedIncludesAllBlockActions
// supplies the combined blocked compatibility selector. It checks that its
// predicate contains all three supported block actions and contributes no bound
// arguments.
func TestBuildContentModerationLogWhere_LegacyBlockedIncludesAllBlockActions(t *testing.T) {
	where, args := buildContentModerationLogWhere(moderation.ContentModerationLogFilter{Result: "blocked"})

	require.Empty(t, args)
	// The compatibility selector combines all block actions in one result category;
	// singular action selectors remain available for the separate administrator review modes.
	require.Contains(t, strings.Join(where, " AND "), "l.action IN ('block', 'keyword_block', 'hash_block')")
}

// TestBuildContentModerationLogWhere_KeywordBlockFiltersKeywordBlocksOnly
// supplies the keyword_block action selector to ordinary filtering. It checks
// the generated equality predicate and absence of additional arguments for that
// fixed action selection.
func TestBuildContentModerationLogWhere_KeywordBlockFiltersKeywordBlocksOnly(t *testing.T) {
	where, args := buildContentModerationLogWhere(moderation.ContentModerationLogFilter{Result: "keyword_block"})

	require.Empty(t, args)
	require.Contains(t, strings.Join(where, " AND "), "l.action = 'keyword_block'")
}

// TestBuildContentModerationLogWhere_HashBlockFiltersHashBlocksOnly supplies
// the hash_block action selector to ordinary filtering. It checks the generated
// equality predicate and absence of additional arguments for that fixed action
// selection.
func TestBuildContentModerationLogWhere_HashBlockFiltersHashBlocksOnly(t *testing.T) {
	where, args := buildContentModerationLogWhere(moderation.ContentModerationLogFilter{Result: "hash_block"})

	require.Empty(t, args)
	require.Contains(t, strings.Join(where, " AND "), "l.action = 'hash_block'")
}

// TestBuildContentModerationLogWhere_HitExcludesAllBlockActions supplies the
// hit selector for administrator allowed-risk review. It checks the flagged
// predicate and exclusion of standard, keyword, and hash blocking actions.
func TestBuildContentModerationLogWhere_HitExcludesAllBlockActions(t *testing.T) {
	where, args := buildContentModerationLogWhere(moderation.ContentModerationLogFilter{Result: "hit"})

	require.Empty(t, args)
	sql := strings.Join(where, " AND ")
	// Allowed-hit review excludes the block action labels, keeping its predicate
	// disjoint from combined blocked review even when both categories contain flagged records.
	require.Contains(t, sql, "l.flagged = TRUE")
	require.Contains(t, sql, "l.action NOT IN ('block', 'keyword_block', 'hash_block')")
}

// TestContentModerationRepositoryCreateLog_PersistsMatchedKeyword supplies
// attribution, timing, keyword, and decision values to a SQL mock. It checks
// argument positions, matched keyword, returned identity/time, and commit
// sequence; JSON payload arguments remain AnyArg observations.
func TestContentModerationRepositoryCreateLog_PersistsMatchedKeyword(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newTestModerationRepository(db)
	userID := int64(1001)
	billingUserID := int64(1000)
	teamID := int64(4001)
	apiKeyID := int64(2001)
	groupID := int64(3001)
	latencyMS := 42
	queueDelayMS := 7
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	log := &moderation.ContentModerationLog{
		RequestID:         "req-keyword",
		UserID:            &userID,
		UserEmail:         "user@example.com",
		BillingUserID:     &billingUserID,
		TeamID:            &teamID,
		APIKeyID:          &apiKeyID,
		APIKeyName:        "default-key",
		GroupID:           &groupID,
		GroupName:         "default",
		Endpoint:          "/v1/messages",
		Provider:          "anthropic",
		Model:             "claude-sonnet-4",
		Mode:              moderation.ContentModerationModePreBlock,
		Action:            moderation.ContentModerationActionKeywordBlock,
		Flagged:           true,
		HighestCategory:   "keyword",
		HighestScore:      1,
		CategoryScores:    map[string]float64{"keyword": 1},
		ThresholdSnapshot: map[string]float64{"keyword": 1},
		InputExcerpt:      "blocked prompt",
		UpstreamLatencyMS: &latencyMS,
		ViolationCount:    1,
		EmailSent:         true,
		QueueDelayMS:      &queueDelayMS,
		MatchedKeyword:    "secret-token",
	}

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO content_moderation_logs").
		WithArgs(
			log.RequestID,
			userID,
			log.UserEmail,
			billingUserID,
			teamID,
			apiKeyID,
			log.APIKeyName,
			groupID,
			log.GroupName,
			log.Endpoint,
			log.Provider,
			log.Model,
			log.Mode,
			log.Action,
			log.Flagged,
			log.HighestCategory,
			log.HighestScore,
			sqlmock.AnyArg(), // Category-score snapshot.
			sqlmock.AnyArg(), // Threshold snapshot.
			log.InputExcerpt,
			latencyMS,
			log.Error,
			log.ViolationCount,
			log.AutoBanned,
			log.EmailSent,
			queueDelayMS,
			log.MatchedKeyword,
			log.Source,
			sqlmock.AnyArg(), // Full input units.
			log.ContentComplete,
			log.AuditComplete,
			log.TextUnitCount,
			log.ImageUnitCount,
			log.FailedUnitCount,
			sqlmock.AnyArg(), // Failed audit units.
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(99), createdAt))
	mock.ExpectCommit()

	err := repo.CreateLog(context.Background(), log)

	require.NoError(t, err)
	require.Equal(t, int64(99), log.ID)
	require.Equal(t, createdAt, log.CreatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestContentModerationRepositoryCreateLogPersistsReviewMedia supplies one
// image review item and binary snapshot to a SQL mock. It checks parent
// identity linkage, media argument values, assigned child identity, and commit
// after both insertions.
func TestContentModerationRepositoryCreateLogPersistsReviewMedia(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newTestModerationRepository(db)
	createdAt := time.Now()
	log := &moderation.ContentModerationLog{
		Source:          moderation.ContentModerationSourceTool,
		ContentComplete: true,
		AuditComplete:   true,
		InputItems: []moderation.ContentModerationInputItem{{
			Index: 0, Source: moderation.ContentModerationSourceTool, Type: moderation.ContentModerationItemTypeImage, ImageRef: "data:image/png;base64,aW1hZ2U=",
		}},
		Media: []moderation.ContentModerationMedia{{
			SourceIndex: 0, Source: moderation.ContentModerationSourceTool, MIMEType: "image/png", SHA256: strings.Repeat("a", 64), ByteSize: 5,
			OriginalRef: "data:image/png;base64,aW1hZ2U=", SnapshotStatus: "ready", Content: []byte("image"),
		}},
	}

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO content_moderation_logs").
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(101), createdAt))
	mock.ExpectQuery("INSERT INTO content_moderation_media").
		WithArgs(int64(101), nil, 0, moderation.ContentModerationSourceTool, "image/png", strings.Repeat("a", 64), int64(5), log.Media[0].OriginalRef, "ready", "", []byte("image")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(201), createdAt))
	mock.ExpectCommit()

	require.NoError(t, repo.CreateLog(context.Background(), log))
	require.Equal(t, int64(201), log.Media[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestContentModerationRepositoryCreateLogRollsBackWhenMediaInsertFails makes
// the SQL mock fail a media insertion after returning a parent identity. It
// checks propagated deadline error and rollback invocation, rather than
// executing a live database transaction.
func TestContentModerationRepositoryCreateLogRollsBackWhenMediaInsertFails(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newTestModerationRepository(db)
	log := &moderation.ContentModerationLog{Media: []moderation.ContentModerationMedia{{OriginalRef: "data:image/png;base64,aW1hZ2U="}}}

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO content_moderation_logs").
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(101), time.Now()))
	mock.ExpectQuery("INSERT INTO content_moderation_media").
		WillReturnError(context.DeadlineExceeded)
	mock.ExpectRollback()

	err := repo.CreateLog(context.Background(), log)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestContentModerationRepositoryListLogsReturnsTeamAttribution returns
// separate actor, billing owner, and team IDs in a mocked list row. It checks
// default page arguments, returned total, and attribution pointers retained in
// the decoded summary.
func TestContentModerationRepositoryListLogsReturnsTeamAttribution(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newTestModerationRepository(db)
	createdAt := time.Now()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM content_moderation_logs").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("FROM content_moderation_logs l").WithArgs(20, 0).WillReturnRows(
		sqlmock.NewRows([]string{
			"id", "request_id", "user_id", "user_email", "billing_user_id", "team_id", "api_key_id", "api_key_name", "group_id", "group_name",
			"endpoint", "provider", "model", "mode", "action", "flagged", "highest_category", "highest_score",
			"category_scores", "threshold_snapshot", "input_excerpt", "upstream_latency_ms", "error",
			"violation_count", "auto_banned", "email_sent", "user_status", "queue_delay_ms", "matched_keyword",
			"source", "content_complete", "audit_complete", "text_unit_count", "image_unit_count", "failed_unit_count", "created_at",
		}).AddRow(
			int64(9), "req", int64(2002), "member@example.com", int64(1001), int64(3003), int64(4004), "team-key", nil, "",
			"/v1/responses", "openai", "gpt-5", "pre_block", "block", true, "violence", 0.9,
			`{"violence":0.9}`, `{"violence":0.8}`, "excerpt", 12, "",
			1, true, false, "disabled", 3, "", "user", true, true, 1, 0, 0, createdAt,
		),
	)

	items, page, err := repo.ListLogs(context.Background(), moderation.ContentModerationLogFilter{})

	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Len(t, items, 1)
	require.Equal(t, int64(2002), *items[0].UserID)
	require.Equal(t, int64(1001), *items[0].BillingUserID)
	require.Equal(t, int64(3003), *items[0].TeamID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestContentModerationRepositoryGetLogReturnsFullReviewPayload returns raw
// input units, one failed image unit, and separate media metadata from mocked
// queries. It checks attribution, completion labels, full text, failure text,
// and absent binary bytes in detail metadata.
func TestContentModerationRepositoryGetLogReturnsFullReviewPayload(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newTestModerationRepository(db)
	createdAt := time.Now()
	inputItems := `[{
		"index":0,"source":"tool","type":"text","text":"complete output"
	}]`
	failedUnits := `[{"type":"image","index":1,"source_index":2,"error":"timeout"}]`
	mock.ExpectQuery("FROM content_moderation_logs l").WithArgs(int64(9)).WillReturnRows(
		sqlmock.NewRows([]string{
			"id", "request_id", "user_id", "user_email", "billing_user_id", "team_id", "api_key_id", "api_key_name", "group_id", "group_name",
			"endpoint", "provider", "model", "mode", "action", "flagged", "highest_category", "highest_score",
			"category_scores", "threshold_snapshot", "input_excerpt", "upstream_latency_ms", "error",
			"violation_count", "auto_banned", "email_sent", "user_status", "queue_delay_ms", "matched_keyword",
			"source", "input_items", "content_complete", "audit_complete", "text_unit_count", "image_unit_count",
			"failed_unit_count", "failed_units", "created_at",
		}).AddRow(
			int64(9), "req", int64(1001), "user@example.com", int64(1000), int64(3001), nil, "", nil, "", "/v1/responses", "openai", "gpt-5", "pre_block", "allow", false, "sexual", 0.1,
			`{"sexual":0.1}`, `{"sexual":0.65}`, "excerpt", 12, "timeout", 0, false, false, "active", 3, "",
			"tool", inputItems, true, false, 1, 1, 1, failedUnits, createdAt,
		),
	)
	mock.ExpectQuery("FROM content_moderation_media").WithArgs(int64(9)).WillReturnRows(
		sqlmock.NewRows([]string{"id", "source_index", "source", "mime_type", "sha256", "byte_size", "original_ref", "snapshot_status", "snapshot_error", "created_at"}).
			AddRow(int64(88), 2, "tool", "image/png", strings.Repeat("b", 64), int64(5), "data:image/png;base64,aW1hZ2U=", "ready", "", createdAt),
	)

	item, err := repo.GetLog(context.Background(), 9)

	require.NoError(t, err)
	require.Equal(t, int64(1001), *item.UserID)
	require.Equal(t, int64(1000), *item.BillingUserID)
	require.Equal(t, int64(3001), *item.TeamID)
	require.True(t, item.ContentComplete)
	require.False(t, item.AuditComplete)
	require.Equal(t, "complete output", item.InputItems[0].Text)
	require.Equal(t, "timeout", item.FailedUnits[0].Error)
	require.Len(t, item.Media, 1)
	require.Empty(t, item.Media[0].Content)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestContentModerationRepositoryListCyberWarningsReturnsTeamAttribution
// returns a mocked Cyber summary row with actor, owner, and team context. It
// checks default pagination and separate attribution pointers after warning-row
// decoding.
func TestContentModerationRepositoryListCyberWarningsReturnsTeamAttribution(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newTestModerationRepository(db)
	createdAt := time.Now()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM content_moderation_cyber_warnings").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("FROM content_moderation_cyber_warnings w").WithArgs(20, 0).WillReturnRows(
		sqlmock.NewRows([]string{
			"id", "request_id", "user_id", "user_email", "billing_user_id", "team_id", "api_key_id", "api_key_name", "group_id", "group_name",
			"provider_id", "provider_name", "endpoint", "model", "upstream_status", "warning_text", "prompt_excerpt",
			"violation_count", "auto_banned", "email_sent", "user_status", "source", "content_complete", "audit_complete",
			"text_unit_count", "image_unit_count", "failed_unit_count", "created_at",
		}).AddRow(
			int64(19), "req", int64(2002), "member@example.com", int64(1001), int64(3003), int64(4004), "team-key", nil, "",
			int64(5005), "openai-1", "/v1/responses", "gpt-5", 400, "cyber_policy", "prompt",
			1, true, false, "disabled", "user", true, true, 1, 0, 0, createdAt,
		),
	)

	items, page, err := repo.ListCyberWarnings(context.Background(), moderation.ContentModerationCyberWarningFilter{})

	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Len(t, items, 1)
	require.Equal(t, int64(2002), *items[0].UserID)
	require.Equal(t, int64(1001), *items[0].BillingUserID)
	require.Equal(t, int64(3003), *items[0].TeamID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestContentModerationRepositoryGetCyberWarningReturnsTeamAttribution returns
// a mocked Cyber detail row and an empty media query. It checks that actor,
// billing owner, and team identifiers survive the full-detail read.
func TestContentModerationRepositoryGetCyberWarningReturnsTeamAttribution(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newTestModerationRepository(db)
	createdAt := time.Now()
	mock.ExpectQuery("FROM content_moderation_cyber_warnings w").WithArgs(int64(19)).WillReturnRows(
		sqlmock.NewRows([]string{
			"id", "request_id", "user_id", "user_email", "billing_user_id", "team_id", "api_key_id", "api_key_name", "group_id", "group_name",
			"provider_id", "provider_name", "endpoint", "model", "upstream_status", "warning_text", "prompt_excerpt",
			"violation_count", "auto_banned", "email_sent", "user_status", "source", "input_items", "content_complete", "audit_complete",
			"text_unit_count", "image_unit_count", "failed_unit_count", "failed_units", "created_at",
		}).AddRow(
			int64(19), "req", int64(2002), "member@example.com", int64(1001), int64(3003), int64(4004), "team-key", nil, "",
			int64(5005), "openai-1", "/v1/responses", "gpt-5", 400, "cyber_policy", "prompt",
			1, true, false, "disabled", "user", `[]`, true, true, 1, 0, 0, `[]`, createdAt,
		),
	)
	mock.ExpectQuery("FROM content_moderation_media").WithArgs(int64(19)).WillReturnRows(
		sqlmock.NewRows([]string{"id", "source_index", "source", "mime_type", "sha256", "byte_size", "original_ref", "snapshot_status", "snapshot_error", "created_at"}),
	)

	item, err := repo.GetCyberWarning(context.Background(), 19)

	require.NoError(t, err)
	require.Equal(t, int64(2002), *item.UserID)
	require.Equal(t, int64(1001), *item.BillingUserID)
	require.Equal(t, int64(3003), *item.TeamID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestContentModerationRepositoryGetMediaContentReturnsBinary returns one
// stored image snapshot from the SQL mock for a selected ID. It checks exact
// binary bytes and the expected query invocation for that media read.
func TestContentModerationRepositoryGetMediaContentReturnsBinary(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newTestModerationRepository(db)
	createdAt := time.Now()
	mock.ExpectQuery("FROM content_moderation_media").WithArgs(int64(88)).WillReturnRows(
		sqlmock.NewRows([]string{"id", "source_index", "source", "mime_type", "sha256", "byte_size", "original_ref", "snapshot_status", "snapshot_error", "content", "created_at"}).
			AddRow(int64(88), 2, "tool", "image/png", strings.Repeat("b", 64), int64(5), "ref", "ready", "", []byte("image"), createdAt),
	)

	item, err := repo.GetMediaContent(context.Background(), 88)

	require.NoError(t, err)
	require.Equal(t, []byte("image"), item.Content)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestContentModerationRepositoryCountFlaggedByUserSince_ExcludesHashBlock
// matches the count query against its hash-block exclusion clause. It checks
// actor and cutoff arguments plus the supplied mock count; the predicate is not
// executed against stored history here.
func TestContentModerationRepositoryCountFlaggedByUserSince_ExcludesHashBlock(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newTestModerationRepository(db)
	since := time.Now().Add(-time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("AND action <> 'hash_block'")).
		WithArgs(int64(1001), since).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	count, err := repo.CountFlaggedByUserSince(context.Background(), 1001, since)

	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestContentModerationRepositoryCreateCyberWarningAndApplyUserBan_DisablesUserAtomically
// sets mocked actor locking, warning insertion, threshold count, identity
// update, policy update, and commit expectations. It checks their sequence and
// returned warning fields, while database atomicity itself is outside this
// SQL-mock case.
func TestContentModerationRepositoryCreateCyberWarningAndApplyUserBan_DisablesUserAtomically(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newTestModerationRepository(db)
	userID := int64(1001)
	billingUserID := int64(1000)
	teamID := int64(3001)
	providerID := int64(2001)
	createdAt := time.Now()
	warning := &moderation.ContentModerationCyberWarning{
		RequestID:      "req_1",
		UserID:         &userID,
		UserEmail:      "user@example.com",
		BillingUserID:  &billingUserID,
		TeamID:         &teamID,
		ProviderID:     &providerID,
		ProviderName:   "openai-1",
		Endpoint:       "/v1/responses",
		Model:          "gpt-5.1",
		UpstreamStatus: 400,
		WarningText:    "This request may pose a cybersecurity risk.",
		PromptExcerpt:  "bad cyber prompt",
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM users WHERE id = $1 FOR UPDATE")).
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(moderation.StatusActive))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO content_moderation_cyber_warnings")).
		WithArgs(
			warning.RequestID,
			userID,
			warning.UserEmail,
			billingUserID,
			teamID,
			nil,
			warning.APIKeyName,
			nil,
			warning.GroupName,
			providerID,
			warning.ProviderName,
			warning.Endpoint,
			warning.Model,
			warning.UpstreamStatus,
			warning.WarningText,
			warning.PromptExcerpt,
			1,
			false,
			false,
			warning.Source,
			sqlmock.AnyArg(),
			warning.ContentComplete,
			warning.AuditComplete,
			warning.TextUnitCount,
			warning.ImageUnitCount,
			warning.FailedUnitCount,
			sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(99), createdAt))
	mock.ExpectQuery(regexp.QuoteMeta("WITH last_auto_ban AS")).
		WithArgs(userID, sqlmock.AnyArg(), int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE users")).
		WithArgs(userID, moderation.StatusDisabled).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE content_moderation_cyber_warnings")).
		WithArgs(int64(99), 3, true).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	autoBanned, err := repo.CreateCyberWarningAndApplyUserBan(context.Background(), warning, moderation.ContentModerationCyberWarningPolicy{
		AutoBanEnabled: true,
		BanThreshold:   3,
		WindowHours:    720,
	})

	require.NoError(t, err)
	require.True(t, autoBanned)
	require.Equal(t, int64(99), warning.ID)
	require.Equal(t, createdAt, warning.CreatedAt)
	require.Equal(t, 3, warning.ViolationCount)
	require.True(t, warning.AutoBanned)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestContentModerationRepositoryCreateCyberWarningAndApplyUserBan_KeepsBelowThreshold
// supplies a mocked count below the Cyber ban threshold after actor locking and
// warning insertion. It checks persisted count, false ban observation, commit,
// and absence of an expected user update.
func TestContentModerationRepositoryCreateCyberWarningAndApplyUserBan_KeepsBelowThreshold(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newTestModerationRepository(db)
	userID := int64(1001)
	warning := &moderation.ContentModerationCyberWarning{
		RequestID:      "req_1",
		UserID:         &userID,
		WarningText:    "flagged by OpenAI cyber policy",
		ViolationCount: 1,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM users WHERE id = $1 FOR UPDATE")).
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(moderation.StatusActive))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO content_moderation_cyber_warnings")).
		WithArgs(
			warning.RequestID,
			userID,
			warning.UserEmail,
			nil,
			nil,
			nil,
			warning.APIKeyName,
			nil,
			warning.GroupName,
			nil,
			warning.ProviderName,
			warning.Endpoint,
			warning.Model,
			warning.UpstreamStatus,
			warning.WarningText,
			warning.PromptExcerpt,
			1,
			false,
			false,
			warning.Source,
			sqlmock.AnyArg(),
			warning.ContentComplete,
			warning.AuditComplete,
			warning.TextUnitCount,
			warning.ImageUnitCount,
			warning.FailedUnitCount,
			sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(100), time.Now()))
	mock.ExpectQuery(regexp.QuoteMeta("WITH last_auto_ban AS")).
		WithArgs(userID, sqlmock.AnyArg(), int64(100)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE content_moderation_cyber_warnings")).
		WithArgs(int64(100), 2, false).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	autoBanned, err := repo.CreateCyberWarningAndApplyUserBan(context.Background(), warning, moderation.ContentModerationCyberWarningPolicy{
		AutoBanEnabled: true,
		BanThreshold:   3,
		WindowHours:    720,
	})

	require.NoError(t, err)
	require.False(t, autoBanned)
	require.Equal(t, 2, warning.ViolationCount)
	require.False(t, warning.AutoBanned)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestContentModerationRepositoryMarkCyberWarningEmailSent expects the marker
// update for one stored warning ID through the SQL mock. It checks successful
// execution and fulfilled query expectations, without testing missing-row
// affected counts.
func TestContentModerationRepositoryMarkCyberWarningEmailSent(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newTestModerationRepository(db)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE content_moderation_cyber_warnings")).
		WithArgs(int64(99)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.MarkCyberWarningEmailSent(context.Background(), 99))
	require.NoError(t, mock.ExpectationsWereMet())
}

// newSQLMock constructs a regexp-query-matching database fixture and registers
// its close attempt with test cleanup. Construction errors fail the test;
// cleanup suppresses DB close errors so they do not replace the recorded
// assertion outcomes.
func newSQLMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, m, e := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, e)
	// Cleanup owns this fixture DB close attempt after assertions finish. Suppress
	// close errors here because the tests observe query and transaction expectations directly.
	t.Cleanup(func() { _ = db.Close() })
	return db, m
}

// newTestModerationRepository binds the supplied mock DB and production
// identity participant factory. Each requested participant uses the current SQL
// transaction, so Cyber fixture expectations exercise the same
// shared-transaction query path as app assembly.
func newTestModerationRepository(db *sql.DB) *Store {
	return NewContentModerationRepository(db, func(tx *sql.Tx) UserStatusTx { return identitypg.NewRiskStatusParticipant(tx) })
}
