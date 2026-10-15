package egress

import (
	"time"
)

// proxy_fallback.go resolves expired proxies to a reachable backup or an
// allowed direct connection.
// Expiration can leave providers on the original proxy, switch them to direct
// connections, or follow a chain of backup IDs. Each expired backup supplies
// the next fallback rule, so the starting proxy's mode alone does not determine
// the final reassignment.
//
// Resolution uses the caller's snapshot and clock, records visited identities,
// and stops on a missing ID or repeated node. A candidate passes when its
// timestamp has not expired and its status is not expired; other stored
// statuses can pass this check. Failure returns change=false so the maintenance
// caller can retain the binding and diagnose the unresolved chain.

// ResolveProxyFallbackTarget returns the provider reassignment selected by
// start's fallback rule using byID and now. change=false retains the binding;
// change=true with a nil target selects direct connections, and a nonnil target
// selects that stored backup ID.
//
// The caller supplies an expiration candidate and a stable snapshot. The
// resolver tracks visited IDs and follows each expired backup's own mode,
// stopping unresolved on cycles, missing targets, or disabled tails. An
// available candidate needs an unexpired timestamp and a status other than
// expired; active status is not additionally required here.
func ResolveProxyFallbackTarget(start Proxy, byID map[int64]Proxy, now time.Time) (*int64, bool) {
	switch start.FallbackMode {
	case FallbackModeDirect:
		return nil, true
	case FallbackModeProxy:
		// Keep the origin visited before following backups. A chain that returns
		// to that proxy or any prior target leaves the binding unresolved.
		visited := map[int64]struct{}{start.ID: {}}
		curID := start.BackupProxyID
		for {
			if curID == nil {
				return nil, false
			}
			if _, seen := visited[*curID]; seen {
				return nil, false
			}
			p, ok := byID[*curID]
			if !ok {
				return nil, false
			}
			// Timestamp and stored expiration both disqualify a backup. Other status
			// values are accepted here, matching the maintenance snapshot selection rule.
			if !(&p).IsExpired(now) && p.Status != StatusExpired {
				id := p.ID
				return &id, true
			}
			visited[*curID] = struct{}{}
			switch p.FallbackMode {
			case FallbackModeDirect:
				return nil, true
			case FallbackModeProxy:
				curID = p.BackupProxyID
			default:
				return nil, false
			}
		}
	default:
		return nil, false
	}
}
