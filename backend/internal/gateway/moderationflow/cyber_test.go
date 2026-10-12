package moderationflow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/modeltrace"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// queuedCyberTasks retains submitted work for deliberate execution after
// request mutations.
type queuedCyberTasks struct {
	// task retains the latest submitted closure and overwrites any earlier
	// one. Tests submit once, mutate request inputs, then invoke that captured
	// work serially.
	task func() // retained pending cyber task closure
}

// cyberEffects captures serial block and Ops effects from the retained
// task.
type cyberEffects struct {
	// events appends callback labels under serial fixture execution. Tests
	// inspect its order after invoking the queued task to distinguish block
	// submission from Ops enqueue.
	events []string // ordered block and Ops observations
	// entry retains the Enqueue argument directly and is not cloned by this
	// fixture. Tests inspect builder-captured values after the queued task has
	// finished.
	entry *ops.OpsInsertErrorLogInput // shared submitted Ops entry pointer
	// ctx retains the context passed to blocking, including values and its
	// deadline after task cleanup cancels it. Tests inspect those snapshots
	// rather than asserting ongoing activity after return.
	ctx context.Context // retained detached task context
	// keys retains the block callback's slice directly for serial assertions.
	// Dispatch supplies a new one-key slice, and this fixture does not mutate
	// it.
	keys []string // shared submitted block key slice
}

// Go retains the submitted closure and reports admission without executing
// it. A later submission replaces saved work; tests invoke it manually
// after request mutation and cancellation to inspect earlier captures.
func (q *queuedCyberTasks) Go(_ string, fn func()) bool { q.task = fn; return true }

// MarkCyberSessionBlocked records block order, context and key references
// for assertions. It stores no real block and runs serially, while later
// Ops enqueue supplies the comparison event.
func (e *cyberEffects) MarkCyberSessionBlocked(ctx context.Context, _ string, keys []string) {
	e.events = append(e.events, "block")
	e.ctx = ctx
	e.keys = keys
}

// Enqueue retains the submitted entry pointer and appends the Ops event.
// The fixture performs no asynchronous persistence, so tests inspect
// captured row values and relative callback order directly.
func (e *cyberEffects) Enqueue(in *ops.OpsInsertErrorLogInput) {
	e.events = append(e.events, "ops")
	e.entry = in
}

// TestPolicyDispatchFreezesMetadataAndKeepsIndependentBudget delays task
// execution while changing group, message, model and trace, then cancels
// the request. It checks captured block/Ops order, original metadata and
// trace values, and the separate thirty-second deadline; the fixture has no
// Recorder.
func TestPolicyDispatchFreezesMetadataAndKeepsIndependentBudget(t *testing.T) {
	q := &queuedCyberTasks{}
	effects := &cyberEffects{}
	runtime := Runtime{Tasks: q, Blocks: effects, Ops: effects}
	group := int64(4)
	trace := modeltrace.NewAPIKeyModelRedirectTrace("original", "original", "mapped")
	request, cancel := context.WithCancel(modeltrace.WithContext(context.Background(), trace))
	in := PolicyCompletion{Meta: OpsMeta{GroupID: &group, Model: "original"}, Mark: Mark{Message: "observed", Body: "body", UpstreamStatus: 403}, BlockKey: "scope"}
	runtime.Dispatch(request, in)
	group = 8
	in.Mark.Message = "changed"
	in.Meta.Model = "changed"
	trace.ClientModel = "changed"
	// Cancel the request before manually running captured work, so its copied
	// trace and independently created deadline are inspected after the source
	// lifecycle has ended.
	cancel()
	require.NotNil(t, q.task)
	q.task()
	require.Equal(t, []string{"block", "ops"}, effects.events)
	require.Equal(t, int64(4), *effects.entry.GroupID)
	require.Equal(t, "original", effects.entry.Model)
	require.Equal(t, "cyber_policy: observed", effects.entry.ErrorMessage)
	require.Equal(t, []string{"scope"}, effects.keys)
	saved, ok := modeltrace.FromContext(effects.ctx)
	require.True(t, ok)
	require.Equal(t, "original", saved.ClientModel)
	deadline, ok := effects.ctx.Deadline()
	require.True(t, ok)
	// Allow one second for fixture execution around the thirty-second
	// deadline.
	require.InDelta(t, 30, time.Until(deadline).Seconds(), 1)
}

// TestPolicyBlockPlanRetainsOriginalKeyOrdering supplies an explicit key
// repeated first in transcript history. It checks suppression of that
// repeat, retained old/new order and empty scope when no transcript is
// supplied.
func TestPolicyBlockPlanRetainsOriginalKeyOrdering(t *testing.T) {
	p := BuildBlockPlan("explicit", []string{"explicit", "old", "new"}, "scope")
	require.Equal(t, BlockPlan{ScopeKey: "scope", Keys: []string{"explicit", "old", "new"}}, p)
	require.Equal(t, BlockPlan{Keys: []string{"explicit"}}, BuildBlockPlan("explicit", nil, "unused"))
}

// TestModerationContentSnapshotIsolatesAllCollections copies Images, Items
// and ImageItems before mutating their source elements. It checks that each
// copied collection still exposes its earlier string value after those
// mutations.
func TestModerationContentSnapshotIsolatesAllCollections(t *testing.T) {
	in := moderation.ContentModerationInput{Images: []string{"image"}, Items: []moderation.ContentModerationInputItem{{Text: "tool"}}, ImageItems: []moderation.ContentModerationImage{{Reference: "ref"}}}
	out := SnapshotContent(in)
	in.Images[0] = "changed"
	in.Items[0].Text = "changed"
	in.ImageItems[0].Reference = "changed"
	require.Equal(t, "image", out.Images[0])
	require.Equal(t, "tool", out.Items[0].Text)
	require.Equal(t, "ref", out.ImageItems[0].Reference)
}
