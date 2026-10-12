package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

type teamLinkedProviderRepoStub struct {
	teamProviders []Record
	listErr       error
	listCalls     int
	setErrorIDs   []int64
	setErrorMsgs  map[int64]string
	failSetError  map[int64]error
}

type teamBlockRecorder struct {
	providers []*Record
	reasons   []string
}

// ListByPlatform returns active fixture providers on the requested platform.
func (r *teamLinkedProviderRepoStub) ListByPlatform(ctx context.Context, platform string) ([]Record, error) {
	r.listCalls++
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := make([]Record, 0, len(r.teamProviders))
	for _, acc := range r.teamProviders {
		if acc.Platform == platform && acc.Status == StatusActive {
			out = append(out, acc)
		}
	}
	return out, nil
}

func (r *teamLinkedProviderRepoStub) SetError(ctx context.Context, id int64, errorMsg string) error {
	if err, ok := r.failSetError[id]; ok {
		return err
	}
	r.setErrorIDs = append(r.setErrorIDs, id)
	if r.setErrorMsgs == nil {
		r.setErrorMsgs = make(map[int64]string)
	}
	r.setErrorMsgs[id] = errorMsg
	return nil
}

func newTeamLinkedProvider(id int64, teamID string) Record {
	return Record{
		ID:          id,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      StatusActive,
		Credentials: map[string]any{"chatgpt_account_id": teamID},
	}
}

// newTeamLinkedFixture includes the trigger, two teammates, another team, an API key, a shadow, and an errored
// teammate.
func newTeamLinkedFixture() []Record {
	parentID := int64(1)
	shadow := Record{
		ID:               5,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		Status:           StatusActive,
		ParentProviderID: &parentID,
	}
	apikey := newTeamLinkedProvider(4, "team-A")
	apikey.Type = capability.ProviderTypeAPIKey
	erroredSibling := newTeamLinkedProvider(7, "team-A")
	erroredSibling.Status = StatusError
	return []Record{
		newTeamLinkedProvider(1, "team-A"),
		newTeamLinkedProvider(2, "team-A"),
		newTeamLinkedProvider(3, "team-B"),
		apikey,
		shadow,
		newTeamLinkedProvider(6, "team-A"),
		erroredSibling,
	}
}

func TestTeamLinkedError_DirectCallSkipsTriggerProvider(t *testing.T) {
	// Direct propagation excludes the triggering provider, whose own error handling is separate.
	repo := &teamLinkedProviderRepoStub{teamProviders: newTeamLinkedFixture()}
	rl, blocker := newTeamLinkedTestService(repo)
	trigger := newTeamLinkedProvider(1, "team-A")

	rl.HandleWorkspaceDeactivated(context.Background(), &trigger, true)

	require.Equal(t, []int64{2, 6}, repo.setErrorIDs)
	require.Equal(t, []string{OpenAITeamLinkedErrorBlockReason, OpenAITeamLinkedErrorBlockReason}, blocker.reasons)
}

func TestTeamLinkedError_MissingTeamIDDoesNothing(t *testing.T) {
	repo := &teamLinkedProviderRepoStub{teamProviders: newTeamLinkedFixture()}
	rl, blocker := newTeamLinkedTestService(repo)
	trigger := Record{ID: 9, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: StatusActive}

	rl.HandleWorkspaceDeactivated(context.Background(), &trigger, true)

	require.Empty(t, repo.setErrorIDs)
	require.Empty(t, blocker.reasons)
	require.Zero(t, repo.listCalls)
}

func TestTeamLinkedError_SetErrorFailureDoesNotAbortRemaining(t *testing.T) {
	repo := &teamLinkedProviderRepoStub{
		teamProviders: newTeamLinkedFixture(),
		failSetError:  map[int64]error{2: errors.New("db down")},
	}
	rl, blocker := newTeamLinkedTestService(repo)
	trigger := newTeamLinkedProvider(1, "team-A")

	rl.HandleWorkspaceDeactivated(context.Background(), &trigger, true)

	require.Equal(t, []int64{6}, repo.setErrorIDs)
	// Both providers are blocked in memory before either persistent write is attempted.
	require.Len(t, blocker.reasons, 2)
}

// newTeamLinkedTestService supplies the fixture store and records runtime blocks.
func newTeamLinkedTestService(repo *teamLinkedProviderRepoStub) (*TeamLinkedHealth, *teamBlockRecorder) {
	blocker := &teamBlockRecorder{}
	return NewTeamLinkedHealth(repo, TeamLinkedOptions{Block: blocker.Block}), blocker
}

func (r *teamBlockRecorder) Block(value *Record, _ time.Time, reason string) {
	r.providers = append(r.providers, value)
	r.reasons = append(r.reasons, reason)
}
