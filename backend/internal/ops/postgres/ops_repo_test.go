package postgres

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/ops"
	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// TestBuildOpsErrorLogsWhere_UserScopedFilters combines caller user, key,
// model, probe exclusion, and category arrays. It protects conjunctive SQL
// predicates and the expected five bound arguments for that supplied selection.
func TestBuildOpsErrorLogsWhere_UserScopedFilters(t *testing.T) {
	uid := int64(42)
	kid := int64(7)
	filter := &ops.OpsErrorLogFilter{
		UserID:             &uid,
		APIKeyID:           &kid,
		Model:              "claude-sonnet-4-5",
		ExcludeCountTokens: true,
		ErrorPhasesAny:     []string{"auth"},
		ErrorTypesAny:      []string{"rate_limit_error"},
		View:               "all",
	}
	where, args := buildOpsErrorLogsWhere(filter)

	for _, want := range []string{
		"e.user_id = $",
		"e.api_key_id = $",
		"COALESCE(e.requested_model, e.model, '') = $",
		"COALESCE(e.is_count_tokens, false) = false",
		"e.error_phase = ANY($",
		"e.error_type = ANY($",
	} {
		if !strings.Contains(where, want) {
			t.Fatalf("where missing %q\nfull: %s", want, where)
		}
	}
	if len(args) != 5 {
		t.Fatalf("expected 5 args, got %d", len(args))
	}
}

// TestBuildOpsErrorLogsWhere_ModelFuzzy compares default exact selection with
// escaped substring matching. It protects SQL operator choice and literal
// percent and underscore handling in caller model text.
func TestBuildOpsErrorLogsWhere_ModelFuzzy(t *testing.T) {
	// ModelFuzzy defaults to exact matching.
	exact := &ops.OpsErrorLogFilter{Model: "claude"}
	whereExact, _ := buildOpsErrorLogsWhere(exact)
	if !strings.Contains(whereExact, "COALESCE(e.requested_model, e.model, '') = $") {
		t.Fatalf("default should be exact match, got: %s", whereExact)
	}

	// ModelFuzzy enables ILIKE matching.
	fuzzy := &ops.OpsErrorLogFilter{Model: "claude", ModelFuzzy: true}
	whereFuzzy, args := buildOpsErrorLogsWhere(fuzzy)
	if !strings.Contains(whereFuzzy, "COALESCE(e.requested_model, e.model, '') ILIKE $") {
		t.Fatalf("ModelFuzzy should use ILIKE, got: %s", whereFuzzy)
	}
	if len(args) != 1 || args[0] != "%claude%" {
		t.Fatalf("expected arg \"%%claude%%\", got %v", args)
	}

	// Caller model percent signs remain literal before outer substring markers are
	// added, preventing user model text from widening the pattern by itself.
	esc := &ops.OpsErrorLogFilter{Model: "50%off", ModelFuzzy: true}
	_, escArgs := buildOpsErrorLogsWhere(esc)
	if len(escArgs) != 1 || escArgs[0] != `%50\%off%` {
		t.Fatalf("expected escaped arg, got %v", escArgs)
	}

	esc2 := &ops.OpsErrorLogFilter{Model: "gpt_4o", ModelFuzzy: true}
	_, escArgs2 := buildOpsErrorLogsWhere(esc2)
	if len(escArgs2) != 1 || escArgs2[0] != `%gpt\_4o%` {
		t.Fatalf("underscore should be escaped, got %v", escArgs2)
	}
}

// TestBuildOpsErrorLogsWhere_CyberPolicyStatusExemption supplies ordinary and
// provider-health phase selections. It protects the generated cyber_policy
// status exemption and restricts recovered opt-in to upstream or provider_auth
// phases, including eligible arrays.
func TestBuildOpsErrorLogsWhere_CyberPolicyStatusExemption(t *testing.T) {
	// The default filter exempts cyber_policy and checks other errors by status.
	where, _ := buildOpsErrorLogsWhere(&ops.OpsErrorLogFilter{})
	if !strings.Contains(where, "e.error_type = 'cyber_policy'") {
		t.Fatalf("default filter must exempt cyber_policy from status >= 400 guard\nfull: %s", where)
	}
	if !strings.Contains(where, "COALESCE(e.status_code, 0) >= 400") {
		t.Fatalf("default filter must still include the status >= 400 guard for non-cyber rows\nfull: %s", where)
	}

	// Upstream queries retain the status guard unless recovered attempts are enabled.
	whereUpstream, _ := buildOpsErrorLogsWhere(&ops.OpsErrorLogFilter{Phase: "upstream"})
	if !strings.Contains(whereUpstream, "COALESCE(e.status_code, 0) >= 400") {
		t.Fatalf("upstream phase without IncludeRecoveredUpstream must keep the status guard\nfull: %s", whereUpstream)
	}
	if !strings.Contains(whereUpstream, "e.error_phase = $") {
		t.Fatalf("upstream phase filter must emit the error_phase condition\nfull: %s", whereUpstream)
	}

	// Provider health queries can opt into recovered upstream attempts.
	whereRecovered, _ := buildOpsErrorLogsWhere(&ops.OpsErrorLogFilter{Phase: "upstream", IncludeRecoveredUpstream: true})
	if strings.Contains(whereRecovered, "COALESCE(e.status_code, 0) >= 400") {
		t.Fatalf("upstream phase with IncludeRecoveredUpstream must not add the client-visible status guard\nfull: %s", whereRecovered)
	}

	// Credential failures use the provider health option and their own phase.
	whereProviderAuth, _ := buildOpsErrorLogsWhere(&ops.OpsErrorLogFilter{Phase: "provider_auth", IncludeRecoveredUpstream: true})
	if strings.Contains(whereProviderAuth, "COALESCE(e.status_code, 0) >= 400") {
		t.Fatalf("provider_auth phase with IncludeRecoveredUpstream must expose recovered rows\nfull: %s", whereProviderAuth)
	}
	if !strings.Contains(whereProviderAuth, "e.error_phase = $") {
		t.Fatalf("provider_auth recovered filter must retain its explicit phase\nfull: %s", whereProviderAuth)
	}

	whereProviderHealth, _ := buildOpsErrorLogsWhere(&ops.OpsErrorLogFilter{
		ErrorPhasesAny:           []string{"upstream", "provider_auth"},
		IncludeRecoveredUpstream: true,
	})
	if strings.Contains(whereProviderHealth, "COALESCE(e.status_code, 0) >= 400") {
		t.Fatalf("provider-health ANY filter must expose recovered inference and credential rows\nfull: %s", whereProviderHealth)
	}
	if !strings.Contains(whereProviderHealth, "e.error_phase = ANY($") {
		t.Fatalf("provider-health filter must preserve distinct phase values\nfull: %s", whereProviderHealth)
	}

	whereUserProviderAuth, _ := buildOpsErrorLogsWhere(&ops.OpsErrorLogFilter{ErrorPhasesAny: []string{"provider_auth"}})
	if !strings.Contains(whereUserProviderAuth, "COALESCE(e.status_code, 0) >= 400") {
		t.Fatalf("request-error provider_auth filters must exclude recovered successes\nfull: %s", whereUserProviderAuth)
	}

	whereMixed, _ := buildOpsErrorLogsWhere(&ops.OpsErrorLogFilter{
		ErrorPhasesAny:           []string{"provider_auth", "request"},
		IncludeRecoveredUpstream: true,
	})
	if !strings.Contains(whereMixed, "COALESCE(e.status_code, 0) >= 400") {
		t.Fatalf("recovered opt-in must not bypass the guard for non-provider phases\nfull: %s", whereMixed)
	}
}

// TestBuildOpsErrorLogsWhere_UserOwnershipIsDirectOnly supplies one user
// identity to error filtering. It protects a direct user_id predicate and
// prevents deleted-key attribution from broadening that SQL selection.
func TestBuildOpsErrorLogsWhere_UserOwnershipIsDirectOnly(t *testing.T) {
	uid := int64(42)
	filter := &ops.OpsErrorLogFilter{UserID: &uid}
	where, args := buildOpsErrorLogsWhere(filter)
	if !strings.Contains(where, "e.user_id = $1") {
		t.Fatalf("user scope should match user_id exactly, got: %s", where)
	}
	if len(args) != 1 || args[0] != uid {
		t.Fatalf("expected user id arg %d, got %v", uid, args)
	}
	if strings.Contains(where, "deleted_key_owner_user_id") {
		t.Fatalf("user ownership must not depend on deleted-key attribution: %s", where)
	}
}

// TestProviderAuthFilterIncludesLegacyRows requests the current credential
// phase against retained legacy data. It protects an OR predicate and an
// account_auth argument alongside the requested provider_auth value.
func TestProviderAuthFilterIncludesLegacyRows(t *testing.T) {
	where, args := buildOpsErrorLogsWhere(&ops.OpsErrorLogFilter{Phase: "provider_auth"})
	if !strings.Contains(where, " OR e.error_phase = $") {
		t.Fatalf("missing legacy phase condition: %s", where)
	}
	found := false
	for _, arg := range args {
		if value, ok := arg.(string); ok && value == "account_auth" {
			found = true
		}
	}
	if !found {
		t.Fatal("legacy phase missing from query arguments")
	}
}

// TestOpsInsertErrorLogArgsPreservesExplicitZeroUpstreamStatus prepares a
// credential observation carrying upstream status zero. It protects a valid
// zero SQL integer in the shared insert's twenty-eighth argument slot.
func TestOpsInsertErrorLogArgsPreservesExplicitZeroUpstreamStatus(t *testing.T) {
	zero := 0
	args := opsInsertErrorLogArgs(&ops.OpsInsertErrorLogInput{UpstreamStatusCode: &zero})

	require.Len(t, args, 38)
	encoded, ok := args[27].(sql.NullInt64)
	require.True(t, ok)
	require.True(t, encoded.Valid)
	require.Zero(t, encoded.Int64)
}

// TestOpsNullableIntPointerDistinguishesNilZeroAndStatus supplies missing,
// zero, and 503 status pointers. It protects SQL validity separately from
// numeric value so zero remains available to credential diagnostics.
func TestOpsNullableIntPointerDistinguishesNilZeroAndStatus(t *testing.T) {
	missing := testassert.MustType[sql.NullInt64](opsNullableIntPointer(nil))
	require.False(t, missing.Valid)

	zeroValue := 0
	zero := testassert.MustType[sql.NullInt64](opsNullableIntPointer(&zeroValue))
	require.True(t, zero.Valid)
	require.Zero(t, zero.Int64)

	statusValue := 503
	status := testassert.MustType[sql.NullInt64](opsNullableIntPointer(&statusValue))
	require.True(t, status.Valid)
	require.EqualValues(t, 503, status.Int64)
}

// TestBuildOpsErrorLogsWhere_QueryUsesQualifiedColumns supplies free text for
// joined error queries. It protects qualified request-ID, client-ID, and
// message predicates with one shared bound search argument.
func TestBuildOpsErrorLogsWhere_QueryUsesQualifiedColumns(t *testing.T) {
	filter := &ops.OpsErrorLogFilter{
		Query: "ACCESS_DENIED",
	}

	where, args := buildOpsErrorLogsWhere(filter)
	if where == "" {
		t.Fatalf("where should not be empty")
	}
	if len(args) != 1 {
		t.Fatalf("args len = %d, want 1", len(args))
	}
	if !strings.Contains(where, "e.request_id ILIKE $") {
		t.Fatalf("where should include qualified request_id condition: %s", where)
	}
	if !strings.Contains(where, "e.client_request_id ILIKE $") {
		t.Fatalf("where should include qualified client_request_id condition: %s", where)
	}
	if !strings.Contains(where, "e.error_message ILIKE $") {
		t.Fatalf("where should include qualified error_message condition: %s", where)
	}
}

// TestBuildOpsErrorLogsWhere_UserQueryUsesExistsSubquery supplies an email
// substring to error filtering. It protects a correlated users existence
// predicate and a single bound value for that criterion.
func TestBuildOpsErrorLogsWhere_UserQueryUsesExistsSubquery(t *testing.T) {
	filter := &ops.OpsErrorLogFilter{
		UserQuery: "admin@",
	}

	where, args := buildOpsErrorLogsWhere(filter)
	if where == "" {
		t.Fatalf("where should not be empty")
	}
	if len(args) != 1 {
		t.Fatalf("args len = %d, want 1", len(args))
	}
	if !strings.Contains(where, "EXISTS (SELECT 1 FROM users u WHERE u.id = e.user_id AND u.email ILIKE $") {
		t.Fatalf("where should include EXISTS user email condition: %s", where)
	}
}

// TestBuildOpsErrorLogsWhere_DefaultErrorsExcludesClientAuthStatuses uses the
// default error view. It protects client 401/403 exclusion expressions while
// retaining stored upstream status or provider ownership as upstream context.
func TestBuildOpsErrorLogsWhere_DefaultErrorsExcludesClientAuthStatuses(t *testing.T) {
	where, _ := buildOpsErrorLogsWhere(&ops.OpsErrorLogFilter{})

	if !strings.Contains(where, "NOT COALESCE(e.is_business_limited, false)") {
		t.Fatalf("where should exclude business-limited rows: %s", where)
	}
	if !strings.Contains(where, "COALESCE(e.status_code, 0) IN (401, 403)") {
		t.Fatalf("where should exclude client-side 401/403 from default errors view: %s", where)
	}
	if !strings.Contains(where, "e.upstream_status_code IS NOT NULL") || !strings.Contains(where, "LOWER(COALESCE(e.error_owner, '')) = 'provider'") {
		t.Fatalf("where should preserve upstream 401/403 in default errors view: %s", where)
	}
}

// TestBuildOpsErrorLogsWhere_CustomIgnoredStatuses supplies duplicate and
// unsorted ignored statuses. It protects normalized 401/418 membership in the
// generated client-status exclusion expression for the default error view.
func TestBuildOpsErrorLogsWhere_CustomIgnoredStatuses(t *testing.T) {
	where, _ := buildOpsErrorLogsWhere(&ops.OpsErrorLogFilter{IgnoredStatusCodes: []int{418, 401, 418}})

	if !strings.Contains(where, "COALESCE(e.status_code, 0) IN (401, 418)") {
		t.Fatalf("where should use normalized custom ignored status codes: %s", where)
	}
}

// TestBuildOpsErrorLogsWhere_EmptyIgnoredStatusesDisablesStatusExclusion
// supplies a present empty ignored-status list. It protects disabled status
// exclusion while retaining the independent business-limit classification
// branch in generated SQL.
func TestBuildOpsErrorLogsWhere_EmptyIgnoredStatusesDisablesStatusExclusion(t *testing.T) {
	where, _ := buildOpsErrorLogsWhere(&ops.OpsErrorLogFilter{IgnoredStatusCodes: []int{}})

	if strings.Contains(where, "COALESCE(e.status_code, 0) IN (401, 403)") {
		t.Fatalf("where should not use default ignored status codes when explicitly empty: %s", where)
	}
	if !strings.Contains(where, "FALSE AND NOT") {
		t.Fatalf("where should keep business-limited branch but disable status-code exclusion: %s", where)
	}
}

// TestBuildOpsErrorLogsWhere_ExcludedIncludesClientAuthStatuses selects
// excluded errors. It protects business-limit and client 401/403 expressions
// with upstream-context handling, so those generated criteria remain distinct
// from the ordinary errors view.
func TestBuildOpsErrorLogsWhere_ExcludedIncludesClientAuthStatuses(t *testing.T) {
	where, _ := buildOpsErrorLogsWhere(&ops.OpsErrorLogFilter{View: "excluded"})

	if !strings.Contains(where, "COALESCE(e.is_business_limited, false) OR") {
		t.Fatalf("where should include business-limited rows: %s", where)
	}
	if !strings.Contains(where, "COALESCE(e.status_code, 0) IN (401, 403)") {
		t.Fatalf("where should include client-side 401/403 in excluded view: %s", where)
	}
	if !strings.Contains(where, "e.upstream_status_code IS NOT NULL") || !strings.Contains(where, "LOWER(COALESCE(e.error_owner, '')) = 'provider'") {
		t.Fatalf("where should keep upstream 401/403 out of excluded view: %s", where)
	}
}

// TestOpsErrorLogInsertDoesNotPersistRequestReplayFields inspects the shared
// INSERT and input type for retired replay data. It protects omission of
// request-body, header, retry, and replay-result fields from this persistence
// API.
func TestOpsErrorLogInsertDoesNotPersistRequestReplayFields(t *testing.T) {
	disallowedColumns := []string{
		"request_body",
		"request_headers",
		"request_body_truncated",
		"request_body_bytes",
		"is_retryable",
		"retry_count",
		"resolved_retry_id",
	}

	insertSQL := strings.ToLower(insertOpsErrorLogSQL)
	for _, column := range disallowedColumns {
		if strings.Contains(insertSQL, column) {
			t.Fatalf("ops error log insert still references dropped replay column %q", column)
		}
	}

	inputType := reflect.TypeFor[ops.OpsInsertErrorLogInput]()
	disallowedFields := []string{
		"RequestBodyJSON",
		"RequestBodyTruncated",
		"RequestBodyBytes",
		"RequestHeadersJSON",
		"IsRetryable",
		"RetryCount",
		"ResolvedRetryID",
	}
	for _, field := range disallowedFields {
		if _, ok := inputType.FieldByName(field); ok {
			t.Fatalf("OpsInsertErrorLogInput still carries replay field %q", field)
		}
	}
}

// TestBuildOpsSystemLogsWhere_WithClientRequestIDAndUserID combines all
// supported system-log dimensions in one supplied filter. It protects an
// effective constraint flag, thirteen arguments, and host/client/user/key
// predicates.
func TestBuildOpsSystemLogsWhere_WithClientRequestIDAndUserID(t *testing.T) {
	start := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	userID := int64(12)
	apiKeyID := int64(56)
	providerID := int64(34)

	filter := &ops.OpsSystemLogFilter{
		StartTime:       &start,
		EndTime:         &end,
		Host:            "api-node-1",
		Level:           "warn",
		Component:       "http.access",
		RequestID:       "req-1",
		ClientRequestID: "creq-1",
		UserID:          &userID,
		APIKeyID:        &apiKeyID,
		ProviderID:      &providerID,
		Platform:        "openai",
		Model:           "gpt-5",
		Query:           "timeout",
	}

	where, args, hasConstraint := buildOpsSystemLogsWhere(filter)
	if !hasConstraint {
		t.Fatalf("expected hasConstraint=true")
	}
	if where == "" {
		t.Fatalf("where should not be empty")
	}
	if len(args) != 13 {
		t.Fatalf("args len = %d, want 13", len(args))
	}
	if !contains(where, "l.host = $") {
		t.Fatalf("where should include host condition: %s", where)
	}
	if !contains(where, "COALESCE(l.client_request_id,'') = $") {
		t.Fatalf("where should include client_request_id condition: %s", where)
	}
	if !contains(where, "l.user_id = $") {
		t.Fatalf("where should include user_id condition: %s", where)
	}
	if !contains(where, "l.api_key_id = $") {
		t.Fatalf("where should include api_key_id condition: %s", where)
	}
}

// TestBuildOpsSystemLogsCleanupWhere_RequireConstraint supplies an empty
// cleanup filter. It protects false effective-constraint status and no
// arguments, which the delete method uses to reject unrestricted deletion.
func TestBuildOpsSystemLogsCleanupWhere_RequireConstraint(t *testing.T) {
	where, args, hasConstraint := buildOpsSystemLogsCleanupWhere(&ops.OpsSystemLogCleanupFilter{})
	if hasConstraint {
		t.Fatalf("expected hasConstraint=false")
	}
	if where == "" {
		t.Fatalf("where should not be empty")
	}
	if len(args) != 0 {
		t.Fatalf("args len = %d, want 0", len(args))
	}
}

// TestBuildOpsSystemLogsCleanupWhere_WithClientRequestIDAndUserID supplies
// host, client correlation, user, and key restrictions. It protects their four
// arguments and effective-constraint flag through the cleanup-to-list mapping.
func TestBuildOpsSystemLogsCleanupWhere_WithClientRequestIDAndUserID(t *testing.T) {
	userID := int64(9)
	apiKeyID := int64(10)
	filter := &ops.OpsSystemLogCleanupFilter{
		Host:            "api-node-2",
		ClientRequestID: "creq-9",
		UserID:          &userID,
		APIKeyID:        &apiKeyID,
	}

	where, args, hasConstraint := buildOpsSystemLogsCleanupWhere(filter)
	if !hasConstraint {
		t.Fatalf("expected hasConstraint=true")
	}
	if len(args) != 4 {
		t.Fatalf("args len = %d, want 4", len(args))
	}
	if !contains(where, "l.host = $") {
		t.Fatalf("where should include host condition: %s", where)
	}
	if !contains(where, "COALESCE(l.client_request_id,'') = $") {
		t.Fatalf("where should include client_request_id condition: %s", where)
	}
	if !contains(where, "l.user_id = $") {
		t.Fatalf("where should include user_id condition: %s", where)
	}
	if !contains(where, "l.api_key_id = $") {
		t.Fatalf("where should include api_key_id condition: %s", where)
	}
}

// contains reports whether expected SQL text occurs within a supplied
// predicate.
func contains(s string, sub string) bool {
	return strings.Contains(s, sub)
}
