package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Team propagation filters a fixture containing two eligible teammates
// alongside the trigger, another team, an API key, a shadow, and an errored
// sibling. The fake store exposes successful writes and runtime callbacks
// separately, including continuation after one teammate's write fails.

// teamLinkedProviderRepoStub supplies platform fixtures and records successful
// teammate error writes.
type teamLinkedProviderRepoStub struct {
	// teamProviders remains borrowed fixture data during propagation. Keep its
	// nested values stable while the handler uses records returned by
	// ListByPlatform.
	teamProviders []Record // platform query fixtures
	// listErr rejects a query after incrementing listCalls, letting tests
	// distinguish an attempted lookup from a skipped propagation.
	listErr   error
	listCalls int // attempted platform queries
	// setErrorIDs and setErrorMsgs record the same successful writes, with IDs
	// retaining call order and messages retaining the latest diagnostic per
	// provider.
	setErrorIDs  []int64          // successful write order
	setErrorMsgs map[int64]string // successful provider diagnostics
	// failSetError intercepts IDs present in the map before recording success. A
	// present nil error also skips the success records.
	failSetError map[int64]error
}

// teamBlockRecorder captures runtime targets and reasons in callback order.
type teamBlockRecorder struct {
	// providers and reasons append one corresponding entry per Block call.
	// Provider pointers remain the exact callback inputs for tests inspecting
	// target identity.
	providers []*Record // received target pointers
	reasons   []string  // corresponding block reasons
}

// ListByPlatform counts a query and returns active fixtures matching platform.
// A configured listErr ends the call after counting it; returned Record values
// use a new slice while their nested reference values remain shared.
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

// SetError records a successful fixture write unless failSetError contains id.
// Any present map entry returns its configured error, including nil, before
// success is appended or its diagnostic is stored.
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

// newTeamLinkedProvider creates an active OpenAI OAuth fixture with the
// supplied ID and team credential. Each call allocates its own credentials map
// for later fixture changes.
func newTeamLinkedProvider(id int64, teamID string) Record {
	return Record{
		ID:          id,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      StatusActive,
		Credentials: map[string]any{"chatgpt_account_id": teamID},
	}
}

// newTeamLinkedFixture supplies seven records spanning eligible teammates and
// the trigger, account, type, shadow, and status exclusions exercised by
// propagation tests.
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

// TestTeamLinkedError_DirectCallSkipsTriggerProvider invokes propagation with
// the mixed team fixture. Successful error writes select IDs 2 and 6, and both
// runtime callbacks carry the team-linked reason.
func TestTeamLinkedError_DirectCallSkipsTriggerProvider(t *testing.T) {
	repo := &teamLinkedProviderRepoStub{teamProviders: newTeamLinkedFixture()}
	rl, blocker := newTeamLinkedTestService(repo)
	trigger := newTeamLinkedProvider(1, "team-A")

	rl.HandleWorkspaceDeactivated(context.Background(), &trigger, true)

	require.Equal(t, []int64{2, 6}, repo.setErrorIDs)
	require.Equal(t, []string{OpenAITeamLinkedErrorBlockReason, OpenAITeamLinkedErrorBlockReason}, blocker.reasons)
}

// TestTeamLinkedError_MissingTeamIDDoesNothing supplies an OAuth trigger
// without its team credential. The handler performs no platform query and
// records neither a successful write nor a runtime block.
func TestTeamLinkedError_MissingTeamIDDoesNothing(t *testing.T) {
	repo := &teamLinkedProviderRepoStub{teamProviders: newTeamLinkedFixture()}
	rl, blocker := newTeamLinkedTestService(repo)
	trigger := Record{ID: 9, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: StatusActive}

	rl.HandleWorkspaceDeactivated(context.Background(), &trigger, true)

	require.Empty(t, repo.setErrorIDs)
	require.Empty(t, blocker.reasons)
	require.Zero(t, repo.listCalls)
}

// TestTeamLinkedError_SetErrorFailureDoesNotAbortRemaining makes teammate 2's
// persistent write fail. Teammate 6 still records success, and the runtime
// recorder receives blocks for both matching teammates.
func TestTeamLinkedError_SetErrorFailureDoesNotAbortRemaining(t *testing.T) {
	repo := &teamLinkedProviderRepoStub{
		teamProviders: newTeamLinkedFixture(),
		failSetError:  map[int64]error{2: errors.New("db down")},
	}
	rl, blocker := newTeamLinkedTestService(repo)
	trigger := newTeamLinkedProvider(1, "team-A")

	rl.HandleWorkspaceDeactivated(context.Background(), &trigger, true)

	require.Equal(t, []int64{6}, repo.setErrorIDs)
	require.Len(t, blocker.reasons, 2)
}

// newTeamLinkedTestService binds repo to a service and returns its runtime
// recorder. The service uses constructor defaults for its clock and logger
// while Block retains the supplied target pointers.
func newTeamLinkedTestService(repo *teamLinkedProviderRepoStub) (*TeamLinkedHealth, *teamBlockRecorder) {
	blocker := &teamBlockRecorder{}
	return NewTeamLinkedHealth(repo, TeamLinkedOptions{Block: blocker.Block}), blocker
}

// Block appends the target pointer and matching reason to the fixture recorder.
// The record is retained by reference, so tests observing it also observe later
// mutations to that input.
func (r *teamBlockRecorder) Block(value *Record, _ time.Time, reason string) {
	r.providers = append(r.providers, value)
	r.reasons = append(r.reasons, reason)
}
