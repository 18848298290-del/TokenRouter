package provider

import (
	"context"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Success cleanup must distinguish the pause observed during exchange from a
// later administrator pause. One fixture simulates a conditional clear conflict,
// while another captures the published record after a matching clear to verify
// its temporary-window fields have been removed.

// refreshSuccessCooldownRepo models a single current row for conditional
// success-cleanup fixtures.
type refreshSuccessCooldownRepo struct {
	// current and clears are used serially by each test. The current row may
	// differ from the exchange observation, and clears counts applied fixture
	// updates.
	current *Record // current administrator fixture state
	clears  int     // applied pause clear count
}

// refreshSuccessScheduler captures the record published after the fixture's
// conditional health cleanup.
type refreshSuccessScheduler struct {
	// last holds a shallow copy with borrowed maps and pointers; tests inspect
	// scalar and nil pause fields after Run completes its synchronous
	// publication.
	last *Record
}

// cooldownPostActions binds the fixture's conditional cooldown clear with clock
// and loggers. It leaves invalidation, scheduler publication and privacy
// unconfigured until a test supplies the callback it needs.
func cooldownPostActions(repo *refreshSuccessCooldownRepo) *RefreshPostActions {
	return &RefreshPostActions{
		Now: time.Now, Info: slog.Info, Warn: slog.Warn, Debug: slog.Debug, ClearBlock: func(int64) {}, NeedsReauth: GrokNeedsReauth,
		ClearCooldown: func(ctx context.Context, value *Record) (bool, error) {
			return repo.ClearRefreshCooldownIfUnchanged(ctx, ObserveRefreshCooldown(value))
		},
	}
}

// ClearRefreshCooldownIfUnchanged compares the current fixture snapshot with the
// observed cooldown version. False leaves health untouched; a match delegates
// the clear, and tests serialize fixture access rather than modeling database
// locking.
func (r *refreshSuccessCooldownRepo) ClearRefreshCooldownIfUnchanged(ctx context.Context, v RefreshCooldownVersion) (bool, error) {
	if !reflect.DeepEqual(ObserveRefreshCooldown(r.current), v) {
		return false, nil
	}
	return true, r.ClearTempUnschedulable(ctx, v.ID)
}

// ClearTempUnschedulable counts a clear and removes the current fixture's pause
// fields. The fixture requires a nonnil current row, ignores context and ID, and
// is used with serialized calls.
func (r *refreshSuccessCooldownRepo) ClearTempUnschedulable(context.Context, int64) error {
	r.clears++
	r.current.TempUnschedulableUntil = nil
	r.current.TempUnschedulableReason = ""
	return nil
}

// TestRefreshSuccessCannotClearNewAdministratorCooldown changes credentials and
// pause reason after the successful exchange observation. Running post actions
// performs zero clears and retains the administrator's live pause and diagnostic
// reason.
func TestRefreshSuccessCannotClearNewAdministratorCooldown(t *testing.T) {
	until := time.Now().Add(time.Hour)
	observed := &Record{ID: 941, Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth, Status: StatusActive, Credentials: map[string]any{"refresh_token": "observed"}, TempUnschedulableUntil: &until, TempUnschedulableReason: "old"}
	current := *observed
	current.Credentials = map[string]any{"refresh_token": "administrator"}
	current.TempUnschedulableReason = "new cooldown"
	repo := &refreshSuccessCooldownRepo{current: &current}
	post := cooldownPostActions(repo)
	post.Run(context.Background(), observed)
	require.Zero(t, repo.clears)
	require.NotNil(t, current.TempUnschedulableUntil)
	require.Equal(t, "new cooldown", current.TempUnschedulableReason)
}

// SetProvider stores a shallow record copy for the publication assertion.
// Callers pass a nonnil record and keep any retained maps or pointers stable
// while the fixture reads them.
func (s *refreshSuccessScheduler) SetProvider(_ context.Context, v *Record) error {
	copy := *v
	s.last = &copy
	return nil
}

// TestRefreshSuccessPublishesClearedCooldown runs success post actions against a
// matching observed pause. One clear occurs, and the captured published record
// has neither the pause deadline nor its reason.
func TestRefreshSuccessPublishesClearedCooldown(t *testing.T) {
	until := time.Now().Add(time.Hour)
	observed := &Record{ID: 942, Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth, Status: StatusActive, TempUnschedulableUntil: &until, TempUnschedulableReason: "old"}
	current := *observed
	repo := &refreshSuccessCooldownRepo{current: &current}
	cache := &refreshSuccessScheduler{}
	post := cooldownPostActions(repo)
	post.SyncProvider = cache.SetProvider
	post.Run(context.Background(), observed)
	require.Equal(t, 1, repo.clears)
	require.NotNil(t, cache.last)
	require.Nil(t, cache.last.TempUnschedulableUntil)
	require.Empty(t, cache.last.TempUnschedulableReason)
}
