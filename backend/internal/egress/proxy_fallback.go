package egress

import (
	"time"
)

// ResolveProxyFallbackTarget resolves provider reassignment for an expired proxy.
// The return values (targetID, change) mean:
//   - change=false: leave providers bound when mode is none or the chain cannot resolve.
//   - change=true, targetID=nil: switch to direct connections.
//   - change=true, targetID!=nil: switch to the resolved backup proxy.
//
// byID is a snapshot of all proxies; now is the expiration comparison time.
func ResolveProxyFallbackTarget(start Proxy, byID map[int64]Proxy, now time.Time) (*int64, bool) {
	switch start.FallbackMode {
	case FallbackModeDirect:
		return nil, true
	case FallbackModeProxy:
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
