//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// Attribute profiles can serve several groups, while each group can bind at
// most one profile. PostgreSQL fixtures exercise that association constraint,
// disabled-profile lookup, and JSONB presence values that distinguish an
// inherited attribute from a deliberate empty or zero override.

// TestModelAttributeBatchLookup binds two groups to one active profile beside
// disabled and unassociated groups. It checks shared result pointers, requested
// subsets, and removal from lookup after disabling the active profile.
func TestModelAttributeBatchLookup(t *testing.T) {
	db := postgrescontainer.New(t)
	ctx := context.Background()
	ids := make([]int64, 4)
	for i := range ids {
		require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES ($1) RETURNING id`, fmt.Sprintf("attribute-batch-%d", i)).Scan(&ids[i]))
	}
	store := NewModelAttributeStore(db)
	active := &routing.ModelAttributeConfig{Name: "shared", Status: "active", GroupIDs: ids[:2], Rules: []routing.ModelAttributeRule{}}
	inactive := &routing.ModelAttributeConfig{Name: "disabled", Status: "disabled", GroupIDs: ids[2:3], Rules: []routing.ModelAttributeRule{}}
	require.NoError(t, store.Save(ctx, active))
	require.NoError(t, store.Save(ctx, inactive))
	result, err := store.ForGroups(ctx, ids)
	require.NoError(t, err)
	require.Len(t, result, 2)
	require.Equal(t, active.ID, result[ids[0]].ID)
	require.Same(t, result[ids[0]], result[ids[1]])
	subset, err := store.ForGroups(ctx, ids[:1])
	require.NoError(t, err)
	require.Len(t, subset, 1)
	active.Status = "disabled"
	require.NoError(t, store.Save(ctx, active))
	result, err = store.ForGroups(ctx, ids)
	require.NoError(t, err)
	require.Empty(t, result)
}

// TestModelAttributeMigrationAndConcurrentAssociation races two profile saves
// for one group after applying migrations again. It checks one committed
// winner, one conflict with its profile rolled back, disabled lookup, and
// cascading association deletion.
func TestModelAttributeMigrationAndConcurrentAssociation(t *testing.T) {
	db := postgrescontainer.New(t)
	ctx := context.Background()
	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	var groupID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES ('attribute-test') RETURNING id`).Scan(&groupID))
	store := NewModelAttributeStore(db)
	var wg sync.WaitGroup
	// Buffer one outcome per concurrent save so both writers can finish before the
	// test waits and drains their results.
	errors := make(chan error, 2)
	for _, name := range []string{"first", "second"} {
		wg.Go(func() {
			value := &routing.ModelAttributeConfig{Name: name, Status: "active", Rules: []routing.ModelAttributeRule{}, GroupIDs: []int64{groupID}}
			errors <- store.Save(ctx, value)
		})
	}
	// Wait for both saves before closing the result channel; ranging afterward
	// observes each outcome without racing a sender with closure.
	wg.Wait()
	close(errors)
	successes, conflicts := 0, 0
	for err := range errors {
		if err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, routing.ErrAttributeConfigConflict)
			conflicts++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	rows, err := store.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	value, err := store.ForGroup(ctx, groupID)
	require.NoError(t, err)
	require.NotNil(t, value)
	value.Status = "disabled"
	require.NoError(t, store.Save(ctx, value))
	inactive, err := store.ForGroup(ctx, groupID)
	require.NoError(t, err)
	require.Nil(t, inactive)
	require.NoError(t, store.Delete(ctx, value.ID))
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM model_attribute_config_groups WHERE group_id=$1`, groupID).Scan(&count))
	require.Zero(t, count)
}

// TestModelAttributeRulesJSONBRoundTrip stores scoped reasoning and media
// overrides with empty collections, nil effort, and zero bounds. It compares
// all rules after JSONB loading and checks that replacing attributes clears
// optional reasoning and media values.
func TestModelAttributeRulesJSONBRoundTrip(t *testing.T) {
	db := postgrescontainer.New(t)
	store := NewModelAttributeStore(db)
	ctx := context.Background()
	zero, maximum := 0, 24576
	high := "high"
	emptyOptions := []modelcatalog.ReasoningOption{}
	options := []modelcatalog.ReasoningOption{{Type: "effort", Values: []*string{nil, &high}}, {Type: "budget_tokens", Min: &zero, Max: &maximum}}
	conditions := modelcatalog.ParameterConditions{"temperature": {Efforts: []string{"none"}}}
	value := &routing.ModelAttributeConfig{Name: "scoped-rules", Status: "active", Rules: []routing.ModelAttributeRule{
		{ID: "empty", Models: []string{"model"}, Match: &routing.ModelAttributeMatch{Platform: "kimi", ProviderMode: "coding", Site: "global"}, Attributes: modelcatalog.Attributes{ReasoningOptions: &emptyOptions, Media: &modelcatalog.MediaCapabilities{Qualities: []string{}, MaxReferenceImages: &zero}}},
		{ID: "controls", Models: []string{"model"}, Match: &routing.ModelAttributeMatch{Platform: "gemini", ProviderType: "service_account"}, Attributes: modelcatalog.Attributes{ReasoningOptions: &options, Conditions: &conditions}},
	}}
	require.NoError(t, store.Save(ctx, value))
	loaded, err := store.Get(ctx, value.ID)
	require.NoError(t, err)
	require.Equal(t, value.Rules, loaded.Rules)
	loaded.Rules[0].Attributes = modelcatalog.Attributes{}
	require.NoError(t, store.Save(ctx, loaded))
	cleared, err := store.Get(ctx, value.ID)
	require.NoError(t, err)
	require.Nil(t, cleared.Rules[0].Attributes.ReasoningOptions)
	require.Nil(t, cleared.Rules[0].Attributes.Media)
}
