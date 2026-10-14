package postgres

import (
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/promotion"
)

// Affiliate profile reads retain nullable inviter and percentage values
// alongside quota totals and timestamps. Executed-query fixtures check both
// lookup predicates, field decoding, and result cleanup. Administrator query
// text checks cover matured funds, stored audit observations, and order
// currencies.

const (
	// affiliateProfileTestSelect describes the stored eleven-column result
	// independently of the production SELECT so query expectations detect
	// reordered fields and changed quota casts.
	affiliateProfileTestSelect = `
SELECT user_id,
       aff_code,
       aff_code_custom,
       aff_rebate_rate_percent,
       inviter_id,
       aff_count,
       aff_quota::double precision,
       aff_frozen_quota::double precision,
       aff_history_quota::double precision,
       created_at,
       updated_at
FROM user_affiliates`
)

// affiliateProfileLookup pairs each lookup with its executed SQL and argument.
type affiliateProfileLookup struct {
	name     string                                                                           // lookup scenario label
	query    string                                                                           // expected executed statement
	argument driver.Value                                                                     // expected bound identifier
	lookup   func(context.Context, affiliateQueryExecer) (*promotion.AffiliateSummary, error) // profile read operation
}

// TestAffiliateUserOverviewSQLIncludesMaturedFrozenQuota normalizes the
// overview query's whitespace before inspecting its text.
//
// It checks that due frozen ledger amounts augment displayed available quota
// using the database clock.
func TestAffiliateUserOverviewSQLIncludesMaturedFrozenQuota(t *testing.T) {
	query := strings.Join(strings.Fields(affiliateUserOverviewSQL), " ")

	require.Contains(t, query, "ua.aff_quota + COALESCE(matured.matured_frozen_quota, 0)")
	require.Contains(t, query, "frozen_until <= NOW()")
}

// TestAffiliateRecordQueriesUseLedgerAuditFields reads affiliate.go as source
// text and inspects its query expressions.
//
// It protects ledger grant and balance fields, order joins, and the currency
// precedence used by administrator records.
func TestAffiliateRecordQueriesUseLedgerAuditFields(t *testing.T) {
	source, err := os.ReadFile("affiliate.go")
	require.NoError(t, err)
	content := string(source)

	require.Contains(t, content, "JOIN payment_orders po ON po.id = ual.source_order_id")
	require.Contains(t, content, "LEFT JOIN subscription_plans sp ON sp.id = po.plan_id")
	require.Contains(t, content, "ual.amount::double precision")
	require.Contains(t, content, "COALESCE(NULLIF(po.order_type, ''), 'balance')")
	require.Contains(t, content, "NULLIF(UPPER(po.plan_snapshot->>'currency'), '')")
	require.Contains(t, content, "NULLIF(UPPER(sp.currency), '')")
	require.Contains(t, content, "COALESCE(NULLIF(UPPER(po.provider_snapshot->>'currency'), ''), 'CNY')")
	require.Contains(t, content, "ual.balance_after::double precision")
	require.NotContains(t, content, "parseAffiliateRebateAmount")
	require.NotContains(t, content, `"current_balance": "u.balance"`)
}

// TestAffiliateProfileQueries_ScanFields reads distinct values through both
// lookup predicates. The result keeps every field in its stored position and
// distinguishes absent percentages from custom zero and positive overrides.
func TestAffiliateProfileQueries_ScanFields(t *testing.T) {
	createdAt := time.Date(2026, time.October, 14, 1, 2, 3, 0, time.UTC)
	updatedAt := createdAt.Add(2 * time.Hour)
	zeroRate := 0.0
	positiveRate := 17.5
	inviterID := int64(42)
	cases := []struct {
		name        string       // nullable value scenario
		rate        driver.Value // stored percentage input
		inviter     driver.Value // stored inviter input
		wantRate    *float64     // expected percentage pointer
		wantInviter *int64       // expected inviter pointer
	}{
		{name: "null"},
		{name: "zero", rate: zeroRate, inviter: inviterID, wantRate: &zeroRate, wantInviter: &inviterID},
		{name: "positive", rate: positiveRate, inviter: inviterID, wantRate: &positiveRate, wantInviter: &inviterID},
	}
	for _, lookup := range affiliateProfileLookups() {
		for _, tc := range cases {
			t.Run(lookup.name+"/"+tc.name, func(t *testing.T) {
				db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
				require.NoError(t, err)
				// Keep fixture cleanup after the lookup assertions so result closure
				// is checked before releasing the mock database connection itself.
				t.Cleanup(func() {
					mock.ExpectClose()
					require.NoError(t, db.Close())
					require.NoError(t, mock.ExpectationsWereMet())
				})
				mock.ExpectQuery(lookup.query).WithArgs(lookup.argument).
					WillReturnRows(affiliateProfileRows().AddRow(
						int64(73), "VIP_CODE", true, tc.rate, tc.inviter, int64(9),
						12.25, 3.5, 45.75, createdAt, updatedAt,
					)).RowsWillBeClosed()

				got, err := lookup.lookup(context.Background(), db)
				require.NoError(t, err)
				require.Equal(t, &promotion.AffiliateSummary{
					UserID:               73,
					AffCode:              "VIP_CODE",
					AffCodeCustom:        true,
					AffRebateRatePercent: tc.wantRate,
					InviterID:            tc.wantInviter,
					AffCount:             9,
					AffQuota:             12.25,
					AffFrozenQuota:       3.5,
					AffHistoryQuota:      45.75,
					CreatedAt:            createdAt,
					UpdatedAt:            updatedAt,
				}, got)
				require.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}

// TestAffiliateProfileQueries_ReadErrors checks failures before and during the
// first row together with deferred result closure. Both lookups return their
// selected error or first profile before any unread-row or close failure.
func TestAffiliateProfileQueries_ReadErrors(t *testing.T) {
	queryErr := errors.New("affiliate query failed")
	rowErr := errors.New("affiliate iteration failed")
	closeErr := errors.New("affiliate close failed")
	stamp := time.Date(2026, time.October, 14, 1, 2, 3, 0, time.UTC)
	profileRows := func() *sqlmock.Rows {
		return affiliateProfileRows().AddRow(int64(73), "VIP_CODE", false, nil, nil,
			int64(0), 0.0, 0.0, 0.0, stamp, stamp)
	}
	cases := []struct {
		name          string               // selected error scenario
		queryErr      error                // initial query failure
		rows          func() *sqlmock.Rows // returned database rows
		wantErr       error                // expected error identity
		wantErrorText string               // expected scan diagnostic
	}{
		{name: "query", queryErr: queryErr, wantErr: queryErr},
		{name: "missing", rows: affiliateProfileRows, wantErr: promotion.ErrAffiliateProfileNotFound},
		{name: "iteration", rows: func() *sqlmock.Rows {
			return profileRows().RowError(0, rowErr).CloseError(closeErr)
		}, wantErr: rowErr},
		{name: "scan", rows: func() *sqlmock.Rows {
			return affiliateProfileRows().AddRow(int64(73), "VIP_CODE", false, nil, nil,
				"invalid-count", 0.0, 0.0, 0.0, stamp, stamp).CloseError(closeErr)
		}, wantErrorText: `column index 5, name "aff_count"`},
		{name: "empty-close", rows: func() *sqlmock.Rows {
			return affiliateProfileRows().CloseError(closeErr)
		}, wantErr: closeErr},
		{name: "successful-close", rows: func() *sqlmock.Rows {
			return profileRows().CloseError(closeErr)
		}},
		{name: "unread-row", rows: func() *sqlmock.Rows {
			return profileRows().AddRow(int64(74), "OTHER_CODE", true, nil, nil,
				int64(1), 1.0, 2.0, 3.0, stamp, stamp).RowError(1, rowErr)
		}},
	}
	for _, lookup := range affiliateProfileLookups() {
		for _, tc := range cases {
			t.Run(lookup.name+"/"+tc.name, func(t *testing.T) {
				db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
				require.NoError(t, err)
				// Keep fixture cleanup after the lookup assertions so result closure
				// is checked before releasing the mock database connection itself.
				t.Cleanup(func() {
					mock.ExpectClose()
					require.NoError(t, db.Close())
					require.NoError(t, mock.ExpectationsWereMet())
				})
				expectation := mock.ExpectQuery(lookup.query).WithArgs(lookup.argument)
				if tc.queryErr != nil {
					expectation.WillReturnError(tc.queryErr)
				} else {
					expectation.WillReturnRows(tc.rows()).RowsWillBeClosed()
				}

				got, err := lookup.lookup(context.Background(), db)
				switch {
				case tc.wantErr != nil:
					require.ErrorIs(t, err, tc.wantErr)
					require.Nil(t, got)
				case tc.wantErrorText != "":
					require.ErrorContains(t, err, tc.wantErrorText)
					require.NotErrorIs(t, err, closeErr)
					require.Nil(t, got)
				default:
					require.NoError(t, err)
					require.NotNil(t, got)
					require.Equal(t, int64(73), got.UserID)
				}
				require.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}

// affiliateProfileLookups supplies independently expected predicates and bound
// values for the two profile reads. The code case passes mixed case and outer
// whitespace to exercise normalization before the SQL argument is bound.
func affiliateProfileLookups() []affiliateProfileLookup {
	return []affiliateProfileLookup{
		{
			name:     "user-id",
			query:    affiliateProfileTestSelect + "\nWHERE user_id = $1",
			argument: int64(73),
			lookup: func(ctx context.Context, client affiliateQueryExecer) (*promotion.AffiliateSummary, error) {
				return queryAffiliateByUserID(ctx, client, 73)
			},
		},
		{
			name:     "code",
			query:    affiliateProfileTestSelect + "\nWHERE aff_code = $1\nLIMIT 1",
			argument: "VIP_CODE",
			lookup: func(ctx context.Context, client affiliateQueryExecer) (*promotion.AffiliateSummary, error) {
				return queryAffiliateByCode(ctx, client, " \tviP_cOdE\n ")
			},
		},
	}
}

// affiliateProfileRows creates the eleven database column names used by profile
// fixtures, keeping scan diagnostics tied to their stored column identities.
func affiliateProfileRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"user_id", "aff_code", "aff_code_custom", "aff_rebate_rate_percent",
		"inviter_id", "aff_count", "aff_quota", "aff_frozen_quota",
		"aff_history_quota", "created_at", "updated_at",
	})
}
