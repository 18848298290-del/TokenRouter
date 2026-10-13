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

// Attribute sessions hold one catalogue and group profile across repeated route
// checks. The fixtures test concurrent cache reads, retained repository
// failures, and value-based route keys so a fresh session can observe updates
// while an existing session keeps its recorded inputs.

// attributeSessionRepo returns shared configuration and counts profile reads
// across session tests.
type attributeSessionRepo struct {
	ModelAttributeRepository // unused repository operations supplied by embedding
	// config and err are set before concurrent reads begin. ForGroup returns
	// these values as supplied; later profile edits exercise the session's copied
	// snapshot after its first load.
	config *ModelAttributeConfig // shared profile returned by fixture
	err    error                 // configured profile read failure
	calls  atomic.Int32          // concurrent profile read counter
}

// ForGroup increments the atomic read counter and returns the fixture's shared
// profile and error. Tests set the profile before concurrent session access and
// use the counter to assert one load.
func (r *attributeSessionRepo) ForGroup(context.Context, int64) (*ModelAttributeConfig, error) {
	r.calls.Add(1)
	return r.config, r.err
}

// TestModelAttributeSessionSnapshotAndConcurrentReads mutates stored limits
// after the first resolution and starts concurrent cache readers. Existing
// results must keep the captured limit, while renewed sessions read updates and
// disabled profiles.
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
	// Concurrent callers reuse the captured profile and receive detached cache
	// results, so their mutations must not affect each other or the retained
	// limit.
	for range 20 {
		workers.Go(func() {
			result, err := session.Resolve(ctx, 1, target)
			require.NoError(t, err)
			require.Equal(t, 100, *result.Attributes.OutputLimit)
			*result.Attributes.OutputLimit = 300
		})
	}
	// Finish every reader before observing counters or renewing the session; later
	// checks then measure a completed request rather than racing its workers.
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

// TestModelAttributeSessionCachesReadFailure supplies a repository error and
// resolves twice through one session. Both calls must expose configuration
// unavailability, and the repository counter must remain at one.
func TestModelAttributeSessionCachesReadFailure(t *testing.T) {
	repo := &attributeSessionRepo{err: errors.New("database offline")}
	session := (&ModelAttributeService{Repo: repo}).NewSession()
	for range 2 {
		_, err := session.Resolve(context.Background(), 1, modelcatalog.Target{Model: "unknown"})
		require.ErrorContains(t, err, "model attribute configuration is unavailable")
	}
	require.EqualValues(t, 1, repo.calls.Load())
}

// TestModelAttributeSessionCachesRouteValues edits returned attributes, source
// labels, and a target cap before repeating an equivalent route. Cached values
// must stay detached, and changing the upstream protocol must trigger another
// lookup.
func TestModelAttributeSessionCachesRouteValues(t *testing.T) {
	lookups := 0
	service := &ModelAttributeService{Catalog: ModelAttributeCatalog{Lookup: func(string) modelcatalog.Attributes {
		lookups++
		value := 100
		return modelcatalog.Attributes{OutputLimit: &value}
	}}}
	session := service.NewSession()
	cap := 100
	target := modelcatalog.Target{Model: "private", Platform: "openai", Ollama: true, OllamaOutputCap: &cap, UpstreamProtocol: "openai_chat_completions"}
	first, err := session.Resolve(context.Background(), 1, target)
	require.NoError(t, err)
	*first.Attributes.OutputLimit = 900
	*first.Target.OllamaOutputCap = 900
	first.Sources["output_limit"] = "changed"
	sameCap := 100
	target.OllamaOutputCap = &sameCap
	second, err := session.Resolve(context.Background(), 1, target)
	require.NoError(t, err)
	require.Equal(t, 1, lookups)
	require.Equal(t, 100, *second.Attributes.OutputLimit)
	require.Equal(t, 100, *second.Target.OllamaOutputCap)
	require.NotEqual(t, "changed", second.Sources["output_limit"])
	target.UpstreamProtocol = "openai_responses"
	_, err = session.Resolve(context.Background(), 1, target)
	require.NoError(t, err)
	require.Equal(t, 2, lookups)
}
