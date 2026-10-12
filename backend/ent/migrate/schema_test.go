package migrate

import (
	"testing"

	"entgo.io/ent/dialect/entsql"
	entschema "entgo.io/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"
)

// TestAuthIdentityFoundationForeignKeyOnDeleteActions inspects generated identity
// relationship metadata. It requires three cascade and two set-null actions
// for the named user, channel, pending-session, and adoption relationships.
func TestAuthIdentityFoundationForeignKeyOnDeleteActions(t *testing.T) {
	require.Equal(
		t,
		entschema.Cascade,
		findForeignKeyBySymbol(t, AuthIdentitiesTable, "auth_identities_users_auth_identities").OnDelete,
	)
	require.Equal(
		t,
		entschema.Cascade,
		findForeignKeyBySymbol(t, AuthIdentityChannelsTable, "auth_identity_channels_auth_identities_channels").OnDelete,
	)
	require.Equal(
		t,
		entschema.Cascade,
		findForeignKeyBySymbol(t, IdentityAdoptionDecisionsTable, "identity_adoption_decisions_pending_auth_sessions_adoption_decision").OnDelete,
	)

	require.Equal(
		t,
		entschema.SetNull,
		findForeignKeyBySymbol(t, PendingAuthSessionsTable, "pending_auth_sessions_users_pending_auth_sessions").OnDelete,
	)
	require.Equal(
		t,
		entschema.SetNull,
		findForeignKeyBySymbol(t, IdentityAdoptionDecisionsTable, "identity_adoption_decisions_auth_identities_adoption_decisions").OnDelete,
	)
}

// TestPaymentOrdersOutTradeNoPartialUniqueIndex inspects the generated merchant
// order-number index. It requires uniqueness on that single column and the
// predicate excluding empty merchant numbers from the index.
func TestPaymentOrdersOutTradeNoPartialUniqueIndex(t *testing.T) {
	idx := findIndexByName(t, PaymentOrdersTable, "paymentorder_out_trade_no")
	require.True(t, idx.Unique)
	require.Len(t, idx.Columns, 1)
	require.Equal(t, "out_trade_no", idx.Columns[0].Name)
	require.NotNil(t, idx.Annotation)
	require.Equal(t, (&entsql.IndexAnnotation{Where: "out_trade_no <> ''"}).Where, idx.Annotation.Where)
}

// TestProvidersParentProviderForeignKey inspects the generated parent-provider
// relationship. It requires one nonunique child column, one referenced ID
// column, and restrictive parent deletion, checking the foreign key independently
// of partial shadow-index enforcement.
func TestProvidersParentProviderForeignKey(t *testing.T) {
	fk := findForeignKeyByColumn(t, ProvidersTable, "parent_provider_id")
	require.Len(t, fk.Columns, 1)
	require.Equal(t, "parent_provider_id", fk.Columns[0].Name)
	require.False(t, fk.Columns[0].Unique, "active-shadow uniqueness is enforced by the partial uq_providers_spark_shadow_per_parent index")
	require.Len(t, fk.RefColumns, 1)
	require.Equal(t, "id", fk.RefColumns[0].Name)
	require.Equal(t, entschema.Restrict, fk.OnDelete)
}

// findForeignKeyBySymbol returns the generated table foreign key with symbol.
// A missing symbol stops the calling test through require.Failf. The returned
// pointer belongs to the table's shared metadata.
func findForeignKeyBySymbol(t *testing.T, table *entschema.Table, symbol string) *entschema.ForeignKey {
	t.Helper()

	for _, fk := range table.ForeignKeys {
		if fk.Symbol == symbol {
			return fk
		}
	}

	require.Failf(t, "missing foreign key", "table %s should include foreign key %s", table.Name, symbol)
	return nil
}

// findIndexByName returns the generated table index with the supplied name.
// A missing name stops the calling test through require.Failf. The returned
// pointer references the index held by the table metadata.
func findIndexByName(t *testing.T, table *entschema.Table, name string) *entschema.Index {
	t.Helper()

	for _, idx := range table.Indexes {
		if idx.Name == name {
			return idx
		}
	}

	require.Failf(t, "missing index", "table %s should include index %s", table.Name, name)
	return nil
}

// findForeignKeyByColumn returns the first generated foreign key containing column.
// It searches every column in each key and stops the calling test when none
// matches. The result remains part of the table's shared metadata.
func findForeignKeyByColumn(t *testing.T, table *entschema.Table, column string) *entschema.ForeignKey {
	t.Helper()

	for _, fk := range table.ForeignKeys {
		for _, col := range fk.Columns {
			if col.Name == column {
				return fk
			}
		}
	}

	require.Failf(t, "missing foreign key", "table %s should include foreign key for column %s", table.Name, column)
	return nil
}
