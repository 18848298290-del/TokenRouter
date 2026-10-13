package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Administrator quota and ledger queries need to expose matured funds and
// stored audit observations consistently. Text-based checks protect the query
// expressions used for those displays and keep order currencies distinct from
// ledger grant amounts.

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
