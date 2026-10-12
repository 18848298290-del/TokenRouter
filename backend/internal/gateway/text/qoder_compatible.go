package text

import "context"

// Qoder compatibility recovery first distinguishes served partial output and
// cancellation from a replayable failure. Those terminal observations end the
// request before a credential refresh or another provider can repeat work.
//
// A ready credential refresh permits one additional attempt on the captured
// provider after reacquiring its resources. Pending refresh excludes that
// provider within the distinct-provider allowance, while error handling can
// otherwise move to another candidate. The adapter's Failure decision rejects
// replay after actual output, including the final unclassified-error path.
// Resource release and funding checks stay with adapter operations; this loop
// has no separate funding recheck after acquisition. Selection diagnostics
// retain whether refresh was pending and the preceding ordinary failure.

// QoderCompatibleOutcome records one Qoder compatibility attempt's error,
// output and recovery decisions.
type QoderCompatibleOutcome struct {
	// Partial and Canceled end before recovery. OutputChanged prevents credential
	// refresh and ordinary CanFailover handling; Failure must also end any
	// committed-output outcome before the final unclassified-error retry path.
	Err                                                       error // observed Qoder forwarding error
	OutputChanged, Partial, Canceled, CanRefresh, CanFailover bool  // downstream writer changed, partial service observed, adapter cancellation evidence, credential recovery permitted and classified failover permission
}

// QoderRefreshResult reports whether refreshed credentials are ready or still
// being prepared.
type QoderRefreshResult struct {
	// Ready permits one same-provider retry; Pending permits excluding that
	// provider while selection can try another. Ready takes precedence when both
	// are set by an adapter.
	Ready, Pending bool // replacement credentials available and credential refresh pending
}

// QoderCompatiblePorts supplies serial Qoder attempt operations with
// adapter-owned output and resources. Failure must terminate committed-output
// paths, and acquisition owns any required funding or admission checks.
type QoderCompatiblePorts interface {
	// Context returns the request context available to Qoder adapter operations.
	// The compatibility loop itself performs no cancellation wait; forwarding and
	// acquisition supply their captured cancellation decisions.
	Context() context.Context
	// Select captures a provider using the excluded identifier set for this
	// request. The loop caps distinct exclusions after failures, and a selection
	// error ends through SelectionFailed with its accumulated diagnostic evidence.
	Select(map[int64]struct{}) (Selection, error)
	// SelectionFailed handles a request's selection error using pending-refresh,
	// first-selection and preceding-error evidence. The adapter owns final client
	// output; the loop ends without another forwarding or completion attempt.
	SelectionFailed(error, bool, bool, error)
	// Acquire obtains attempt resources for the captured provider, distinguishing
	// a credential-refresh retry with true. False ends execution; resource cleanup
	// and any funding validation are owned by the adapter.
	Acquire(bool) bool
	// Forward performs one captured Qoder compatibility attempt and returns
	// output/recovery evidence. The adapter releases acquired resources according
	// to its execution strategy and retains partial observations for terminal
	// handling.
	Forward() QoderCompatibleOutcome
	// Refresh requests credential recovery for a failed attempt with no changed
	// output. Ready permits one additional acquisition and forward on this
	// provider; Pending permits excluding it within the remaining provider
	// allowance.
	Refresh() QoderRefreshResult
	// RefreshPending handles a refresh still pending when another provider cannot
	// be attempted. The bool carries whether this is refresh-retry handling, and
	// the adapter owns the request's final output.
	RefreshPending(bool)
	// Partial captures/completes served partial observations and ends Qoder
	// request handling. It runs before cancellation or retry branches, and the
	// loop performs no success binding or credential recovery afterward.
	Partial(QoderCompatibleOutcome)
	// Canceled handles adapter-observed cancellation, distinguishing a refreshed
	// retry with its bool. It owns terminal output and any already observed usage
	// handling; the loop returns after the operation.
	Canceled(bool, error)
	// Failure handles known terminal errors and returns whether execution should
	// stop. It must stop committed-output outcomes; false lets an unclassified
	// error attempt another provider even when CanFailover was false.
	Failure(QoderCompatibleOutcome) bool
	// Exhausted handles the final ordinary error after the distinct-provider
	// allowance ends. The loop calls it after Failure declined terminal handling,
	// leaving final HTTP output to the adapter.
	Exhausted(error)
	// Success finalizes a successful Qoder compatibility attempt and its
	// completion capture. The loop returns after this operation; persistent funds
	// and usage results remain with the adapter's recorder.
	Success()
	// Switched observes an allowed move after the current provider is excluded. It
	// precedes another selection and can follow pending refresh as well as
	// ordinary failover or unclassified error handling.
	Switched()
}

// RunQoderCompatible serially coordinates Qoder compatibility attempts within a
// distinct-provider allowance. Callers supply adapter-owned resources and a
// Failure handler that stops replay after changed output; the loop attempts an
// initial provider even with a nonpositive allowance.
//
// Partial service and cancellation end before refresh. A ready refresh
// reacquires and forwards once on the same target; a pending refresh can move
// to another provider while selection diagnostics retain its pending status.
// Ordinary CanFailover permits provider replacement before terminal
// classification. If Failure still declines an unclassified error, the loop can
// replace providers through its final recovery path until exclusions reach the
// allowance.
func RunQoderCompatible(p QoderCompatiblePorts, maxProviders int) {
	excluded := make(map[int64]struct{})
	pending := false
	var last error
	for {
		selected, err := p.Select(excluded)
		if err != nil {
			p.SelectionFailed(err, pending, len(excluded) == 0, last)
			return
		}
		pending = false
		if !p.Acquire(false) {
			return
		}
		result := p.Forward()
		if result.Partial {
			p.Partial(result)
			return
		}
		if result.Canceled {
			p.Canceled(false, result.Err)
			return
		}
		if result.Err != nil && !result.OutputChanged && result.CanRefresh {
			refresh := p.Refresh()
			// Refreshed credentials still require a new attempt acquisition. The prior
			// forwarding operation owns release of its resources before this retry begins.
			if refresh.Ready {
				if !p.Acquire(true) {
					return
				}
				result = p.Forward()
				if result.Partial {
					p.Partial(result)
					return
				}
				if result.Canceled {
					p.Canceled(true, result.Err)
					return
				}
			} else if refresh.Pending {
				// Pending credentials do not supply a usable same-provider attempt. Exclude
				// this target while the remaining allowance can select a different provider.
				if !result.OutputChanged {
					excluded[selected.Provider.ID] = struct{}{}
					if len(excluded) < maxProviders {
						pending = true
						p.Switched()
						continue
					}
				}
				p.RefreshPending(false)
				return
			}
		}
		if result.Err == nil {
			p.Success()
			return
		}
		if !result.OutputChanged && result.CanFailover {
			excluded[selected.Provider.ID] = struct{}{}
			if len(excluded) < maxProviders {
				last = result.Err
				p.Switched()
				continue
			}
		}
		// Failure owns committed-output rejection on this final path. A declined
		// unclassified error retains provider-switch eligibility even without CanFailover.
		if p.Failure(result) {
			return
		}
		excluded[selected.Provider.ID] = struct{}{}
		if len(excluded) < maxProviders {
			last = result.Err
			p.Switched()
			continue
		}
		p.Exhausted(result.Err)
		return
	}
}
