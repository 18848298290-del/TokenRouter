package text

import "context"

// QoderCompatibleOutcome records output progress and partial-result, cancellation, and retry eligibility.
type QoderCompatibleOutcome struct {
	Err                                                       error
	OutputChanged, Partial, Canceled, CanRefresh, CanFailover bool
}
type QoderRefreshResult struct{ Ready, Pending bool }

// QoderCompatiblePorts supplies individual operations for Messages and Responses; Chat uses a separate interface.
// This loop does not recheck funding after waiting. Failure must stop retries after any actual output.
type QoderCompatiblePorts interface {
	Context() context.Context
	Select(map[int64]struct{}) (Selection, error)
	SelectionFailed(error, bool, bool, error)
	Acquire(bool) bool
	Forward() QoderCompatibleOutcome
	Refresh() QoderRefreshResult
	RefreshPending(bool)
	Partial(QoderCompatibleOutcome)
	Canceled(bool, error)
	Failure(QoderCompatibleOutcome) bool
	Exhausted(error)
	Success()
	Switched()
}

// RunQoderCompatible runs provider attempts for the current request.
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
		// Failure handles known errors and committed output; unhandled errors may try another provider.
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
