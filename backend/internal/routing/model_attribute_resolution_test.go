package routing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

type attributeSessionRepo struct {
	ModelAttributeRepository
	config *ModelAttributeConfig
	err    error
	calls  atomic.Int32
}

func (r *attributeSessionRepo) ForGroup(context.Context, int64) (*ModelAttributeConfig, error) {
	r.calls.Add(1)
	return r.config, r.err
}

func TestModelAttributeSessionSnapshotAndConcurrentReads(t *testing.T) {
	ctx := context.Background()
	limit := 100
	repo := &attributeSessionRepo{config: &ModelAttributeConfig{Status: StatusActive, Rules: []ModelAttributeRule{{Models: []string{"model"}, Attributes: modelcatalog.Attributes{OutputLimit: &limit}}}}}
	reads := 0
	service := &ModelAttributeService{Repo: repo, Catalog: ModelAttributeCatalog{View: func() modelcatalog.View { reads++; return modelcatalog.View{} }}}
	ctx = WithModelAttributes(ctx, service)
	session := ModelAttributesFromContext(ctx)
	target := modelcatalog.Target{Model: "model", Platform: "openai"}
	first, err := session.Resolve(ctx, 1, target)
	require.NoError(t, err)
	require.Equal(t, 100, *first.Attributes.OutputLimit)
	limit = 200
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			result, err := session.Resolve(ctx, 1, target)
			require.NoError(t, err)
			require.Equal(t, 100, *result.Attributes.OutputLimit)
			*result.Attributes.OutputLimit = 300
		})
	}
	workers.Wait()
	require.EqualValues(t, 1, repo.calls.Load())
	require.Equal(t, 1, reads)
	renewed := ModelAttributesFromContext(RenewModelAttributes(ctx))
	second, err := renewed.Resolve(ctx, 1, target)
	require.NoError(t, err)
	require.Equal(t, 200, *second.Attributes.OutputLimit)
	require.EqualValues(t, 2, repo.calls.Load())
	require.Equal(t, 2, reads)
	repo.config.Status = StatusDisabled
	third, err := service.NewSession().Resolve(ctx, 1, target)
	require.NoError(t, err)
	require.Nil(t, third.Attributes.OutputLimit)
}

func TestModelAttributeSessionCachesReadFailure(t *testing.T) {
	repo := &attributeSessionRepo{err: errors.New("database offline")}
	session := (&ModelAttributeService{Repo: repo}).NewSession()
	for range 2 {
		_, err := session.Resolve(context.Background(), 1, modelcatalog.Target{Model: "unknown"})
		require.ErrorContains(t, err, "model attribute configuration is unavailable")
	}
	require.EqualValues(t, 1, repo.calls.Load())
}
