package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Batch profile reads return one decoded object to all requested groups sharing
// that profile. SQL expectations check subset filtering, explicit false
// attributes, and query or decoding failures, while a failed association insert
// verifies rollback of the surrounding profile save.

// TestAttributeBatchLookup supplies one profile associated with requested and
// unrelated groups. It checks that only requested groups appear, shared groups
// reuse one pointer, and an explicit false attribute survives decoding.
func TestAttributeBatchLookup(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	store := NewModelAttributeStore(db)
	query := attributeSelect + "WHERE c.status='active' AND EXISTS (SELECT 1 FROM model_attribute_config_groups g WHERE g.config_id=c.id AND g.group_id=ANY($1))"
	rows := sqlmock.NewRows([]string{"id", "name", "description", "status", "rules", "created_at", "updated_at", "groups"}).
		AddRow(9, "shared", "", "active", `[{"models":["model"],"attributes":{"tool_call":false}}]`, time.Now(), time.Now(), "{1,2,99}")
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs("{1,2,3,1}").WillReturnRows(rows).RowsWillBeClosed()
	result, err := store.ForGroups(context.Background(), []int64{1, 2, 3, 1})
	require.NoError(t, err)
	require.Len(t, result, 2)
	require.Same(t, result[1], result[2])
	require.False(t, *result[1].Rules[0].Attributes.ToolCall)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAttributeBatchLookupFailure injects query, JSON decoding, and first-row
// read failures. Each failed lookup returns an error and nil result, while an
// empty group list succeeds without a SQL query.
func TestAttributeBatchLookupFailure(t *testing.T) {
	for _, failure := range []string{"query", "decode", "rows"} {
		t.Run(failure, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			store := NewModelAttributeStore(db)
			empty, err := store.ForGroups(context.Background(), nil)
			require.NoError(t, err)
			require.Empty(t, empty)
			expect := mock.ExpectQuery("SELECT c.id").WithArgs("{1}")
			if failure == "query" {
				expect.WillReturnError(errors.New("database unavailable"))
			} else {
				body := "[]"
				if failure == "decode" {
					body = "invalid"
				}
				rows := sqlmock.NewRows([]string{"id", "name", "description", "status", "rules", "created_at", "updated_at", "groups"}).
					AddRow(9, "shared", "", "active", body, time.Now(), time.Now(), "{1}")
				if failure == "rows" {
					rows.RowError(0, errors.New("read interrupted"))
				}
				expect.WillReturnRows(rows).RowsWillBeClosed()
			}
			result, err := store.ForGroups(context.Background(), []int64{1})
			require.Error(t, err)
			require.Nil(t, result)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestAttributeAssociationConflictRollsBackEntireConfig injects an association
// conflict after the mocked profile insertion. It checks that Save requests
// rollback and returns ErrAttributeConfigConflict, covering transaction cleanup
// and error translation.
func TestAttributeAssociationConflictRollsBackEntireConfig(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	store := NewModelAttributeStore(db)
	config := &routing.ModelAttributeConfig{Name: "new", Status: "active", GroupIDs: []int64{7}, Rules: []routing.ModelAttributeRule{}}
	// Fail after the profile insert to require rollback of preceding writes; a
	// constraint error must not leave a committed unassociated profile.
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO model_attribute_configs").WithArgs("new", "", "active", "[]").WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(5, time.Now(), time.Now()))
	mock.ExpectExec("DELETE FROM model_attribute_config_groups").WithArgs(int64(5)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO model_attribute_config_groups").WithArgs(int64(5), int64(7)).WillReturnError(&pq.Error{Code: "23505"})
	mock.ExpectRollback()
	require.ErrorIs(t, store.Save(context.Background(), config), routing.ErrAttributeConfigConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}
