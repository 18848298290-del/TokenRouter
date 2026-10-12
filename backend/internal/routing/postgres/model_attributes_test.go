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

// TestAttributeBatchLookup checks one row per shared profile and limits results to requested groups.
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

// TestAttributeBatchLookupFailure discards partial query results and skips SQL for an empty group list.
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

func TestAttributeAssociationConflictRollsBackEntireConfig(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	store := NewModelAttributeStore(db)
	config := &routing.ModelAttributeConfig{Name: "new", Status: "active", GroupIDs: []int64{7}, Rules: []routing.ModelAttributeRule{}}
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO model_attribute_configs").WithArgs("new", "", "active", "[]").WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(5, time.Now(), time.Now()))
	mock.ExpectExec("DELETE FROM model_attribute_config_groups").WithArgs(int64(5)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO model_attribute_config_groups").WithArgs(int64(5), int64(7)).WillReturnError(&pq.Error{Code: "23505"})
	mock.ExpectRollback()
	require.ErrorIs(t, store.Save(context.Background(), config), routing.ErrAttributeConfigConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}
