package provider

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// size returns the retained pair count under the state's mutex for tests.
// Nil state returns zero. The count describes the moment of the read; subsequent
// observations can change it.
func (s *ModelTransientState) size() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// TestOpenAIModelTransient_FirstFailureDoesNotCreateLongBlock records one
// failure for a fresh provider/model pair.
// It checks streak one, zero cooldown, and immediate admission for that model.
func TestOpenAIModelTransient_FirstFailureDoesNotCreateLongBlock(t *testing.T) {
	state := NewModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)

	decision := state.RecordFailure(35, "gpt-5.5", now)

	assert.Equal(t, 1, decision.FailureStreak)
	assert.Zero(t, decision.Cooldown)
	assert.False(t, state.IsBlocked(35, "gpt-5.5", now))
}

// TestOpenAIModelTransient_SecondFailureCreatesShortModelBlock records a
// second failure one second later.
// It checks the short cooldown, rejection inside that interval, and admission
// after the selected deadline.
func TestOpenAIModelTransient_SecondFailureCreatesShortModelBlock(t *testing.T) {
	state := NewModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	state.RecordFailure(35, "gpt-5.5", now)

	decision := state.RecordFailure(35, "gpt-5.5", now.Add(time.Second))

	assert.Equal(t, 2, decision.FailureStreak)
	assert.Equal(t, openAIModelTransientShortCooldown, decision.Cooldown)
	assert.True(t, state.IsBlocked(35, "gpt-5.5", now.Add(2*time.Second)))
	assert.False(t, state.IsBlocked(35, "gpt-5.5", now.Add(openAIModelTransientShortCooldown+2*time.Second)))
}

// TestOpenAIModelTransient_ThirdFailureCreatesFortyFiveSecondModelBlock
// records three failures at one-second intervals.
// It checks streak three, a forty-five-second pause, and admission on either side
// of that pause's expiry.
func TestOpenAIModelTransient_ThirdFailureCreatesFortyFiveSecondModelBlock(t *testing.T) {
	state := NewModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	state.RecordFailure(35, "gpt-5.5", now)
	state.RecordFailure(35, "gpt-5.5", now.Add(time.Second))

	decision := state.RecordFailure(35, "gpt-5.5", now.Add(2*time.Second))

	assert.Equal(t, 3, decision.FailureStreak)
	assert.Equal(t, 45*time.Second, decision.Cooldown)
	assert.True(t, state.IsBlocked(35, "gpt-5.5", now.Add(40*time.Second)))
	assert.False(t, state.IsBlocked(35, "gpt-5.5", now.Add(48*time.Second)))
}

// TestOpenAIModelTransient_BlockIsIsolatedByModel mixes uppercase and
// lowercase spellings for one model.
// It checks their shared cooldown while a different model and another provider
// remain unblocked.
func TestOpenAIModelTransient_BlockIsIsolatedByModel(t *testing.T) {
	state := NewModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	state.RecordFailure(35, "gpt-5.6-terra", now)
	state.RecordFailure(35, "GPT-5.6-TERRA", now.Add(time.Second))

	assert.True(t, state.IsBlocked(35, "gpt-5.6-terra", now.Add(2*time.Second)))
	assert.False(t, state.IsBlocked(35, "gpt-5.5", now.Add(2*time.Second)))
	assert.False(t, state.IsBlocked(47, "gpt-5.6-terra", now.Add(2*time.Second)))
}

// TestOpenAIModelTransient_SuccessClearsStreakAndBlock records success after
// a short cooldown begins.
// It checks immediate admission and verifies that the next failure restarts at
// streak one with zero cooldown.
func TestOpenAIModelTransient_SuccessClearsStreakAndBlock(t *testing.T) {
	state := NewModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	state.RecordFailure(35, "gpt-5.5", now)
	state.RecordFailure(35, "gpt-5.5", now.Add(time.Second))
	require.True(t, state.IsBlocked(35, "gpt-5.5", now.Add(2*time.Second)))

	state.RecordSuccess(35, "gpt-5.5")

	assert.False(t, state.IsBlocked(35, "gpt-5.5", now.Add(2*time.Second)))
	decision := state.RecordFailure(35, "gpt-5.5", now.Add(3*time.Second))
	assert.Equal(t, 1, decision.FailureStreak)
	assert.Zero(t, decision.Cooldown)
}

// TestOpenAIModelTransient_StaleStreakExpires places a second observation
// beyond failure retention.
// It checks that the old streak expires and the new observation has streak one
// with zero cooldown.
func TestOpenAIModelTransient_StaleStreakExpires(t *testing.T) {
	state := NewModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	state.RecordFailure(35, "gpt-5.5", now)

	decision := state.RecordFailure(35, "gpt-5.5", now.Add(openAIModelTransientStreakTTL+time.Second))

	assert.Equal(t, 1, decision.FailureStreak)
	assert.Zero(t, decision.Cooldown)
}

// TestOpenAIModelTransient_StreakSurvivesSparseTraffic separates failures by gaps
// longer than both cooldowns. It checks that the retained streak still escalates
// through short and long pauses and blocks after the third failure.
func TestOpenAIModelTransient_StreakSurvivesSparseTraffic(t *testing.T) {
	state := NewModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	gap := 5 * time.Minute
	require.Greater(t, gap, openAIModelTransientLongCooldown,
		"请求间隔必须超过所有冷却时间，避免用例因错误原因通过")

	first := state.RecordFailure(35, "gpt-5.5", now)
	second := state.RecordFailure(35, "gpt-5.5", now.Add(gap))
	third := state.RecordFailure(35, "gpt-5.5", now.Add(2*gap))

	assert.Equal(t, 1, first.FailureStreak)
	assert.Zero(t, first.Cooldown)
	assert.Equal(t, 2, second.FailureStreak)
	assert.Equal(t, openAIModelTransientShortCooldown, second.Cooldown)
	assert.Equal(t, 3, third.FailureStreak)
	assert.Equal(t, openAIModelTransientLongCooldown, third.Cooldown)
	assert.True(t, state.IsBlocked(35, "gpt-5.5", now.Add(2*gap+time.Second)))
}

// TestOpenAIModelTransient_SuccessResetsStreakAcrossSparseTraffic inserts success
// between widely spaced failures. It checks that the next failure starts at one,
// selects zero cooldown, and leaves the model immediately schedulable.
func TestOpenAIModelTransient_SuccessResetsStreakAcrossSparseTraffic(t *testing.T) {
	state := NewModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	gap := 5 * time.Minute

	state.RecordFailure(35, "gpt-5.5", now)
	state.RecordSuccess(35, "gpt-5.5")

	decision := state.RecordFailure(35, "gpt-5.5", now.Add(gap))

	assert.Equal(t, 1, decision.FailureStreak)
	assert.Zero(t, decision.Cooldown)
	assert.False(t, state.IsBlocked(35, "gpt-5.5", now.Add(gap+time.Second)))
}

// TestOpenAIModelTransient_IgnoresInvalidKeys supplies a zero provider ID
// and blank model names.
// It checks zero failure streaks, unblocked admission, and an empty retained
// state after those invalid observations.
func TestOpenAIModelTransient_IgnoresInvalidKeys(t *testing.T) {
	state := NewModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)

	assert.Zero(t, state.RecordFailure(0, "gpt-5.5", now).FailureStreak)
	assert.Zero(t, state.RecordFailure(35, " ", now).FailureStreak)
	assert.False(t, state.IsBlocked(0, "gpt-5.5", now))
	assert.False(t, state.IsBlocked(35, "", now))
	assert.Equal(t, 0, state.size())
}

// TestOpenAIModelTransient_IgnoresOversizedModelKey supplies a model one
// byte above the input limit.
// It checks a zero failure streak, unblocked admission, and no retained state
// for that oversized model.
func TestOpenAIModelTransient_IgnoresOversizedModelKey(t *testing.T) {
	state := NewModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	model := strings.Repeat("m", openAIModelTransientMaxModelBytes+1)

	decision := state.RecordFailure(35, model, now)

	assert.Zero(t, decision.FailureStreak)
	assert.False(t, state.IsBlocked(35, model, now))
	assert.Equal(t, 0, state.size())
}

// TestOpenAIModelTransient_StateIsBoundedAndConcurrencySafe runs parallel
// failure and admission observations for distinct pairs.
// After joining those operations, it checks that retained entries stay within
// the configured capacity.
func TestOpenAIModelTransient_StateIsBoundedAndConcurrencySafe(t *testing.T) {
	// Sixteen slots force capacity eviction during parallel insertion.
	const maxEntries = 16
	state := NewModelTransientState(maxEntries)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup

	// Register each operation before launching it, then join all registered workers
	// before inspecting size so the capacity assertion observes completed mutations.
	for i := range 128 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			model := fmt.Sprintf("gpt-test-%d", i)
			state.RecordFailure(int64(i+1), model, now.Add(time.Duration(i)*time.Millisecond))
			_ = state.IsBlocked(int64(i+1), model, now.Add(time.Second))
		}(i)
	}
	wg.Wait()

	assert.LessOrEqual(t, state.size(), maxEntries)
}
